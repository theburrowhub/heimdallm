package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/heimdallr/daemon/internal/config"
	"github.com/heimdallr/daemon/internal/executor"
	gh "github.com/heimdallr/daemon/internal/github"
	"github.com/heimdallr/daemon/internal/keychain"
	"github.com/heimdallr/daemon/internal/notify"
	"github.com/heimdallr/daemon/internal/pipeline"
	"github.com/heimdallr/daemon/internal/scheduler"
	"github.com/heimdallr/daemon/internal/server"
	"github.com/heimdallr/daemon/internal/sse"
	"github.com/heimdallr/daemon/internal/store"
	"github.com/heimdallr/daemon/launchagent"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "install":
			bin, _ := os.Executable()
			if err := launchagent.Install(bin); err != nil {
				fmt.Fprintf(os.Stderr, "install: %v\n", err)
				os.Exit(1)
			}
			return
		case "uninstall":
			if err := launchagent.Uninstall(); err != nil {
				fmt.Fprintf(os.Stderr, "uninstall: %v\n", err)
				os.Exit(1)
			}
			return
		}
	}

	setupLogging()

	cfgPath := config.DefaultPath()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		slog.Error("config load failed", "path", cfgPath, "err", err)
		os.Exit(1)
	}

	token, err := keychain.Get()
	if err != nil {
		slog.Error("token not found", "err", err)
		os.Exit(1)
	}

	dbPath := filepath.Join(dataDir(), "heimdallr.db")
	s, err := store.Open(dbPath)
	if err != nil {
		slog.Error("store open failed", "err", err)
		os.Exit(1)
	}
	defer s.Close()

	if err := s.PurgeOldReviews(cfg.Retention.MaxDays); err != nil {
		slog.Warn("retention purge failed", "err", err)
	}

	broker := sse.NewBroker()
	broker.Start()

	notifier := notify.New()
	ghClient := gh.NewClient(token)
	exec := executor.New()

	p := pipeline.New(s, ghClient, exec, &notifyWithSSE{notifier: notifier})
	srv := server.New(s, broker, p)

	// cfgMu protects cfg and sched so reload is safe from any goroutine.
	var cfgMu sync.Mutex
	var sched *scheduler.Scheduler

	makePollFn := func(c *config.Config) func() {
		return func() {
			cfgMu.Lock()
			repos := c.GitHub.Repositories
			cfgMu.Unlock()
			prs, err := ghClient.FetchPRs(repos)
			if err != nil {
				slog.Error("poll: fetch PRs", "err", err)
				return
			}
			for _, pr := range prs {
				pr.ResolveRepo()
				cfgMu.Lock()
				aiCfg := c.AIForRepo(pr.Repo)
				cfgMu.Unlock()
				existing, _ := s.GetPRByGithubID(pr.ID)
				if existing != nil {
					if rev, err := s.LatestReviewForPR(existing.ID); err == nil && rev != nil {
						// Skip if PR has not changed since our last review.
						// pr.UpdatedAt is newer only if new commits or review requests arrived.
						if !pr.UpdatedAt.After(rev.CreatedAt) {
							continue
						}
					}
				}
				broker.Publish(sse.Event{Type: sse.EventPRDetected, Data: fmt.Sprintf(`{"pr_number":%d,"repo":%q}`, pr.Number, pr.Repo)})
				broker.Publish(sse.Event{Type: sse.EventReviewStarted, Data: fmt.Sprintf(`{"pr_number":%d,"repo":%q}`, pr.Number, pr.Repo)})
				if rev, err := p.Run(pr, aiCfg.Primary, aiCfg.Fallback); err != nil {
					slog.Error("pipeline run failed", "pr", pr.Number, "err", err)
					broker.Publish(sse.Event{Type: sse.EventReviewError, Data: fmt.Sprintf(`{"pr_number":%d,"error":%q}`, pr.Number, err.Error())})
				} else {
					// Include pr_id (store ID) so the Flutter app can deep-link to the PR detail
					broker.Publish(sse.Event{Type: sse.EventReviewCompleted, Data: fmt.Sprintf(`{"pr_number":%d,"repo":%q,"pr_id":%d,"severity":%q}`, pr.Number, pr.Repo, rev.PRID, rev.Severity)})
				}
			}
			// Retry reviews stored locally but not yet published to GitHub
			p.PublishPending()
		}
	}

	startScheduler := func(c *config.Config) *scheduler.Scheduler {
		sc := scheduler.New(parsePollInterval(c.GitHub.PollInterval), makePollFn(c))
		sc.Start()
		return sc
	}

	sched = startScheduler(cfg)
	defer sched.Stop()

	// Expose live config for GET /config
	srv.SetConfigFn(func() map[string]any {
		cfgMu.Lock()
		c := cfg
		cfgMu.Unlock()
		repoOverrides := make(map[string]map[string]string)
		for repo, ai := range c.AI.Repos {
			repoOverrides[repo] = map[string]string{
				"primary":  ai.Primary,
				"fallback": ai.Fallback,
			}
		}
		return map[string]any{
			"server_port":    c.Server.Port,
			"poll_interval":  c.GitHub.PollInterval,
			"repositories":   c.GitHub.Repositories,
			"ai_primary":     c.AI.Primary,
			"ai_fallback":    c.AI.Fallback,
			"retention_days": c.Retention.MaxDays,
			"repo_overrides": repoOverrides,
		}
	})

	// Cache authenticated username for GET /me
	var cachedLogin string
	srv.SetMeFn(func() (string, error) {
		if cachedLogin != "" {
			return cachedLogin, nil
		}
		login, err := ghClient.AuthenticatedUser()
		if err == nil {
			cachedLogin = login
		}
		return login, err
	})

	// Wire the reload callback: re-read config from disk, restart scheduler.
	srv.SetReloadFn(func() error {
		newCfg, err := config.Load(cfgPath)
		if err != nil {
			return fmt.Errorf("reload: %w", err)
		}
		cfgMu.Lock()
		cfg = newCfg
		cfgMu.Unlock()

		// Restart scheduler with new interval and repo list
		sched.Stop()
		sched = startScheduler(newCfg)

		// Run first poll immediately with new config
		go makePollFn(newCfg)()
		return nil
	})

	// Wire the trigger-review callback: re-run pipeline on a single stored PR.
	srv.SetTriggerReviewFn(func(prID int64) error {
		pr, err := s.GetPR(prID)
		if err != nil {
			return fmt.Errorf("trigger review: get pr %d: %w", prID, err)
		}
		cfgMu.Lock()
		aiCfg := cfg.AIForRepo(pr.Repo)
		cfgMu.Unlock()

		// Construct a minimal github.PullRequest from the stored data
		ghPR := &gh.PullRequest{
			ID:      pr.GithubID,
			Number:  pr.Number,
			Title:   pr.Title,
			HTMLURL: pr.URL,
			State:   pr.State,
			Repo:    pr.Repo,
		}
		ghPR.User.Login = pr.Author

		rev, err := p.Run(ghPR, aiCfg.Primary, aiCfg.Fallback)
		if err != nil {
			broker.Publish(sse.Event{Type: sse.EventReviewError, Data: fmt.Sprintf(`{"pr_id":%d,"error":%q}`, prID, err.Error())})
			return err
		}
		broker.Publish(sse.Event{Type: sse.EventReviewCompleted, Data: fmt.Sprintf(
			`{"pr_number":%d,"repo":%q,"pr_id":%d,"severity":%q}`,
			pr.Number, pr.Repo, prID, rev.Severity,
		)})
		return nil
	})

	// Initial poll
	go makePollFn(cfg)()

	go func() {
		slog.Info("daemon started", "port", cfg.Server.Port)
		if err := srv.Start(cfg.Server.Port); err != nil {
			slog.Error("server stopped", "err", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	slog.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	broker.Stop()
}

func setupLogging() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))
}

func dataDir() string {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".local", "share", "heimdallr")
	os.MkdirAll(dir, 0700)
	return dir
}

func parsePollInterval(s string) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 5 * time.Minute
	}
	return d
}

type notifyWithSSE struct {
	notifier *notify.Notifier
}

func (n *notifyWithSSE) Notify(title, message string) {
	n.notifier.Notify(title, message)
}

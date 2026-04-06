package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
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

	// Load or create the per-daemon API token.  All mutating HTTP endpoints
	// require this token in X-Heimdallr-Token (security issue #3).
	apiToken, err := loadOrCreateAPIToken(dataDir())
	if err != nil {
		slog.Warn("could not create API token — mutating endpoints unprotected", "err", err)
	}

	p := pipeline.New(s, ghClient, exec, &notifyWithSSE{notifier: notifier})
	srv := server.New(s, broker, p, apiToken)

	// cfgMu protects cfg and sched so reload is safe from any goroutine.
	var cfgMu sync.Mutex
	var sched *scheduler.Scheduler

	// reviewMu prevents concurrent pipeline runs for the same GitHub PR ID.
	// Key: pr.ID (GitHub PR ID), Value: true while being reviewed.
	var reviewMu sync.Mutex
	inFlight := make(map[int64]bool)

	buildRunOpts := func(pr *gh.PullRequest, aiCfg config.RepoAI) pipeline.RunOptions {
		cli := aiCfg.Primary
		if cli == "" {
			cli = cfg.AI.Primary
		}
		cfgMu.Lock()
		agentCfg := cfg.AgentConfigFor(cli)
		cfgMu.Unlock()
		extraFlags := agentCfg.ExtraFlags
		if extraFlags != "" {
			if err := executor.ValidateExtraFlags(extraFlags); err != nil {
				slog.Warn("buildRunOpts: extra_flags from config rejected", "err", err)
				extraFlags = ""
			}
		}
		return pipeline.RunOptions{
			Primary:        aiCfg.Primary,
			Fallback:       aiCfg.Fallback,
			PromptOverride: aiCfg.Prompt,
			AgentPromptID:  agentCfg.PromptID,
			ReviewMode:     aiCfg.ReviewMode,
			ExecOpts: executor.ExecOptions{
				Model:                agentCfg.Model,
				MaxTurns:             agentCfg.MaxTurns,
				ApprovalMode:         agentCfg.ApprovalMode,
				ExtraFlags:           extraFlags,
				WorkDir:              aiCfg.LocalDir,
				Effort:               agentCfg.Effort,
				PermissionMode:       agentCfg.PermissionMode,
				Bare:                 agentCfg.Bare,
				DangerouslySkipPerms: agentCfg.DangerouslySkipPerms,
				NoSessionPersistence: agentCfg.NoSessionPersistence,
			},
		}
	}

	runReview := func(pr *gh.PullRequest, aiCfg config.RepoAI) {
		// Guard: skip if already being reviewed
		reviewMu.Lock()
		if inFlight[pr.ID] {
			reviewMu.Unlock()
			slog.Info("review already in flight, skipping", "pr", pr.Number, "repo", pr.Repo)
			return
		}
		inFlight[pr.ID] = true
		reviewMu.Unlock()
		defer func() {
			reviewMu.Lock()
			delete(inFlight, pr.ID)
			reviewMu.Unlock()
		}()

		// Safety check: log exactly what we're about to review
		slog.Info("pipeline: reviewing PR",
			"repo", pr.Repo, "number", pr.Number, "github_id", pr.ID, "title", pr.Title)

		broker.Publish(sse.Event{Type: sse.EventPRDetected, Data: fmt.Sprintf(`{"pr_number":%d,"repo":%q}`, pr.Number, pr.Repo)})
		broker.Publish(sse.Event{Type: sse.EventReviewStarted, Data: fmt.Sprintf(`{"pr_number":%d,"repo":%q}`, pr.Number, pr.Repo)})
		rev, err := p.Run(pr, buildRunOpts(pr, aiCfg))
		if err != nil {
			slog.Error("pipeline run failed", "repo", pr.Repo, "pr", pr.Number, "err", err)
			broker.Publish(sse.Event{Type: sse.EventReviewError, Data: fmt.Sprintf(`{"pr_number":%d,"repo":%q,"error":%q}`, pr.Number, pr.Repo, err.Error())})
			return
		}
		slog.Info("pipeline: review done",
			"repo", pr.Repo, "number", pr.Number, "severity", rev.Severity,
			"github_review_id", rev.GitHubReviewID)
		broker.Publish(sse.Event{Type: sse.EventReviewCompleted, Data: fmt.Sprintf(
			`{"pr_number":%d,"repo":%q,"pr_id":%d,"severity":%q}`,
			pr.Number, pr.Repo, rev.PRID, rev.Severity,
		)})
	}

	makePollFn := func(c *config.Config) func() {
		return func() {
			cfgMu.Lock()
			repos := c.GitHub.Repositories
			cfgMu.Unlock()

			// Fetch all review-requested PRs without a repo filter — adding many
			// repo: terms to the Search API query can exceed its length limit and
			// silently return zero results. We filter to monitored repos below.
			prs, err := ghClient.FetchPRsToReview()
			if err != nil {
				slog.Error("poll: fetch PRs to review", "err", err)
				return
			}
			// Build a quick lookup set for monitored repos.
			monitoredSet := make(map[string]struct{}, len(repos))
			for _, r := range repos {
				monitoredSet[r] = struct{}{}
			}
			for _, pr := range prs {
				pr.ResolveRepo()
				if pr.Repo == "" {
					slog.Warn("poll: skipping PR with empty repo", "pr_number", pr.Number)
					continue
				}
				// Only auto-review PRs from repos the user has opted in to monitor.
				if _, monitored := monitoredSet[pr.Repo]; !monitored {
					continue
				}
				cfgMu.Lock()
				aiCfg := c.AIForRepo(pr.Repo)
				cfgMu.Unlock()
				existing, _ := s.GetPRByGithubID(pr.ID)
				if existing != nil {
					// Skip PRs the user has dismissed
					if existing.Dismissed {
						continue
					}
					if rev, err := s.LatestReviewForPR(existing.ID); err == nil && rev != nil {
						// Skip if PR hasn't been meaningfully updated since our last review.
						// Add a 30-second grace period: GitHub bumps updated_at by ~2s when
						// a review is submitted, which would otherwise trigger an immediate re-review.
						if !pr.UpdatedAt.After(rev.CreatedAt.Add(30 * time.Second)) {
							continue
						}
					}
				}
				prCopy := *pr // copy to avoid loop variable capture
				// Run each review in its own goroutine so the poll loop is not
				// blocked by a long-running AI review (especially when local_dir
				// is set and the CLI analyses the full repo).  The inFlight guard
				// inside runReview prevents concurrent reviews of the same PR.
				go runReview(&prCopy, aiCfg)
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
				"primary":     ai.Primary,
				"fallback":    ai.Fallback,
				"review_mode": ai.ReviewMode,
				"local_dir":   ai.LocalDir,
			}
		}
		agentConfigs := make(map[string]map[string]any)
		for name, ac := range c.AI.Agents {
			agentConfigs[name] = map[string]any{
				"model":                    ac.Model,
				"max_turns":                ac.MaxTurns,
				"approval_mode":            ac.ApprovalMode,
				"extra_flags":              ac.ExtraFlags,
				"prompt":                   ac.PromptID,
				"effort":                   ac.Effort,
				"permission_mode":          ac.PermissionMode,
				"bare":                     ac.Bare,
				"dangerously_skip_perms":   ac.DangerouslySkipPerms,
				"no_session_persistence":   ac.NoSessionPersistence,
			}
		}
		return map[string]any{
			"server_port":    c.Server.Port,
			"poll_interval":  c.GitHub.PollInterval,
			"repositories":   c.GitHub.Repositories,
			"non_monitored":  c.GitHub.NonMonitored,
			"ai_primary":     c.AI.Primary,
			"ai_fallback":    c.AI.Fallback,
			"review_mode":    c.AI.ReviewMode,
			"retention_days": c.Retention.MaxDays,
			"repo_overrides": repoOverrides,
			"agent_configs":  agentConfigs,
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
		publishErr := func(msg string) {
			broker.Publish(sse.Event{
				Type: sse.EventReviewError,
				Data: fmt.Sprintf(`{"pr_id":%d,"error":%q}`, prID, msg),
			})
		}

		pr, err := s.GetPR(prID)
		if err != nil {
			publishErr(fmt.Sprintf("PR not found: %v", err))
			return fmt.Errorf("trigger review: get pr %d: %w", prID, err)
		}
		if pr.Repo == "" {
			publishErr("Repo unknown — this PR was stored before repo detection was working. " +
				"Wait for the next poll cycle or re-discover repos in Settings.")
			return fmt.Errorf("trigger review: pr %d has empty repo", prID)
		}
		cfgMu.Lock()
		aiCfg := cfg.AIForRepo(pr.Repo)
		cfgMu.Unlock()

		// Construct github.PullRequest from stored data
		ghPR := &gh.PullRequest{
			ID:        pr.GithubID,
			Number:    pr.Number,
			Title:     pr.Title,
			HTMLURL:   pr.URL,
			State:     pr.State,
			Repo:      pr.Repo,
			UpdatedAt: pr.UpdatedAt, // required so UpsertPR doesn't zero-out the timestamp
		}
		ghPR.User.Login = pr.Author

		slog.Info("trigger review: running pipeline",
			"store_pr_id", prID, "repo", pr.Repo, "number", pr.Number, "github_id", pr.GithubID)

		// Use the same in-flight guard as the poll loop
		reviewMu.Lock()
		if inFlight[ghPR.ID] {
			reviewMu.Unlock()
			return fmt.Errorf("review already in progress for PR %d", ghPR.Number)
		}
		inFlight[ghPR.ID] = true
		reviewMu.Unlock()
		defer func() {
			reviewMu.Lock()
			delete(inFlight, ghPR.ID)
			reviewMu.Unlock()
		}()

		rev, err := p.Run(ghPR, buildRunOpts(ghPR, aiCfg))
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

// loadOrCreateAPIToken reads an existing token from <dataDir>/api_token, or
// generates a new cryptographically-random one and writes it with mode 0600.
// The token is used by the HTTP server to authenticate all mutating requests
// (POST/PUT/DELETE) — see security issue #3.
func loadOrCreateAPIToken(dir string) (string, error) {
	path := filepath.Join(dir, "api_token")

	// Try to read existing token first.
	data, err := os.ReadFile(path)
	if err == nil {
		tok := strings.TrimSpace(string(data))
		if len(tok) >= 32 {
			return tok, nil
		}
	}

	// Generate a new 32-byte random token (64 hex chars).
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("api_token: generate random: %w", err)
	}
	tok := hex.EncodeToString(buf)

	// Write with mode 0600 so only the owning user can read it.
	if err := os.WriteFile(path, []byte(tok+"\n"), 0600); err != nil {
		return "", fmt.Errorf("api_token: write %s: %w", path, err)
	}
	slog.Info("api_token: created new token", "path", path)
	return tok, nil
}

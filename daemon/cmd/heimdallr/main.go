package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
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

	dbPath := filepath.Join(dataDir(), "auto-pr.db")
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

	pollFn := func() {
		prs, err := ghClient.FetchPRs(cfg.GitHub.Repositories)
		if err != nil {
			slog.Error("poll: fetch PRs", "err", err)
			return
		}
		for _, pr := range prs {
			if pr.Head.Repo.FullName != "" {
				pr.Repo = pr.Head.Repo.FullName
			}
			aiCfg := cfg.AIForRepo(pr.Repo)
			existing, _ := s.GetPRByGithubID(pr.ID)
			if existing != nil {
				if rev, err := s.LatestReviewForPR(existing.ID); err == nil && rev != nil {
					interval := parsePollInterval(cfg.GitHub.PollInterval)
					if time.Since(rev.CreatedAt) < interval {
						continue
					}
				}
			}
			broker.Publish(sse.Event{Type: sse.EventPRDetected, Data: fmt.Sprintf(`{"pr_number":%d,"repo":%q}`, pr.Number, pr.Repo)})
			broker.Publish(sse.Event{Type: sse.EventReviewStarted, Data: fmt.Sprintf(`{"pr_number":%d,"repo":%q}`, pr.Number, pr.Repo)})
			if _, err := p.Run(pr, aiCfg.Primary, aiCfg.Fallback); err != nil {
				slog.Error("pipeline run failed", "pr", pr.Number, "err", err)
				broker.Publish(sse.Event{Type: sse.EventReviewError, Data: fmt.Sprintf(`{"pr_number":%d,"error":%q}`, pr.Number, err.Error())})
			} else {
				broker.Publish(sse.Event{Type: sse.EventReviewCompleted, Data: fmt.Sprintf(`{"pr_number":%d,"repo":%q}`, pr.Number, pr.Repo)})
			}
		}
	}

	interval := parsePollInterval(cfg.GitHub.PollInterval)
	sched := scheduler.New(interval, pollFn)
	sched.Start()
	defer sched.Stop()

	go pollFn()

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
	dir := filepath.Join(home, ".local", "share", "auto-pr")
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

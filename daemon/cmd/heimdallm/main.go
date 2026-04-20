package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/discovery"
	"github.com/heimdallm/daemon/internal/executor"
	gh "github.com/heimdallm/daemon/internal/github"
	issuepipeline "github.com/heimdallm/daemon/internal/issues"
	"github.com/heimdallm/daemon/internal/keychain"
	"github.com/heimdallm/daemon/internal/notify"
	"github.com/heimdallm/daemon/internal/pipeline"
	"github.com/heimdallm/daemon/internal/scheduler"
	"github.com/heimdallm/daemon/internal/server"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/heimdallm/daemon/internal/store"
	"github.com/heimdallm/daemon/launchagent"
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

	// Resolve the data directory first so setupLogging can mirror the
	// daemon's slog output into <dataDir>/heimdallm.log. The web UI's
	// /logs stream reads that file; writing only to stderr (as we used
	// to) left the stream empty under Docker — see #75.
	logDir := dataDir()
	logCloser := setupLogging(logDir)
	if logCloser != nil {
		// Flush buffered writes on shutdown so the last lines reach
		// disk even when the daemon is killed mid-log.
		defer logCloser.Close()
	}

	cfgPath := configPath()
	var cfg *config.Config
	var err error
	if os.Getenv("HEIMDALLM_DATA_DIR") != "" {
		cfg, err = config.LoadOrCreate(cfgPath)
	} else {
		cfg, err = config.Load(cfgPath)
	}
	if err != nil {
		slog.Error("config load failed", "path", cfgPath, "err", err)
		os.Exit(1)
	}

	token, err := keychain.Get()
	if err != nil {
		slog.Error("token not found", "err", err)
		os.Exit(1)
	}

	dbPath := filepath.Join(dataDir(), "heimdallm.db")
	s, err := store.Open(dbPath)
	if err != nil {
		slog.Error("store open failed", "err", err)
		os.Exit(1)
	}
	defer s.Close()

	// Merge PUT /config values on top of TOML+env. This is the third and
	// highest-precedence layer: UI saves win over env vars, env vars win
	// over TOML. See daemon/internal/config/store.go for the key mapping.
	//
	// Bootstrap treats any failure here as a warning: with no previous
	// in-memory cfg to fall back to, rejecting a startup over a corrupted
	// configs row would lock the operator out. Reload is stricter (below).
	if err := cfg.MergeStoreLayer(s); err != nil {
		slog.Warn("config: store layer not applied, continuing with TOML+env", "err", err)
	}

	if err := s.PurgeOldReviews(cfg.Retention.MaxDays); err != nil {
		slog.Warn("retention purge failed", "err", err)
	}

	broker := sse.NewBroker()
	broker.Start()

	notifier := notify.New()
	ghClient := gh.NewClient(token)
	exec := executor.New()

	// Load or create the per-daemon API token.  All mutating HTTP endpoints
	// require this token in X-Heimdallm-Token (security issue #3).
	apiToken, err := loadOrCreateAPIToken(dataDir())
	if err != nil {
		slog.Error("could not create API token — refusing to start without authentication", "err", err)
		os.Exit(1)
	}

	p := pipeline.New(s, ghClient, exec, &notifyWithSSE{notifier: notifier})
	// GitExec drives the auto_implement flow (#27): branch, commit, push, PR.
	// Wired unconditionally — the pipeline guards against running git ops on
	// an issue that is classified as review_only, so this dep is harmless
	// when auto_implement is not in use.
	issuePipe := issuepipeline.New(s, ghClient, exec, issuepipeline.NewGitExec(), broker, &notifyWithSSE{notifier: notifier})
	issueFetcher := issuepipeline.NewFetcher(ghClient, s, issuePipe)
	srv := server.New(s, broker, p, apiToken)

	// cfgMu protects cfg, sched and the discovery loop so reload is safe from any goroutine.
	var cfgMu sync.Mutex
	var sched *scheduler.Scheduler

	// discoverySvc holds the discovered repo cache; it is nil when topic-based
	// discovery is disabled. discoveryCancel stops the background loop so reload
	// can restart it with fresh config.
	discoverySvc := discovery.NewService(ghClient)
	var discoveryCancel context.CancelFunc

	// loginMu guards cachedLogin against concurrent reads/writes from the
	// poll cycle and HTTP goroutines.
	var loginMu sync.Mutex
	var cachedLogin string

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

		broker.Publish(sse.Event{Type: sse.EventPRDetected, Data: sseData(map[string]any{"pr_number": pr.Number, "repo": pr.Repo})})
		broker.Publish(sse.Event{Type: sse.EventReviewStarted, Data: sseData(map[string]any{"pr_number": pr.Number, "repo": pr.Repo})})
		rev, err := p.Run(pr, buildRunOpts(pr, aiCfg))
		if err != nil {
			slog.Error("pipeline run failed", "repo", pr.Repo, "pr", pr.Number, "err", err)
			broker.Publish(sse.Event{Type: sse.EventReviewError, Data: sseData(map[string]any{"pr_number": pr.Number, "repo": pr.Repo, "error": err.Error()})})
			return
		}
		slog.Info("pipeline: review done",
			"repo", pr.Repo, "number", pr.Number, "severity", rev.Severity,
			"github_review_id", rev.GitHubReviewID)
		broker.Publish(sse.Event{Type: sse.EventReviewCompleted, Data: sseData(map[string]any{
			"pr_number": pr.Number,
			"repo":      pr.Repo,
			"pr_id":     rev.PRID,
			"severity":  rev.Severity,
		})})
	}

	makePollFn := func(c *config.Config) func() {
		return func() {
			cfgMu.Lock()
			static := c.GitHub.Repositories
			cfgMu.Unlock()
			// Merge static list with repos discovered via topic tag (empty when disabled).
			repos := discovery.MergeRepos(static, discoverySvc.Discovered(), c.GitHub.NonMonitored)

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

			// ── Issue tracking cycle ─────────────────────────────────────
			cfgMu.Lock()
			itCfg := c.GitHub.IssueTracking
			cfgMu.Unlock()
			if itCfg.Enabled {
				loginMu.Lock()
				authUser := cachedLogin
				loginMu.Unlock()
				if authUser == "" {
					if u, err := ghClient.AuthenticatedUser(); err == nil {
						authUser = u
						loginMu.Lock()
						cachedLogin = u
						loginMu.Unlock()
					}
				}

				optsFor := func(issue *gh.Issue) issuepipeline.RunOptions {
					cfgMu.Lock()
					aiCfg := c.AIForRepo(issue.Repo)
					if aiCfg.Primary == "" {
						aiCfg.Primary = c.AI.Primary
					}
					agentCfg := c.AgentConfigFor(aiCfg.Primary)
					cfgMu.Unlock()

					extraFlags := agentCfg.ExtraFlags
					if extraFlags != "" {
						if err := executor.ValidateExtraFlags(extraFlags); err != nil {
							slog.Warn("issue poll: extra_flags rejected", "err", err)
							extraFlags = ""
						}
					}

					issuePrompt, issueInstructions := resolveIssuePrompt(s, aiCfg.IssuePrompt, agentCfg.PromptID)
					implPrompt, implInstructions := resolveImplementPrompt(s, aiCfg.ImplementPrompt, agentCfg.PromptID)

					return issuepipeline.RunOptions{
						Primary:  aiCfg.Primary,
						Fallback: aiCfg.Fallback,
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
						IssuePromptOverride:     issuePrompt,
						IssueInstructions:       issueInstructions,
						ImplementPromptOverride: implPrompt,
						ImplementInstructions:   implInstructions,
						PRReviewers: aiCfg.PRReviewers,
						PRAssignee:  aiCfg.PRAssignee,
						PRLabels:    aiCfg.PRLabels,
						PRDraft:     aiCfg.PRDraft,
					}
				}

				// Dependency promotion pass: flip issues carrying a
				// blocked label to the promote-to label once all their
				// declared `## Depends on` targets have closed. Runs BEFORE
				// the fetch so a freshly-promoted issue is picked up in the
				// same poll cycle rather than waiting for the next one.
				// No-op when BlockedLabels is unset, so default installs
				// don't pay for the extra calls.
				if len(itCfg.BlockedLabels) > 0 {
					if n, err := issuepipeline.PromoteReady(context.Background(), ghClient, itCfg, repos); err != nil {
						slog.Error("poll: issue promotion failed", "err", err)
					} else if n > 0 {
						slog.Info("poll: promoted issues", "count", n)
					}
				}

				for _, repo := range repos {
					n, err := issueFetcher.ProcessRepo(context.Background(), repo, itCfg, authUser, optsFor)
					if err != nil {
						slog.Error("poll: issue fetch failed", "repo", repo, "err", err)
						continue
					}
					if n > 0 {
						slog.Info("poll: processed issues", "repo", repo, "count", n)
					}
				}
			}
		}
	}

	startScheduler := func(c *config.Config) *scheduler.Scheduler {
		sc := scheduler.New(parsePollInterval(c.GitHub.PollInterval), makePollFn(c))
		sc.Start()
		return sc
	}

	// startDiscovery spawns the discovery loop when discovery_topic is configured.
	// It returns a cancel func for the running loop, or nil when discovery is off.
	// Must be called with cfgMu held so the caller can swap cancel funcs atomically.
	startDiscovery := func(c *config.Config) context.CancelFunc {
		if c.GitHub.DiscoveryTopic == "" {
			return nil
		}
		interval := parseDiscoveryInterval(c.GitHub.DiscoveryInterval)
		ctx, cancel := context.WithCancel(context.Background())
		topic := c.GitHub.DiscoveryTopic
		orgs := append([]string(nil), c.GitHub.DiscoveryOrgs...)
		if len(orgs) == 0 {
			orgs = discovery.InferOrgs(c.GitHub.Repositories)
		}
		go discoverySvc.Run(ctx, interval, topic, orgs)
		slog.Info("discovery: loop started", "topic", topic, "orgs", orgs, "interval", interval)
		return cancel
	}

	cfgMu.Lock()
	sched = startScheduler(cfg)
	discoveryCancel = startDiscovery(cfg)
	cfgMu.Unlock()

	// Capture the scheduler pointer under mutex so the deferred Stop is safe
	// even if a concurrent reload replaces sched before this goroutine exits.
	defer func() {
		cfgMu.Lock()
		sc := sched
		dc := discoveryCancel
		cfgMu.Unlock()
		sc.Stop()
		if dc != nil {
			dc()
		}
	}()

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
			"issue_tracking": c.GitHub.IssueTracking,
			"repo_overrides": repoOverrides,
			"agent_configs":  agentConfigs,
		}
	})

	// Cache authenticated username for GET /me.
	srv.SetMeFn(func() (string, error) {
		loginMu.Lock()
		if cachedLogin != "" {
			l := cachedLogin
			loginMu.Unlock()
			return l, nil
		}
		loginMu.Unlock()

		login, err := ghClient.AuthenticatedUser()

		loginMu.Lock()
		if err == nil && cachedLogin == "" {
			cachedLogin = login
		}
		loginMu.Unlock()

		return login, err
	})

	// Wire the reload callback: re-read config from disk, restart scheduler
	// and the discovery loop so changes to discovery_topic / orgs / interval
	// take effect without a daemon restart.
	srv.SetReloadFn(func() error {
		newCfg, err := config.Load(cfgPath)
		if err != nil {
			return fmt.Errorf("reload: %w", err)
		}
		// On reload we have a working cfg already — a transient DB error or
		// a corrupted row must NOT silently revert the running daemon to
		// TOML+env and wipe operator customisations. Propagate the error;
		// handleReload returns 500 and the in-memory cfg is untouched.
		if err := newCfg.MergeStoreLayer(s); err != nil {
			return fmt.Errorf("reload: %w", err)
		}

		cfgMu.Lock()
		oldSched := sched
		oldDiscoveryCancel := discoveryCancel
		cfgMu.Unlock()

		// Stop the old discovery loop BEFORE starting the new one. The Search
		// API rate limit (30 req/min) is tight enough that running two loops
		// in parallel — even briefly during reload — risks throttling the
		// daemon.
		if oldDiscoveryCancel != nil {
			oldDiscoveryCancel()
		}

		cfgMu.Lock()
		cfg = newCfg
		sched = startScheduler(newCfg)
		discoveryCancel = startDiscovery(newCfg)
		cfgMu.Unlock()

		// Scheduler overlap is pre-existing behaviour and tolerated; Stop is
		// idempotent and safe to call outside the lock.
		oldSched.Stop()

		// Run first poll immediately with new config
		go makePollFn(newCfg)()
		return nil
	})

	// Wire the trigger-review callback: re-run pipeline on a single stored PR.
	srv.SetTriggerReviewFn(func(prID int64) error {
		publishErr := func(msg string) {
			broker.Publish(sse.Event{
				Type: sse.EventReviewError,
				Data: sseData(map[string]any{"pr_id": prID, "error": msg}),
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
			broker.Publish(sse.Event{Type: sse.EventReviewError, Data: sseData(map[string]any{"pr_id": prID, "error": err.Error()})})
			return err
		}
		broker.Publish(sse.Event{Type: sse.EventReviewCompleted, Data: sseData(map[string]any{
			"pr_number": pr.Number,
			"repo":      pr.Repo,
			"pr_id":     prID,
			"severity":  rev.Severity,
		})})
		return nil
	})

	// Wire the issue-review trigger callback: re-run issue pipeline on a stored issue.
	srv.SetTriggerIssueReviewFn(func(issueID int64) error {
		publishIssueErr := func(msg string) {
			broker.Publish(sse.Event{
				Type: sse.EventIssueReviewError,
				Data: sseData(map[string]any{"issue_id": issueID, "error": msg}),
			})
		}

		iss, err := s.GetIssue(issueID)
		if err != nil {
			publishIssueErr(fmt.Sprintf("Issue not found: %v", err))
			return fmt.Errorf("trigger issue review: get issue %d: %w", issueID, err)
		}

		cfgMu.Lock()
		aiCfg := cfg.AIForRepo(iss.Repo)
		if aiCfg.Primary == "" {
			aiCfg.Primary = cfg.AI.Primary
		}
		agentCfg := cfg.AgentConfigFor(aiCfg.Primary)
		cfgMu.Unlock()

		// Reconstruct github.Issue from store data for the pipeline
		ghIssue := &gh.Issue{
			ID:      iss.GithubID,
			Number:  iss.Number,
			Title:   iss.Title,
			Body:    iss.Body,
			State:   iss.State,
			Repo:    iss.Repo,
			HTMLURL: fmt.Sprintf("https://github.com/%s/issues/%d", iss.Repo, iss.Number),
		}
		ghIssue.User.Login = iss.Author
		ghIssue.Mode = config.IssueModeReviewOnly

		extraFlags := agentCfg.ExtraFlags
		if extraFlags != "" {
			if err := executor.ValidateExtraFlags(extraFlags); err != nil {
				slog.Warn("triggerIssueReview: extra_flags rejected", "err", err)
				extraFlags = ""
			}
		}

		issuePrompt, issueInstructions := resolveIssuePrompt(s, aiCfg.IssuePrompt, agentCfg.PromptID)
		// ImplementPrompt/ImplementInstructions are populated for completeness
		// but are ignored by this path: ghIssue.Mode is forced to review_only
		// above, so runReviewOnly runs and never consults the Implement* fields.
		// Kept in sync with the poll path so the two RunOptions literals stay
		// visually identical and future changes propagate without skew.
		implPrompt, implInstructions := resolveImplementPrompt(s, aiCfg.ImplementPrompt, agentCfg.PromptID)

		opts := issuepipeline.RunOptions{
			Primary:  aiCfg.Primary,
			Fallback: aiCfg.Fallback,
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
			IssuePromptOverride:     issuePrompt,
			IssueInstructions:       issueInstructions,
			ImplementPromptOverride: implPrompt,
			ImplementInstructions:   implInstructions,
			PRReviewers: aiCfg.PRReviewers,
			PRAssignee:  aiCfg.PRAssignee,
			PRLabels:    aiCfg.PRLabels,
			PRDraft:     aiCfg.PRDraft,
		}

		slog.Info("trigger issue review: running pipeline",
			"store_issue_id", issueID, "repo", iss.Repo, "number", iss.Number)

		_, err = issuePipe.Run(context.Background(), ghIssue, opts)
		if err != nil {
			broker.Publish(sse.Event{Type: sse.EventIssueReviewError, Data: sseData(map[string]any{
				"issue_id": issueID, "repo": iss.Repo, "error": err.Error(),
			})})
			return err
		}
		return nil
	})

	// Initial poll
	go makePollFn(cfg)()

	go func() {
		slog.Info("daemon started", "port", cfg.Server.Port, "bind", cfg.Server.BindAddr)
		if err := srv.Start(cfg.Server.Port, cfg.Server.BindAddr); err != nil {
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

// logRotationConfig reads HEIMDALLM_LOG_MAX_MB and HEIMDALLM_LOG_KEEP from
// the environment, falling back to the package defaults. Invalid values
// fall back to the default *and* warn to stderr so operators notice typos
// instead of silently losing the override they thought they had set.
// Logging is non-critical enough that a bad env var should never take the
// daemon down.
func logRotationConfig() (maxBytes int64, keep int) {
	maxBytes = server.DefaultLogMaxBytes
	keep = server.DefaultLogKeep
	if v := os.Getenv("HEIMDALLM_LOG_MAX_MB"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxBytes = int64(n) * 1024 * 1024
		} else {
			fmt.Fprintf(os.Stderr, "heimdallm: ignoring invalid HEIMDALLM_LOG_MAX_MB=%q (want positive integer, using default %d MiB)\n",
				v, server.DefaultLogMaxBytes/(1024*1024))
		}
	}
	if v := os.Getenv("HEIMDALLM_LOG_KEEP"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			keep = n
		} else {
			fmt.Fprintf(os.Stderr, "heimdallm: ignoring invalid HEIMDALLM_LOG_KEEP=%q (want positive integer, using default %d)\n",
				v, server.DefaultLogKeep)
		}
	}
	return
}

// setupLogging configures slog to write to stderr and, when possible, also
// to <dataDir>/heimdallm.log — the file the web UI's /logs endpoint tails
// (see #75). Returns an io.Closer so the caller can flush on shutdown;
// returns nil when we're running stderr-only (either dataDir is empty or
// the file open failed). The daemon never refuses to start because
// logging to disk failed; `docker logs` / the host terminal continue to
// work.
//
// The log file is wrapped in a size-based rotator (see #77). MaxBytes
// and Keep come from HEIMDALLM_LOG_MAX_MB / HEIMDALLM_LOG_KEEP with the
// server package defaults.
func setupLogging(dataDir string) io.Closer {
	handlerOpts := &slog.HandlerOptions{Level: slog.LevelInfo}

	if dataDir == "" {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, handlerOpts)))
		return nil
	}

	logPath := filepath.Join(dataDir, server.DaemonLogFileName)
	maxBytes, keep := logRotationConfig()
	w, err := server.NewRotatingWriter(logPath, maxBytes, keep)
	if err != nil {
		// Warn via a temporary logger that is visible on stderr even
		// before SetDefault runs below.
		tmp := slog.New(slog.NewTextHandler(os.Stderr, handlerOpts))
		tmp.Warn("logging: could not open daemon log file, stderr only",
			"path", logPath, "err", err)
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, handlerOpts)))
		return nil
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(io.MultiWriter(os.Stderr, w), handlerOpts)))
	return w
}

// dataDir resolves the data directory.
// Priority: HEIMDALLM_DATA_DIR env > /data (Docker) > ~/.local/share/heimdallm
func dataDir() string {
	if v := os.Getenv("HEIMDALLM_DATA_DIR"); v != "" {
		os.MkdirAll(v, 0700)
		return v
	}
	if info, err := os.Stat("/data"); err == nil && info.IsDir() {
		return "/data"
	}
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".local", "share", "heimdallm")
	os.MkdirAll(dir, 0700)
	return dir
}

// configPath resolves the config file location.
// Priority: HEIMDALLM_CONFIG_PATH env > /config/config.toml (Docker) > ~/.config/heimdallm/config.toml
func configPath() string {
	if v := os.Getenv("HEIMDALLM_CONFIG_PATH"); v != "" {
		return v
	}
	if info, err := os.Stat("/config"); err == nil && info.IsDir() {
		return "/config/config.toml"
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "heimdallm", "config.toml")
}

func parsePollInterval(s string) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 5 * time.Minute
	}
	return d
}

// parseDiscoveryInterval falls back to 15m when the value is empty or invalid.
// Config.Validate rejects invalid durations before we reach here, so the
// fallback only covers the unset-in-TOML-but-topic-defaulted case.
func parseDiscoveryInterval(s string) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 15 * time.Minute
	}
	return d
}

type notifyWithSSE struct {
	notifier *notify.Notifier
}

func (n *notifyWithSSE) Notify(title, message string) {
	n.notifier.Notify(title, message)
}

// tokenFileMode is the permission mask for <dataDir>/api_token.
//
// 0644 (world-readable) is deliberate: the file lives on a Docker volume
// that is private to the compose stack and is consumed by two services we
// control (the daemon that writes it and the SvelteKit web UI that reads
// it). Those services run under different UIDs in their respective images
// (daemon: heimdallm UID 100; web: node UID 1000), so the previous 0600
// blocked the web container from reading the token via the shared volume,
// forcing operators to run `make setup` as a manual workaround. See #71.
const tokenFileMode = 0644

// loadOrCreateAPIToken reads an existing token from <dataDir>/api_token, or
// generates a new cryptographically-random one and writes it with
// tokenFileMode. The token is used by the HTTP server to authenticate all
// mutating requests (POST/PUT/DELETE) — see security issue #3.
//
// SECURITY (M-4): Uses O_CREATE|O_EXCL to create the file atomically. If two
// daemon instances race, only one will win the exclusive create; the other reads
// the file that was created by the winner, ensuring both instances share the
// same token rather than silently diverging.
func loadOrCreateAPIToken(dir string) (string, error) {
	path := filepath.Join(dir, "api_token")

	// Try to read existing token first.
	data, err := os.ReadFile(path)
	if err == nil {
		tok := strings.TrimSpace(string(data))
		if len(tok) >= 32 {
			// Best-effort upgrade for tokens written by older daemons with
			// mode 0600 — see tokenFileMode comment above. Errors are logged
			// but non-fatal: the daemon itself can still read the token, and
			// `make setup` remains a viable fallback.
			if err := os.Chmod(path, tokenFileMode); err != nil {
				slog.Warn("api_token: could not upgrade permissions", "path", path, "err", err)
			}
			return tok, nil
		}
	}

	// Generate a new 32-byte random token (64 hex chars).
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("api_token: generate random: %w", err)
	}
	tok := hex.EncodeToString(buf)

	// Use O_CREATE|O_EXCL for atomic creation: if another process created the
	// file between our ReadFile and here, os.OpenFile returns an error that
	// satisfies os.IsExist — we then read the file created by the other process.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, tokenFileMode)
	if err != nil {
		if os.IsExist(err) {
			// Another process created the file first — read their token.
			data2, readErr := os.ReadFile(path)
			if readErr != nil {
				return "", fmt.Errorf("api_token: read after race: %w", readErr)
			}
			existing := strings.TrimSpace(string(data2))
			if len(existing) >= 32 {
				return existing, nil
			}
		}
		return "", fmt.Errorf("api_token: create %s: %w", path, err)
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "%s\n", tok); err != nil {
		return "", fmt.Errorf("api_token: write %s: %w", path, err)
	}
	// os.OpenFile's mode arg is masked by the process umask (typically 0022),
	// which would leave the file 0644 anyway — but chmod explicitly so the
	// final mode is deterministic regardless of the daemon's umask at startup.
	if err := os.Chmod(path, tokenFileMode); err != nil {
		slog.Warn("api_token: could not set permissions", "path", path, "err", err)
	}
	slog.Info("api_token: created new token", "path", path)
	return tok, nil
}

// resolveAgentByPriority returns the Agent selected by the 3-level priority
// that every prompt-customisation feature in this daemon uses:
//
//  1. repoPromptID — repo-level override (from [ai.repos."org/repo"] *_prompt)
//  2. agentPromptID — agent-level override (from [ai.agents.<cli>] prompt)
//  3. global default agent (is_default = true)
//
// Returns nil when nothing matches (or when ListAgents errors — the caller
// should treat this as "use the built-in default template"). Each resolver
// above this function then reads its own field pair from the returned Agent,
// so adding a third prompt type is a 4-line wrapper rather than a copied
// 30-line loop.
func resolveAgentByPriority(s *store.Store, repoPromptID, agentPromptID string) *store.Agent {
	agents, err := s.ListAgents()
	if err != nil || len(agents) == 0 {
		return nil
	}

	// 1. Repo-level override
	if repoPromptID != "" {
		for _, ag := range agents {
			if ag.ID == repoPromptID {
				return ag
			}
		}
	}
	// 2. Agent-level override
	if agentPromptID != "" {
		for _, ag := range agents {
			if ag.ID == agentPromptID {
				return ag
			}
		}
	}
	// 3. Global default
	for _, ag := range agents {
		if ag.IsDefault {
			return ag
		}
	}
	return nil
}

// resolveIssuePrompt returns (customTemplate, customInstructions) for the
// issue-triage prompt. Agent selection follows resolveAgentByPriority;
// IssuePrompt takes precedence over IssueInstructions (same as Prompt vs
// Instructions for PR reviews). Both empty = use built-in default template.
func resolveIssuePrompt(s *store.Store, repoPromptID, agentPromptID string) (string, string) {
	a := resolveAgentByPriority(s, repoPromptID, agentPromptID)
	if a == nil {
		return "", ""
	}
	if a.IssuePrompt != "" {
		return a.IssuePrompt, ""
	}
	return "", a.IssueInstructions
}

// resolveImplementPrompt returns (customTemplate, customInstructions) for the
// auto_implement code-generation prompt. Same selection rules as
// resolveIssuePrompt; ImplementPrompt takes precedence over
// ImplementInstructions. Both empty = use built-in default template.
func resolveImplementPrompt(s *store.Store, repoPromptID, agentPromptID string) (string, string) {
	a := resolveAgentByPriority(s, repoPromptID, agentPromptID)
	if a == nil {
		return "", ""
	}
	if a.ImplementPrompt != "" {
		return a.ImplementPrompt, ""
	}
	return "", a.ImplementInstructions
}

// sseData serializes a map to a compact JSON string for SSE event Data fields.
// Using json.Marshal instead of fmt.Sprintf/%q avoids encoding divergence with
// Unicode or special characters in error messages and repo names.
func sseData(v map[string]any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

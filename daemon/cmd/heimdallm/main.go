package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
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

	"github.com/heimdallm/daemon/internal/activity"
	"github.com/heimdallm/daemon/internal/bus"
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
	"github.com/heimdallm/daemon/internal/worker"
	"github.com/heimdallm/daemon/launchagent"
	"github.com/nats-io/nats.go"
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

	// loadConfig is captured once at startup so the reload path further
	// down cannot drift: both read the same env-var and select the same
	// loader. Docker deployments (HEIMDALLM_DATA_DIR set) use LoadOrCreate
	// so a missing config.toml is not fatal — the daemon rebuilds from env
	// vars. Desktop deployments use Load; the Flutter app is expected to
	// have written the TOML before the daemon starts, so ENOENT is a real
	// error there.
	loadConfig := config.Load
	if os.Getenv("HEIMDALLM_DATA_DIR") != "" {
		loadConfig = config.LoadOrCreate
	}

	cfg, err := loadConfig(cfgPath)
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

	if err := s.PurgeOldActivity(*cfg.ActivityLog.RetentionDays); err != nil {
		slog.Warn("activity retention purge failed", "err", err)
	}

	// Clear in-flight review claims leaked by a daemon that crashed between
	// claim and release. The 30-minute cutoff gives normal reviews (which
	// finish in seconds-minutes) plenty of headroom while still cleaning up
	// anything stuck from a previous process. See theburrowhub/heimdallm#243.
	if n, err := s.ClearStaleInFlight(30 * time.Minute); err != nil {
		slog.Warn("startup: clear stale inflight failed", "err", err)
	} else if n > 0 {
		slog.Info("startup: cleared stale inflight rows", "count", n)
	}

	// Mirror of the PR-side sweep above for issue-triage claims (#292).
	if n, err := s.ClearStaleIssueTriageInFlight(30 * time.Minute); err != nil {
		slog.Warn("startup: clear stale issue triage inflight failed", "err", err)
	} else if n > 0 {
		slog.Info("startup: cleared stale issue triage inflight rows", "count", n)
	}

	// ── NATS event bus (core only, no JetStream) ───────────────────────
	eventBus := bus.New(bus.Config{
		MaxConcurrentWorkers: cfg.Server.MaxConcurrentWorkers,
	})
	if err := eventBus.Start(context.Background()); err != nil {
		slog.Error("nats bus failed to start", "err", err)
		os.Exit(1)
	}
	defer eventBus.Stop()

	// ── Watch store (SQLite, replaces JetStream KV) ─────────────────
	watchStore, err := bus.NewWatchStore(s.DB())
	if err != nil {
		slog.Error("watch store failed to initialize", "err", err)
		os.Exit(1)
	}

	broker := sse.NewBroker()
	broker.Start()

	// ── Bridge: SSE broker → NATS events ────────────────────────────────
	// Re-publishes every broker event to NATS so the SSE handler (which
	// now reads from NATS) receives events from all existing publishers.
	// This bridge is interim — Task 12 will have workers publish directly
	// to NATS events subjects, removing the need for the broker entirely.
	bridgeCh := broker.Subscribe()
	if bridgeCh != nil {
		go func() {
			for event := range bridgeCh {
				subj := "heimdallm.events." + event.Type
				if err := eventBus.Conn().Publish(subj, []byte(event.Data)); err != nil {
					slog.Warn("sse-bridge: publish to NATS failed", "type", event.Type, "err", err)
				}
			}
		}()
	} else {
		slog.Warn("sse-bridge: broker subscriber cap reached, SSE bridge disabled")
	}

	// ActivityRecorder subscribes to the broker and writes a row into
	// activity_log for every significant event. Disabled → not constructed.
	// A nil broker subscription (subscriber cap reached) is a warning, not
	// a fatal — activity logging is optional.
	// applyDefaults guarantees Enabled is non-nil before we reach here.
	if *cfg.ActivityLog.Enabled {
		rec := activity.New(s, broker)
		if rec == nil {
			slog.Warn("activity: broker subscriber cap reached; activity log will not record this session")
		} else {
			activityCtx, activityCancel := context.WithCancel(context.Background())
			defer activityCancel()
			go rec.Start(activityCtx)
			slog.Info("activity recorder started")
		}

		// Activity retention ticker. The startup purge above runs once; this
		// keeps the log bounded for long-running daemons. Only ticks when
		// activity recording is enabled — a disabled session has nothing new
		// to prune beyond what startup already handled.
		activityPurge := scheduler.New(24*time.Hour, func() {
			if err := s.PurgeOldActivity(*cfg.ActivityLog.RetentionDays); err != nil {
				slog.Warn("activity retention purge failed", "err", err)
			}
		})
		activityPurge.Start()
		defer activityPurge.Stop()
	}

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

	// Circuit-breaker caps (see theburrowhub/heimdallm#243). The defaults are
	// populated by config.applyDefaults so the caps are always set; nil disables
	// them only if a downstream test wants unbounded behaviour.
	cbLimits := store.CircuitBreakerLimits{
		PerPR24h:  cfg.CircuitBreaker.PerPR24h,
		PerRepoHr: cfg.CircuitBreaker.PerRepoHr,
	}
	p.SetCircuitBreakerLimits(&cbLimits)

	// Wire the GitHub client as the timeline fetcher so the SHA-skip
	// path can detect explicit re-request review actions and bypass the
	// dedup. See theburrowhub/heimdallm#322 Bug 5. Requires the bot
	// login resolved below; the pipeline no-ops the bypass if either
	// p.timeline or p.botLogin is unset.
	p.SetTimelineFetcher(ghClient)

	// Wire the SSE broker as the lifecycle publisher so Run emits
	// pr_detected / review_started / review_completed / review_skipped
	// at the correct semantic point (after the SHA-skip + gate
	// decisions). The caller used to publish these blindly at function
	// entry, leaving Flutter spinners colgados on every SHA-skip and
	// firing phantom desktop notifications. See #322 Bugs 3+4.
	p.SetPublisher(broker)

	// Issue-side circuit-breaker caps (theburrowhub/heimdallm#292) — mirrors
	// the PR-side defenses against runaway triage loops.
	issueCBLimits := store.IssueCircuitBreakerLimits{
		PerIssue24h: cfg.CircuitBreaker.PerIssue24h,
		PerRepoHr:   cfg.CircuitBreaker.PerIssueRepoHr,
	}

	// GitExec drives the auto_implement flow (#27): branch, commit, push, PR.
	// Wired unconditionally — the pipeline guards against running git ops on
	// an issue that is classified as review_only, so this dep is harmless
	// when auto_implement is not in use.
	issuePipe := issuepipeline.New(s, ghClient, exec, issuepipeline.NewGitExec(), broker, &notifyWithSSE{notifier: notifier})
	issuePipe.SetCircuitBreakerLimits(&issueCBLimits)

	// Resolve bot login for re-review / re-triage context filtering.
	var resolvedBotLogin string
	if login, err := ghClient.AuthenticatedUser(); err == nil {
		resolvedBotLogin = login
		p.SetBotLogin(login)
		issuePipe.SetBotLogin(login)
		slog.Info("bot login resolved", "login", login)
	} else {
		slog.Warn("could not resolve bot login for re-review context", "err", err)
	}
	issueFetcher := issuepipeline.NewFetcher(ghClient, ghClient, s, issuePipe)
	issueFetcher.SetBotLogin(resolvedBotLogin) // break re-triage loop (#362)
	srv := server.New(s, broker, p, apiToken)
	srv.SetNATSConn(eventBus.Conn())
	srv.SetConfigPath(cfgPath)

	// cfgMu protects cfg and the pipeline so reload is safe from any goroutine.
	var cfgMu sync.Mutex
	var reloadMu sync.Mutex // serialises config reloads to prevent duplicate pipelines

	// discoverySvc holds the discovered repo cache.
	discoverySvc := discovery.NewService(ghClient)

	// loginMu guards cachedLogin against concurrent reads/writes from the
	// poll cycle and HTTP goroutines.
	var loginMu sync.Mutex
	var cachedLogin string

	buildRunOpts := func(pr *gh.PullRequest, aiCfg config.RepoAI) pipeline.RunOptions {
		cli := aiCfg.Primary
		if cli == "" {
			cli = cfg.AI.Primary
		}
		// Resolve botLogin once using cached value
		loginMu.Lock()
		botLogin := cachedLogin
		loginMu.Unlock()

		cfgMu.Lock()
		agentCfg := cfg.AgentConfigFor(cli)
		globalTimeout := cfg.AI.ExecutionTimeout
		// Convert config.ResolvedReviewGuards to pipeline.GateConfig via same-shape cast.
		// config cannot import pipeline (import cycle), so the helper returns a shadow
		// type that callers cast here.
		guards := pipeline.GateConfig(cfg.ReviewGuards(botLogin))
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
				Timeout:              resolveExecutionTimeout(globalTimeout, agentCfg.ExecutionTimeout),
			},
			Guards: guards,
		}
	}

	runReview := func(pr *gh.PullRequest, aiCfg config.RepoAI) *store.Review {
		// Persistent in-flight claim: survives daemon restart and config reload.
		// Keyed on (pr_id, head_sha) so a new commit on the same PR is not
		// gated by a stale in-flight row from a prior HEAD. See
		// theburrowhub/heimdallm#243.
		//
		// For PRs where the HEAD SHA is not yet known, skip the claim —
		// the downstream SHA dedup in pipeline.Run (already fail-closed per
		// Task 1) handles that path.
		//
		// On Claim error (transient SQLite blip, disk pressure), we log and
		// proceed fail-open. This is safe because the downstream defenses
		// ALREADY bound the worst-case cost of a slipped review:
		//   1. pipeline.Run's HEAD-SHA guard is fail-closed (Task 1 / PR #245) —
		//      a second daemon running the same SHA is rejected.
		//   2. The SQLite-backed circuit breaker caps reviews at
		//      3/PR/24h + 20/repo/hour (Task 2 / PR #246) — worst case is
		//      a handful of reviews, not the €1,300 incident.
		//   3. PRAlreadyReviewed uses PublishedAt + 2-min grace (Task 3 / PR #247) —
		//      the common "bot bumped updated_at" case is still dedup'd even
		//      without the persistent claim.
		// Fail-closed here would block legitimate reviews on a transient DB
		// error; the layered defenses make fail-open the right trade.
		//
		// Always use the GitHub-assigned ID for the in-flight claim key.
		// This avoids mixing two ID namespaces (internal SQLite autoincrement
		// vs GitHub global ID) in the same reviews_in_flight.pr_id column,
		// which would let a cold-start claim and a post-upsert retry both
		// succeed for the same (PR, SHA) pair (#359).
		claimPRID := pr.ID

		var claimed bool
		var claimSHA string
		if pr.Head.SHA != "" {
			ok, err := s.ClaimInFlightReview(claimPRID, pr.Head.SHA)
			if err != nil {
				slog.Warn("runReview: claim inflight failed, proceeding", "err", err)
			} else if !ok {
				slog.Info("runReview: already in flight (persistent), skipping",
					"pr", pr.Number, "repo", pr.Repo, "head_sha", pr.Head.SHA)
				return nil
			} else {
				claimed = true
				claimSHA = pr.Head.SHA
			}
		} else {
			slog.Info("runReview: in-flight claim skipped (defenses still apply)",
				"pr", pr.Number, "repo", pr.Repo, "reason", "empty Head.SHA from caller")
		}
		defer func() {
			if claimed {
				if err := s.ReleaseInFlightReview(claimPRID, claimSHA); err != nil {
					slog.Warn("runReview: release inflight failed", "err", err,
						"pr_id", claimPRID, "head_sha", claimSHA)
				}
			}
		}()

		// Caller-side gate: evaluate review guards BEFORE announcing the review.
		// This prevents review_started from being emitted for PRs that will be
		// rejected, which would leave the Flutter dashboard spinner stuck forever.
		loginMu.Lock()
		botLogin := cachedLogin
		loginMu.Unlock()
		cfgMu.Lock()
		guards := pipeline.GateConfig(cfg.ReviewGuards(botLogin))
		cfgMu.Unlock()
		if reason := pipeline.Evaluate(pipeline.PRGate{
			State:  pr.State,
			Draft:  pr.Draft,
			Author: pr.User.Login,
		}, guards); reason != pipeline.SkipReasonNone {
			broker.Publish(sse.Event{
				Type: sse.EventReviewSkipped,
				Data: sseData(map[string]any{
					"repo":      pr.Repo,
					"pr_number": pr.Number,
					"pr_title":  pr.Title,
					"reason":    string(reason),
				}),
			})
			slog.Info("runReview: skipping PR",
				"repo", pr.Repo, "pr", pr.Number, "reason", string(reason))
			return nil
		}

		// Safety check: log exactly what we're about to review
		slog.Info("pipeline: reviewing PR",
			"repo", pr.Repo, "number", pr.Number, "github_id", pr.ID, "title", pr.Title)

		// Lifecycle SSEs (pr_detected, review_started, review_completed,
		// review_skipped) are published from within p.Run via its
		// Publisher dependency — this caller only handles the error
		// paths because they need contextual error data the pipeline
		// doesn't pre-shape (the err.Error() string and the
		// CircuitBreakerError discriminant). See theburrowhub/heimdallm#322
		// Bugs 3+4 for the regression that made emitting from here unsafe.
		rev, err := p.Run(pr, buildRunOpts(pr, aiCfg))
		if err != nil {
			slog.Error("pipeline run failed", "repo", pr.Repo, "pr", pr.Number, "err", err)
			var cbErr *pipeline.CircuitBreakerError
			if errors.As(err, &cbErr) {
				broker.Publish(sse.Event{
					Type: sse.EventCircuitBreakerTripped,
					Data: sseData(map[string]any{
						"pr_number": pr.Number,
						"repo":      pr.Repo,
						"reason":    cbErr.Reason,
					}),
				})
				return nil
			}
			broker.Publish(sse.Event{Type: sse.EventReviewError, Data: sseData(map[string]any{"pr_number": pr.Number, "repo": pr.Repo, "error": err.Error()})})
			return nil
		}
		if rev == nil {
			// Pipeline took a skip path and already emitted
			// EventReviewSkipped with the correct reason
			// (sha_unchanged / legacy_backfill / not_open / draft /
			// self_authored). Nothing else to do.
			return nil
		}
		slog.Info("pipeline: review done",
			"repo", pr.Repo, "number", pr.Number, "severity", rev.Severity,
			"github_review_id", rev.GitHubReviewID)
		return rev
	}

	// ── Standalone pollers (replaced the Pipeline orchestrator) ─────────
	conn := eventBus.Conn()
	maxWorkers := eventBus.MaxConcurrentWorkers()
	publishPub := bus.NewPRPublishPublisher(conn)
	issuePublisher := bus.NewIssuePublisher(conn)
	issueFetcher.SetPublisher(issuePublisher)

	// Shared rate limiter (was Pipeline.limiter).
	limiter := scheduler.NewRateLimiter(4500)

	// tier2Adapter bridges main.go's concrete types to the polling logic.
	adapter := &tier2Adapter{
		ghClient:             ghClient,
		ghToken:              token,
		pipeline:             p,
		issuePipe:            issuePipe,
		fetcher:              issueFetcher,
		store:                s,
		broker:               broker,
		cfgMu:                &cfgMu,
		cfg:                  &cfg,
		loginMu:              &loginMu,
		login:                &cachedLogin,
		runReview:            runReview,
		publishPub:           publishPub,
		watchStore:           watchStore,
		lastSkippedUpdatedAt: make(map[int64]time.Time),
	}

	repoPublisher := bus.NewRepoPublisher(conn)
	prReviewPublisher := bus.NewPRReviewPublisher(conn)

	// reposChan bridges Tier 1 (discovery) → Tier 2 (per-repo polling) via
	// the NATS discovery stream. Tier 1 publishes to NATS, the bridge
	// consumes from NATS and forwards repo lists through this channel.
	reposChan := make(chan []string, 1)

	// startPollers launches all polling goroutines under the given context.
	// Returns a cancel function and a WaitGroup that completes when all
	// goroutines have exited.
	startPollers := func(ctx context.Context, coldStart bool) (context.CancelFunc, *sync.WaitGroup) {
		ctx, cancel := context.WithCancel(ctx)
		var wg sync.WaitGroup

		// Rate limiter hourly refill
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(1 * time.Hour)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					limiter.Refill()
					slog.Info("pollers: rate limiter refilled")
				}
			}
		}()

		// Tier 1: Discovery — publishes to NATS
		cfgMu.Lock()
		discoveryInterval := parseDiscoveryInterval(cfg.GitHub.DiscoveryInterval)
		cfgMu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			tier1ConfigFn := func() scheduler.Tier1Config {
				cfgMu.Lock()
				defer cfgMu.Unlock()
				orgs := append([]string(nil), cfg.GitHub.DiscoveryOrgs...)
				if len(orgs) == 0 {
					orgs = discovery.InferOrgs(cfg.GitHub.Repositories)
				}
				return scheduler.Tier1Config{
					StaticRepos:    cfg.GitHub.Repositories,
					NonMonitored:   cfg.GitHub.NonMonitored,
					DiscoveryTopic: cfg.GitHub.DiscoveryTopic,
					DiscoveryOrgs:  orgs,
				}
			}

			// Publish initial repos immediately
			sendDiscoveryRepos(ctx, discoverySvc, limiter, repoPublisher, tier1ConfigFn)

			ticker := time.NewTicker(discoveryInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if err := limiter.Acquire(ctx, scheduler.TierDiscovery); err != nil {
						return
					}
					sendDiscoveryRepos(ctx, discoverySvc, limiter, repoPublisher, tier1ConfigFn)
				}
			}
		}()

		// Bridge: NATS discovery subscription → reposChan
		wg.Add(1)
		go func() {
			defer wg.Done()
			bridgeDiscovery(ctx, conn, reposChan)
		}()

		// Tier 2: PR / issue polling
		cfgMu.Lock()
		pollInterval := parsePollInterval(cfg.GitHub.PollInterval)
		cfgMu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			tier2ConfigFn := func() []string {
				cfgMu.Lock()
				defer cfgMu.Unlock()
				return discovery.MergeRepos(cfg.GitHub.Repositories, discoverySvc.Discovered(), cfg.GitHub.NonMonitored)
			}
			runTier2(ctx, adapter, limiter, prReviewPublisher, tier2ConfigFn, reposChan, pollInterval, coldStart)
		}()

		slog.Info("pollers: started",
			"discovery", discoveryInterval,
			"poll", pollInterval)

		return cancel, &wg
	}

	// Initial daemon start → coldStart=true so Tier 2 fires its first tick
	// immediately; operators see polling activity without waiting an entire
	// PollInterval. The reload path below passes false.
	pollerCancel, pollerWg := startPollers(context.Background(), true)

	// ── NATS PR review worker ───────────────────────────────────────────
	// Consumes PR review requests published by Tier 2 and runs the
	// existing review pipeline. This replaces the goroutine-per-PR
	// pattern that Tier 2 used to use.
	reviewHandler := func(ctx context.Context, msg bus.PRReviewMsg) {
		// Acquire returns only ctx.Err() (shutdown). On cancellation the
		// message is acked without processing — acceptable because the
		// daemon is shutting down and the PR will be re-detected next startup.
		if err := limiter.Acquire(ctx, scheduler.TierRepo); err != nil {
			return
		}

		pr, err := ghClient.GetPR(msg.Repo, msg.Number)
		if err != nil {
			slog.Error("review-worker: fetch PR from GitHub",
				"repo", msg.Repo, "pr", msg.Number, "err", err)
			return
		}
		// Stale message guard: if HEAD SHA changed since publish, skip.
		// The next poll cycle will publish a new message with the updated SHA.
		if msg.HeadSHA != "" && pr.Head.SHA != msg.HeadSHA {
			slog.Info("review-worker: stale message (HEAD SHA changed), skipping",
				"repo", msg.Repo, "pr", msg.Number,
				"msg_sha", msg.HeadSHA, "current_sha", pr.Head.SHA)
			return
		}

		cfgMu.Lock()
		c := *cfg
		aiCfg := c.AIForRepo(pr.Repo)
		localDirBase := c.GitHub.LocalDirBase
		cfgMu.Unlock()
		aiCfg.LocalDir = config.ResolveLocalDir(aiCfg.LocalDir, pr.Repo, localDirBase)

		rev := runReview(pr, aiCfg)

		// If review succeeded but wasn't published to GitHub yet,
		// enqueue for the publish worker.
		if rev != nil && rev.GitHubReviewID == 0 {
			if err := publishPub.PublishPRPublish(ctx, rev.ID); err != nil {
				slog.Warn("review-worker: failed to enqueue publish",
					"review_id", rev.ID, "err", err)
			}
		}

		// Enroll for state watching via SQLite watch store.
		if err := watchStore.Enroll(ctx, "pr", pr.Repo, pr.Number, pr.ID); err != nil {
			slog.Warn("review-worker: failed to enroll watch",
				"repo", pr.Repo, "pr", pr.Number, "err", err)
		}
	}

	reviewWorker := worker.NewReviewWorker(conn, maxWorkers, reviewHandler)
	workerCtx, workerCancel := context.WithCancel(context.Background())
	defer workerCancel()
	go func() {
		if err := reviewWorker.Start(workerCtx); err != nil {
			slog.Error("review worker stopped", "err", err)
		}
	}()

	// ── NATS PR publish worker ──────────────────────────────────────────
	// Consumes publish requests and submits stored reviews to GitHub.
	// Replaces the manual retry loop in PublishPending with NATS retry
	// semantics (NakWithDelay for transient GitHub errors).
	publishHandler := func(ctx context.Context, msg bus.PRPublishMsg) error {
		rev, err := s.GetReview(msg.ReviewID)
		if err != nil {
			slog.Warn("publish-worker: review not found, skipping",
				"review_id", msg.ReviewID, "err", err)
			return nil // permanent — ack
		}
		if rev.GitHubReviewID != 0 {
			slog.Info("publish-worker: already published, skipping",
				"review_id", msg.ReviewID, "github_review_id", rev.GitHubReviewID)
			return nil // idempotent — ack
		}

		pr, err := s.GetPR(rev.PRID)
		if err != nil {
			slog.Warn("publish-worker: PR not found, marking orphaned",
				"review_id", msg.ReviewID, "pr_id", rev.PRID, "err", err)
			_ = s.MarkReviewPublished(rev.ID, -1, "", time.Now().UTC())
			return nil // permanent — ack
		}
		if pr.Repo == "" {
			slog.Info("publish-worker: PR has no repo, marking orphaned",
				"review_id", msg.ReviewID)
			_ = s.MarkReviewPublished(rev.ID, -1, "", time.Now().UTC())
			return nil // permanent — ack
		}

		// Rebuild ReviewResult from stored JSON
		var issues []executor.Issue
		if err := json.Unmarshal([]byte(rev.Issues), &issues); err != nil {
			slog.Error("publish-worker: corrupt issues JSON, skipping",
				"review_id", msg.ReviewID, "err", err)
			return nil // permanent — ack
		}
		result := &executor.ReviewResult{
			Summary:  rev.Summary,
			Issues:   issues,
			Severity: rev.Severity,
		}

		if err := limiter.Acquire(ctx, scheduler.TierRepo); err != nil {
			return fmt.Errorf("rate limit cancelled: %w", err)
		}

		ghID, ghState, err := ghClient.SubmitReview(
			pr.Repo, pr.Number,
			pipeline.BuildGitHubBody(result),
			pipeline.SeverityToEvent(rev.Severity, len(issues)),
		)
		if err != nil {
			errStr := err.Error()
			// 4xx errors (except 429 rate limit) are permanent — no point retrying.
			// 5xx and network errors are transient — nak for NATS retry.
			if strings.Contains(errStr, "status 4") && !strings.Contains(errStr, "status 429") {
				slog.Error("publish-worker: permanent GitHub error, marking orphaned",
					"review_id", msg.ReviewID, "err", err)
				_ = s.MarkReviewPublished(msg.ReviewID, -1, "", time.Now().UTC())
				return nil // permanent — ack
			}
			return fmt.Errorf("submit review to GitHub: %w", err)
		}

		publishedAt := time.Now().UTC()
		if err := s.MarkReviewPublished(rev.ID, ghID, ghState, publishedAt); err != nil {
			slog.Warn("publish-worker: failed to mark published",
				"review_id", rev.ID, "err", err)
		}
		slog.Info("publish-worker: review published",
			"review_id", rev.ID, "github_review_id", ghID,
			"github_review_state", ghState)
		return nil // success — ack
	}

	publishW := worker.NewPublishWorker(conn, maxWorkers, publishHandler)
	publishWCtx, publishWCancel := context.WithCancel(context.Background())
	defer publishWCancel()
	go func() {
		if err := publishW.Start(publishWCtx); err != nil {
			slog.Error("publish worker stopped", "err", err)
		}
	}()

	// ── NATS issue triage worker ────────────────────────────────────────
	// Consumes triage requests published by the Fetcher when it classifies
	// an issue as review_only. Fetches the issue from GitHub for fresh data,
	// resolves per-repo config, and runs the issue pipeline.
	triageHandler := func(ctx context.Context, msg bus.IssueMsg) {
		ghIssue, err := ghClient.GetIssue(msg.Repo, msg.Number)
		if err != nil {
			slog.Error("triage-worker: fetch issue from GitHub",
				"repo", msg.Repo, "number", msg.Number, "err", err)
			return
		}
		ghIssue.Mode = config.IssueModeReviewOnly

		cfgMu.Lock()
		c := *cfg
		aiCfg := c.AIForRepo(msg.Repo)
		if aiCfg.Primary == "" {
			aiCfg.Primary = c.AI.Primary
		}
		agentCfg := c.AgentConfigFor(aiCfg.Primary)
		localDirBase := c.GitHub.LocalDirBase
		globalTimeout := c.AI.ExecutionTimeout
		cfgMu.Unlock()
		aiCfg.LocalDir = config.ResolveLocalDir(aiCfg.LocalDir, msg.Repo, localDirBase)

		extraFlags := agentCfg.ExtraFlags
		if extraFlags != "" {
			if err := executor.ValidateExtraFlags(extraFlags); err != nil {
				slog.Warn("triage-worker: extra_flags rejected", "err", err)
				extraFlags = ""
			}
		}

		issuePrompt, issueInstructions := resolveIssuePrompt(s, aiCfg.IssuePrompt, agentCfg.PromptID)
		implPrompt, implInstructions := resolveImplementPrompt(s, aiCfg.ImplementPrompt, agentCfg.PromptID)

		opts := issuepipeline.RunOptions{
			GitHubToken: token,
			Primary:     aiCfg.Primary,
			Fallback:    aiCfg.Fallback,
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
				Timeout:              resolveExecutionTimeout(globalTimeout, agentCfg.ExecutionTimeout),
			},
			IssuePromptOverride:     issuePrompt,
			IssueInstructions:       issueInstructions,
			ImplementPromptOverride: implPrompt,
			ImplementInstructions:   implInstructions,
			PRReviewers:             aiCfg.PRReviewers,
			PRAssignee:              aiCfg.PRAssignee,
			PRLabels:                aiCfg.PRLabels,
			PRDraft:                 aiCfg.PRDraft != nil && *aiCfg.PRDraft,
			GeneratePRDescription:   aiCfg.GeneratePRDescription != nil && *aiCfg.GeneratePRDescription,
		}

		if _, err := issuePipe.Run(ctx, ghIssue, opts); err != nil {
			slog.Error("triage-worker: pipeline run failed",
				"repo", msg.Repo, "number", msg.Number, "err", err)
		}

		// Enroll for state watching so closed/resolved issues update in the UI.
		// Runs even after pipeline failure — state tracking is independent of
		// pipeline success, and we want the UI to reflect closures regardless.
		if err := watchStore.Enroll(ctx, "issue", msg.Repo, msg.Number, msg.GithubID); err != nil {
			slog.Warn("triage-worker: failed to enroll watch",
				"repo", msg.Repo, "number", msg.Number, "err", err)
		}
	}

	triageW := worker.NewTriageWorker(conn, maxWorkers, triageHandler)
	triageWCtx, triageWCancel := context.WithCancel(context.Background())
	defer triageWCancel()
	go func() {
		if err := triageW.Start(triageWCtx); err != nil {
			slog.Error("triage worker stopped", "err", err)
		}
	}()

	// ── NATS issue implement worker ─────────────────────────────────────
	// Consumes implement requests published by the Fetcher when it classifies
	// an issue as develop. Same config resolution as triage, different mode.
	implementHandler := func(ctx context.Context, msg bus.IssueMsg) {
		ghIssue, err := ghClient.GetIssue(msg.Repo, msg.Number)
		if err != nil {
			slog.Error("implement-worker: fetch issue from GitHub",
				"repo", msg.Repo, "number", msg.Number, "err", err)
			return
		}
		ghIssue.Mode = config.IssueModeDevelop

		cfgMu.Lock()
		c := *cfg
		aiCfg := c.AIForRepo(msg.Repo)
		if aiCfg.Primary == "" {
			aiCfg.Primary = c.AI.Primary
		}
		agentCfg := c.AgentConfigFor(aiCfg.Primary)
		localDirBase := c.GitHub.LocalDirBase
		globalTimeout := c.AI.ExecutionTimeout
		cfgMu.Unlock()
		aiCfg.LocalDir = config.ResolveLocalDir(aiCfg.LocalDir, msg.Repo, localDirBase)

		extraFlags := agentCfg.ExtraFlags
		if extraFlags != "" {
			if err := executor.ValidateExtraFlags(extraFlags); err != nil {
				slog.Warn("implement-worker: extra_flags rejected", "err", err)
				extraFlags = ""
			}
		}

		issuePrompt, issueInstructions := resolveIssuePrompt(s, aiCfg.IssuePrompt, agentCfg.PromptID)
		implPrompt, implInstructions := resolveImplementPrompt(s, aiCfg.ImplementPrompt, agentCfg.PromptID)

		opts := issuepipeline.RunOptions{
			GitHubToken: token,
			Primary:     aiCfg.Primary,
			Fallback:    aiCfg.Fallback,
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
				Timeout:              resolveExecutionTimeout(globalTimeout, agentCfg.ExecutionTimeout),
			},
			IssuePromptOverride:     issuePrompt,
			IssueInstructions:       issueInstructions,
			ImplementPromptOverride: implPrompt,
			ImplementInstructions:   implInstructions,
			PRReviewers:             aiCfg.PRReviewers,
			PRAssignee:              aiCfg.PRAssignee,
			PRLabels:                aiCfg.PRLabels,
			PRDraft:                 aiCfg.PRDraft != nil && *aiCfg.PRDraft,
			GeneratePRDescription:   aiCfg.GeneratePRDescription != nil && *aiCfg.GeneratePRDescription,
		}

		if _, err := issuePipe.Run(ctx, ghIssue, opts); err != nil {
			slog.Error("implement-worker: pipeline run failed",
				"repo", msg.Repo, "number", msg.Number, "err", err)
		}

		// Enroll for state watching so closed/resolved issues update in the UI.
		// Runs even after pipeline failure — state tracking is independent of
		// pipeline success, and we want the UI to reflect closures regardless.
		if err := watchStore.Enroll(ctx, "issue", msg.Repo, msg.Number, msg.GithubID); err != nil {
			slog.Warn("implement-worker: failed to enroll watch",
				"repo", msg.Repo, "number", msg.Number, "err", err)
		}
	}

	implementW := worker.NewImplementWorker(conn, maxWorkers, implementHandler)
	implementWCtx, implementWCancel := context.WithCancel(context.Background())
	defer implementWCancel()
	go func() {
		if err := implementW.Start(implementWCtx); err != nil {
			slog.Error("implement worker stopped", "err", err)
		}
	}()

	// ── State check poller ──────────────────────────────────────────────
	// Scans the NATS KV watch bucket every 30s and publishes StateCheckMsg
	// for items due for a state check. Replaces the in-memory WatchQueue.
	stateCheckPub := bus.NewStateCheckPublisher(conn)
	statePollerCtx, statePollerCancel := context.WithCancel(context.Background())
	defer statePollerCancel()
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-statePollerCtx.Done():
				return
			case <-ticker.C:
				// Gradually enroll one open item not yet in watch_state per tick.
				// Backfills items from before the NATS migration without blocking startup.
				enrollOpenItems(statePollerCtx, s, watchStore)

				if evicted, err := watchStore.EvictStale(statePollerCtx); err != nil {
					slog.Warn("state-poller: evict failed", "err", err)
				} else if evicted > 0 {
					slog.Debug("state-poller: evicted stale items", "count", evicted)
				}

				ready, err := watchStore.ScanReady(statePollerCtx)
				if err != nil {
					slog.Warn("state-poller: scan failed", "err", err)
					continue
				}
				for _, entry := range ready {
					if err := stateCheckPub.PublishStateCheck(statePollerCtx, entry.Type, entry.Repo, entry.Number, entry.GithubID); err != nil {
						slog.Warn("state-poller: publish failed",
							"type", entry.Type, "repo", entry.Repo, "number", entry.Number, "err", err)
					}
				}
			}
		}
	}()

	// ── NATS state check worker ─────────────────────────────────────────
	// Consumes state check requests, calls GitHub API, updates KV backoff.
	// Reuses the existing CheckItem/HandleChange logic from tier2Adapter.
	stateHandler := func(ctx context.Context, msg bus.StateCheckMsg) (bool, error) {
		// Auto-dismiss legacy items with missing data — they can never be checked.
		if msg.Repo == "" {
			key := fmt.Sprintf("%s.%d", msg.Type, msg.GithubID)
			if err := watchStore.Delete(ctx, key); err != nil {
				slog.Warn("state-handler: failed to delete legacy item", "key", key, "err", err)
			}
			slog.Info("state-handler: auto-dismissed legacy item with empty repo",
				"type", msg.Type, "number", msg.Number, "github_id", msg.GithubID)
			return false, nil
		}

		// Rate limit before any GitHub API call. TierWatch (50ms) matches
		// the old Tier 3 priority — state checks are lightweight and high-priority.
		if err := limiter.Acquire(ctx, scheduler.TierWatch); err != nil {
			return false, fmt.Errorf("rate limit cancelled: %w", err)
		}

		item := &scheduler.WatchItem{
			Type:     msg.Type,
			Repo:     msg.Repo,
			Number:   msg.Number,
			GithubID: msg.GithubID,
		}

		// Read LastSeen from KV for the dedup check inside CheckItem.
		key := fmt.Sprintf("%s.%d", msg.Type, msg.GithubID)
		entry, err := watchStore.Get(ctx, key)
		if err == nil {
			item.LastSeen = entry.LastSeen
		} else {
			slog.Warn("state-handler: KV get failed, using zero LastSeen",
				"key", key, "err", err)
		}

		changed, snap, err := adapter.CheckItem(ctx, item)
		if err != nil {
			// 404 means the repo/PR was deleted or we don't have access.
			// Remove from watch to stop retrying.
			var apiErr *gh.APIError
			if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
				if delErr := watchStore.Delete(ctx, key); delErr != nil {
					slog.Warn("state-handler: failed to delete unreachable item", "key", key, "err", delErr)
				}
				slog.Info("state-handler: removed unreachable item from watch",
					"type", msg.Type, "repo", msg.Repo, "number", msg.Number)
				return false, nil
			}
			return false, err
		}
		if !changed {
			return false, nil
		}
		if err := adapter.HandleChange(ctx, item, snap); err != nil {
			return true, err
		}
		return true, nil
	}

	stateW := worker.NewStateWorker(conn, maxWorkers*2, watchStore, stateHandler)
	stateWCtx, stateWCancel := context.WithCancel(context.Background())
	defer stateWCancel()
	go func() {
		if err := stateW.Start(stateWCtx); err != nil {
			slog.Error("state worker stopped", "err", err)
		}
	}()

	// Use a closure so the defer reads the current cancel/wg at shutdown
	// time, not the initial values captured at defer-statement time. After a
	// reload, pollerCancel/pollerWg point to the new goroutines — the bare
	// defer would stop the already-halted original set and leak the
	// post-reload ones.
	defer func() {
		cfgMu.Lock()
		cancel := pollerCancel
		wg := pollerWg
		cfgMu.Unlock()
		cancel()
		wg.Wait()
		slog.Info("pollers: stopped")
	}()

	// Expose live config for GET /config
	srv.SetRepoMetaFns(ghClient.FetchLabels, ghClient.FetchCollaborators)

	srv.SetConfigFn(func() map[string]any {
		// Snapshot the mutable slice fields under cfgMu. The poll-cycle
		// auto-discovery path (upsertDiscoveredRepos) appends to
		// GitHub.Repositories / GitHub.NonMonitored while holding the same
		// mutex — without this snapshot, reading those slices after the
		// unlock would race with concurrent header writes. Cloning into a
		// fresh backing array also means the returned map never shares
		// state with the live Config after we release the lock.
		cfgMu.Lock()
		c := cfg
		reposList := append([]string(nil), c.GitHub.Repositories...)
		nonMonList := append([]string(nil), c.GitHub.NonMonitored...)
		localDirBaseList := append([]string(nil), c.GitHub.LocalDirBase...)
		discoveryOrgsList := append([]string(nil), c.GitHub.DiscoveryOrgs...)
		cfgMu.Unlock()
		repoOverrides := make(map[string]map[string]any)
		for repo, ai := range c.AI.Repos {
			ro := map[string]any{
				"primary":     ai.Primary,
				"fallback":    ai.Fallback,
				"review_mode": ai.ReviewMode,
				"local_dir":   ai.LocalDir,
			}
			if len(ai.PRReviewers) > 0 {
				ro["pr_reviewers"] = ai.PRReviewers
			}
			if ai.PRAssignee != "" {
				ro["pr_assignee"] = ai.PRAssignee
			}
			if len(ai.PRLabels) > 0 {
				ro["pr_labels"] = ai.PRLabels
			}
			if ai.PRDraft != nil {
				ro["pr_draft"] = *ai.PRDraft
			}
			if ai.IssueTracking != nil {
				ro["issue_tracking"] = ai.IssueTracking
			}
			repoOverrides[repo] = ro
		}
		// Auto-detected local_dir for every repo the UI may render. Populated
		// only when config.ResolveLocalDir() finds a matching directory under
		// DefaultReposMountPath — i.e. the operator's bind-mount is in effect
		// and the repo has been cloned there. The UI uses this to display
		// "Auto-detected: /home/heimdallm/repos/<name>" next to repos where the user has
		// not set `local_dir` manually but a review would still get
		// full-repo context.
		localDirsDetected := make(map[string]string)
		seenRepo := make(map[string]bool)
		addDetection := func(repo string) {
			if repo == "" || seenRepo[repo] {
				return
			}
			seenRepo[repo] = true
			if d := config.ResolveLocalDir("", repo, c.GitHub.LocalDirBase); d != "" {
				localDirsDetected[repo] = d
			}
		}
		for _, r := range reposList {
			addDetection(r)
		}
		for _, r := range nonMonList {
			addDetection(r)
		}
		for r := range c.AI.Repos {
			addDetection(r)
		}
		agentConfigs := make(map[string]map[string]any)
		for name, ac := range c.AI.Agents {
			agentConfigs[name] = map[string]any{
				"model":                  ac.Model,
				"max_turns":              ac.MaxTurns,
				"approval_mode":          ac.ApprovalMode,
				"extra_flags":            ac.ExtraFlags,
				"prompt":                 ac.PromptID,
				"effort":                 ac.Effort,
				"permission_mode":        ac.PermissionMode,
				"bare":                   ac.Bare,
				"dangerously_skip_perms": ac.DangerouslySkipPerms,
				"no_session_persistence": ac.NoSessionPersistence,
			}
		}
		// Expose first-seen timestamps so the Flutter app can show NEW
		// badges on auto-discovered repos. Read-only; populated by the
		// poll cycle. Errors are logged (not propagated) so a transient
		// store failure degrades gracefully — the response goes out
		// without first_seen_at, NEW badges disappear, and the operator
		// sees a Warn entry instead of silent UI breakage.
		if rows, err := s.ListConfigs(); err != nil {
			slog.Warn("config: list configs for repo_first_seen failed", "err", err)
		} else if fsMap, err := config.ParseFirstSeen(rows["repo_first_seen"]); err != nil {
			slog.Warn("config: parse repo_first_seen failed", "err", err)
		} else {
			for repo, ts := range fsMap {
				ro := repoOverrides[repo]
				if ro == nil {
					ro = map[string]any{}
				}
				ro["first_seen_at"] = ts.Unix()
				repoOverrides[repo] = ro
			}
		}
		result := map[string]any{
			"server_port":                 c.Server.Port,
			"bind_addr":                   c.Server.BindAddr,
			"poll_interval":               c.GitHub.PollInterval,
			"repositories":                reposList,
			"non_monitored":               nonMonList,
			"local_dir_base":              localDirBaseList,
			"discovery_topic":             c.GitHub.DiscoveryTopic,
			"discovery_orgs":              discoveryOrgsList,
			"discovery_interval":          c.GitHub.DiscoveryInterval,
			"ai_primary":                  c.AI.Primary,
			"ai_fallback":                 c.AI.Fallback,
			"review_mode":                 c.AI.ReviewMode,
			"execution_timeout":           c.AI.ExecutionTimeout,
			"retention_days":              c.Retention.MaxDays,
			"issue_tracking":              c.GitHub.IssueTracking,
			"repo_overrides":              repoOverrides,
			"agent_configs":               agentConfigs,
			"local_dirs_detected":         localDirsDetected,
			"activity_log_enabled":        ptrBoolOrTrue(c.ActivityLog.Enabled),
			"activity_log_retention_days": ptrIntOr(c.ActivityLog.RetentionDays, 90),
			"issue_prompt":                c.AI.IssuePrompt,
			"implement_prompt":            c.AI.ImplementPrompt,
		}
		reviewers, labels, assignee, draft := c.ResolvedPRMetadata()
		pm := map[string]any{}
		if len(reviewers) > 0 {
			pm["reviewers"] = reviewers
		}
		if len(labels) > 0 {
			pm["labels"] = labels
		}
		if assignee != "" {
			pm["pr_assignee"] = assignee
		}
		if draft != nil {
			pm["pr_draft"] = *draft
		}
		if len(pm) > 0 {
			result["pr_metadata"] = pm
		}
		return result
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

	// Wire the reload callback: re-read config from disk, restart the
	// pipeline so changes to discovery_topic / orgs / intervals take effect
	// without a daemon restart. Reuses the `loadConfig` closure captured at
	// startup so the two paths cannot drift on which loader they pick — both
	// see the same HEIMDALLM_DATA_DIR snapshot.
	srv.SetReloadFn(func() error {
		// Serialise reloads: without this, two concurrent /reload calls
		// could each read the same pollerCancel, both cancel it, both
		// start new pollers, and leave two sets running against the same
		// GitHub API budget.
		reloadMu.Lock()
		defer reloadMu.Unlock()

		newCfg, err := loadConfig(cfgPath)
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

		// Read the current cancel/wg under cfgMu, then stop OUTSIDE the lock.
		// Holding cfgMu across Wait() risks deadlock: if Wait() blocks
		// waiting for in-flight goroutines that also acquire cfgMu (e.g.
		// tier2Adapter callbacks), both sides block forever.
		cfgMu.Lock()
		oldCancel := pollerCancel
		oldWg := pollerWg
		cfgMu.Unlock()

		oldCancel()
		oldWg.Wait()

		// Swap cfg + pollers atomically under the lock so readers never
		// see a half-updated state.
		cfgMu.Lock()
		cfg = newCfg
		cfgMu.Unlock()

		// Reload path → coldStart=false so Tier 2 waits one full PollInterval
		// before its first tick. A UI config PATCH triggers this path; firing
		// an immediate tick on every PATCH would fan out reviews across the
		// whole fleet and amplify the cost-runaway loop #243 closed.
		newCancel, newWg := startPollers(context.Background(), false)

		cfgMu.Lock()
		pollerCancel = newCancel
		pollerWg = newWg
		cfgMu.Unlock()

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
		localDirBase := cfg.GitHub.LocalDirBase
		cfgMu.Unlock()
		// /home/heimdallm/repos/<short-name> fallback when local_dir is unset (stat-based,
		// keep outside the mutex).
		aiCfg.LocalDir = config.ResolveLocalDir(aiCfg.LocalDir, pr.Repo, localDirBase)

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

		// Persistent in-flight claim: keyed on (store pr_id, head_sha).
		// Triggered reviews are reconstructed from stored PR data which has no
		// HEAD SHA populated; in that case we skip the claim and rely on the
		// downstream SHA dedup inside pipeline.Run (Task 1, fail-closed) to
		// prevent duplicate work. When the head SHA is known, claim/release
		// using the same mechanism as the poll loop so both paths share the
		// same persistent guard across daemon restart / config reload.
		//
		// Fail-open on Claim error here for the same reason as the poll path —
		// see runReview above for the full layered-defense rationale
		// (HEAD-SHA guard + circuit breaker + PublishedAt dedup cap the
		// worst case at <€30/PR).
		var triggerClaimed bool
		if ghPR.Head.SHA != "" {
			ok, err := s.ClaimInFlightReview(pr.ID, ghPR.Head.SHA)
			if err != nil {
				slog.Warn("trigger review: claim inflight failed, proceeding", "err", err)
			} else if !ok {
				return fmt.Errorf("review already in progress for PR %d", ghPR.Number)
			} else {
				triggerClaimed = true
			}
		}
		defer func() {
			if triggerClaimed {
				if err := s.ReleaseInFlightReview(pr.ID, ghPR.Head.SHA); err != nil {
					slog.Warn("trigger review: release inflight failed", "err", err,
						"pr_id", pr.ID, "head_sha", ghPR.Head.SHA)
				}
			}
		}()

		// Lifecycle SSEs (pr_detected, review_started, review_completed,
		// review_skipped with the actual reason) are published by p.Run
		// via its Publisher dependency. Trigger only owns error paths so
		// it can attach the err.Error() string the caller surfaces.
		// Pre-#322 the trigger fabricated review_skipped(not_open) on
		// every nil return — that lied for SHA-skip / legacy-backfill
		// paths added in #322 Bug 4.
		rev, err := p.Run(ghPR, buildRunOpts(ghPR, aiCfg))
		if err != nil {
			var cbErr *pipeline.CircuitBreakerError
			if errors.As(err, &cbErr) {
				broker.Publish(sse.Event{
					Type: sse.EventCircuitBreakerTripped,
					Data: sseData(map[string]any{
						"pr_number": pr.Number,
						"repo":      pr.Repo,
						"reason":    cbErr.Reason,
					}),
				})
				return err
			}
			broker.Publish(sse.Event{Type: sse.EventReviewError, Data: sseData(map[string]any{"pr_id": prID, "error": err.Error()})})
			return err
		}
		// rev == nil → pipeline already emitted EventReviewSkipped with
		// the actual reason. rev != nil → pipeline already emitted
		// EventReviewCompleted. Either way the trigger callback only
		// has to report success/failure (its signature is
		// `func(prID int64) error`, see SetTriggerReviewFn) so the
		// review payload itself is not needed here.
		_ = rev
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
		localDirBase := cfg.GitHub.LocalDirBase
		globalTimeout := cfg.AI.ExecutionTimeout
		cfgMu.Unlock()
		// /home/heimdallm/repos/<short-name> fallback when local_dir is unset (stat-based,
		// keep outside the mutex).
		aiCfg.LocalDir = config.ResolveLocalDir(aiCfg.LocalDir, iss.Repo, localDirBase)

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
			GitHubToken: token,
			Primary:     aiCfg.Primary,
			Fallback:    aiCfg.Fallback,
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
				Timeout:              resolveExecutionTimeout(globalTimeout, agentCfg.ExecutionTimeout),
			},
			IssuePromptOverride:     issuePrompt,
			IssueInstructions:       issueInstructions,
			ImplementPromptOverride: implPrompt,
			ImplementInstructions:   implInstructions,
			PRReviewers:             aiCfg.PRReviewers,
			PRAssignee:              aiCfg.PRAssignee,
			PRLabels:                aiCfg.PRLabels,
			PRDraft:                 aiCfg.PRDraft != nil && *aiCfg.PRDraft,
			GeneratePRDescription:   aiCfg.GeneratePRDescription != nil && *aiCfg.GeneratePRDescription,
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

	// Wire the promote callback: runs the auto_implement pipeline for a review_only issue,
	// effectively reclassifying it to develop from the UI without needing a GitHub label change.
	srv.SetTriggerPromoteFn(func(issueID int64) error {
		publishIssueErr := func(msg string) {
			broker.Publish(sse.Event{
				Type: sse.EventIssueReviewError,
				Data: sseData(map[string]any{"issue_id": issueID, "error": msg}),
			})
		}

		iss, err := s.GetIssue(issueID)
		if err != nil {
			publishIssueErr(fmt.Sprintf("Issue not found: %v", err))
			return fmt.Errorf("promote issue: get issue %d: %w", issueID, err)
		}

		// Read the issue tracking config to know which labels to add/remove.
		cfgMu.Lock()
		it := cfg.GitHub.IssueTracking
		cfgMu.Unlock()

		if len(it.DevelopLabels) == 0 {
			publishIssueErr("No develop labels configured — cannot promote")
			return fmt.Errorf("promote issue: no develop labels configured")
		}

		slog.Info("promote issue: updating labels on GitHub",
			"store_issue_id", issueID, "repo", iss.Repo, "number", iss.Number,
			"add", it.DevelopLabels[0], "remove", it.ReviewOnlyLabels)

		// Add the first develop label so the polling pipeline classifies as DEV.
		if err := ghClient.AddIssueLabel(iss.Repo, iss.Number, it.DevelopLabels[0]); err != nil {
			publishIssueErr(fmt.Sprintf("Failed to add develop label: %v", err))
			return fmt.Errorf("promote issue: add label: %w", err)
		}

		// Remove review_only labels so classification is unambiguous.
		for _, label := range it.ReviewOnlyLabels {
			if err := ghClient.RemoveIssueLabel(iss.Repo, iss.Number, label); err != nil {
				slog.Warn("promote issue: could not remove review_only label",
					"label", label, "repo", iss.Repo, "number", iss.Number, "err", err)
				// Non-fatal — the develop label is already set, classification will still prefer DEV.
			}
		}

		slog.Info("promote issue: labels updated, polling will pick it up as DEV",
			"store_issue_id", issueID, "repo", iss.Repo, "number", iss.Number)
		return nil
	})

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

// resolveExecutionTimeout returns the effective execution timeout for the CLI
// process. Per-agent timeout wins over the global timeout; zero means "use
// executor default (5m)".
func resolveExecutionTimeout(globalTimeout, agentTimeout string) time.Duration {
	// Per-agent wins
	if agentTimeout != "" {
		if d, err := time.ParseDuration(agentTimeout); err == nil && d > 0 {
			return d
		}
	}
	// Global fallback
	if globalTimeout != "" {
		if d, err := time.ParseDuration(globalTimeout); err == nil && d > 0 {
			return d
		}
	}
	// Zero = executor uses its default (5m)
	return 0
}

// ── Standalone poller functions (replaced Pipeline goroutines) ───────────

// sendDiscoveryRepos merges static + discovered repos and publishes the
// full list to NATS. Extracted from the old tier1.go sendRepos.
func sendDiscoveryRepos(
	ctx context.Context,
	disc scheduler.Tier1Discovery,
	limiter *scheduler.RateLimiter,
	pub scheduler.Tier1Publisher,
	configFn func() scheduler.Tier1Config,
) {
	cfg := configFn()
	discovered := disc.Discovered()

	// Merge static + discovered, exclude non-monitored
	nonMon := make(map[string]struct{}, len(cfg.NonMonitored))
	for _, r := range cfg.NonMonitored {
		nonMon[r] = struct{}{}
	}
	seen := make(map[string]struct{})
	var repos []string
	for _, r := range cfg.StaticRepos {
		if _, skip := nonMon[r]; skip {
			continue
		}
		if _, dup := seen[r]; dup {
			continue
		}
		seen[r] = struct{}{}
		repos = append(repos, r)
	}
	for _, r := range discovered {
		if _, skip := nonMon[r]; skip {
			continue
		}
		if _, dup := seen[r]; dup {
			continue
		}
		seen[r] = struct{}{}
		repos = append(repos, r)
	}

	slog.Info("tier1: discovery complete", "repos", len(repos))
	if err := pub.PublishRepos(ctx, repos); err != nil {
		slog.Error("tier1: publish repos failed", "err", err)
	}
}

// bridgeDiscovery subscribes to the NATS discovery subject and forwards
// repo lists to the reposChan that Tier 2 reads. Uses core NATS (no JetStream).
func bridgeDiscovery(ctx context.Context, conn *nats.Conn, out chan<- []string) {
	ch := make(chan *nats.Msg, 8)
	sub, err := conn.ChanSubscribe(bus.SubjDiscoveryRepos, ch)
	if err != nil {
		slog.Error("bridge: subscribe to discovery subject failed", "err", err)
		return
	}
	defer sub.Unsubscribe()

	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-ch:
			var dm bus.DiscoveryMsg
			if err := bus.Decode(msg.Data, &dm); err != nil {
				slog.Error("bridge: decode discovery msg", "err", err)
				continue
			}
			select {
			case out <- dm.Repos:
			case <-ctx.Done():
				return
			}
		}
	}
}

// runTier2 runs the PR/issue polling loop. Replaces the old RunTier2 from
// the scheduler package.
func runTier2(
	ctx context.Context,
	adapter *tier2Adapter,
	limiter *scheduler.RateLimiter,
	prPublisher scheduler.Tier2PRPublisher,
	configFn func() []string,
	reposChan <-chan []string,
	interval time.Duration,
	coldStart bool,
) {
	var (
		mu    sync.Mutex
		repos []string
	)

	// Goroutine to receive repo updates from Tier 1
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case r := <-reposChan:
				mu.Lock()
				repos = r
				mu.Unlock()
				slog.Info("tier2: received repo list", "count", len(r))
			}
		}
	}()

	// Brief delay for Tier 1 to send first batch
	select {
	case <-time.After(2 * time.Second):
	case <-ctx.Done():
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	processTick := func() {
		mu.Lock()
		currentRepos := append([]string(nil), repos...)
		mu.Unlock()

		if len(currentRepos) == 0 {
			return
		}

		// PR processing
		if err := limiter.Acquire(ctx, scheduler.TierRepo); err != nil {
			return
		}
		prs, err := adapter.FetchPRsToReview()
		if err != nil {
			slog.Error("tier2: fetch PRs", "err", err)
		} else {
			monitoredSet := make(map[string]struct{}, len(currentRepos))
			for _, r := range currentRepos {
				monitoredSet[r] = struct{}{}
			}
			for _, pr := range prs {
				if _, ok := monitoredSet[pr.Repo]; !ok {
					continue
				}
				if adapter.PRAlreadyReviewed(pr.ID, pr.Repo, pr.Number, pr.UpdatedAt) {
					continue
				}
				if err := prPublisher.PublishPRReview(ctx, pr.Repo, pr.Number, pr.ID, pr.HeadSHA); err != nil {
					slog.Error("tier2: publish PR review", "repo", pr.Repo, "pr", pr.Number, "err", err)
				}
			}
		}

		// Issue promotion
		if err := limiter.Acquire(ctx, scheduler.TierRepo); err != nil {
			return
		}
		if n, err := adapter.PromoteReady(ctx, currentRepos); err != nil {
			slog.Error("tier2: promotion", "err", err)
		} else if n > 0 {
			slog.Info("tier2: promoted issues", "count", n)
		}

		// Issue processing per repo
		for _, repo := range currentRepos {
			if err := limiter.Acquire(ctx, scheduler.TierRepo); err != nil {
				return
			}
			n, err := adapter.ProcessRepo(ctx, repo)
			if err != nil {
				slog.Error("tier2: issue processing", "repo", repo, "err", err)
				continue
			}
			if n > 0 {
				slog.Info("tier2: processed issues", "repo", repo, "count", n)
			}
		}

		// Retry pending publishes
		adapter.PublishPending()
	}

	if coldStart {
		processTick()
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			processTick()
		}
	}
}

// upsertDiscoveredRepos adds PRs' repos to the monitored (or non-monitored)
// list when they're new. Returns the list of repos that were added.
// Never removes; mutually-exclusive with NonMonitored when adding.
//
// The Flutter UI maps prEnabled via list membership: prEnabled=true ⇔
// Repositories, prEnabled=false ⇔ NonMonitored, neither ⇔ undiscovered.
// AutoEnablePRForDiscovery() controls which list a new repo lands in.
//
// Caller is responsible for persisting the updated Config and recording
// first-seen timestamps. This helper is pure state mutation so it's easy
// to test in isolation.
func upsertDiscoveredRepos(c *config.Config, prs []*gh.PullRequest) []string {
	known := make(map[string]struct{})
	for _, r := range c.GitHub.Repositories {
		known[r] = struct{}{}
	}
	for _, r := range c.GitHub.NonMonitored {
		known[r] = struct{}{}
	}

	enable := c.GitHub.AutoEnablePRForDiscovery()
	added := []string{}
	for _, pr := range prs {
		if pr.Repo == "" {
			continue
		}
		if _, alreadyKnown := known[pr.Repo]; alreadyKnown {
			continue
		}
		if enable {
			c.GitHub.Repositories = append(c.GitHub.Repositories, pr.Repo)
		} else {
			c.GitHub.NonMonitored = append(c.GitHub.NonMonitored, pr.Repo)
		}
		known[pr.Repo] = struct{}{}
		added = append(added, pr.Repo)
	}
	return added
}

// ── tier2Adapter bridges main.go's concrete types to Pipeline interfaces ──

type tier2Adapter struct {
	ghClient   *gh.Client
	ghToken    string
	pipeline   *pipeline.Pipeline
	issuePipe  *issuepipeline.Pipeline
	fetcher    *issuepipeline.Fetcher
	store      *store.Store
	broker     *sse.Broker
	cfgMu      *sync.Mutex
	cfg        **config.Config
	loginMu    *sync.Mutex
	login      *string
	runReview  func(pr *gh.PullRequest, aiCfg config.RepoAI) *store.Review
	publishPub *bus.PRPublishPublisher
	watchStore *bus.WatchStore

	// skipMu protects lastSkippedUpdatedAt, which deduplicates review_skipped
	// SSE events across consecutive poll cycles for the same (PR ID, updated_at)
	// pair. Entries are pruned at the end of each FetchPRsToReview cycle so the
	// map stays bounded to the current set of review-requested PRs.
	skipMu               sync.Mutex
	lastSkippedUpdatedAt map[int64]time.Time
}

// discoveryStore is the subset of *store.Store that processDiscoveredRepos
// needs. Narrowing to this interface lets the discovery path be unit-tested
// without standing up the full adapter (which pulls in ghClient, pipelines,
// etc.).
type discoveryStore interface {
	SetConfig(key, value string) (int64, error)
	ListConfigs() (map[string]string, error)
}

// processDiscoveredRepos persists newly-discovered repos to the K/V store
// (monitored/non-monitored lists + first-seen map) and publishes one
// EventRepoDiscovered per added repo on the broker.
//
// Inputs are already-snapshot slices so the caller can drop its config mutex
// before invoking this helper. No-op when added is empty.
func processDiscoveredRepos(
	added []string,
	reposSnap []string,
	nonMonSnap []string,
	st discoveryStore,
	broker *sse.Broker,
	now time.Time,
) {
	if len(added) == 0 {
		return
	}
	// Persist the updated monitored/non-monitored lists via the K/V store
	// so the Flutter app's cached view survives a daemon restart.
	//
	// Two guards against the #183 bug, where a nil snapshot of
	// NonMonitored (brief race window: reload swaps *a.cfg between the
	// caller's mutex unlock and this helper) marshalled to the literal
	// string "null" and clobbered the operator's list on the next
	// reload — MergeStoreLayer parsed "null" as "no entries", and
	// upsertDiscoveredRepos on the next poll re-added every PR's repo
	// into Repositories because NonMonitored was gone from the `known`
	// set. End state: ops' "not monitored" choice silently evaporated
	// every few minutes.
	//
	//   1. Skip the write entirely when the snapshot is nil. We only
	//      persist state the caller gave us explicitly; a nil slice is
	//      never a legitimate "clear" signal from the poll path — only
	//      the PUT /config handler intentionally writes empty lists.
	//      Keeps the existing store row intact, letting MergeStoreLayer
	//      carry the operator's TOML list through the race.
	//   2. Only touch the row when the serialized value actually
	//      changed. Cuts both the corruption window and the write load:
	//      a no-op poll (added>0 but the lists didn't shift) no longer
	//      rewrites these rows at all.
	existing, _ := st.ListConfigs()
	if reposSnap != nil {
		if reposJSON, err := json.Marshal(reposSnap); err != nil {
			slog.Warn("poll: marshal repositories failed", "err", err)
		} else if string(reposJSON) != existing["repositories"] {
			if _, err := st.SetConfig("repositories", string(reposJSON)); err != nil {
				slog.Warn("poll: persist repositories failed", "err", err)
			}
		}
	}
	if nonMonSnap != nil {
		if nmJSON, err := json.Marshal(nonMonSnap); err != nil {
			slog.Warn("poll: marshal non_monitored failed", "err", err)
		} else if string(nmJSON) != existing["non_monitored"] {
			if _, err := st.SetConfig("non_monitored", string(nmJSON)); err != nil {
				slog.Warn("poll: persist non_monitored failed", "err", err)
			}
		}
	}

	// Update first-seen map in the same store so GET /config can expose
	// repo_overrides[repo].first_seen_at to the UI (NEW badge).
	//
	// Guard both reads: if either fails, bail out without writing.
	// Writing a partial FirstSeenMap back would permanently erase every
	// previously-stored timestamp — the UI would lose NEW badges for all
	// historical repos the next time a single new repo is discovered.
	rows, err := st.ListConfigs()
	if err != nil {
		slog.Warn("poll: list configs for repo_first_seen failed — skipping update", "err", err)
	} else {
		fs, err := config.ParseFirstSeen(rows["repo_first_seen"])
		if err != nil {
			slog.Warn("poll: parse repo_first_seen failed — skipping update to preserve stored data", "err", err)
		} else {
			for _, r := range added {
				fs.Mark(r, now)
			}
			if fsStr, err := fs.Marshal(); err != nil {
				slog.Warn("poll: marshal repo_first_seen failed", "err", err)
			} else if _, err := st.SetConfig("repo_first_seen", fsStr); err != nil {
				slog.Warn("poll: persist repo_first_seen failed", "err", err)
			}
		}
	}

	for _, r := range added {
		broker.Publish(sse.Event{
			Type: sse.EventRepoDiscovered,
			Data: sseData(map[string]any{"repo": r}),
		})
		slog.Info("poll: auto-discovered repo", "repo", r)
	}
}

// FetchPRsToReview implements scheduler.Tier2PRFetcher.
// After fetching, any repos not yet in the config are auto-discovered and
// persisted — the daemon never silently skips unknown repos again.
func (a *tier2Adapter) FetchPRsToReview() ([]scheduler.Tier2PR, error) {
	prs, err := a.ghClient.FetchPRsToReview()
	if err != nil {
		return nil, err
	}
	// Resolve repo on every PR before the upsert step — upsertDiscoveredRepos
	// reads pr.Repo and skips empty ones, so we must populate the field first.
	for _, pr := range prs {
		pr.ResolveRepo()
	}

	// Auto-discover repos we've never seen before. A PR whose repo is not in
	// Repositories or NonMonitored gets appended to one of those lists based
	// on AutoEnablePRForDiscovery(). This is how the Flutter UI learns about
	// review-requested repos the operator never explicitly configured.
	//
	// Snapshot the updated slices under the same mutex that guards the
	// mutation so a concurrent reload-swap cannot race with the Marshal below.
	a.cfgMu.Lock()
	// a.cfg is **config.Config (a handle to the "current Config" pointer
	// that config reloads can swap). Dereference once to get the *Config we
	// mutate in place under the mutex.
	cfg := *a.cfg
	added := upsertDiscoveredRepos(cfg, prs)
	reposSnap := append([]string(nil), cfg.GitHub.Repositories...)
	nonMonSnap := append([]string(nil), cfg.GitHub.NonMonitored...)
	a.cfgMu.Unlock()

	// Benign race window: between the Unlock above and the SetConfig calls
	// inside processDiscoveredRepos, a config reload can swap *a.cfg to a
	// fresh Config that does not contain the just-appended repos. On the
	// next poll cycle they look new again, triggering one burst of
	// duplicate repo_discovered SSE events. Self-heals via the store (the
	// reloaded Config picks up "repositories"/"non_monitored" from it),
	// so we accept the duplicate rather than hold cfgMu across the
	// blocking store I/O below.
	processDiscoveredRepos(added, reposSnap, nonMonSnap, a.store, a.broker, time.Now())

	// Resolve bot login for the self-author guard.
	a.loginMu.Lock()
	botLogin := *a.login
	a.loginMu.Unlock()
	if botLogin == "" {
		if u, err := a.ghClient.AuthenticatedUser(); err == nil {
			botLogin = u
			a.loginMu.Lock()
			*a.login = u
			a.loginMu.Unlock()
		} else {
			// Empty botLogin silently disables the self-author guard for this
			// cycle; log so operators can diagnose why it's not firing.
			slog.Warn("adapter: failed to resolve bot login, self-author guard disabled this cycle", "err", err)
		}
	}

	a.cfgMu.Lock()
	// Convert config.ResolvedReviewGuards to pipeline.GateConfig via same-shape cast.
	// Shadow type exists because config cannot import pipeline (import cycle).
	guards := pipeline.GateConfig((*a.cfg).ReviewGuards(botLogin))
	a.cfgMu.Unlock()

	out := make([]scheduler.Tier2PR, 0, len(prs))
	// seenIDs tracks every PR GitHub ID encountered this cycle so we can prune
	// the skip-dedup map to only live PRs after the loop.
	seenIDs := make(map[int64]struct{}, len(prs))
	for _, pr := range prs {
		if pr.Repo == "" {
			slog.Warn("adapter: skipping PR with empty repo", "pr_number", pr.Number)
			continue
		}
		seenIDs[pr.ID] = struct{}{}
		reason := pipeline.Evaluate(pipeline.PRGate{
			State:  pr.State,
			Draft:  pr.Draft,
			Author: pr.User.Login,
		}, guards)
		if reason != pipeline.SkipReasonNone {
			// Dedup: only emit review_skipped once per (PR ID, updated_at). A
			// long-lived draft PR stays in the search results every cycle, but
			// its updated_at doesn't change, so we suppress the repeat events.
			a.skipMu.Lock()
			prev, seen := a.lastSkippedUpdatedAt[pr.ID]
			alreadyEmitted := seen && !pr.UpdatedAt.After(prev)
			if !alreadyEmitted {
				a.lastSkippedUpdatedAt[pr.ID] = pr.UpdatedAt
			}
			a.skipMu.Unlock()

			if !alreadyEmitted {
				a.broker.Publish(sse.Event{
					Type: sse.EventReviewSkipped,
					Data: sseData(map[string]any{
						"repo":      pr.Repo,
						"pr_number": pr.Number,
						"pr_title":  pr.Title,
						"reason":    string(reason),
					}),
				})
				slog.Info("tier2: skipping PR",
					"repo", pr.Repo, "pr", pr.Number, "reason", string(reason))
			}
			continue
		}
		// PR passed the gate — clear any prior skip record so if it is later
		// re-skipped (e.g. converted to draft) we emit the event again.
		a.skipMu.Lock()
		delete(a.lastSkippedUpdatedAt, pr.ID)
		a.skipMu.Unlock()

		// Resolve the HEAD SHA so the persistent in-flight claim (#258) can
		// key on (pr_id, head_sha) downstream. The Search Issues API does
		// NOT populate head.sha, so this is an extra /pulls/N call per PR
		// that cleared the review guards — bounded by the small number of
		// review-requested PRs per cycle. See theburrowhub/heimdallm#264
		// for the bug this closes: before this lookup the SHA was empty,
		// and runReview silently skipped the claim guard on every tick.
		//
		// Fail-open on resolver error: empty HeadSHA makes runReview fall
		// back to the other layered defenses (fail-closed SHA in
		// pipeline.Run, circuit breaker, PublishedAt grace). Blocking a
		// review on a transient SHA-lookup blip would be worse than
		// leaning on those defenses for one cycle.
		headSHA, shaErr := a.ghClient.GetPRHeadSHA(pr.Repo, pr.Number)
		if shaErr != nil {
			slog.Warn("tier2: HEAD SHA lookup failed, in-flight claim will be skipped for this tick",
				"repo", pr.Repo, "pr", pr.Number, "err", shaErr)
			headSHA = ""
		}

		out = append(out, scheduler.Tier2PR{
			ID:        pr.ID,
			Number:    pr.Number,
			Repo:      pr.Repo,
			Title:     pr.Title,
			HTMLURL:   pr.HTMLURL,
			Author:    pr.User.Login,
			State:     pr.State,
			Draft:     pr.Draft,
			UpdatedAt: pr.UpdatedAt,
			HeadSHA:   headSHA,
		})
	}

	// Prune skip-dedup entries for PRs that left the review-requested set
	// (closed, review request removed, etc.) so the map stays bounded.
	a.skipMu.Lock()
	for id := range a.lastSkippedUpdatedAt {
		if _, inCurrentBatch := seenIDs[id]; !inCurrentBatch {
			delete(a.lastSkippedUpdatedAt, id)
		}
	}
	a.skipMu.Unlock()

	return out, nil
}

// ProcessPR implements scheduler.Tier2PRProcessor.
func (a *tier2Adapter) ProcessPR(ctx context.Context, pr scheduler.Tier2PR) error {
	a.cfgMu.Lock()
	c := *a.cfg
	aiCfg := c.AIForRepo(pr.Repo)
	localDirBase := c.GitHub.LocalDirBase
	a.cfgMu.Unlock()
	// /home/heimdallm/repos/<short-name> fallback when local_dir is unset (stat-based,
	// keep outside the mutex). Lets HEIMDALLM_LOCAL_DIR_BASE give every
	// monitored repo full-repo context without a per-repo override.
	aiCfg.LocalDir = config.ResolveLocalDir(aiCfg.LocalDir, pr.Repo, localDirBase)

	ghPR := &gh.PullRequest{
		ID:        pr.ID,
		Number:    pr.Number,
		Repo:      pr.Repo,
		Title:     pr.Title,
		HTMLURL:   pr.HTMLURL,
		User:      gh.User{Login: pr.Author},
		State:     pr.State,
		Draft:     pr.Draft,
		UpdatedAt: pr.UpdatedAt,
		// Head.SHA is populated by FetchPRsToReview (after the review-guard
		// filter). Passing it here is what lets runReview's persistent
		// in-flight claim actually fire; before theburrowhub/heimdallm#264
		// this field was zero-valued and the claim guard silently skipped,
		// allowing two concurrent reviews on the same PR (#243 pattern).
		Head: gh.Branch{SHA: pr.HeadSHA},
	}
	rev := a.runReview(ghPR, aiCfg)
	if rev != nil && rev.GitHubReviewID == 0 && a.publishPub != nil {
		if err := a.publishPub.PublishPRPublish(context.Background(), rev.ID); err != nil {
			slog.Warn("ProcessPR: failed to enqueue publish", "review_id", rev.ID, "err", err)
		}
	}
	if a.watchStore != nil {
		if err := a.watchStore.Enroll(ctx, "pr", pr.Repo, pr.Number, pr.ID); err != nil {
			slog.Warn("ProcessPR: failed to enroll watch", "repo", pr.Repo, "pr", pr.Number, "err", err)
		}
	}
	return nil
}

// PublishPending implements scheduler.Tier2PRProcessor.
func (a *tier2Adapter) PublishPending() {
	reviews, err := a.store.ListUnpublishedReviews()
	if err != nil || len(reviews) == 0 {
		return
	}
	for _, rev := range reviews {
		if err := a.publishPub.PublishPRPublish(context.Background(), rev.ID); err != nil {
			slog.Warn("publish-pending: enqueue failed", "review_id", rev.ID, "err", err)
		}
	}
}

// ProcessRepo implements scheduler.Tier2IssueProcessor.
func (a *tier2Adapter) ProcessRepo(ctx context.Context, repo string) (int, error) {
	a.cfgMu.Lock()
	c := *a.cfg
	repoIT := c.IssueTrackingForRepo(repo)
	globalIT := c.GitHub.IssueTracking
	anyITEnabled := globalIT.Enabled
	if !anyITEnabled {
		for _, r := range c.AI.Repos {
			if r.IssueTracking != nil && r.IssueTracking.Enabled {
				anyITEnabled = true
				break
			}
		}
	}
	a.cfgMu.Unlock()

	if !anyITEnabled || !repoIT.Enabled {
		return 0, nil
	}

	// Resolve authenticated user
	a.loginMu.Lock()
	authUser := *a.login
	a.loginMu.Unlock()
	if authUser == "" {
		if u, err := a.ghClient.AuthenticatedUser(); err == nil {
			authUser = u
			a.loginMu.Lock()
			*a.login = u
			a.loginMu.Unlock()
		}
	}

	optsFor := func(issue *gh.Issue) issuepipeline.RunOptions {
		a.cfgMu.Lock()
		c := *a.cfg
		aiCfg := c.AIForRepo(issue.Repo)
		if aiCfg.Primary == "" {
			aiCfg.Primary = c.AI.Primary
		}
		agentCfg := c.AgentConfigFor(aiCfg.Primary)
		localDirBase := c.GitHub.LocalDirBase
		globalTimeout := c.AI.ExecutionTimeout
		a.cfgMu.Unlock()
		// /home/heimdallm/repos/<short-name> fallback when local_dir is unset (stat-based,
		// keep outside the mutex).
		aiCfg.LocalDir = config.ResolveLocalDir(aiCfg.LocalDir, issue.Repo, localDirBase)

		extraFlags := agentCfg.ExtraFlags
		if extraFlags != "" {
			if err := executor.ValidateExtraFlags(extraFlags); err != nil {
				slog.Warn("issue poll: extra_flags rejected", "err", err)
				extraFlags = ""
			}
		}

		issuePrompt, issueInstructions := resolveIssuePrompt(a.store, aiCfg.IssuePrompt, agentCfg.PromptID)
		implPrompt, implInstructions := resolveImplementPrompt(a.store, aiCfg.ImplementPrompt, agentCfg.PromptID)

		return issuepipeline.RunOptions{
			GitHubToken: a.ghToken,
			Primary:     aiCfg.Primary,
			Fallback:    aiCfg.Fallback,
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
				Timeout:              resolveExecutionTimeout(globalTimeout, agentCfg.ExecutionTimeout),
			},
			IssuePromptOverride:     issuePrompt,
			IssueInstructions:       issueInstructions,
			ImplementPromptOverride: implPrompt,
			ImplementInstructions:   implInstructions,
			PRReviewers:             aiCfg.PRReviewers,
			PRAssignee:              aiCfg.PRAssignee,
			PRLabels:                aiCfg.PRLabels,
			PRDraft:                 aiCfg.PRDraft != nil && *aiCfg.PRDraft,
			GeneratePRDescription:   aiCfg.GeneratePRDescription != nil && *aiCfg.GeneratePRDescription,
		}
	}

	return a.fetcher.ProcessRepo(ctx, repo, repoIT, authUser, optsFor)
}

// PromoteReady implements scheduler.Tier2Promoter.
func (a *tier2Adapter) PromoteReady(ctx context.Context, repos []string) (int, error) {
	a.cfgMu.Lock()
	c := *a.cfg
	globalIT := c.GitHub.IssueTracking
	a.cfgMu.Unlock()

	// Promotion only makes sense when blocked labels are configured.
	// Intentionally NOT gated on globalIT.Enabled — per-repo IT configs
	// can enable issue tracking independently while global is disabled.
	// Gating on Enabled here would silently regress promotion for those users.
	if len(globalIT.BlockedLabels) == 0 {
		return 0, nil
	}
	return issuepipeline.PromoteReady(ctx, a.ghClient, globalIT, repos, a.broker)
}

// PRAlreadyReviewed implements scheduler.Tier2Store.
func (a *tier2Adapter) PRAlreadyReviewed(githubID int64, repo string, number int, updatedAt time.Time) bool {
	existing, _ := a.store.GetPRByGithubID(githubID)
	if existing == nil && repo != "" && number > 0 {
		existing, _ = a.store.GetPRByRepoNumber(repo, number)
		if existing != nil {
			slog.Debug("pr dedup: matched stored PR by repo/number after github_id miss",
				"repo", repo, "pr", number, "incoming_github_id", githubID, "stored_github_id", existing.GithubID)
		}
	}
	if existing == nil {
		return false
	}
	// Skip PRs the user has dismissed
	if existing.Dismissed {
		return true
	}
	rev, err := a.store.LatestReviewForPR(existing.ID)
	if err != nil || rev == nil {
		return false
	}
	// Prefer PublishedAt (stamped when SubmitReview returned); fall back to
	// CreatedAt for legacy rows. CreatedAt is stamped BEFORE the Claude call,
	// so a 30s grace on CreatedAt was useless for reviews taking >30s — the
	// 2026-04-22 cost-runaway regression. See theburrowhub/heimdallm#243.
	anchor := rev.PublishedAt
	if anchor.IsZero() {
		anchor = rev.CreatedAt
	}
	return pipeline.ReviewFreshEnough(anchor, updatedAt, pipeline.GraceDefault)
}

// CheckItem implements scheduler.Tier3ItemChecker.
//
// For PRs we fetch a full snapshot (state/draft/author/updated_at) via the
// Pulls API, which lets HandleChange apply the draft and self-author guards
// against fresh data without a second round-trip. For issues we still call
// the Issues API (no draft concept).
//
// When an item has transitioned to not-open (closed/merged), persist the new
// state to the store and emit a state-changed SSE event once (only if the
// store previously recorded "open"), then return false so HandleChange does
// not run. Closed items never need a review run at Tier 3.
func (a *tier2Adapter) CheckItem(ctx context.Context, item *scheduler.WatchItem) (bool, *scheduler.ItemSnapshot, error) {
	if item.Type == "pr" {
		snap, err := a.ghClient.GetPRSnapshot(item.Repo, item.Number)
		if err != nil {
			return false, nil, err
		}
		if snap.State != "open" {
			existing, _ := a.store.GetPRByGithubID(item.GithubID)
			wasOpen := existing != nil && existing.State == "open"
			a.store.UpdatePRStateByGithubID(item.GithubID, "closed")
			if wasOpen {
				a.broker.Publish(sse.Event{
					Type: sse.EventPRStateChanged,
					Data: fmt.Sprintf(`{"pr_id":%d,"state":"closed"}`, item.GithubID),
				})
				slog.Info("tier3: PR closed/merged", "repo", item.Repo, "number", item.Number)
			}
			return false, nil, nil
		}
		if !snap.UpdatedAt.After(item.LastSeen) {
			return false, nil, nil
		}
		// Forward HeadSHA so HandleChange can feed it into runReview's
		// persistent in-flight claim (#258, theburrowhub/heimdallm#264).
		// GetPRSnapshot already fetches head.sha in the same /pulls/N call —
		// this is a free copy, no extra GitHub API cost.
		return true, &scheduler.ItemSnapshot{
			State:     snap.State,
			Draft:     snap.Draft,
			Author:    snap.Author,
			UpdatedAt: snap.UpdatedAt,
			HeadSHA:   snap.HeadSHA,
		}, nil
	}
	// Issues: GetIssue returns state + updated_at in one call. Draft is always
	// false for issues.
	issue, err := a.ghClient.GetIssue(item.Repo, item.Number)
	if err != nil {
		return false, nil, err
	}
	if issue.State != "open" {
		existing, _ := a.store.GetIssueByGithubID(item.GithubID)
		wasOpen := existing != nil && existing.State == "open"
		a.store.UpdateIssueStateByGithubID(item.GithubID, "closed")
		if wasOpen {
			a.broker.Publish(sse.Event{
				Type: sse.EventIssueStateChanged,
				Data: fmt.Sprintf(`{"issue_id":%d,"state":"closed"}`, item.GithubID),
			})
			slog.Info("tier3: issue closed", "repo", item.Repo, "number", item.Number)
		}
		return false, nil, nil
	}
	if !issue.UpdatedAt.After(item.LastSeen) {
		return false, nil, nil
	}
	return true, &scheduler.ItemSnapshot{
		State:     issue.State,
		Author:    issue.User.Login,
		UpdatedAt: issue.UpdatedAt,
	}, nil
}

// HandleChange implements scheduler.Tier3ItemChecker.
func (a *tier2Adapter) HandleChange(ctx context.Context, item *scheduler.WatchItem, snap *scheduler.ItemSnapshot) error {
	if item.Type == "pr" {
		if snap == nil {
			return nil
		}

		// Guard: apply review guards against the FRESH state from snap, not
		// the stale store copy. This closes the closed/merged-PR hole —
		// Tier 3 previously reviewed PRs that had merged between cycles.
		a.loginMu.Lock()
		botLogin := *a.login
		a.loginMu.Unlock()

		a.cfgMu.Lock()
		// Convert config.ResolvedReviewGuards to pipeline.GateConfig via same-shape
		// cast (config cannot import pipeline — import cycle).
		guards := pipeline.GateConfig((*a.cfg).ReviewGuards(botLogin))
		c := *a.cfg
		aiCfg := c.AIForRepo(item.Repo)
		a.cfgMu.Unlock()

		stored, _ := a.store.GetPRByGithubID(item.GithubID)
		title := ""
		if stored != nil {
			title = stored.Title
		}

		reason := pipeline.Evaluate(pipeline.PRGate{
			State:  snap.State,
			Draft:  snap.Draft,
			Author: snap.Author,
		}, guards)
		if reason != pipeline.SkipReasonNone {
			a.broker.Publish(sse.Event{
				Type: sse.EventReviewSkipped,
				Data: sseData(map[string]any{
					"repo":      item.Repo,
					"pr_number": item.Number,
					"pr_title":  title,
					"reason":    string(reason),
				}),
			})
			slog.Info("tier3: skipping PR",
				"repo", item.Repo, "pr", item.Number, "reason", string(reason))
			return nil
		}

		// Mirror the Tier 2 updated_at dedup against the freshly-observed
		// GitHub snapshot timestamp, NOT item.LastSeen — the queue's
		// LastSeen has already been overwritten by ResetBackoff on earlier
		// ticks and is no longer a faithful representation of the PR's
		// current updated_at.
		if a.PRAlreadyReviewed(item.GithubID, item.Repo, item.Number, snap.UpdatedAt) {
			slog.Debug("tier3: PR already reviewed, skipping", "pr", item.Number, "repo", item.Repo)
			return nil
		}

		ghPR := &gh.PullRequest{
			ID:        item.GithubID,
			Number:    item.Number,
			Repo:      item.Repo,
			State:     snap.State,
			Draft:     snap.Draft,
			UpdatedAt: snap.UpdatedAt,
			// Head.SHA is carried through ItemSnapshot from GetPRSnapshot
			// (same /pulls/N call that already populated State/Draft). The
			// persistent in-flight claim (#258) needs it to key on
			// (pr_id, head_sha); without it we reproduce the Tier 3 half of
			// theburrowhub/heimdallm#264 — a second tick on the same watched
			// PR silently bypasses the claim and runs a concurrent review.
			Head: gh.Branch{SHA: snap.HeadSHA},
		}
		if stored != nil {
			ghPR.Title = stored.Title
			ghPR.HTMLURL = stored.URL
			ghPR.User = gh.User{Login: snap.Author}
		}
		rev := a.runReview(ghPR, aiCfg)
		if rev != nil && rev.GitHubReviewID == 0 && a.publishPub != nil {
			if err := a.publishPub.PublishPRPublish(context.Background(), rev.ID); err != nil {
				slog.Warn("HandleChange: failed to enqueue publish", "review_id", rev.ID, "err", err)
			}
		}
		if a.watchStore != nil {
			if err := a.watchStore.Enroll(ctx, "pr", item.Repo, item.Number, item.GithubID); err != nil {
				slog.Warn("HandleChange: failed to enroll watch", "repo", item.Repo, "pr", item.Number, "err", err)
			}
		}
		return nil
	}
	if item.Type == "issue" {
		slog.Info("tier3: issue change detected, backoff will reset",
			"repo", item.Repo, "number", item.Number)
	}
	return nil
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
//  3. global default agent for `category` (is_default_<category> = true)
//
// The category parameter selects which of the three per-category global-
// default flags to filter on. Returns nil when nothing matches (or when
// ListAgents errors — the caller should treat this as "use the built-in
// default template"). Each resolver above this function then reads its
// own field pair from the returned Agent, so adding a third prompt type
// is a 4-line wrapper rather than a copied 30-line loop.
func resolveAgentByPriority(s *store.Store, category store.AgentCategory, repoPromptID, agentPromptID string) *store.Agent {
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
	// 3. Global default for the requested category
	for _, ag := range agents {
		switch category {
		case store.AgentCategoryPR:
			if ag.IsDefaultPR {
				return ag
			}
		case store.AgentCategoryIssue:
			if ag.IsDefaultIssue {
				return ag
			}
		case store.AgentCategoryDev:
			if ag.IsDefaultDev {
				return ag
			}
		}
	}
	return nil
}

// resolveIssuePrompt returns (customTemplate, customInstructions) for the
// issue-triage prompt. Agent selection follows resolveAgentByPriority;
// IssuePrompt takes precedence over IssueInstructions (same as Prompt vs
// Instructions for PR reviews). Both empty = use built-in default template.
func resolveIssuePrompt(s *store.Store, repoPromptID, agentPromptID string) (string, string) {
	a := resolveAgentByPriority(s, store.AgentCategoryIssue, repoPromptID, agentPromptID)
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
	a := resolveAgentByPriority(s, store.AgentCategoryDev, repoPromptID, agentPromptID)
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

// ptrBoolOrTrue returns the dereferenced value of p, or true if p is nil.
// Used to serialize *bool config fields where nil means "default enabled".
func ptrBoolOrTrue(p *bool) bool {
	if p == nil {
		return true
	}
	return *p
}

// ptrIntOr returns the dereferenced value of p, or defaultV if p is nil.
// Used to serialize *int config fields where nil means "use the built-in default".
func ptrIntOr(p *int, defaultV int) int {
	if p == nil {
		return defaultV
	}
	return *p
}

// enrollOpenItems enrolls up to 10 open PRs/issues not yet in watch_state.
// Called once per state-poller tick (every 30s) to gradually backfill items
// from before the NATS migration. Uses a batch query with LEFT JOIN to find
// unenrolled items without holding a read lock during writes.
func enrollOpenItems(ctx context.Context, s *store.Store, ws *bus.WatchStore) {
	for _, q := range []struct {
		typ   string
		query string
	}{
		{"pr", `SELECT p.github_id, p.repo, p.number FROM prs p
			LEFT JOIN watch_state w ON w.key = 'pr.' || p.github_id
			WHERE p.state='open' AND p.repo != '' AND w.key IS NULL LIMIT 10`},
		{"issue", `SELECT i.github_id, i.repo, i.number FROM issues i
			LEFT JOIN watch_state w ON w.key = 'issue.' || i.github_id
			WHERE i.state='open' AND i.repo != '' AND w.key IS NULL LIMIT 10`},
	} {
		rows, err := s.DB().Query(q.query)
		if err != nil {
			continue
		}
		type item struct {
			ghID   int64
			repo   string
			number int
		}
		var batch []item
		for rows.Next() {
			var it item
			if err := rows.Scan(&it.ghID, &it.repo, &it.number); err != nil {
				slog.Warn("state-poller: backfill scan failed", "type", q.typ, "err", err)
				continue
			}
			batch = append(batch, it)
		}
		if err := rows.Err(); err != nil {
			slog.Warn("state-poller: backfill iteration error", "type", q.typ, "err", err)
		}
		rows.Close() // close before writing to avoid SQLite lock contention

		for _, it := range batch {
			if err := ws.Enroll(ctx, q.typ, it.repo, it.number, it.ghID); err != nil {
				slog.Warn("state-poller: backfill enroll failed", "type", q.typ, "repo", it.repo, "number", it.number, "err", err)
				break // skip rest of this type, try next type
			}
		}
		if len(batch) > 0 {
			slog.Debug("state-poller: backfill enrolled", "type", q.typ, "count", len(batch))
		}
	}
}

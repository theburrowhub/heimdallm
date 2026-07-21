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
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/heimdallm/daemon/internal/activity"
	"github.com/heimdallm/daemon/internal/autonomous"
	"github.com/heimdallm/daemon/internal/bus"
	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/discovery"
	"github.com/heimdallm/daemon/internal/executor"
	gh "github.com/heimdallm/daemon/internal/github"
	issuepipeline "github.com/heimdallm/daemon/internal/issues"
	"github.com/heimdallm/daemon/internal/keychain"
	"github.com/heimdallm/daemon/internal/notify"
	"github.com/heimdallm/daemon/internal/pipeline"
	"github.com/heimdallm/daemon/internal/repoctx"
	"github.com/heimdallm/daemon/internal/scheduler"
	"github.com/heimdallm/daemon/internal/server"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/heimdallm/daemon/internal/store"
	"github.com/heimdallm/daemon/internal/worker"
	"github.com/heimdallm/daemon/launchagent"
	"github.com/nats-io/nats.go"
)

// version is overridden via -ldflags "-X main.version=..." at build time.
var version = "dev"

func versionString() string { return version }

// publishBridgeEvents re-publishes every SSE broker event to NATS so the SSE
// handler (which reads from NATS) sees events from all broker publishers.
// Each event is processed under its own recover(): a panic on one event is
// logged with a stack and the loop continues to the next, so a single bad
// event can neither crash the daemon (a panic in a bare goroutine is fatal)
// nor permanently kill the bridge that feeds every SSE client.
func publishBridgeEvents(events <-chan sse.Event, publish func(subject string, data []byte) error) {
	for event := range events {
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("sse-bridge: recovered from panic; continuing",
						"type", event.Type, "panic", r, "stack", string(debug.Stack()))
				}
			}()
			if err := publish(bus.SubjEventPrefix+event.Type, []byte(event.Data)); err != nil {
				slog.Warn("sse-bridge: publish to NATS failed", "type", event.Type, "err", err)
			}
		}()
	}
}

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
	var monitoringConflictWarner repoMonitoringConflictWarner
	monitoringConflictWarner.warn(cfg)

	if err := s.PurgeOldReviews(cfg.Retention.MaxDays); err != nil {
		slog.Warn("retention purge failed", "err", err)
	}

	if err := s.PurgeOldActivity(*cfg.ActivityLog.RetentionDays); err != nil {
		slog.Warn("activity retention purge failed", "err", err)
	}

	// Clear in-flight review claims leaked by a daemon that crashed between
	// claim and release. See theburrowhub/heimdallm#243.
	//
	// Single-instance deployment: any claim that survives a restart is, by
	// definition, orphaned — no goroutine from the previous process can still
	// be holding the work. The earlier 30-minute cutoff left a "dead zone"
	// (#544) where a claim younger than the cutoff would survive forever —
	// the daemon's restart-only sweep skipped it, and there was no periodic
	// sweep to reap it later. Clearing unconditionally at startup eliminates
	// that dead zone. The periodic sweep in startPollers handles claims
	// leaked at runtime (still uses the age-based cutoff so live reviews are
	// not killed). If multi-instance support is ever added (see #243/#426)
	// this must be replaced by a lease + instance-id scheme — a time-based
	// cutoff is the wrong abstraction for multi-instance.
	if n, err := s.ClearAllInFlight(); err != nil {
		slog.Warn("startup: clear all inflight failed", "err", err)
	} else if n > 0 {
		slog.Info("startup: cleared inflight rows", "count", n)
	}

	// Mirror of the PR-side sweep above for issue-triage claims (#292).
	if n, err := s.ClearAllIssueTriageInFlight(); err != nil {
		slog.Warn("startup: clear all issue triage inflight failed", "err", err)
	} else if n > 0 {
		slog.Info("startup: cleared issue triage inflight rows", "count", n)
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
		// publishBridgeEvents recovers per event and never panics outward, so
		// it is safe to run in this bare goroutine.
		go publishBridgeEvents(bridgeCh, eventBus.Conn().Publish)
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
	repoCtx := repoctx.NewManagerWithOptions(repoctx.ManagerOptions{
		MaxWorktreesPerRepo: cfg.AI.MaxWorktreesPerRepo,
	})

	// Sweep worktrees left behind by a previous daemon process. At
	// startup the manager has no active worktrees, so every directory
	// under `<clone>/.worktrees/` is by definition stale and safe to
	// remove. Mirrors the in-flight DB sweeps above. (#461)
	{
		ctx := context.Background()
		for _, cloneDir := range managedCloneDirs(cfg) {
			if n, err := repoCtx.PruneStaleWorktreesUnder(ctx, cloneDir); err != nil {
				slog.Warn("startup: prune stale worktrees", "dir", cloneDir, "err", err)
			} else if n > 0 {
				slog.Info("startup: pruned stale worktrees", "dir", cloneDir, "count", n)
			}
		}
	}

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

	// Wire the GitHub client as the requested-reviewers fetcher so the
	// SHA-skip path can re-review new commits when the bot is a current
	// requested reviewer but GitHub emitted no review_requested timeline
	// event (theburrowhub/heimdallm#1532). No-ops if the bot login is unset.
	p.SetReviewerFetcher(ghClient)

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
	// Wire the Tier 3 watch enroller so auto_implement-created PRs are
	// picked up by the new review-state checker (#482). watchStore
	// implements the WatchEnroller interface via its Enroll method.
	issuePipe.SetWatchEnroller(watchStore)

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
	// cfgMu protects cfg and the pipeline so reload is safe from any goroutine.
	var cfgMu sync.Mutex
	repoCurrentlyMonitored := func(repo string) bool {
		cfgMu.Lock()
		defer cfgMu.Unlock()
		return repoIsMonitored(cfg, repo)
	}
	var reloadMu sync.Mutex // serialises config reloads to prevent duplicate pipelines
	// restartMu serialises the BACKGROUND poller teardown+restart kicked off by
	// a reload that changed a poller-relevant field. Kept separate from
	// reloadMu so a config save never blocks (waiting out in-flight poll
	// cycles/reviews in oldWg.Wait()) — the reload applies cfg and returns,
	// while at most one restart runs at a time here.
	var restartMu sync.Mutex
	var lastPollUnixNano int64
	var pollIntervalNano int64
	storePollInterval := func(interval time.Duration) {
		atomic.StoreInt64(&pollIntervalNano, int64(interval))
	}
	storePollInterval(parsePollInterval(cfg.GitHub.PollInterval))
	recordPollCompleted := func(_ string, at time.Time) {
		atomic.StoreInt64(&lastPollUnixNano, at.UTC().UnixNano())
	}

	srv := server.NewWithOptions(s, broker, p, apiToken, server.Options{
		Version:   versionString(),
		StartedAt: time.Now(),
	})
	srv.SetNATSConn(eventBus.Conn())
	srv.SetConfigPath(cfgPath)
	srv.SetHealthSnapshotFn(func() server.HealthSnapshot {
		interval := time.Duration(atomic.LoadInt64(&pollIntervalNano))
		var lastPoll time.Time
		if n := atomic.LoadInt64(&lastPollUnixNano); n > 0 {
			lastPoll = time.Unix(0, n).UTC()
		}
		return server.HealthSnapshot{
			LastPollAt:   lastPoll,
			PollInterval: interval,
		}
	})
	shutdownReq := make(chan struct{}, 1)

	// discoverySvc holds the discovered repo cache.
	discoverySvc := discovery.NewService(ghClient)

	srv.SetCleanCloneFn(func(ctx context.Context, repo string) error {
		cfgMu.Lock()
		aiCfg := cfg.AIForRepo(repo)
		cfgMu.Unlock()
		return repoCtx.Purge(ctx, repo, aiCfg.CloneDir)
	})
	// Manual rename trigger for POST /admin/repo-rename (#489).
	// Constructs a one-shot reconciler with the same deps the probe
	// uses so manual triggers and automatic detection share idempotency
	// guarantees end-to-end. Reuses srv.TOMLMu() so the rewrite races
	// safely with concurrent PATCH /config writes.
	srv.SetRepoRenameFn(func(ctx context.Context, oldRepo, newRepo string) error {
		reconciler := newRenameReconciler(cfg, &cfgMu, srv.TOMLMu(), s, repoCtx, broker, cfgPath)
		return reconciler.Run(ctx, oldRepo, newRepo)
	})
	srv.SetCleanClonesFn(func(ctx context.Context) (int, error) {
		cfgMu.Lock()
		cfgSnap := cfg
		cfgMu.Unlock()
		return purgeAllManagedClones(ctx, repoCtx, cfgSnap)
	})

	runCloneRetention := func(reason string) {
		cfgMu.Lock()
		cfgSnap := cfg
		var discovered []string
		if cfg.GitHub.DiscoveryTopic != "" {
			discovered = discoverySvc.Discovered()
		}
		cfgMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		removed, err := purgeStaleManagedClones(ctx, repoCtx, cfgSnap, discovered)
		if err != nil {
			slog.Warn("clone retention purge failed", "reason", reason, "err", err)
			return
		}
		if removed > 0 {
			slog.Info("clone retention purge removed managed clones", "reason", reason, "removed", removed)
		}
	}
	runCloneRetention("startup")
	clonePurge := scheduler.New(24*time.Hour, func() { runCloneRetention("periodic") })
	clonePurge.Start()
	defer clonePurge.Stop()

	// loginMu guards cachedLogin against concurrent reads/writes from the
	// poll cycle and HTTP goroutines.
	var loginMu sync.Mutex
	var cachedLogin = resolvedBotLogin

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
			Primary:                 aiCfg.Primary,
			Fallback:                aiCfg.Fallback,
			PromptOverride:          aiCfg.Prompt,
			AgentPromptID:           agentCfg.PromptID,
			ReviewMode:              aiCfg.ReviewMode,
			InstructionAuthors:      aiCfg.InstructionAuthors,
			NeverApproveWithIssues:  aiCfg.NeverApproveWithIssues != nil && *aiCfg.NeverApproveWithIssues,
			NeverApproveMinSeverity: aiCfg.NeverApproveMinSeverity,
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
		runOpts := buildRunOpts(pr, aiCfg)
		runOpts.RepoEligible = repoCurrentlyMonitored
		rev, err := p.Run(pr, runOpts)
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
	issueFetcher.SetStageTransitioner(ghClient, broker)

	// Shared rate limiter (was Pipeline.limiter).
	limiter := scheduler.NewRateLimiter(4500)

	// tier2Adapter bridges main.go's concrete types to the polling logic.
	adapter := &tier2Adapter{
		ghClient:             ghClient,
		ghToken:              token,
		pipeline:             p,
		issuePipe:            issuePipe,
		fetcher:              issueFetcher,
		repoCtx:              repoCtx,
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
		lastBreakerTrips:     make(map[breakerTripKey]breakerTripDedup),
	}

	// Phase 2/3 of #482: build the Responder and FixRunner with real
	// dependencies and wire them into the adapter. Both modules check
	// their own Enabled flag on every Run so a cold-start with the
	// feature disabled costs nothing; flipping the flag in TOML and
	// reloading is enough to opt in.
	// botLoginAccessor is the single source for the bot's login the
	// Responder and FixRunner consume — wraps the same loginMu /
	// cachedLogin pair the adapter's cachedAuthenticatedUser uses, so
	// locking discipline lives in one closure rather than being
	// duplicated at each callsite.
	botLoginAccessor := func() string {
		loginMu.Lock()
		defer loginMu.Unlock()
		return cachedLogin
	}
	adapter.responder = issuepipeline.NewResponder(
		s, ghClient,
		&prReviewExecutor{runner: exec, cfg: &cfg, cfgMu: &cfgMu},
		broker,
		func() config.ReviewResponseConfig {
			cfgMu.Lock()
			defer cfgMu.Unlock()
			return cfg.AI.ReviewResponse
		},
		botLoginAccessor,
	)
	adapter.fixRunner = issuepipeline.NewFixRunner(
		s, ghClient,
		&prFixExecutor{
			pipeline: issuePipe,
			repoCtx:  repoCtx,
			ghClient: ghClient,
			ghToken:  token,
			cfg:      &cfg,
			cfgMu:    &cfgMu,
		},
		broker,
		func() config.ReviewFixConfig {
			cfgMu.Lock()
			defer cfgMu.Unlock()
			return cfg.AI.ReviewFix
		},
		botLoginAccessor,
	)

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

		// In-flight claim sweep (#544). Catches reviews_in_flight and
		// issue_triage_in_flight claims that were leaked at runtime (panic,
		// SIGKILL between claim and the deferred release, etc.). Worst-case
		// reap latency is sweepInterval + inflightSweepMaxAge ≈ 35 min, well
		// under the previous "forever" failure mode. The 30-min maxAge gives
		// normal reviews (seconds to a few minutes) plenty of headroom; the
		// PeriodicSweepPreservesYoungClaims tests in package store lock in
		// that a fresh claim survives this sweep.
		wg.Add(1)
		go func() {
			defer wg.Done()
			const sweepInterval = 5 * time.Minute
			const inflightSweepMaxAge = 30 * time.Minute
			ticker := time.NewTicker(sweepInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if n, err := s.ClearStaleInFlight(inflightSweepMaxAge); err != nil {
						slog.Warn("sweep: clear stale inflight failed", "err", err)
					} else if n > 0 {
						slog.Info("sweep: cleared stale inflight rows", "count", n)
					}
					if n, err := s.ClearStaleIssueTriageInFlight(inflightSweepMaxAge); err != nil {
						slog.Warn("sweep: clear stale issue triage inflight failed", "err", err)
					} else if n > 0 {
						slog.Info("sweep: cleared stale issue triage inflight rows", "count", n)
					}
				}
			}
		}()

		// Tier 1: Discovery — publishes to NATS
		cfgMu.Lock()
		discoveryInterval := parseDiscoveryInterval(
			cfg.GitHub.DiscoveryInterval,
			cfg.GitHub.PollInterval,
		)
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
					StaticRepos:     cfg.GitHub.Repositories,
					ConfiguredRepos: aiRepoKeys(cfg),
					NonMonitored:    cfg.GitHub.NonMonitored,
					DiscoveryTopic:  cfg.GitHub.DiscoveryTopic,
					DiscoveryOrgs:   orgs,
				}
			}

			// Cache archived-status lookups: an archived/active repo almost
			// never flips, but FilterArchived otherwise re-checks every repo
			// (one GET /repos each) on every discovery tick — a large slice of
			// the hourly GitHub budget for no new information. See the constant
			// rate-limit exhaustion with 80+ monitored repos.
			archiveChecker := newCachingArchivedChecker(ghClient, 6*time.Hour)

			// Publish initial repos immediately
			sendDiscoveryRepos(ctx, discoverySvc, limiter, repoPublisher, tier1ConfigFn, archiveChecker)

			ticker := time.NewTicker(discoveryInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					sendDiscoveryRepos(ctx, discoverySvc, limiter, repoPublisher, tier1ConfigFn, archiveChecker)
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
		storePollInterval(pollInterval)
		wg.Add(1)
		go func() {
			defer wg.Done()
			tier2ConfigFn := func() []string {
				cfgMu.Lock()
				defer cfgMu.Unlock()
				// Topic results become eligible only after Tier 2 classifies and
				// persists them into Repositories/NonMonitored. Reading the raw
				// discovery cache here would create a first-seen race when
				// auto-enable is off.
				return discovery.MergeRepos(cfg.GitHub.Repositories, aiRepoKeys(cfg), nil, cfg.GitHub.NonMonitored)
			}
			tier2RepoConcurrencyFn := func() int {
				cfgMu.Lock()
				defer cfgMu.Unlock()
				return cfg.AI.Tier2RepoConcurrency
			}
			runTier2(ctx, adapter, limiter, prReviewPublisher, broker, tier2ConfigFn, tier2RepoConcurrencyFn, reposChan, pollInterval, coldStart, recordPollCompleted)
		}()

		// Repo/org rename probe (#489). Detects when GitHub has
		// renamed a monitored repo (or its parent org) and dispatches
		// the reconciler to propagate the new slug across SQLite,
		// config TOML, in-memory config, and worktrees. Interval "0"
		// disables — operators can still trigger rename manually via
		// POST /admin/repo-rename. The probe runs its own goroutine
		// because its cadence (1h default) is orders of magnitude
		// longer than Tier 2's, and we want it independent of Tier 2
		// failures.
		cfgMu.Lock()
		renameInterval := parseRenameProbeInterval(cfg.AI.RepoRenameCheckInterval)
		cfgMu.Unlock()
		if renameInterval > 0 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				probe := newRenameProbe(ctx, cfg, &cfgMu, srv.TOMLMu(), ghClient, s, repoCtx, broker, cfgPath, renameInterval)
				probe.Run(ctx)
			}()
			slog.Info("rename probe: started", "interval", renameInterval)
		} else {
			slog.Info("rename probe: disabled (ai.repo_rename_check_interval=0)")
		}

		// Autonomous end-to-end poller. Selects an issue (bot-assigned >
		// unassigned > others), drives it through triage→refinement→development
		// single-flight, and lets Tier 3 react to reviews. Reuses the same
		// repo enumeration as Tier 2. The whole tick is a cheap no-op when no
		// monitored repo has autonomous enabled, so it is always started; the
		// per-repo Enabled flag (resolved under cfgMu each tick) is the gate.
		autonomousReposFn := func() []string {
			cfgMu.Lock()
			defer cfgMu.Unlock()
			return discovery.MergeRepos(cfg.GitHub.Repositories, aiRepoKeys(cfg), nil, cfg.GitHub.NonMonitored)
		}
		autonomousStageR := &autonomousStageRunner{
			ghClient:  ghClient,
			issuePipe: issuePipe,
			store:     s,
			repoCtx:   repoCtx,
			broker:    broker,
			token:     token,
			cfg:       &cfg,
			cfgMu:     &cfgMu,
			authUser:  botLoginAccessor,
		}
		autonomousPoller := &AutonomousPoller{
			ghClient: ghClient,
			store:    s,
			broker:   broker,
			orch:     autonomous.NewOrchestrator(autonomousStageR, autonomous.NewPhaseGuard()),
			runner:   exec,
			cfg:      &cfg,
			cfgMu:    &cfgMu,
			botLogin: botLoginAccessor,
			reposFn:  autonomousReposFn,
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(pollInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					// Run drives repos SEQUENTIALLY within a tick: at most one
					// issue per repo per tick, and each Drive blocks (up to
					// DevTimeout, ~45 min) before the next repo is considered.
					// For multi-repo setups the next repo therefore waits for
					// the current Drive to finish. This is intentional single-
					// flight behavior (bounded concurrency, no agent stampede),
					// not a bug.
					autonomousPoller.Run(ctx)
				}
			}
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
		skipIfUnmonitored := func(stage string) bool {
			if repoCurrentlyMonitored(msg.Repo) {
				return false
			}
			broker.Publish(sse.Event{
				Type: sse.EventReviewSkipped,
				Data: sseData(map[string]any{
					"repo":      msg.Repo,
					"pr_number": msg.Number,
					"reason":    string(pipeline.SkipReasonNotMonitored),
				}),
			})
			slog.Info("review-worker: repo no longer monitored, skipping",
				"repo", msg.Repo, "pr", msg.Number, "stage", stage)
			return true
		}

		// A repo may be disabled after Tier 2 publishes this Core NATS
		// message but before a worker slot becomes available. Revalidate before
		// spending rate-limit budget or starting the AI pipeline.
		if skipIfUnmonitored("dequeue") {
			return
		}

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
		if skipIfUnmonitored("pre_run") {
			return
		}

		cfgMu.Lock()
		c := *cfg
		aiCfg := c.AIForRepo(pr.Repo)
		localDirBase := c.GitHub.LocalDirBase
		cfgMu.Unlock()
		repoHandle, err := acquireRepoContext(ctx, repoCtx, pr.Repo, &aiCfg, localDirBase, token, repoctx.ModeRead, wtTokenFor("pr-review", pr.Number), "", "")
		if err != nil {
			logRepoContextFallback("review-worker", pr.Repo, err)
			aiCfg.LocalDir = ""
		}
		if repoHandle != nil {
			defer repoHandle.Release()
		}
		// Repo acquisition may clone/fetch for several seconds. Recheck at the
		// final execution boundary so a disable during that wait stops the CLI.
		if skipIfUnmonitored("post_acquire") {
			return
		}

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
		claimed, err := s.ClaimInFlightReview(pr.GithubID, rev.HeadSHA)
		if err != nil {
			return fmt.Errorf("claim review for publish: %w", err)
		}
		if !claimed {
			slog.Debug("publish-worker: review already claimed, skipping duplicate publish",
				"review_id", msg.ReviewID, "github_id", pr.GithubID, "head_sha", rev.HeadSHA)
			return nil // PublishPending re-enqueues if the row remains unpublished after release.
		}
		defer func() {
			if err := s.ReleaseInFlightReview(pr.GithubID, rev.HeadSHA); err != nil {
				slog.Warn("publish-worker: failed to release publish claim",
					"review_id", msg.ReviewID, "github_id", pr.GithubID,
					"head_sha", rev.HeadSHA, "err", err)
			}
		}()

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

		// Use the event decided and persisted at review time so a COMMENT
		// (never_approve_with_issues) is never resubmitted as APPROVE on retry;
		// legacy rows without a stored event fall back to severity. Mirrors the
		// pipeline's Run / PublishPending paths.
		publishEvent := pipeline.PublishEventFor(rev)
		deferForMonitoringChange := func(stage string) bool {
			if !deferPublishIfUnmonitored(pr.Repo, repoCurrentlyMonitored) {
				return false
			}
			broker.Publish(sse.Event{
				Type: sse.EventReviewSkipped,
				Data: sseData(map[string]any{
					"repo":      pr.Repo,
					"pr_number": pr.Number,
					"pr_title":  pr.Title,
					"reason":    string(pipeline.SkipReasonNotMonitored),
				}),
			})
			slog.Info("publish-worker: repo no longer monitored, deferring unpublished review",
				"review_id", rev.ID, "repo", pr.Repo, "stage", stage)
			return true
		}
		if deferForMonitoringChange("before_head_refresh") {
			return nil // ack; PublishPending re-enqueues it after the repo is re-enabled
		}

		// A deferred review is valid only for the commit it analysed. Re-fetch
		// the live PR snapshot immediately before SubmitReview so re-enabling a
		// repo cannot attach stale findings to a newer HEAD. This uses the same
		// rate-limit slot acquired above and happens while the atomic publish
		// claim is held, so duplicate messages cannot race this decision.
		snapshot, err := ghClient.GetPRSnapshot(pr.Repo, pr.Number)
		if err != nil {
			var apiErr *gh.APIError
			if errors.As(err, &apiErr) && (apiErr.StatusCode == http.StatusNotFound || apiErr.StatusCode == http.StatusGone) {
				if markErr := s.MarkReviewPublished(rev.ID, -1, "", time.Now().UTC()); markErr != nil {
					return fmt.Errorf("mark missing-PR review %d orphaned: %w", rev.ID, markErr)
				}
				slog.Info("publish-worker: PR no longer exists, marking review orphaned",
					"review_id", rev.ID, "repo", pr.Repo, "pr", pr.Number)
				return nil
			}
			return fmt.Errorf("refresh PR before publishing review: %w", err)
		}
		if reason := pendingReviewInvalidReason(rev, snapshot); reason != pipeline.SkipReasonNone {
			terminalReviewID := int64(-1)
			if reason == pipeline.SkipReasonHeadChanged {
				terminalReviewID = pipeline.SupersededReviewID
			}
			if err := s.MarkReviewPublished(rev.ID, terminalReviewID, "", time.Now().UTC()); err != nil {
				return fmt.Errorf("retire stale review %d: %w", rev.ID, err)
			}
			broker.Publish(sse.Event{
				Type: sse.EventReviewSkipped,
				Data: sseData(map[string]any{
					"repo":      pr.Repo,
					"pr_number": pr.Number,
					"pr_title":  pr.Title,
					"reason":    string(reason),
				}),
			})
			slog.Info("publish-worker: stored review no longer matches live PR, retiring it",
				"review_id", rev.ID, "repo", pr.Repo, "pr", pr.Number,
				"review_head_sha", rev.HeadSHA, "current_head_sha", snapshot.HeadSHA,
				"state", snapshot.State, "reason", string(reason))
			return nil
		}
		if rev.HeadSHA != "" && snapshot.HeadSHA == "" {
			return fmt.Errorf("refresh PR before publishing review: empty HEAD SHA for %s #%d", pr.Repo, pr.Number)
		}
		if deferForMonitoringChange("before_submit") {
			return nil
		}
		reviewBody := pipeline.AnnotateBodyForEvent(pipeline.BuildGitHubBody(result), publishEvent, len(result.Issues))
		var ghID int64
		var ghState string
		if rev.HeadSHA != "" {
			ghID, ghState, err = ghClient.SubmitReviewForCommit(
				pr.Repo, pr.Number, reviewBody, publishEvent, rev.HeadSHA,
			)
		} else {
			// Preserve retry compatibility for legacy rows created before head_sha
			// was populated. New reviews always use the commit-anchored path above.
			ghID, ghState, err = ghClient.SubmitReview(
				pr.Repo, pr.Number, reviewBody, publishEvent,
			)
		}
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

		cfgMu.Lock()
		c := *cfg
		aiCfg := c.AIForRepo(msg.Repo)
		if aiCfg.Primary == "" {
			aiCfg.Primary = c.AI.Primary
		}
		repoIT := c.IssueTrackingForRepo(msg.Repo)
		agentCfg := c.AgentConfigFor(aiCfg.Primary)
		localDirBase := c.GitHub.LocalDirBase
		globalTimeout := c.AI.ExecutionTimeout
		cfgMu.Unlock()
		loginMu.Lock()
		authUser := cachedLogin
		loginMu.Unlock()
		var ok bool
		repoIT, ok = issueTrackingWithAssigneeScope("triage-worker", msg.Repo, repoIT, authUser)
		if !ok {
			return
		}
		if !issueStageStillCurrent("triage-worker", ghIssue, repoIT, config.IssueModeReviewOnly) {
			return
		}
		ghIssue.Mode = config.IssueModeReviewOnly

		repoHandle, err := acquireRepoContext(ctx, repoCtx, msg.Repo, &aiCfg, localDirBase, token, repoctx.ModeRead, wtTokenFor("triage", msg.Number), "", "")
		if err != nil {
			logRepoContextFallback("triage-worker", msg.Repo, err)
			aiCfg.LocalDir = ""
		}
		if repoHandle != nil {
			defer repoHandle.Release()
			ensureRepoContextFullHistory(ctx, repoCtx, repoHandle, token, "triage-worker", msg.Repo)
		}

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
			TriageOwner:             aiCfg.TriageOwner,
			ImplementPromptOverride: implPrompt,
			ImplementInstructions:   implInstructions,
			PRReviewers:             aiCfg.PRReviewers,
			PRAssignee:              defaultAutoImplementPRAssignee(aiCfg.PRAssignee, authUser),
			PRLabels:                aiCfg.PRLabels,
			PRDraft:                 aiCfg.PRDraft != nil && *aiCfg.PRDraft,
			GeneratePRDescription:   aiCfg.GeneratePRDescription != nil && *aiCfg.GeneratePRDescription,
			AuthUser:                authUser,
		}

		rev, err := issuePipe.Run(ctx, ghIssue, opts)
		if err != nil {
			slog.Error("triage-worker: pipeline run failed",
				"repo", msg.Repo, "number", msg.Number, "err", err)
		} else if rev != nil && rev.ActionTaken == string(config.IssueModeReviewOnly) {
			autoPromoteAfterStage(ctx, ghClient, broker, ghIssue, rev.IssueID, repoIT, aiCfg, issuepipeline.IssueStageTriage, "triage-worker")
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

	// ── NATS issue refinement worker ────────────────────────────────────
	// Consumes refinement requests published by the Fetcher when it classifies
	// an issue as refinement. Refinement is read-only but requires a full local
	// checkout so the agent can inspect code and git history.
	refinementHandler := func(ctx context.Context, msg bus.IssueMsg) {
		ghIssue, err := ghClient.GetIssue(msg.Repo, msg.Number)
		if err != nil {
			slog.Error("refinement-worker: fetch issue from GitHub",
				"repo", msg.Repo, "number", msg.Number, "err", err)
			return
		}

		cfgMu.Lock()
		c := *cfg
		aiCfg := c.AIForRepo(msg.Repo)
		if aiCfg.Primary == "" {
			aiCfg.Primary = c.AI.Primary
		}
		repoIT := c.IssueTrackingForRepo(msg.Repo)
		agentCfg := c.AgentConfigFor(aiCfg.Primary)
		localDirBase := c.GitHub.LocalDirBase
		globalTimeout := c.AI.ExecutionTimeout
		cfgMu.Unlock()
		loginMu.Lock()
		authUser := cachedLogin
		loginMu.Unlock()
		var ok bool
		repoIT, ok = issueTrackingWithAssigneeScope("refinement-worker", msg.Repo, repoIT, authUser)
		if !ok {
			return
		}
		if !issueStageStillCurrent("refinement-worker", ghIssue, repoIT, config.IssueModeRefinement) {
			return
		}
		ghIssue.Mode = config.IssueModeRefinement

		opts, releaseRepoContext, err := buildRefinementRunOptions(ctx, s, repoCtx, msg.Repo, msg.Number, token, aiCfg, agentCfg, localDirBase, globalTimeout, false, "refinement-worker")
		if err != nil {
			slog.Error("refinement-worker: prepare repo context failed",
				"repo", msg.Repo, "number", msg.Number, "err", err)
			broker.Publish(sse.Event{
				Type: sse.EventIssueReviewError,
				Data: sseData(map[string]any{
					"repo": msg.Repo, "number": msg.Number, "error": err.Error(),
				}),
			})
			return
		}
		if releaseRepoContext != nil {
			defer releaseRepoContext()
		}

		rev, err := issuePipe.Run(ctx, ghIssue, opts)
		if err != nil {
			slog.Error("refinement-worker: pipeline run failed",
				"repo", msg.Repo, "number", msg.Number, "err", err)
		} else if rev != nil && rev.ActionTaken == string(config.IssueModeRefinement) {
			autoPromoteAfterStage(ctx, ghClient, broker, ghIssue, rev.IssueID, repoIT, aiCfg, issuepipeline.IssueStageRefinement, "refinement-worker")
		}

		// Enroll for state watching so closed/resolved issues update in the UI.
		// Runs even after pipeline failure, matching triage/implement: state
		// tracking is independent of whether the refinement artifact completed.
		if err := watchStore.Enroll(ctx, "issue", msg.Repo, msg.Number, msg.GithubID); err != nil {
			slog.Warn("refinement-worker: failed to enroll watch",
				"repo", msg.Repo, "number", msg.Number, "err", err)
		}
	}

	refinementW := worker.NewRefinementWorker(conn, maxWorkers, refinementHandler)
	refinementWCtx, refinementWCancel := context.WithCancel(context.Background())
	defer refinementWCancel()
	go func() {
		if err := refinementW.Start(refinementWCtx); err != nil {
			slog.Error("refinement worker stopped", "err", err)
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

		cfgMu.Lock()
		c := *cfg
		aiCfg := c.AIForRepo(msg.Repo)
		if aiCfg.Primary == "" {
			aiCfg.Primary = c.AI.Primary
		}
		repoIT := c.IssueTrackingForRepo(msg.Repo)
		agentCfg := c.AgentConfigFor(aiCfg.Primary)
		localDirBase := c.GitHub.LocalDirBase
		globalTimeout := c.AI.ExecutionTimeout
		cfgMu.Unlock()
		loginMu.Lock()
		authUser := cachedLogin
		loginMu.Unlock()
		var ok bool
		repoIT, ok = issueTrackingWithAssigneeScope("implement-worker", msg.Repo, repoIT, authUser)
		if !ok {
			return
		}
		if !issueStageStillCurrent("implement-worker", ghIssue, repoIT, config.IssueModeDevelop) {
			return
		}
		ghIssue.Mode = config.IssueModeDevelop

		repoHandle, err := acquireRepoContext(ctx, repoCtx, msg.Repo, &aiCfg, localDirBase, token, repoctx.ModeWrite, wtTokenFor("develop", msg.Number), "", "")
		if err != nil {
			slog.Error("implement-worker: prepare repo context failed",
				"repo", msg.Repo, "number", msg.Number, "err", err)
			broker.Publish(sse.Event{
				Type: sse.EventIssueReviewError,
				Data: sseData(map[string]any{
					"repo": msg.Repo, "number": msg.Number, "error": err.Error(),
				}),
			})
			return
		}
		if repoHandle != nil {
			defer repoHandle.Release()
		}

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
			IssuePromptOverride:      issuePrompt,
			IssueInstructions:        issueInstructions,
			TriageOwner:              aiCfg.TriageOwner,
			ImplementPromptOverride:  implPrompt,
			ImplementInstructions:    implInstructions,
			PRReviewers:              aiCfg.PRReviewers,
			PRAssignee:               defaultAutoImplementPRAssignee(aiCfg.PRAssignee, authUser),
			PRLabels:                 aiCfg.PRLabels,
			PRDraft:                  aiCfg.PRDraft != nil && *aiCfg.PRDraft,
			GeneratePRDescription:    aiCfg.GeneratePRDescription != nil && *aiCfg.GeneratePRDescription,
			AuthUser:                 authUser,
			RequireWorkDirForDevelop: true,
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
				// Gradually enroll one monitored open item not yet in watch_state per tick.
				// Backfills items from before the NATS migration without blocking startup.
				enrollOpenItems(statePollerCtx, s, watchStore, adapter.monitoredRepos())

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
		if !adapter.repoIsMonitored(msg.Repo) {
			key := fmt.Sprintf("%s.%d", msg.Type, msg.GithubID)
			if err := watchStore.Delete(ctx, key); err != nil {
				slog.Warn("state-handler: failed to remove unmonitored item",
					"key", key, "repo", msg.Repo, "err", err)
			}
			slog.Info("state-handler: repo no longer monitored, skipping",
				"type", msg.Type, "repo", msg.Repo, "number", msg.Number)
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

	// Live GitHub API rate-limit lookup for GET /github/rate_limit.
	srv.SetRateLimitFn(func() (any, error) { return ghClient.RateLimit() })

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
		reposList, nonMonList := effectiveRepoLists(c)
		localDirBaseList := append([]string(nil), c.GitHub.LocalDirBase...)
		cfgMu.Unlock()
		loginMu.Lock()
		authUser := cachedLogin
		loginMu.Unlock()
		issueTracking := c.GitHub.IssueTracking.WithDefaultAssignee(authUser)
		orgOverrides := make(map[string]map[string]any)
		for org, ai := range c.AI.Orgs {
			orgOverrides[org] = orgAIOverrideMap(ai)
		}
		repoOverrides := make(map[string]map[string]any)
		for repo, ai := range c.AI.Repos {
			repoOverrides[repo] = repoAIOverrideMap(ai)
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
			"poll_interval":               c.GitHub.PollInterval,
			"repositories":                reposList,
			"non_monitored":               nonMonList,
			"local_dir_base":              localDirBaseList,
			"ai_primary":                  c.AI.Primary,
			"ai_fallback":                 c.AI.Fallback,
			"review_mode":                 c.AI.ReviewMode,
			"retention_days":              c.Retention.MaxDays,
			"issue_tracking":              issueTracking,
			"repo_overrides":              repoOverrides,
			"org_overrides":               orgOverrides,
			"agent_configs":               agentConfigs,
			"local_dirs_detected":         localDirsDetected,
			"activity_log_enabled":        ptrBoolOrTrue(c.ActivityLog.Enabled),
			"activity_log_retention_days": ptrIntOr(c.ActivityLog.RetentionDays, 90),
			"issue_prompt":                c.AI.IssuePrompt,
			"implement_prompt":            c.AI.ImplementPrompt,
			"refinement_timeout":          c.AI.RefinementTimeout,
			"triage_owner":                c.AI.TriageOwner,
			"clone_dir":                   c.AI.CloneDir,
			"generate_pr_description":     c.AI.GeneratePRDescription,
			"never_approve_with_issues":   c.AI.NeverApproveWithIssues,
			"never_approve_min_severity":  c.AI.NeverApproveMinSeverity,
		}
		if c.AI.AutoPromoteTriage != nil {
			result["auto_promote_triage"] = *c.AI.AutoPromoteTriage
		}
		if c.AI.AutoPromoteRefinement != nil {
			result["auto_promote_refinement"] = *c.AI.AutoPromoteRefinement
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
		// Autonomous end-to-end mode config.
		autonomousOrgs := make(map[string]any)
		for org, o := range c.Autonomous.Orgs {
			autonomousOrgs[org] = autonomousOverrideMap(o)
		}
		autonomousRepos := make(map[string]any)
		for repo, o := range c.Autonomous.Repos {
			autonomousRepos[repo] = autonomousOverrideMap(o)
		}
		result["autonomous"] = map[string]any{
			"enabled":           c.Autonomous.Enabled,
			"auto_merge":        c.Autonomous.AutoMerge,
			"merge_method":      c.Autonomous.MergeMethod,
			"take_others_tasks": c.Autonomous.TakeOthersTasks,
			"reassign_on_take":  c.Autonomous.ReassignOnTake,
			"dev_max_turns":     c.Autonomous.DevMaxTurns,
			"dev_effort":        c.Autonomous.DevEffort,
			"dev_timeout":       c.Autonomous.DevTimeout,
			"claim_lease":       c.Autonomous.ClaimLease,
			"orgs":              autonomousOrgs,
			"repos":             autonomousRepos,
		}
		result["circuit_breaker"] = map[string]any{
			"per_pr_24h":        c.CircuitBreaker.PerPR24h,
			"per_repo_hr":       c.CircuitBreaker.PerRepoHr,
			"per_issue_24h":     c.CircuitBreaker.PerIssue24h,
			"per_issue_repo_hr": c.CircuitBreaker.PerIssueRepoHr,
			"per_impl_repo_hr":  c.CircuitBreaker.PerImplRepoHr,
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

	srv.SetShutdownFn(func() {
		select {
		case shutdownReq <- struct{}{}:
		default:
		}
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
		monitoringConflictWarner.warn(newCfg)

		cfgMu.Lock()
		restartPollers := configReloadRequiresPollerRestart(cfg, newCfg)
		if !restartPollers {
			cfg = newCfg
			cfgMu.Unlock()
			slog.Info("config reload: applied without poller restart")
			return nil
		}
		cfgMu.Unlock()

		// Apply the new config immediately so config reads (GET /config, the
		// next tier1ConfigFn tick, etc.) reflect it right away.
		cfgMu.Lock()
		cfg = newCfg
		cfgMu.Unlock()

		// Restart the pollers in the BACKGROUND. oldWg.Wait() can block for
		// tens of seconds (an in-flight poll cycle or agent review must finish
		// first); doing it inline made every restart-triggering config save
		// hang that long and froze the UI. restartMu serialises restarts so a
		// burst of saves can never leave two poller sets racing on the same
		// GitHub budget, and each restart tears the old set fully down before
		// starting the new one (no overlap).
		//
		// coldStart=false: Tier 2 waits one full PollInterval before its first
		// tick. Firing an immediate tick on every PATCH would fan out reviews
		// across the whole fleet and amplify the cost-runaway loop #243 closed.
		go func() {
			restartMu.Lock()
			defer restartMu.Unlock()

			cfgMu.Lock()
			oldCancel := pollerCancel
			oldWg := pollerWg
			cfgMu.Unlock()

			oldCancel()
			oldWg.Wait()

			newCancel, newWg := startPollers(context.Background(), false)

			cfgMu.Lock()
			pollerCancel = newCancel
			pollerWg = newWg
			cfgMu.Unlock()

			slog.Info("config reload: pollers restarted")
		}()

		return nil
	})

	// Wire the trigger-review callback: re-run pipeline on a single stored PR.
	// In-process per-PR-ID guard for manual triggers. Backstops the persistent
	// in-flight claim for the window where the HEAD SHA lookup fails (see
	// triggerGuard and RunOptions.Force). One instance shared across all
	// trigger invocations via the closure below.
	manualReviewGuard := newTriggerGuard()
	srv.SetTriggerReviewFn(func(prID int64) error {
		publishErr := func(msg string) {
			broker.Publish(sse.Event{
				Type: sse.EventReviewError,
				Data: sseData(map[string]any{"pr_id": prID, "error": msg}),
			})
		}

		// Reject a concurrent second click for the same PR outright. Keyed on
		// PR ID so it holds even when the SHA lookup below fails and the
		// persistent (pr_id, head_sha) claim cannot engage. Publish an
		// EventReviewError so the UI shows feedback: the HTTP handler already
		// returned 202 and runs this callback in a goroutine, so a bare error
		// return would only reach the log and the click would look accepted but
		// do nothing.
		if !manualReviewGuard.tryAcquire(prID) {
			slog.Info("trigger review: already in progress for this PR, skipping", "pr_id", prID)
			publishErr("Review already in progress for this PR.")
			return fmt.Errorf("trigger review: pr %d already in progress", prID)
		}
		defer manualReviewGuard.release(prID)

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
		repoHandle, err := acquireRepoContext(context.Background(), repoCtx, pr.Repo, &aiCfg, localDirBase, token, repoctx.ModeRead, wtTokenFor("pr-review", pr.Number), "", "")
		if err != nil {
			logRepoContextFallback("trigger review", pr.Repo, err)
			aiCfg.LocalDir = ""
		}
		if repoHandle != nil {
			defer repoHandle.Release()
		}

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

		// Resolve the current HEAD SHA up front so the in-flight claim below
		// actually engages for manual triggers. The ghPR is reconstructed from
		// stored data with an empty Head.SHA; without this lookup the claim is
		// skipped, and because Force (set below) bypasses the pipeline's own
		// SHA dedup, two rapid clicks of the Re-review button — the handler
		// queues work and returns 202 — would run two full concurrent reviews
		// and double-publish. Resolving the SHA restores the (pr_id, head_sha)
		// in-flight claim as the concurrency guard for this path. Fail-open on
		// lookup error (log + proceed without the claim), matching the poll
		// path's posture: better to lose the guard for one request than to
		// block a legitimate manual re-review on a transient API blip.
		if sha, shaErr := ghClient.GetPRHeadSHA(pr.Repo, pr.Number); shaErr != nil {
			slog.Warn("trigger review: HEAD SHA lookup failed, proceeding without in-flight claim",
				"repo", pr.Repo, "pr", pr.Number, "err", shaErr)
		} else if sha != "" {
			ghPR.Head.SHA = sha
		}
		// An empty-but-nil SHA is left unset on purpose: pipeline.Run resolves
		// it again and fails closed if it still comes back empty, rather than
		// storing an ambiguous empty-HeadSHA row. The in-process guard above
		// covers concurrency for this no-claim path.

		// Persistent in-flight claim: keyed on (store pr_id, head_sha), the
		// same mechanism as the poll loop so both paths share one guard across
		// daemon restart / config reload. This is the ONLY duplicate-work
		// defense left on the forced path: Force deliberately bypasses the
		// pipeline's SHA/re-request dedup and the circuit breaker (explicit
		// operator intent — see pipeline.RunOptions.Force), so a second
		// concurrent click is rejected here with "already in progress" while a
		// review is running, and a fresh click after it completes (claim
		// released via the defer) re-reviews as intended. Fail-open on Claim
		// error: a transient SQLite blip must not block a manual re-review.
		var triggerClaimed bool
		if ghPR.Head.SHA != "" {
			ok, err := s.ClaimInFlightReview(pr.ID, ghPR.Head.SHA)
			if err != nil {
				slog.Warn("trigger review: claim inflight failed, proceeding", "err", err)
			} else if !ok {
				// Same UX gap as the in-process guard above: publish an
				// EventReviewError so the 202-then-async click gets feedback
				// instead of a silent no-op. Message kept identical for a
				// consistent UI/log correlation across both rejection paths.
				slog.Info("trigger review: already in progress for this PR, skipping",
					"pr_id", prID, "pr", ghPR.Number)
				publishErr("Review already in progress for this PR.")
				return fmt.Errorf("trigger review: pr %d already in progress", prID)
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
		// Force: the manual "Re-review" button is explicit operator intent.
		// It must re-review the current HEAD on demand, bypassing the
		// re-request/SHA dedup gate (which fires because the app cannot
		// create a GitHub review_requested event) AND the circuit breaker.
		// Concurrency/duplicate protection is retained via the in-flight
		// claim taken above (keyed on the HEAD SHA resolved just before it).
		// See pipeline.RunOptions.Force. The poll path never sets this.
		runOpts := buildRunOpts(ghPR, aiCfg)
		runOpts.Force = true
		rev, err := p.Run(ghPR, runOpts)
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

	// Wire the issue-review trigger callback: re-run the issue at its
	// CURRENT stage. Triggered by POST /issues/{id}/review (the GUI's
	// "Re-review" button). The endpoint path is historical — when it was
	// added only review_only existed; today it dispatches to whichever
	// stage the issue is in (triage / refinement / develop) based on the
	// fresh GitHub labels. See #462.
	//
	// We refetch the issue from GitHub before classifying so an auto-
	// promote that happened since the last poll is reflected in the
	// dispatched mode; the previously stored ActionTaken lagged behind
	// and re-review always fell back to triage. Dispatch via NATS reuses
	// the existing triage / refinement / implement workers end-to-end
	// (repo context, opts, single-flight claim, auto-promote) instead of
	// duplicating that wiring in-process here.
	srv.SetTriggerIssueReviewFn(func(issueID int64) error {
		// The HTTP handler queues this work in a goroutine and returns 202,
		// so r.Context() would be cancelled as soon as the response is
		// written. Use an explicit operation timeout instead so the
		// GitHub refetch and NATS publish remain bounded.
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
		defer cancel()

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

		ghIssue, err := ghClient.GetIssue(iss.Repo, iss.Number)
		if err != nil {
			publishIssueErr(fmt.Sprintf("Failed to fetch issue from GitHub: %v", err))
			return fmt.Errorf("trigger issue review: fetch %s#%d: %w", iss.Repo, iss.Number, err)
		}

		cfgMu.Lock()
		repoIT := cfg.IssueTrackingForRepo(iss.Repo)
		cfgMu.Unlock()
		loginMu.Lock()
		authUser := cachedLogin
		loginMu.Unlock()
		// Default the scope to the daemon's own login when the config
		// leaves Assignees empty, mirroring the worker entries. Without
		// this, MatchesAssignees would pass vacuously and a manual
		// trigger from one operator could be dispatched against an
		// issue assigned to a completely different operator.
		repoIT = repoIT.WithDefaultAssignee(authUser)

		slog.Info("trigger issue review: dispatching by current stage",
			"store_issue_id", issueID, "repo", iss.Repo, "number", iss.Number,
			"labels", ghIssue.LabelNames(), "scope", repoIT.Assignees)

		if err := dispatchIssueRunByCurrentMode(ctx, issuePublisher, repoIT, ghIssue); err != nil {
			publishIssueErr(err.Error())
			return err
		}
		return nil
	})

	// Wire the issue-refinement trigger callback: run deep repo investigation on a stored issue.
	srv.SetTriggerIssueRefineFn(func(issueID int64, force bool) error {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		publishIssueErr := func(msg string) {
			broker.Publish(sse.Event{
				Type: sse.EventIssueReviewError,
				Data: sseData(map[string]any{"issue_id": issueID, "error": msg}),
			})
		}

		iss, err := s.GetIssue(issueID)
		if err != nil {
			publishIssueErr(fmt.Sprintf("Issue not found: %v", err))
			return fmt.Errorf("trigger issue refinement: get issue %d: %w", issueID, err)
		}

		ghIssue, err := ghClient.GetIssue(iss.Repo, iss.Number)
		if err != nil {
			publishIssueErr(fmt.Sprintf("Failed to fetch issue from GitHub: %v", err))
			return fmt.Errorf("trigger issue refinement: fetch GitHub issue %s #%d: %w", iss.Repo, iss.Number, err)
		}
		ghIssue.Mode = config.IssueModeRefinement

		cfgMu.Lock()
		c := *cfg
		aiCfg := c.AIForRepo(iss.Repo)
		if aiCfg.Primary == "" {
			aiCfg.Primary = c.AI.Primary
		}
		agentCfg := c.AgentConfigFor(aiCfg.Primary)
		localDirBase := c.GitHub.LocalDirBase
		globalTimeout := c.AI.ExecutionTimeout
		cfgMu.Unlock()
		opts, releaseRepoContext, err := buildRefinementRunOptions(ctx, s, repoCtx, iss.Repo, iss.Number, token, aiCfg, agentCfg, localDirBase, globalTimeout, force, "trigger issue refinement")
		if err != nil {
			publishIssueErr(fmt.Sprintf("Failed to prepare repo context: %v", err))
			return fmt.Errorf("trigger issue refinement: prepare repo context: %w", err)
		}
		if releaseRepoContext != nil {
			defer releaseRepoContext()
		}

		slog.Info("trigger issue refinement: running pipeline",
			"store_issue_id", issueID, "repo", iss.Repo, "number", iss.Number, "force", force)

		_, err = issuePipe.Run(ctx, ghIssue, opts)
		if err != nil {
			broker.Publish(sse.Event{Type: sse.EventIssueReviewError, Data: sseData(map[string]any{
				"issue_id": issueID, "repo": iss.Repo, "error": err.Error(),
			})})
			return err
		}
		return nil
	})

	// Wire the promote callback. Promotion only changes GitHub stage labels and
	// records an audit comment; the next poll executes the newly-visible stage.
	srv.SetTriggerPromoteFn(func(issueID int64) error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

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

		ghIssue, err := ghClient.GetIssue(iss.Repo, iss.Number)
		if err != nil {
			publishIssueErr(fmt.Sprintf("Failed to fetch issue from GitHub: %v", err))
			return fmt.Errorf("promote issue: fetch GitHub issue %s #%d: %w", iss.Repo, iss.Number, err)
		}

		cfgMu.Lock()
		it := cfg.IssueTrackingForRepo(iss.Repo)
		cfgMu.Unlock()

		mode := it.Classify(ghIssue.LabelNames())
		from, ok := issuepipeline.StageFromMode(mode)
		if !ok {
			msg := fmt.Sprintf("Issue is in %q mode and cannot be promoted", mode)
			publishIssueErr(msg)
			return fmt.Errorf("%w: %s", server.ErrPromoteConflict, msg)
		}
		to, err := issuepipeline.NextStage(from, it, true)
		if err != nil {
			publishIssueErr(fmt.Sprintf("Cannot promote issue: %v", err))
			if errors.Is(err, issuepipeline.ErrStageTargetLabelMissing) || errors.Is(err, issuepipeline.ErrNoNextStage) {
				return fmt.Errorf("%w: %v", server.ErrPromoteConflict, err)
			}
			return fmt.Errorf("promote issue: resolve next stage: %w", err)
		}

		comments, commentErr := ghClient.FetchIssueCommentsOnly(iss.Repo, iss.Number)
		if commentErr != nil {
			slog.Warn("promote issue: comment fetch failed, continuing without audit dedup context",
				"repo", iss.Repo, "number", iss.Number, "err", commentErr)
		}

		slog.Info("promote issue: moving issue stage labels",
			"store_issue_id", issueID, "repo", iss.Repo, "number", iss.Number, "from", from, "to", to)
		if err := issuepipeline.TransitionIssueStage(ctx, ghClient, issuepipeline.StageTransition{
			Issue:          ghIssue,
			StoreIssueID:   issueID,
			Config:         it,
			From:           from,
			To:             to,
			Trigger:        issuepipeline.StagePromotionManualAPI,
			Time:           time.Now().UTC(),
			RecentComments: comments,
			Broker:         broker,
		}); err != nil {
			publishIssueErr(fmt.Sprintf("Failed to promote issue: %v", err))
			return fmt.Errorf("promote issue: transition stage: %w", err)
		}

		slog.Info("promote issue: labels updated; poll will execute the next stage",
			"store_issue_id", issueID, "repo", iss.Repo, "number", iss.Number, "from", from, "to", to)
		return nil
	})

	go func() {
		slog.Info("daemon started", "port", cfg.Server.Port, "bind", cfg.Server.BindAddr)
		if err := srv.Start(cfg.Server.Port, cfg.Server.BindAddr); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server stopped", "err", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case received := <-sig:
		slog.Info("shutting down", "signal", received.String())
	case <-shutdownReq:
		slog.Info("shutting down via API")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Warn("server shutdown failed", "err", err)
	}
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

// parseDiscoveryInterval falls back to pollInterval when the discovery-specific
// value is empty or invalid.
// Config.Validate rejects invalid durations before we reach here, so the
// fallback normally covers the unset discovery_interval case.
func parseDiscoveryInterval(discoveryInterval, pollInterval string) time.Duration {
	d, err := time.ParseDuration(discoveryInterval)
	if err != nil || d <= 0 {
		return parsePollInterval(pollInterval)
	}
	return d
}

// configReloadRequiresPollerRestart returns true unless the diff is limited to
// fields known to be read dynamically through cfg under cfgMu. This keeps new
// config fields conservative by default: if a future field is not scrubbed in
// configReloadRestartSnapshot, reloads restart pollers until the field is
// explicitly classified as dynamic.
func configReloadRequiresPollerRestart(oldCfg, newCfg *config.Config) bool {
	if oldCfg == nil || newCfg == nil {
		return true
	}
	return !reflect.DeepEqual(configReloadRestartSnapshot(oldCfg), configReloadRestartSnapshot(newCfg))
}

func configReloadRestartSnapshot(c *config.Config) config.Config {
	snap := *c
	snap.GitHub.Repositories = normalizeReloadStringSlice(snap.GitHub.Repositories)
	snap.GitHub.NonMonitored = normalizeReloadStringSlice(snap.GitHub.NonMonitored)
	snap.GitHub.DiscoveryOrgs = normalizeReloadStringSlice(snap.GitHub.DiscoveryOrgs)
	snap.GitHub.LocalDirBase = nil
	snap.GitHub.AutoEnablePROnDiscovery = nil
	snap.GitHub.WatchInterval = ""
	snap.GitHub.IssueTracking = config.IssueTrackingConfig{}
	snap.GitHub.ReviewGuards = config.ReviewGuardsConfig{}

	snap.AI.Primary = ""
	snap.AI.Fallback = ""
	snap.AI.ReviewMode = ""
	snap.AI.ExecutionTimeout = ""
	snap.AI.Agents = nil
	snap.AI.Repos = nil
	snap.AI.Orgs = nil
	snap.AI.PRMetadata = config.PRMetadataConfig{}
	snap.AI.PRReviewers = nil
	snap.AI.PRLabels = nil
	snap.AI.PRAssignee = ""
	snap.AI.PRDraft = nil
	snap.AI.IssuePrompt = ""
	snap.AI.ImplementPrompt = ""
	snap.AI.RefinementTimeout = ""
	snap.AI.TriageOwner = ""
	snap.AI.CloneDir = ""
	snap.AI.AutoPromoteTriage = nil
	snap.AI.AutoPromoteRefinement = nil
	snap.AI.Tier2RepoConcurrency = 0
	snap.AI.GeneratePRDescription = false
	snap.AI.NeverApproveWithIssues = false
	snap.AI.NeverApproveMinSeverity = ""
	snap.AI.ReviewResponse = config.ReviewResponseConfig{}
	snap.AI.ReviewFix = config.ReviewFixConfig{}

	snap.Retention = config.RetentionConfig{}
	snap.ActivityLog = config.ActivityLogConfig{}
	snap.CircuitBreaker = config.CircuitBreakerConfig{}
	return snap
}

func normalizeReloadStringSlice(v []string) []string {
	if len(v) == 0 {
		return nil
	}
	return append([]string(nil), v...)
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

// resolveRefinementTimeout lets the stage-specific cap win over generic
// per-agent/global execution timeouts. Refinement is expected to inspect the
// repo and git history, so the default intentionally runs longer than normal
// review/develop executor calls.
func resolveRefinementTimeout(refinementTimeout, globalTimeout, agentTimeout string) time.Duration {
	if refinementTimeout != "" {
		if d, err := time.ParseDuration(refinementTimeout); err == nil && d > 0 {
			return d
		}
	}
	if agentTimeout != "" {
		if d, err := time.ParseDuration(agentTimeout); err == nil && d > 0 {
			return d
		}
	}
	return resolveExecutionTimeout(globalTimeout, "")
}

// ── Standalone poller functions (replaced Pipeline goroutines) ───────────

// sendDiscoveryRepos merges static + discovered repos and publishes the
// full list to NATS. Extracted from the old tier1.go sendRepos.
// cachingArchivedChecker wraps a discovery.ArchivedChecker with a TTL cache so
// the tier1 discovery loop does not spend one GET /repos per monitored repo on
// every tick re-confirming near-static archived status. Only successful lookups
// are cached (errors fall through, preserving FilterArchived's fail-open
// behavior).
type cachingArchivedChecker struct {
	inner discovery.ArchivedChecker
	ttl   time.Duration
	mu    sync.Mutex
	cache map[string]archivedCacheEntry
}

type archivedCacheEntry struct {
	archived bool
	at       time.Time
}

func newCachingArchivedChecker(inner discovery.ArchivedChecker, ttl time.Duration) *cachingArchivedChecker {
	return &cachingArchivedChecker{inner: inner, ttl: ttl, cache: make(map[string]archivedCacheEntry)}
}

func (c *cachingArchivedChecker) IsRepoArchived(repo string) (bool, error) {
	c.mu.Lock()
	e, ok := c.cache[repo]
	c.mu.Unlock()
	if ok && time.Since(e.at) < c.ttl {
		return e.archived, nil
	}
	archived, err := c.inner.IsRepoArchived(repo)
	if err != nil {
		return archived, err // don't cache transient failures
	}
	c.mu.Lock()
	c.cache[repo] = archivedCacheEntry{archived: archived, at: time.Now()}
	c.mu.Unlock()
	return archived, nil
}

func sendDiscoveryRepos(
	ctx context.Context,
	disc scheduler.Tier1Discovery,
	limiter *scheduler.RateLimiter,
	pub scheduler.Tier1Publisher,
	configFn func() scheduler.Tier1Config,
	archiveChecker discovery.ArchivedChecker,
) {
	cfg := configFn()
	var discovered []string
	if cfg.DiscoveryTopic != "" {
		if limiter != nil {
			if err := limiter.Acquire(ctx, scheduler.TierDiscovery); err != nil {
				slog.Warn("tier1: acquire discovery rate-limit token failed", "err", err)
				return
			}
		}
		if err := disc.Refresh(cfg.DiscoveryTopic, cfg.DiscoveryOrgs); err != nil {
			slog.Warn("tier1: discovery refresh failed, using cached discovered repos", "err", err)
		}
		discovered = disc.Discovered()
	}

	repos := discovery.MergeRepos(cfg.StaticRepos, cfg.ConfiguredRepos, discovered, cfg.NonMonitored)

	if archiveChecker != nil {
		active, archived := discovery.FilterArchived(repos, archiveChecker, discovered)
		for _, r := range archived {
			slog.Warn("tier1: dropping archived/deleted repo from active set", "repo", r)
		}
		repos = active
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

// processReposInParallel runs workFn for every repo in repos with at
// most `concurrency` calls in flight at once. Returns the sum of the
// integer return values from the workers. workFn errors are silently
// counted as zero — the helper has no domain context to log them
// usefully; callers wrap workFn to log per-repo failures in the
// vocabulary that fits their tier. (#481)
//
// A non-positive concurrency falls back to
// config.DefaultTier2RepoConcurrency so a misconfiguration cannot
// deadlock the daemon. Nil or empty repo list is a no-op.
//
// Cancellation: both the per-repo scheduling loop and the semaphore
// acquire observe ctx.Done so a daemon shutdown does not get stuck
// waiting for the last free slot when workers are still draining.
// Already-running workers receive ctx through workFn and are
// responsible for their own short-circuit.
func processReposInParallel(
	ctx context.Context,
	repos []string,
	concurrency int,
	workFn func(ctx context.Context, repo string) (int, error),
) int {
	if len(repos) == 0 {
		return 0
	}
	if concurrency <= 0 {
		concurrency = config.DefaultTier2RepoConcurrency
	}
	if concurrency > len(repos) {
		concurrency = len(repos)
	}

	sem := make(chan struct{}, concurrency)
	var total int64
	var wg sync.WaitGroup
schedule:
	for _, repo := range repos {
		select {
		case <-ctx.Done():
			// Stop dispatching new work; a plain `break` here would
			// only exit the select, leaving the loop to keep queuing.
			break schedule
		case sem <- struct{}{}:
			// Acquired a slot. Fall through to spawn the worker.
		}
		wg.Add(1)
		go func(repo string) {
			defer wg.Done()
			defer func() { <-sem }()
			n, err := workFn(ctx, repo)
			if err != nil {
				return
			}
			atomic.AddInt64(&total, int64(n))
		}(repo)
	}
	wg.Wait()
	return int(total)
}

// runTier2 runs the PR/issue polling loop. Replaces the old RunTier2 from
// the scheduler package.
//
// repoConcurrencyFn returns the live per-tick cap on parallel
// per-repo issue processing (`ai.tier2_repo_concurrency`). It is
// evaluated on every tick so a config reload takes effect without
// restarting the daemon.
func runTier2(
	ctx context.Context,
	adapter *tier2Adapter,
	limiter *scheduler.RateLimiter,
	prPublisher scheduler.Tier2PRPublisher,
	ssePub sse.Publisher,
	configFn func() []string,
	repoConcurrencyFn func() int,
	reposChan <-chan []string,
	interval time.Duration,
	coldStart bool,
	pollCompletedFn func(kind string, at time.Time),
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
				// Classify first so live eligibility callbacks can never observe a
				// raw topic result before auto-enable=false records it disabled.
				adapter.upsertDiscoveredFromTopics(r)
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

	snapshotRepos := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), repos...)
	}

	// runPRTier publishes review requests for every reviewable PR.
	// Independent of the issue tier so a slow GitHub Search call does
	// not block per-repo issue work.
	runPRTier := func(currentRepos []string) {
		sse.EmitPollingStarted(ssePub, "prs", currentRepos)
		prStart := time.Now()
		prCount := 0
		defer func() {
			completedAt := time.Now()
			if pollCompletedFn != nil {
				pollCompletedFn("prs", completedAt)
			}
			sse.EmitPollingCompleted(ssePub, "prs", prCount, completedAt.Sub(prStart))
		}()
		if err := limiter.Acquire(ctx, scheduler.TierRepo); err != nil {
			return
		}
		prs, err := adapter.FetchPRsToReview()
		if err != nil {
			slog.Error("tier2: fetch PRs", "err", err)
			return
		}
		monitoredSet := make(map[string]struct{}, len(currentRepos))
		for _, r := range currentRepos {
			monitoredSet[r] = struct{}{}
		}
		for _, pr := range prs {
			if _, ok := monitoredSet[pr.Repo]; !ok {
				continue
			}
			if adapter.PRAlreadyReviewed(pr.ID, pr.Repo, pr.Number, pr.UpdatedAt, pr.HeadSHA) {
				continue
			}
			prCount++
			if err := prPublisher.PublishPRReview(ctx, pr.Repo, pr.Number, pr.ID, pr.HeadSHA); err != nil {
				slog.Error("tier2: publish PR review", "repo", pr.Repo, "pr", pr.Number, "err", err)
			}
		}
	}

	// runIssueTier promotes ready issues and processes every repo's
	// issue list in parallel, bounded by ai.tier2_repo_concurrency.
	//
	// NOTE: if NATS publishes are ever added to this tier, also call
	// adapter.PublishPending() at the end — it is intentionally bound
	// to the PR tick today (see prTick) because pending publishes
	// originate exclusively from runPRTier.
	runIssueTier := func(currentRepos []string) {
		sse.EmitPollingStarted(ssePub, "issues", currentRepos)
		issueStart := time.Now()
		// issueCount is intentionally captured by the deferred
		// EmitPollingCompleted closure so the final value (assigned
		// after processReposInParallel returns) is what the SSE event
		// reports. Reassigning it later via `=` keeps the capture
		// valid; a future refactor that switches to a fresh local
		// would silently emit 0.
		issueCount := 0
		defer func() {
			completedAt := time.Now()
			if pollCompletedFn != nil {
				pollCompletedFn("issues", completedAt)
			}
			sse.EmitPollingCompleted(ssePub, "issues", issueCount, completedAt.Sub(issueStart))
		}()
		if err := limiter.Acquire(ctx, scheduler.TierRepo); err != nil {
			return
		}
		if n, err := adapter.PromoteReady(ctx, currentRepos); err != nil {
			slog.Error("tier2: promotion", "err", err)
		} else if n > 0 {
			slog.Info("tier2: promoted issues", "count", n)
		}
		concurrency := config.DefaultTier2RepoConcurrency
		if repoConcurrencyFn != nil {
			if v := repoConcurrencyFn(); v > 0 {
				concurrency = v
			}
		}
		issueCount = processReposInParallel(ctx, currentRepos, concurrency, func(ctx context.Context, repo string) (int, error) {
			if err := limiter.Acquire(ctx, scheduler.TierRepo); err != nil {
				return 0, err
			}
			n, err := adapter.ProcessRepo(ctx, repo)
			if err != nil {
				// Demote to debug during graceful shutdown — a
				// cancelled ctx is expected behaviour, not a fault
				// worth paging an operator.
				if ctx.Err() != nil {
					slog.Debug("tier2: issue processing cancelled", "repo", repo, "err", err)
				} else {
					slog.Error("tier2: issue processing", "repo", repo, "err", err)
				}
				return 0, err
			}
			if n > 0 {
				slog.Info("tier2: processed issues", "repo", repo, "count", n)
			}
			return n, nil
		})
	}

	// PR and issue tiers run on independent tickers so a slow issue
	// cycle (taking longer than `interval`) cannot delay the next PR
	// poll. Each tier serialises against itself: a Ticker drops
	// extra ticks when its previous run is still in flight, so we
	// never spawn two concurrent issue cycles.
	prTick := func() {
		currentRepos := intersectMonitoredRepos(snapshotRepos(), configFn)
		if len(currentRepos) > 0 {
			runPRTier(currentRepos)
		}
		// PublishPending lives in the PR tick on purpose: pending
		// publishes are almost exclusively PR-review NATS messages
		// from runPRTier, and retries are idempotent. It must still run
		// when the live repo set is empty so the adapter can re-evaluate
		// pending rows immediately after a config reload re-enables their
		// repositories. Disabled repos remain pending and are not enqueued.
		// If we ever
		// route issue-side NATS publishes through the same queue,
		// add a sibling call inside issueTick rather than removing
		// this one.
		adapter.PublishPending()
	}
	issueTick := func() {
		currentRepos := intersectMonitoredRepos(snapshotRepos(), configFn)
		if len(currentRepos) == 0 {
			return
		}
		runIssueTier(currentRepos)
	}

	// runTickerLoop is the per-tier event loop. time.Ticker's channel
	// is buffered to size 1, so a tick that fires while the previous
	// `tick()` is still running is silently dropped — this is the
	// invariant that prevents a tier from running concurrently against
	// itself without needing a mutex. PR and issue tiers each get
	// their own goroutine + ticker so a slow run on one tier never
	// stalls the other.
	runTickerLoop := func(tick func()) {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		if coldStart {
			tick()
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				tick()
			}
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); runTickerLoop(prTick) }()
	go func() { defer wg.Done(); runTickerLoop(issueTick) }()
	wg.Wait()
}

// intersectMonitoredRepos applies the live config as a final eligibility gate
// to Tier 1's last published repo snapshot. Tier 1 remains responsible for
// discovery and archived-repo filtering, while the live set makes an explicit
// disable effective immediately instead of waiting for the next discovery
// publication. It also closes the first-discovery race where Tier 1 published
// a new topic repo, Tier 2 persisted it as non_monitored, then reviewed it from
// the stale pre-persistence snapshot.
func intersectMonitoredRepos(current []string, configFn func() []string) []string {
	if len(current) == 0 {
		return nil
	}
	if configFn == nil {
		return append([]string(nil), current...)
	}

	live := configFn()
	if len(live) == 0 {
		return nil
	}
	liveSet := make(map[string]struct{}, len(live))
	for _, repo := range live {
		if repo = strings.TrimSpace(repo); repo != "" {
			liveSet[repo] = struct{}{}
		}
	}

	out := make([]string, 0, len(current))
	for _, repo := range current {
		if _, ok := liveSet[strings.TrimSpace(repo)]; ok {
			out = append(out, repo)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// aiRepoKeys returns the sorted list of repos with an explicit [ai.repos.*]
// entry. Used to seed MergeRepos with the operator's TOML opt-ins so a repo
// that is wired up but has no active PRs still receives issue polling.
// See theburrowhub/heimdallm#281.
//
// Sorted output keeps the published repo list deterministic between ticks
// (Go map iteration is randomised) so log lines and SSE payloads do not
// re-shuffle when nothing has actually changed.
func aiRepoKeys(c *config.Config) []string {
	if c == nil || len(c.AI.Repos) == 0 {
		return nil
	}
	out := make([]string, 0, len(c.AI.Repos))
	for repo := range c.AI.Repos {
		// TrimSpace to match monitoredRepoSet's normalization so a TOML key
		// like " org/repo " is treated identically across both code paths.
		if r := strings.TrimSpace(repo); r != "" {
			out = append(out, r)
		}
	}
	sort.Strings(out)
	return out
}

// aiReposInNonMonitored returns repo-specific AI entries that are disabled by
// the effective non_monitored set. Older releases could persist auto-discovery
// decisions in the same SQLite row as explicit UI toggles, so there is no safe
// way to rewrite either list automatically. Keeping this helper read-only lets
// the daemon surface the ambiguity without changing operator state.
func aiReposInNonMonitored(c *config.Config) []string {
	if c == nil || len(c.AI.Repos) == 0 || len(c.GitHub.NonMonitored) == 0 {
		return nil
	}
	disabled := make(map[string]struct{}, len(c.GitHub.NonMonitored))
	for _, repo := range c.GitHub.NonMonitored {
		if repo != "" {
			disabled[repo] = struct{}{}
		}
	}
	conflicts := make([]string, 0)
	for _, repo := range aiRepoKeys(c) {
		if _, ok := disabled[repo]; ok {
			conflicts = append(conflicts, repo)
		}
	}
	return conflicts
}

// repoMonitoringConflictWarner logs each distinct conflict set once per
// process. Reloads are serialized by reloadMu, so this small deduper does not
// need its own lock. Clearing the conflict resets the fingerprint so a later
// reintroduction is reported again.
type repoMonitoringConflictWarner struct {
	lastFingerprint string
}

func (w *repoMonitoringConflictWarner) next(c *config.Config) []string {
	conflicts := aiReposInNonMonitored(c)
	fingerprint := strings.Join(conflicts, "\x00")
	if fingerprint == w.lastFingerprint {
		return nil
	}
	w.lastFingerprint = fingerprint
	return conflicts
}

func (w *repoMonitoringConflictWarner) warn(c *config.Config) {
	conflicts := w.next(c)
	if len(conflicts) == 0 {
		return
	}
	slog.Warn(
		"config: repo-specific AI overrides are present for repos in github.non_monitored; automatic processing stays disabled and overrides are retained for manual runs",
		"repos", conflicts,
		"count", len(conflicts),
	)
}

// effectiveRepoLists returns the same monitored view used by the schedulers
// for GET /config. Explicit [ai.repos.*] entries are implicit opt-ins unless
// they also appear in non_monitored; exposing only the raw github.repositories
// slice would make the UI label an actively-polled repo as Not monitored.
func effectiveRepoLists(c *config.Config) (monitored, nonMonitored []string) {
	if c == nil {
		return nil, nil
	}
	nonMonitored = append([]string(nil), c.GitHub.NonMonitored...)
	monitored = discovery.MergeRepos(
		c.GitHub.Repositories,
		aiRepoKeys(c),
		nil,
		nonMonitored,
	)
	return monitored, nonMonitored
}

// upsertDiscoveredRepos adds PRs' repos to the monitored (or non-monitored)
// list when they're new. Returns the list of repos that were added.
// Never removes and never overrides an explicit NonMonitored entry.
//
// The Flutter UI maps prEnabled via list membership: prEnabled=true ⇔
// Repositories, prEnabled=false ⇔ NonMonitored, neither ⇔ undiscovered.
// AutoEnablePRForDiscovery() controls which list a new repo lands in.
//
// Caller is responsible for persisting the updated Config and recording
// first-seen timestamps. This helper is pure state mutation so it's easy
// to test in isolation.
func upsertDiscoveredRepos(c *config.Config, prs []*gh.PullRequest) []string {
	inRepositories := make(map[string]struct{}, len(c.GitHub.Repositories))
	for _, r := range c.GitHub.Repositories {
		inRepositories[r] = struct{}{}
	}
	inNonMonitored := make(map[string]struct{}, len(c.GitHub.NonMonitored))
	for _, r := range c.GitHub.NonMonitored {
		inNonMonitored[r] = struct{}{}
	}

	// Build an org allowlist from DiscoveryOrgs. When set, repos whose org
	// prefix is not in the list are silently skipped — prevents the PR
	// review-requested search (which spans all of GitHub) from auto-adopting
	// repos outside the operator's intended organisations.
	allowedOrgs := make(map[string]struct{}, len(c.GitHub.DiscoveryOrgs))
	for _, o := range c.GitHub.DiscoveryOrgs {
		allowedOrgs[strings.ToLower(o)] = struct{}{}
	}

	enable := c.GitHub.AutoEnablePRForDiscovery()
	added := []string{}
	for _, pr := range prs {
		if pr.Repo == "" {
			continue
		}
		// An explicit disable is authoritative. In particular, an
		// [ai.repos.*] override may customize a repo without opting it back in;
		// seeing a review-requested PR must never erase the operator's choice.
		if _, disabled := inNonMonitored[pr.Repo]; disabled {
			continue
		}

		// A repo with an explicit [ai.repos.*] entry is an implicit opt-in only
		// while it is not explicitly disabled. It still bypasses the discovery
		// org allowlist so a configured repo outside discovery_orgs can be
		// monitored, but NonMonitored above always wins.
		if _, hasExplicitAIConfig := c.AI.Repos[pr.Repo]; hasExplicitAIConfig {
			_, alreadyMonitored := inRepositories[pr.Repo]
			if !alreadyMonitored {
				c.GitHub.Repositories = append(c.GitHub.Repositories, pr.Repo)
				inRepositories[pr.Repo] = struct{}{}
				slog.Info("upsertDiscoveredRepos: added explicitly-configured repo",
					"repo", pr.Repo)
				added = append(added, pr.Repo)
			}
			continue
		}

		// Auto-discovered repos (no explicit config): skip when already tracked
		// in either list.
		if _, ok := inRepositories[pr.Repo]; ok {
			continue
		}
		// Filter by allowed orgs when configured.
		if len(allowedOrgs) > 0 {
			org := ""
			if i := strings.IndexByte(pr.Repo, '/'); i > 0 {
				org = pr.Repo[:i]
			}
			if _, ok := allowedOrgs[strings.ToLower(org)]; !ok {
				slog.Debug("upsertDiscoveredRepos: skipping repo outside allowed orgs",
					"repo", pr.Repo, "org", org, "allowed", c.GitHub.DiscoveryOrgs)
				continue
			}
		}
		if enable {
			c.GitHub.Repositories = append(c.GitHub.Repositories, pr.Repo)
			inRepositories[pr.Repo] = struct{}{}
		} else {
			c.GitHub.NonMonitored = append(c.GitHub.NonMonitored, pr.Repo)
			inNonMonitored[pr.Repo] = struct{}{}
		}
		added = append(added, pr.Repo)
	}
	return added
}

// upsertDiscoveredFromTopics persists repos received from tier1's topic
// discovery into cfg.GitHub.Repositories (or NonMonitored) and invokes
// processDiscoveredRepos to write them to the K/V store. This ensures repos
// discovered by topic appear in heimdallm-cli status and GET /config even
// when they have no open PRs. Fixes #507.
func (a *tier2Adapter) upsertDiscoveredFromTopics(repos []string) {
	if len(repos) == 0 {
		return
	}

	a.cfgMu.Lock()
	cfg := *a.cfg

	known := make(map[string]struct{}, len(cfg.GitHub.Repositories)+len(cfg.GitHub.NonMonitored))
	for _, r := range cfg.GitHub.Repositories {
		known[r] = struct{}{}
	}
	for _, r := range cfg.GitHub.NonMonitored {
		known[r] = struct{}{}
	}
	// Explicit [ai.repos.*] entries are already operator opt-ins. They may not
	// also appear in github.repositories, and auto-enable=false must not
	// misclassify them as newly discovered and append them to NonMonitored.
	for repo := range cfg.AI.Repos {
		known[repo] = struct{}{}
	}

	enable := cfg.GitHub.AutoEnablePRForDiscovery()
	var added []string
	for _, repo := range repos {
		if repo == "" {
			continue
		}
		if _, ok := known[repo]; ok {
			continue
		}
		if enable {
			cfg.GitHub.Repositories = append(cfg.GitHub.Repositories, repo)
		} else {
			cfg.GitHub.NonMonitored = append(cfg.GitHub.NonMonitored, repo)
		}
		known[repo] = struct{}{}
		added = append(added, repo)
	}

	reposSnap := append([]string(nil), cfg.GitHub.Repositories...)
	nonMonSnap := append([]string(nil), cfg.GitHub.NonMonitored...)
	a.cfgMu.Unlock()

	// Intentionally release the mutex before persisting to the K/V store:
	// processDiscoveredRepos performs I/O (SQLite writes, SSE publish) that
	// should not hold the config lock. The in-memory config is already
	// mutated above; the snapshots capture the post-mutation state.
	if len(added) > 0 {
		slog.Info("tier2: persisting topic-discovered repos", "added", len(added), "repos", added)
		processDiscoveredRepos(added, reposSnap, nonMonSnap, a.store, a.broker, time.Now())
	}
}

// ── tier2Adapter bridges main.go's concrete types to Pipeline interfaces ──

type tier2Adapter struct {
	ghClient   *gh.Client
	ghToken    string
	pipeline   *pipeline.Pipeline
	issuePipe  *issuepipeline.Pipeline
	fetcher    *issuepipeline.Fetcher
	repoCtx    *repoctx.Manager
	store      *store.Store
	broker     *sse.Broker
	cfgMu      *sync.Mutex
	cfg        **config.Config
	loginMu    *sync.Mutex
	login      *string
	runReview  func(pr *gh.PullRequest, aiCfg config.RepoAI) *store.Review
	publishPub *bus.PRPublishPublisher
	watchStore *bus.WatchStore
	// Review-state vigilance dispatch (#482). Optional — nil-safe so a
	// daemon configured without the opt-in feature flags simply skips
	// the dispatch and the new CheckItem branch remains observational.
	responder reviewResponderDispatcher
	fixRunner reviewFixDispatcher

	// skipMu protects the lightweight SSE dedup caches below.
	skipMu               sync.Mutex
	lastSkippedUpdatedAt map[int64]time.Time
	lastBreakerTrips     map[breakerTripKey]breakerTripDedup
}

const breakerTripDedupTTL = 24 * time.Hour

func (a *tier2Adapter) repoIsMonitored(repo string) bool {
	if a == nil || a.cfg == nil {
		return false
	}
	if a.cfgMu == nil {
		return repoIsMonitored(*a.cfg, repo)
	}
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	return repoIsMonitored(*a.cfg, repo)
}

func (a *tier2Adapter) monitoredRepos() []string {
	if a == nil || a.cfg == nil {
		return nil
	}
	if a.cfgMu != nil {
		a.cfgMu.Lock()
		defer a.cfgMu.Unlock()
	}
	set := monitoredRepoSet(*a.cfg, nil)
	repos := make([]string, 0, len(set))
	for repo := range set {
		repos = append(repos, repo)
	}
	sort.Strings(repos)
	return repos
}

func (a *tier2Adapter) cachedAuthenticatedUser() string {
	if a == nil || a.login == nil {
		return ""
	}
	if a.loginMu == nil {
		return *a.login
	}
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	return *a.login
}

func (a *tier2Adapter) resolveAuthenticatedUser() string {
	if authUser := a.cachedAuthenticatedUser(); authUser != "" {
		return authUser
	}
	if a == nil || a.ghClient == nil {
		return ""
	}
	u, err := a.ghClient.AuthenticatedUser()
	if err != nil {
		return ""
	}
	if a.login != nil {
		if a.loginMu == nil {
			*a.login = u
		} else {
			a.loginMu.Lock()
			*a.login = u
			a.loginMu.Unlock()
		}
	}
	return u
}

type breakerTripKey struct {
	Repo    string
	Number  int
	HeadSHA string
	Reason  string
}

type breakerTripDedup struct {
	UpdatedAt time.Time
	EmittedAt time.Time
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

	// Defer reviews on repos discovered THIS call to upsertDiscoveredRepos
	// by one tick so the UI receives `repo_discovered` before
	// `review_started` (#481). The guarantee is "one tick relative to
	// upsertDiscoveredRepos", not "guaranteed UI delivery": if the SSE
	// bridge to NATS is stalled, the UI may still see the events out of
	// order despite the deferral. On the next tick
	// `upsertDiscoveredRepos` returns an empty `added` list for these
	// repos (they're already in the config), and the same PR flows
	// through normally.
	addedThisTick := make(map[string]struct{}, len(added))
	for _, r := range added {
		addedThisTick[r] = struct{}{}
	}

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

	// Build org allowlist for PR filtering — mirrors the upsert filter.
	prAllowedOrgs := make(map[string]struct{}, len(cfg.GitHub.DiscoveryOrgs))
	for _, o := range cfg.GitHub.DiscoveryOrgs {
		prAllowedOrgs[strings.ToLower(o)] = struct{}{}
	}

	for _, pr := range prs {
		if pr.Repo == "" {
			slog.Warn("adapter: skipping PR with empty repo", "pr_number", pr.Number)
			continue
		}
		// Skip PRs from orgs outside discovery_orgs when configured.
		if len(prAllowedOrgs) > 0 {
			org := ""
			if i := strings.IndexByte(pr.Repo, '/'); i > 0 {
				org = pr.Repo[:i]
			}
			if _, ok := prAllowedOrgs[strings.ToLower(org)]; !ok {
				continue
			}
		}
		// Defer reviews for repos that were auto-discovered this tick;
		// the next tick picks them up after `repo_discovered` has
		// reached the UI. See #481.
		if _, justDiscovered := addedThisTick[pr.Repo]; justDiscovered {
			slog.Info("tier2: deferring review for newly-discovered repo to next tick",
				"repo", pr.Repo, "pr", pr.Number)
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

		// Resolve the HEAD SHA and confirm the bot is still a pending
		// reviewer via the Pulls API (same call, zero extra cost). The
		// Search Issues API does NOT populate head.sha, and its index
		// can lag behind the actual requested_reviewers list — a PR may
		// still appear in review-requested:<bot> results for up to ~2 min
		// after the bot submits a review. Checking requested_reviewers
		// here eliminates those "ghost" enqueues at the source, replacing
		// the former 2-minute PublishedAt grace in PRAlreadyReviewed.
		//
		// See theburrowhub/heimdallm#264 for the SHA plumbing bug this
		// closes, and theburrowhub/heimdallm#243 for the cost-runaway
		// that the grace window originally mitigated.
		//
		// Fail-open on resolver error: empty HeadSHA makes runReview fall
		// back to the other layered defenses (fail-closed SHA in
		// pipeline.Run, circuit breaker). Blocking a review on a
		// transient lookup blip would be worse than leaning on those
		// defenses for one cycle.
		info, shaErr := a.ghClient.GetPRHeadInfo(pr.Repo, pr.Number)
		if shaErr != nil {
			slog.Warn("tier2: HEAD info lookup failed, in-flight claim will be skipped for this tick",
				"repo", pr.Repo, "pr", pr.Number, "err", shaErr)
		} else if botLogin != "" && !info.ReviewRequestedFor(botLogin) {
			// The bot is no longer in requested_reviewers — this is a
			// ghost result from the Search API's replication lag. Skip
			// silently; the PR will drop out of search results soon.
			slog.Debug("tier2: bot not in requested_reviewers, skipping search-index ghost",
				"repo", pr.Repo, "pr", pr.Number, "bot", botLogin)
			continue
		}
		headSHA := info.HeadSHA

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
	a.pruneBreakerTripDedup(time.Now())

	return out, nil
}

// ProcessPR implements scheduler.Tier2PRProcessor.
func (a *tier2Adapter) ProcessPR(ctx context.Context, pr scheduler.Tier2PR) error {
	a.cfgMu.Lock()
	c := *a.cfg
	aiCfg := c.AIForRepo(pr.Repo)
	localDirBase := c.GitHub.LocalDirBase
	a.cfgMu.Unlock()
	repoHandle, err := acquireRepoContext(ctx, a.repoCtx, pr.Repo, &aiCfg, localDirBase, a.ghToken, repoctx.ModeRead, wtTokenFor("pr-tier2", pr.Number), "", "")
	if err != nil {
		logRepoContextFallback("tier2 PR", pr.Repo, err)
		aiCfg.LocalDir = ""
	}
	if repoHandle != nil {
		defer repoHandle.Release()
	}

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
	a.publishPending()
}

func (a *tier2Adapter) publishPending() {
	reviews, err := a.store.ListUnpublishedReviews()
	if err != nil || len(reviews) == 0 {
		return
	}
	for _, rev := range reviews {
		ready, err := a.reviewReadyForPublishRetry(rev)
		if err != nil {
			slog.Warn("publish-pending: in-flight check failed", "review_id", rev.ID, "err", err)
			continue
		}
		if !ready {
			continue
		}
		if err := a.publishPub.PublishPRPublish(context.Background(), rev.ID); err != nil {
			slog.Warn("publish-pending: enqueue failed", "review_id", rev.ID, "err", err)
		}
	}
}

func (a *tier2Adapter) reviewReadyForPublishRetry(rev *store.Review) (bool, error) {
	if rev == nil || rev.GitHubReviewID != 0 {
		return false, nil
	}
	pr, err := a.store.GetPR(rev.PRID)
	if err != nil {
		return false, err
	}
	// Tests and legacy callers may construct the adapter without live config.
	// Production always wires it; there, keep disabled repos pending until a
	// later config reload makes them eligible again.
	if a.cfg != nil && !a.repoIsMonitored(pr.Repo) {
		return false, nil
	}
	inFlight, err := a.store.ReviewInFlight(pr.GithubID, rev.HeadSHA)
	if err != nil {
		return false, err
	}
	return !inFlight, nil
}

// ProcessRepo implements scheduler.Tier2IssueProcessor.
func (a *tier2Adapter) ProcessRepo(ctx context.Context, repo string) (int, error) {
	a.cfgMu.Lock()
	c := *a.cfg
	repoIT := c.IssueTrackingForRepo(repo)
	autonomousEnabled := c.AutonomousForRepo(repo).Enabled
	a.cfgMu.Unlock()

	// When autonomous mode is enabled for a repo, the autonomous poller owns
	// the issue lifecycle (selection + triage→refinement→development single-
	// flight) for that repo. The legacy label-driven issue pipeline must NOT
	// run in parallel: otherwise a Tier 2 tick could re-pick an issue the
	// autonomous Drive is already working (which has no IssueReview/PR yet, so
	// alreadyProcessed misses), enqueue a second NATS implement job, and run a
	// duplicate agent → duplicate PRs / failed git push / wasted compute. This
	// guard is scoped to ISSUE processing only — PR review (ProcessPR + the
	// Tier 3 Responder/FixRunner) is unaffected and remains the autonomous
	// review loop.
	if !repoIT.Enabled || autonomousEnabled {
		return 0, nil
	}

	// Resolve authenticated user before applying issue tracking defaults: an
	// empty assignee list means "this daemon's user", not a shared queue.
	authUser := a.resolveAuthenticatedUser()

	var ok bool
	repoIT, ok = issueTrackingWithAssigneeScope("tier2 issue processing", repo, repoIT, authUser)
	if !ok {
		return 0, nil
	}

	optsFor := func(issue *gh.Issue) (issuepipeline.RunOptions, bool) {
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
		mode := repoctx.ModeRead
		requireWorkDir := false
		requireRefinementWorkDir := false
		if issue.Mode == config.IssueModeDevelop {
			mode = repoctx.ModeWrite
			requireWorkDir = true
		} else if issue.Mode == config.IssueModeRefinement {
			requireRefinementWorkDir = true
		}
		var releaseRepoContext func()
		releaseOnReturn := true
		wtPrefix := "stage"
		switch issue.Mode {
		case config.IssueModeDevelop:
			wtPrefix = "develop"
		case config.IssueModeRefinement:
			wtPrefix = "refinement"
		case config.IssueModeReviewOnly:
			wtPrefix = "triage"
		}
		repoHandle, err := acquireRepoContext(ctx, a.repoCtx, issue.Repo, &aiCfg, localDirBase, a.ghToken, mode, wtTokenFor(wtPrefix, issue.Number), "", "")
		defer func() {
			if releaseOnReturn && repoHandle != nil {
				repoHandle.Release()
			}
		}()
		if err != nil {
			if issue.Mode == config.IssueModeDevelop || issue.Mode == config.IssueModeRefinement {
				slog.Error("issue poll: prepare repo context failed",
					"repo", issue.Repo, "number", issue.Number, "err", err)
				if a.broker != nil {
					a.broker.Publish(sse.Event{
						Type: sse.EventIssueReviewError,
						Data: sseData(map[string]any{
							"repo": issue.Repo, "number": issue.Number, "error": err.Error(),
						}),
					})
				}
				return issuepipeline.RunOptions{}, false
			} else {
				logRepoContextFallback("issue poll", issue.Repo, err)
			}
			aiCfg.LocalDir = ""
		} else if repoHandle != nil {
			if issue.Mode == config.IssueModeReviewOnly || issue.Mode == config.IssueModeRefinement {
				ensureRepoContextFullHistory(ctx, a.repoCtx, repoHandle, a.ghToken, "issue poll", issue.Repo)
			}
			releaseRepoContext = repoHandle.Release
		}

		extraFlags := agentCfg.ExtraFlags
		if extraFlags != "" {
			if err := executor.ValidateExtraFlags(extraFlags); err != nil {
				slog.Warn("issue poll: extra_flags rejected", "err", err)
				extraFlags = ""
			}
		}

		issuePrompt, issueInstructions := resolveIssuePrompt(a.store, aiCfg.IssuePrompt, agentCfg.PromptID)
		implPrompt, implInstructions := resolveImplementPrompt(a.store, aiCfg.ImplementPrompt, agentCfg.PromptID)
		execTimeout := resolveExecutionTimeout(globalTimeout, agentCfg.ExecutionTimeout)
		if issue.Mode == config.IssueModeRefinement {
			execTimeout = resolveRefinementTimeout(aiCfg.RefinementTimeout, globalTimeout, agentCfg.ExecutionTimeout)
		}

		opts := issuepipeline.RunOptions{
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
				Timeout:              execTimeout,
			},
			IssuePromptOverride:         issuePrompt,
			IssueInstructions:           issueInstructions,
			TriageOwner:                 aiCfg.TriageOwner,
			ImplementPromptOverride:     implPrompt,
			ImplementInstructions:       implInstructions,
			PRReviewers:                 aiCfg.PRReviewers,
			PRAssignee:                  defaultAutoImplementPRAssignee(aiCfg.PRAssignee, authUser),
			PRLabels:                    aiCfg.PRLabels,
			PRDraft:                     aiCfg.PRDraft != nil && *aiCfg.PRDraft,
			GeneratePRDescription:       aiCfg.GeneratePRDescription != nil && *aiCfg.GeneratePRDescription,
			AuthUser:                    authUser,
			RequireWorkDirForDevelop:    requireWorkDir,
			RequireWorkDirForRefinement: requireRefinementWorkDir,
			ReleaseRepoContext:          releaseRepoContext,
		}
		releaseOnReturn = false
		return opts, true
	}

	return a.fetcher.ProcessRepo(ctx, repo, repoIT, authUser, optsFor)
}

// PromoteReady implements scheduler.Tier2Promoter.
func (a *tier2Adapter) PromoteReady(ctx context.Context, repos []string) (int, error) {
	type promoteGroup struct {
		it    config.IssueTrackingConfig
		repos []string
	}

	authUser := a.cachedAuthenticatedUser()

	a.cfgMu.Lock()
	c := *a.cfg
	groupOrder := make([]string, 0, len(repos))
	groups := make(map[string]*promoteGroup)
	for _, repo := range repos {
		// Skip autonomous-owned repos: the autonomous poller owns the issue
		// lifecycle (including stage advancement, which it records as a best-
		// effort audit trail). Letting Tier 2 also auto-promote stage labels
		// here would race the poller and cause spurious label advances on
		// issues the autonomous pipeline is driving. PR review is unaffected.
		if c.AutonomousForRepo(repo).Enabled {
			continue
		}
		it := c.IssueTrackingForRepo(repo)
		if it.Enabled && len(it.BlockedLabels) > 0 && len(it.Assignees) == 0 && authUser == "" {
			authUser = a.resolveAuthenticatedUser()
		}
		it, ok := issueTrackingWithAssigneeScope("tier2 issue promotion", repo, it, authUser)
		if !ok {
			continue
		}
		if it.Enabled && len(it.BlockedLabels) > 0 {
			key := promoteIssueTrackingKey(it)
			group := groups[key]
			if group == nil {
				group = &promoteGroup{it: it}
				groups[key] = group
				groupOrder = append(groupOrder, key)
			}
			group.repos = append(group.repos, repo)
		}
	}
	a.cfgMu.Unlock()

	total := 0
	var promoteErr error
	for _, key := range groupOrder {
		item := groups[key]
		n, err := issuepipeline.PromoteReady(ctx, a.ghClient, item.it, item.repos, a.broker)
		total += n
		if err != nil {
			promoteErr = errors.Join(promoteErr, fmt.Errorf("issues promote for %s: %w", strings.Join(item.repos, ","), err))
		}
	}
	return total, promoteErr
}

func promoteIssueTrackingKey(it config.IssueTrackingConfig) string {
	b, _ := json.Marshal(struct {
		Enabled          bool              `json:"enabled"`
		FilterMode       config.FilterMode `json:"filter_mode"`
		Organizations    []string          `json:"organizations"`
		Assignees        []string          `json:"assignees"`
		DevelopLabels    []string          `json:"develop_labels"`
		ReviewOnlyLabels []string          `json:"review_only_labels"`
		SkipLabels       []string          `json:"skip_labels"`
		BlockedLabels    []string          `json:"blocked_labels"`
		PromoteToLabel   string            `json:"promote_to_label"`
		DefaultAction    string            `json:"default_action"`
	}{
		Enabled:          it.Enabled,
		FilterMode:       it.FilterMode,
		Organizations:    it.Organizations,
		Assignees:        it.Assignees,
		DevelopLabels:    it.DevelopLabels,
		ReviewOnlyLabels: it.ReviewOnlyLabels,
		SkipLabels:       it.SkipLabels,
		BlockedLabels:    it.BlockedLabels,
		PromoteToLabel:   it.PromoteToLabel,
		DefaultAction:    it.DefaultAction,
	})
	return string(b)
}

// PRAlreadyReviewed implements scheduler.Tier2Store.
func (a *tier2Adapter) PRAlreadyReviewed(githubID int64, repo string, number int, updatedAt time.Time, headSHA string) bool {
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
	// NOTE: The former 2-minute PublishedAt grace window (GraceDefault) has
	// been removed. The tier-2 FetchPRsToReview loop now confirms the bot is
	// still in requested_reviewers via the Pulls API before a PR reaches this
	// point. That check eliminates "ghost" enqueues from the Search API's
	// replication lag — the only scenario the grace protected against. Without
	// the grace, a push + re-request-review within 2 minutes of the last
	// review is picked up immediately instead of being suppressed until the
	// grace expired. See theburrowhub/heimdallm#243 for the original incident
	// and the commit that added GetPRHeadInfo for the replacement check.
	//
	// The circuit breaker remains as the emergency brake for cross-bot review
	// loops and per-PR/per-repo rate caps.
	if a.circuitBreakerBlocksPR(existing, updatedAt, headSHA) {
		return true
	}
	return false
}

func (a *tier2Adapter) circuitBreakerBlocksPR(pr *store.PR, updatedAt time.Time, headSHA string) bool {
	if a == nil || a.store == nil || pr == nil {
		return false
	}
	if a.cfgMu == nil || a.cfg == nil || *a.cfg == nil {
		return false
	}
	a.cfgMu.Lock()
	c := *a.cfg
	limits := store.CircuitBreakerLimits{
		PerPR24h:  c.CircuitBreaker.PerPR24h,
		PerRepoHr: c.CircuitBreaker.PerRepoHr,
	}
	a.cfgMu.Unlock()
	if limits.PerPR24h == 0 && limits.PerRepoHr == 0 {
		return false
	}
	tripped, reason, err := a.store.CheckCircuitBreaker(pr.ID, pr.Repo, headSHA, limits)
	if err != nil {
		slog.Warn("pr dedup: circuit breaker check failed, proceeding", "repo", pr.Repo, "pr", pr.Number, "err", err)
		return false
	}
	if !tripped {
		a.clearCircuitBreakerDedup(pr.Repo, pr.Number)
		return false
	}
	a.publishCircuitBreakerTrippedOnce(pr, updatedAt, headSHA, reason)
	return true
}

func (a *tier2Adapter) clearCircuitBreakerDedup(repo string, number int) {
	if a == nil {
		return
	}
	a.skipMu.Lock()
	for key := range a.lastBreakerTrips {
		if key.Repo == repo && key.Number == number {
			delete(a.lastBreakerTrips, key)
		}
	}
	a.skipMu.Unlock()
}

func (a *tier2Adapter) publishCircuitBreakerTrippedOnce(pr *store.PR, updatedAt time.Time, headSHA, reason string) {
	if a == nil || a.broker == nil || pr == nil {
		return
	}
	key := breakerTripKey{Repo: pr.Repo, Number: pr.Number, HeadSHA: headSHA, Reason: reason}
	a.skipMu.Lock()
	if a.lastBreakerTrips == nil {
		a.lastBreakerTrips = make(map[breakerTripKey]breakerTripDedup)
	}
	prev, seen := a.lastBreakerTrips[key]
	// Treat non-monotonic updated_at as already emitted for this exact
	// breaker key. GitHub updated_at should be monotonic, but suppressing a
	// duplicate banner is safer than paging the operator for clock skew.
	alreadyEmitted := seen && !updatedAt.After(prev.UpdatedAt)
	if !alreadyEmitted {
		a.lastBreakerTrips[key] = breakerTripDedup{UpdatedAt: updatedAt, EmittedAt: time.Now()}
	}
	a.skipMu.Unlock()
	if alreadyEmitted {
		return
	}
	a.broker.Publish(sse.Event{
		Type: sse.EventCircuitBreakerTripped,
		Data: sseData(map[string]any{
			"pr_number": pr.Number,
			"repo":      pr.Repo,
			"reason":    reason,
		}),
	})
	slog.Info("pr dedup: circuit breaker tripped, suppressing review enqueue",
		"repo", pr.Repo, "pr", pr.Number, "reason", reason)
}

func (a *tier2Adapter) pruneBreakerTripDedup(now time.Time) {
	if a == nil {
		return
	}
	a.skipMu.Lock()
	defer a.skipMu.Unlock()
	for key, trip := range a.lastBreakerTrips {
		if trip.EmittedAt.IsZero() || now.Sub(trip.EmittedAt) > breakerTripDedupTTL {
			delete(a.lastBreakerTrips, key)
		}
	}
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
		// Review-state vigilance branch (#482): for PRs that
		// auto_implement created, the snapshot's updated_at advance is
		// almost always a reviewer submitting feedback. Fetch the
		// reviews list, aggregate, and short-circuit out of the
		// standard review codepath — the daemon's own PRs would be
		// rejected by SkipReasonSelfAuthored anyway, but routing them
		// here keeps the observation layer's intent explicit.
		stored, storeErr := a.store.GetPRByGithubID(item.GithubID)
		if storeErr != nil {
			// A non-ErrNoRows failure means SQLite is unhappy
			// (corruption, FS error). Logging it makes operational
			// debugging tractable; CheckItem still falls through to
			// the standard review codepath rather than swallowing
			// silently so the daemon keeps watching the PR — at
			// worst the standard path applies its own guards.
			slog.Warn("tier3: GetPRByGithubID failed, falling through to standard review path",
				"repo", item.Repo, "number", item.Number, "err", storeErr)
		}
		if stored != nil && stored.AutoImplementIssueID != 0 {
			if err := a.refreshAutoImplementPRReviewState(ctx, item, stored); err != nil {
				// Propagate the error so the state-handler can apply
				// its 404 cleanup + the StateWorker increases backoff
				// (no LastSeen advance) rather than burning the API
				// on every tick.
				//
				// Two error shapes flow through here. A GetPRReviews
				// failure surfaces before any persist, so the store
				// row is untouched and the next refresh re-observes
				// from scratch. A runner failure (Responder /
				// FixRunner) surfaces AFTER the new aggregate state
				// was persisted + SSE-emitted on this tick — the
				// stateMoved gate in refresh then sees
				// stateMoved=false on the retry tick and re-dispatches
				// without re-emitting the event.
				return false, nil, err
			}
			// Success: signal `changed=true` so the StateWorker resets
			// backoff and advances LastSeen. A nil snap means
			// HandleChange's first guard short-circuits — we already
			// handled dispatch inline inside refresh, the standard
			// review codepath has nothing to do here.
			return true, nil, nil
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
		monitored := repoIsMonitored(c, item.Repo)
		aiCfg := c.AIForRepo(item.Repo)
		a.cfgMu.Unlock()
		if !monitored {
			a.broker.Publish(sse.Event{
				Type: sse.EventReviewSkipped,
				Data: sseData(map[string]any{
					"repo":      item.Repo,
					"pr_number": item.Number,
					"reason":    string(pipeline.SkipReasonNotMonitored),
				}),
			})
			slog.Info("tier3: repo no longer monitored, skipping PR",
				"repo", item.Repo, "pr", item.Number)
			return nil
		}

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
		if a.PRAlreadyReviewed(item.GithubID, item.Repo, item.Number, snap.UpdatedAt, snap.HeadSHA) {
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

func buildRefinementRunOptions(
	ctx context.Context,
	s *store.Store,
	manager *repoctx.Manager,
	repo string,
	issueNumber int,
	token string,
	aiCfg config.RepoAI,
	agentCfg config.CLIAgentConfig,
	localDirBase []string,
	globalTimeout string,
	force bool,
	scope string,
) (issuepipeline.RunOptions, func(), error) {
	repoHandle, err := acquireRepoContext(ctx, manager, repo, &aiCfg, localDirBase, token, repoctx.ModeRead, wtTokenFor("refinement", issueNumber), "", "")
	if err != nil {
		return issuepipeline.RunOptions{}, nil, err
	}
	var releaseRepoContext func()
	if repoHandle != nil {
		releaseRepoContext = repoHandle.Release
		ensureRepoContextFullHistory(ctx, manager, repoHandle, token, scope, repo)
	}

	extraFlags := agentCfg.ExtraFlags
	if extraFlags != "" {
		if err := executor.ValidateExtraFlags(extraFlags); err != nil {
			slog.Warn(scope+": extra_flags rejected", "err", err)
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
			Timeout:              resolveRefinementTimeout(aiCfg.RefinementTimeout, globalTimeout, agentCfg.ExecutionTimeout),
		},
		IssuePromptOverride:         issuePrompt,
		IssueInstructions:           issueInstructions,
		TriageOwner:                 aiCfg.TriageOwner,
		ImplementPromptOverride:     implPrompt,
		ImplementInstructions:       implInstructions,
		PRReviewers:                 aiCfg.PRReviewers,
		PRAssignee:                  aiCfg.PRAssignee,
		PRLabels:                    aiCfg.PRLabels,
		PRDraft:                     aiCfg.PRDraft != nil && *aiCfg.PRDraft,
		GeneratePRDescription:       aiCfg.GeneratePRDescription != nil && *aiCfg.GeneratePRDescription,
		Force:                       force,
		RequireWorkDirForRefinement: true,
	}
	return opts, releaseRepoContext, nil
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

func issueStageStillCurrent(scope string, issue *gh.Issue, it config.IssueTrackingConfig, want config.IssueMode) bool {
	if issue == nil {
		return false
	}
	if !it.Enabled {
		slog.Info(scope+": issue tracking disabled before worker run, skipping stale job",
			"repo", issue.Repo, "number", issue.Number)
		return false
	}
	if !it.MatchesAssignees(issue.AssigneeLogins()) {
		slog.Info(scope+": issue assigned outside this daemon scope, skipping stale job",
			"repo", issue.Repo, "number", issue.Number,
			"assignees", issue.AssigneeLogins(), "allowed_assignees", it.Assignees)
		return false
	}
	// Best-effort stale-job guard: workers fetch the issue immediately before
	// this check, so queued jobs whose labels changed since dispatch skip
	// before running AI. A label edit after this fetch is handled by the next
	// poll rather than adding another GitHub round-trip here.
	got := it.Classify(issue.LabelNames())
	if got == want {
		return true
	}
	slog.Info(scope+": issue stage changed before worker run, skipping stale job",
		"repo", issue.Repo, "number", issue.Number, "want", want, "got", got, "labels", issue.LabelNames())
	return false
}

func issueTrackingWithAssigneeScope(scope, repo string, it config.IssueTrackingConfig, defaultAssignee string) (config.IssueTrackingConfig, bool) {
	it = it.WithDefaultAssignee(defaultAssignee)
	if it.Enabled && len(it.Assignees) == 0 {
		slog.Warn(scope+": issue tracking has no assignee scope; skipping issues for this repo",
			"repo", repo)
		return it, false
	}
	return it, true
}

func defaultAutoImplementPRAssignee(configured, authUser string) string {
	if assignee := strings.TrimSpace(configured); assignee != "" {
		return assignee
	}
	return strings.TrimSpace(strings.TrimLeft(authUser, "@"))
}

// issueRunPublisher is the narrow NATS surface dispatchIssueRunByCurrentMode
// needs. Defined here as a local seam so unit tests can fake it without
// standing up a NATS server; *bus.NATSIssuePublisher satisfies it.
type issueRunPublisher interface {
	PublishIssueTriage(ctx context.Context, repo string, number int, githubID int64) error
	PublishIssueRefinement(ctx context.Context, repo string, number int, githubID int64) error
	PublishIssueImplement(ctx context.Context, repo string, number int, githubID int64) error
}

// dispatchIssueRunByCurrentMode publishes the issue to the NATS subject
// matching its label-derived stage. Used by the manual re-review endpoint
// (POST /issues/{id}/review) so an operator who clicked "Re-review" after
// an auto-promote runs the *current* stage instead of falling back to a
// stored classification that lagged behind the labels — see #462.
//
// The label-driven classification mirrors the fetcher's path
// (IssueTrackingConfig.Classify); keeping a single source of truth means
// future stage additions only need a new publisher + a switch case here.
//
// Two gates produce a clear error instead of publishing:
//   - Out-of-scope assignees: the worker entries silently drop work whose
//     assignees fall outside the daemon's scope (see
//     issueTrackingWithAssigneeScope + issueStageStillCurrent). For a
//     fetcher tick that is fine — log spam at most. For a manual click it
//     looks like the GUI is broken (spinner + silence), so reject here
//     with the assignees + scope spelled out in the error message.
//   - Blocked / Ignore classifications: nothing to re-run; surface a
//     reason instead of queuing work the worker would discard.
//
// Callers are expected to populate cfg.Assignees (e.g., via
// WithDefaultAssignee) before invoking — an empty scope means
// MatchesAssignees is vacuously true and the gate is a no-op.
func dispatchIssueRunByCurrentMode(
	ctx context.Context,
	pub issueRunPublisher,
	cfg config.IssueTrackingConfig,
	issue *gh.Issue,
) error {
	if issue == nil {
		return fmt.Errorf("dispatch issue run: nil issue")
	}
	if !cfg.MatchesAssignees(issue.AssigneeLogins()) {
		return fmt.Errorf("dispatch issue run: %s#%d assignees %v are outside this daemon's scope %v; re-run from the assignee's operator",
			issue.Repo, issue.Number, issue.AssigneeLogins(), cfg.Assignees)
	}
	mode := cfg.Classify(issue.LabelNames())
	switch mode {
	case config.IssueModeReviewOnly:
		return pub.PublishIssueTriage(ctx, issue.Repo, issue.Number, issue.ID)
	case config.IssueModeRefinement:
		return pub.PublishIssueRefinement(ctx, issue.Repo, issue.Number, issue.ID)
	case config.IssueModeDevelop:
		return pub.PublishIssueImplement(ctx, issue.Repo, issue.Number, issue.ID)
	case config.IssueModeBlocked:
		return fmt.Errorf("dispatch issue run: %s#%d is blocked by current labels; cannot re-run",
			issue.Repo, issue.Number)
	case config.IssueModeIgnore:
		return fmt.Errorf("dispatch issue run: %s#%d is ignored by current label configuration; cannot re-run",
			issue.Repo, issue.Number)
	default:
		return fmt.Errorf("dispatch issue run: unsupported mode %q for %s#%d",
			mode, issue.Repo, issue.Number)
	}
}

func autoPromoteAfterStage(
	ctx context.Context,
	client *gh.Client,
	broker issuepipeline.Publisher,
	issue *gh.Issue,
	storeIssueID int64,
	it config.IssueTrackingConfig,
	aiCfg config.RepoAI,
	from issuepipeline.IssueStage,
	scope string,
) {
	if !autoPromoteStageEnabled(aiCfg, it, from) {
		return
	}
	// Auto-promote moves the stage label unconditionally when enabled.
	// The handoff to a different operator (issue reassigned during
	// triage / refinement) is enforced by the *next* stage's worker
	// entry — `issueTrackingWithAssigneeScope` + `issueStageStillCurrent`
	// already skip work whose assignees fall outside the daemon's
	// scope. Gating the label transition here too (as #457 did) double-
	// gated the flow and left issues stuck at the current stage so the
	// new assignee's daemon never picked them up at the next stage. See
	// #458 for the regression report.
	to, err := issuepipeline.NextStage(from, it, false)
	if err != nil {
		if errors.Is(err, issuepipeline.ErrStageTargetLabelMissing) {
			slog.Warn(scope+": auto-promote target label missing; leaving issue in current stage",
				"repo", issue.Repo, "number", issue.Number, "from", from, "err", err)
			return
		}
		slog.Warn(scope+": auto-promote skipped",
			"repo", issue.Repo, "number", issue.Number, "from", from, "err", err)
		return
	}

	comments, err := client.FetchIssueCommentsOnly(issue.Repo, issue.Number)
	if err != nil {
		slog.Warn(scope+": auto-promote comment fetch failed, continuing without audit dedup context",
			"repo", issue.Repo, "number", issue.Number, "err", err)
	}
	if err := issuepipeline.TransitionIssueStage(ctx, client, issuepipeline.StageTransition{
		Issue:          issue,
		StoreIssueID:   storeIssueID,
		Config:         it,
		From:           from,
		To:             to,
		Trigger:        issuepipeline.StagePromotionAuto,
		Time:           time.Now().UTC(),
		RecentComments: comments,
		Broker:         broker,
	}); err != nil {
		slog.Warn(scope+": auto-promote failed",
			"repo", issue.Repo, "number", issue.Number, "from", from, "to", to, "err", err)
	}
}

func autoPromoteStageEnabled(aiCfg config.RepoAI, it config.IssueTrackingConfig, stage issuepipeline.IssueStage) bool {
	switch stage {
	case issuepipeline.IssueStageTriage:
		if aiCfg.AutoPromoteTriage == nil {
			// Default-on only after the operator has configured a refinement
			// target. Legacy review_only-only deployments keep their prior
			// behavior instead of gaining autonomous label transitions.
			return hasConfiguredLabel(it.RefinementLabels)
		}
		return *aiCfg.AutoPromoteTriage
	case issuepipeline.IssueStageRefinement:
		if aiCfg.AutoPromoteRefinement == nil {
			// Same staged-default rule as triage: once a development target
			// label exists, refinement can safely advance to develop unless the
			// operator explicitly disables it.
			return hasConfiguredLabel(it.DevelopLabels)
		}
		return *aiCfg.AutoPromoteRefinement
	default:
		return false
	}
}

func hasConfiguredLabel(labels []string) bool {
	for _, label := range labels {
		if strings.TrimSpace(label) != "" {
			return true
		}
	}
	return false
}

func acquireRepoContext(
	ctx context.Context,
	manager *repoctx.Manager,
	repo string,
	aiCfg *config.RepoAI,
	localDirBase []string,
	token string,
	mode repoctx.Mode,
	wtToken string,
	wtBaseRef string,
	branch string,
) (*repoctx.Handle, error) {
	if manager == nil {
		return nil, fmt.Errorf("repoctx: nil manager")
	}
	h, err := manager.Acquire(ctx, repoctx.Request{
		Repo:               repo,
		ConfiguredLocalDir: aiCfg.LocalDir,
		LocalDirBases:      localDirBase,
		CloneDir:           aiCfg.CloneDir,
		Token:              token,
		Mode:               mode,
		WorktreeToken:      wtToken,
		WorktreeBaseRef:    wtBaseRef,
		Branch:             branch,
	})
	if err != nil {
		return nil, err
	}
	// aiCfg.LocalDir is valid only while the returned handle is held. Callers
	// must release the handle after the pipeline/executor has finished with the
	// path.
	aiCfg.LocalDir = h.Path()
	return h, nil
}

// wtTokenFor produces a sanitisation-safe worktree token for a
// pipeline stage. The prefix names the stage (`pr-review`, `triage`,
// `develop`, `refinement`, `pr-tier2`) so operators can correlate
// `<clone>/.worktrees/<token>/` with the running execution.
func wtTokenFor(prefix string, n int) string {
	return fmt.Sprintf("%s-%d", prefix, n)
}

func ensureRepoContextFullHistory(ctx context.Context, manager *repoctx.Manager, h *repoctx.Handle, token, scope, repo string) {
	if manager == nil || h == nil {
		return
	}
	if err := manager.EnsureFullHistory(ctx, h, token); err != nil {
		slog.Warn(scope+": full git history unavailable; triage owner verification may fall back",
			"repo", repo, "err", err)
	}
}

func logRepoContextFallback(scope, repo string, err error) {
	slog.Warn(scope+": repo context unavailable; continuing without local checkout",
		"repo", repo, "err", err)
}

// autonomousOverrideMap serialises an AutonomousOverride into a map[string]any
// for the GET /config DTO. Only fields that are explicitly set (non-nil pointer
// or non-empty string) are included so the caller can distinguish "inherit" from
// an explicit false/zero value.
func autonomousOverrideMap(o config.AutonomousOverride) map[string]any {
	out := map[string]any{}
	if o.Enabled != nil {
		out["enabled"] = *o.Enabled
	}
	if o.AutoMerge != nil {
		out["auto_merge"] = *o.AutoMerge
	}
	if o.MergeMethod != "" {
		out["merge_method"] = o.MergeMethod
	}
	if o.TakeOthersTasks != nil {
		out["take_others_tasks"] = *o.TakeOthersTasks
	}
	if o.ReassignOnTake != nil {
		out["reassign_on_take"] = *o.ReassignOnTake
	}
	if o.DevMaxTurns != nil {
		out["dev_max_turns"] = *o.DevMaxTurns
	}
	if o.DevEffort != "" {
		out["dev_effort"] = o.DevEffort
	}
	if o.DevTimeout != "" {
		out["dev_timeout"] = o.DevTimeout
	}
	if o.ClaimLease != "" {
		out["claim_lease"] = o.ClaimLease
	}
	return out
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

func repoAIOverrideMap(ai config.RepoAI) map[string]any {
	out := map[string]any{
		"primary":     ai.Primary,
		"fallback":    ai.Fallback,
		"review_mode": ai.ReviewMode,
		"local_dir":   ai.LocalDir,
	}
	addCommonAIOverrideFields(out, aiOverrideFields{
		Prompt:                  ai.Prompt,
		IssuePrompt:             ai.IssuePrompt,
		ImplementPrompt:         ai.ImplementPrompt,
		RefinementTimeout:       ai.RefinementTimeout,
		TriageOwner:             ai.TriageOwner,
		CloneDir:                ai.CloneDir,
		AutoPromoteTriage:       ai.AutoPromoteTriage,
		AutoPromoteRefinement:   ai.AutoPromoteRefinement,
		PRReviewers:             ai.PRReviewers,
		PRAssignee:              ai.PRAssignee,
		PRLabels:                ai.PRLabels,
		PRDraft:                 ai.PRDraft,
		GeneratePRDescription:   ai.GeneratePRDescription,
		NeverApproveWithIssues:  ai.NeverApproveWithIssues,
		NeverApproveMinSeverity: ai.NeverApproveMinSeverity,
	})
	if ai.IssueTracking != nil {
		out["issue_tracking"] = issueTrackingOverrideMap(ai.IssueTracking)
	}
	return out
}

func orgAIOverrideMap(ai config.OrgAI) map[string]any {
	out := map[string]any{}
	if ai.Primary != "" {
		out["primary"] = ai.Primary
	}
	if ai.Fallback != "" {
		out["fallback"] = ai.Fallback
	}
	if ai.ReviewMode != "" {
		out["review_mode"] = ai.ReviewMode
	}
	if ai.LocalDir != "" {
		out["local_dir"] = ai.LocalDir
	}
	addCommonAIOverrideFields(out, aiOverrideFields{
		Prompt:                  ai.Prompt,
		IssuePrompt:             ai.IssuePrompt,
		ImplementPrompt:         ai.ImplementPrompt,
		RefinementTimeout:       ai.RefinementTimeout,
		TriageOwner:             ai.TriageOwner,
		CloneDir:                ai.CloneDir,
		AutoPromoteTriage:       ai.AutoPromoteTriage,
		AutoPromoteRefinement:   ai.AutoPromoteRefinement,
		PRReviewers:             ai.PRReviewers,
		PRAssignee:              ai.PRAssignee,
		PRLabels:                ai.PRLabels,
		PRDraft:                 ai.PRDraft,
		GeneratePRDescription:   ai.GeneratePRDescription,
		NeverApproveWithIssues:  ai.NeverApproveWithIssues,
		NeverApproveMinSeverity: ai.NeverApproveMinSeverity,
	})
	if ai.IssueTracking != nil {
		out["issue_tracking"] = issueTrackingOverrideMap(ai.IssueTracking)
	}
	return out
}

type aiOverrideFields struct {
	Prompt                  string
	IssuePrompt             string
	ImplementPrompt         string
	RefinementTimeout       string
	TriageOwner             string
	CloneDir                string
	AutoPromoteTriage       *bool
	AutoPromoteRefinement   *bool
	PRReviewers             []string
	PRAssignee              string
	PRLabels                []string
	PRDraft                 *bool
	GeneratePRDescription   *bool
	NeverApproveWithIssues  *bool
	NeverApproveMinSeverity string
}

func addCommonAIOverrideFields(out map[string]any, fields aiOverrideFields) {
	if fields.Prompt != "" {
		out["prompt"] = fields.Prompt
	}
	if fields.IssuePrompt != "" {
		out["issue_prompt"] = fields.IssuePrompt
	}
	if fields.ImplementPrompt != "" {
		out["implement_prompt"] = fields.ImplementPrompt
	}
	if fields.RefinementTimeout != "" {
		out["refinement_timeout"] = fields.RefinementTimeout
	}
	if fields.TriageOwner != "" {
		out["triage_owner"] = fields.TriageOwner
	}
	if fields.CloneDir != "" {
		out["clone_dir"] = fields.CloneDir
	}
	if fields.AutoPromoteTriage != nil {
		out["auto_promote_triage"] = *fields.AutoPromoteTriage
	}
	if fields.AutoPromoteRefinement != nil {
		out["auto_promote_refinement"] = *fields.AutoPromoteRefinement
	}
	if fields.PRReviewers != nil {
		out["pr_reviewers"] = fields.PRReviewers
	}
	if fields.PRAssignee != "" {
		out["pr_assignee"] = fields.PRAssignee
	}
	if fields.PRLabels != nil {
		out["pr_labels"] = fields.PRLabels
	}
	if fields.PRDraft != nil {
		out["pr_draft"] = *fields.PRDraft
	}
	if fields.GeneratePRDescription != nil {
		out["generate_pr_description"] = *fields.GeneratePRDescription
	}
	if fields.NeverApproveWithIssues != nil {
		out["never_approve_with_issues"] = *fields.NeverApproveWithIssues
	}
	if fields.NeverApproveMinSeverity != "" {
		out["never_approve_min_severity"] = fields.NeverApproveMinSeverity
	}
}

func issueTrackingOverrideMap(ov *config.IssueTrackingOverride) map[string]any {
	out := map[string]any{}
	if ov.Enabled != nil {
		out["enabled"] = *ov.Enabled
	}
	if ov.DevelopEnabled != nil {
		out["develop_enabled"] = *ov.DevelopEnabled
	}
	if ov.FilterMode != "" {
		out["filter_mode"] = ov.FilterMode
	}
	if ov.DefaultAction != "" {
		out["default_action"] = ov.DefaultAction
	}
	if ov.DevelopLabels != nil {
		out["develop_labels"] = ov.DevelopLabels
	}
	if ov.RefinementLabels != nil {
		out["refinement_labels"] = ov.RefinementLabels
	}
	if ov.ReviewOnlyLabels != nil {
		out["review_only_labels"] = ov.ReviewOnlyLabels
	}
	if ov.SkipLabels != nil {
		out["skip_labels"] = ov.SkipLabels
	}
	if ov.BlockedLabels != nil {
		out["blocked_labels"] = ov.BlockedLabels
	}
	if ov.PromoteToLabel != "" {
		out["promote_to_label"] = ov.PromoteToLabel
	}
	if ov.Organizations != nil {
		out["organizations"] = ov.Organizations
	}
	if ov.Assignees != nil {
		out["assignees"] = ov.Assignees
	}
	return out
}

func purgeAllManagedClones(ctx context.Context, manager *repoctx.Manager, cfg *config.Config) (int, error) {
	if manager == nil {
		return 0, fmt.Errorf("repoctx: nil manager")
	}
	var total int
	var errs []error
	for _, cloneDir := range managedCloneDirs(cfg) {
		report, err := manager.PurgeAll(ctx, cloneDir)
		total += report.Removed
		if err != nil {
			errs = append(errs, err)
		}
	}
	return total, errors.Join(errs...)
}

func purgeStaleManagedClones(ctx context.Context, manager *repoctx.Manager, cfg *config.Config, discovered []string) (int, error) {
	if manager == nil {
		return 0, fmt.Errorf("repoctx: nil manager")
	}
	if cfg == nil || cfg.Retention.MaxDays <= 0 {
		return 0, nil
	}
	monitored := monitoredRepoSet(cfg, discovered)
	var total int
	var errs []error
	for _, cloneDir := range managedCloneDirs(cfg) {
		report, err := manager.PurgeStale(ctx, cloneDir, monitored, cfg.Retention.MaxDays)
		total += report.Removed
		if err != nil {
			errs = append(errs, err)
		}
	}
	return total, errors.Join(errs...)
}

func managedCloneDirs(cfg *config.Config) []string {
	seen := map[string]struct{}{}
	add := func(path string) {
		seen[strings.TrimSpace(path)] = struct{}{}
	}
	add("")
	if cfg != nil {
		add(cfg.AI.CloneDir)
		for _, org := range cfg.AI.Orgs {
			add(org.CloneDir)
		}
		for _, repo := range cfg.AI.Repos {
			add(repo.CloneDir)
		}
	}
	out := make([]string, 0, len(seen))
	for path := range seen {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

func monitoredRepoSet(cfg *config.Config, discovered []string) map[string]struct{} {
	out := map[string]struct{}{}
	if cfg == nil {
		return out
	}
	repos := discovery.MergeRepos(cfg.GitHub.Repositories, aiRepoKeys(cfg), discovered, cfg.GitHub.NonMonitored)
	for _, repo := range repos {
		repo = strings.TrimSpace(repo)
		if repo != "" {
			out[repo] = struct{}{}
		}
	}
	return out
}

// repoIsMonitored is the shared live eligibility check for automatic review
// paths. Passing no discovered slice is sufficient after discovery has been
// persisted into Repositories/NonMonitored; callers that own a fresh discovery
// snapshot should use monitoredRepoSet directly.
func repoIsMonitored(cfg *config.Config, repo string) bool {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return false
	}
	_, ok := monitoredRepoSet(cfg, nil)[repo]
	return ok
}

// deferPublishIfUnmonitored performs the final live eligibility check before
// SubmitReview. It deliberately leaves the stored review unpublished: unlike
// a permanent GitHub error, an operator disabling a repo is reversible, and
// PublishPending can safely resume the same review after re-enable.
func deferPublishIfUnmonitored(repo string, isMonitored func(string) bool) bool {
	return isMonitored != nil && !isMonitored(repo)
}

// pendingReviewInvalidReason protects retry/deferred publication from live PR
// drift. Empty review SHAs are legacy rows whose provenance cannot be checked;
// they retain the pre-existing retry behavior rather than being destroyed by
// an upgrade. A non-empty current SHA is required before classifying a commit
// mismatch so transient/incomplete API responses retry instead of orphaning.
func pendingReviewInvalidReason(rev *store.Review, snapshot *gh.PRSnapshot) pipeline.SkipReason {
	if rev == nil || snapshot == nil {
		return pipeline.SkipReasonNone
	}
	if snapshot.State != "open" {
		return pipeline.SkipReasonNotOpen
	}
	if rev.HeadSHA != "" && snapshot.HeadSHA != "" && rev.HeadSHA != snapshot.HeadSHA {
		return pipeline.SkipReasonHeadChanged
	}
	return pipeline.SkipReasonNone
}

// enrollOpenItems enrolls up to 10 open PRs/issues not yet in watch_state.
// Called once per state-poller tick (every 30s) to gradually backfill items
// from before the NATS migration. The monitored set is snapshotted before the
// query and pushed into SQL, so disabled rows cannot consume the LIMIT and the
// daemon's single SQLite connection is never held during an unbounded scan.
func enrollOpenItems(
	ctx context.Context,
	s *store.Store,
	ws *bus.WatchStore,
	monitoredRepos []string,
) {
	seen := make(map[string]struct{}, len(monitoredRepos))
	normalizedRepos := make([]string, 0, len(monitoredRepos))
	for _, repo := range monitoredRepos {
		repo = strings.TrimSpace(repo)
		if repo == "" {
			continue
		}
		if _, ok := seen[repo]; ok {
			continue
		}
		seen[repo] = struct{}{}
		normalizedRepos = append(normalizedRepos, repo)
	}
	if len(normalizedRepos) == 0 {
		return
	}

	for _, q := range []struct {
		typ   string
		query string // one %s placeholder for a bounded repo IN clause
	}{
		{"pr", `SELECT p.github_id, p.repo, p.number FROM prs p
			LEFT JOIN watch_state w ON w.key = 'pr.' || p.github_id
			WHERE p.state='open' AND p.repo != '' AND w.key IS NULL
			AND p.repo IN (%s)
			ORDER BY p.id LIMIT 10`},
		{"issue", `SELECT i.github_id, i.repo, i.number FROM issues i
			LEFT JOIN watch_state w ON w.key = 'issue.' || i.github_id
			WHERE i.state='open' AND i.repo != '' AND w.key IS NULL
			AND i.repo IN (%s)
			ORDER BY i.id LIMIT 10`},
	} {
		type item struct {
			ghID   int64
			repo   string
			number int
		}
		var batch []item
		// Keep each query well below SQLite's bind-variable ceiling. Continue
		// through chunks until ten eligible rows are found globally.
		const repoChunkSize = 500
		for start := 0; start < len(normalizedRepos) && len(batch) < 10; start += repoChunkSize {
			end := min(start+repoChunkSize, len(normalizedRepos))
			chunk := normalizedRepos[start:end]
			args := make([]any, len(chunk))
			for i, repo := range chunk {
				args[i] = repo
			}
			placeholders := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
			rows, err := s.DB().Query(fmt.Sprintf(q.query, placeholders), args...)
			if err != nil {
				slog.Warn("state-poller: backfill query failed", "type", q.typ, "err", err)
				continue
			}
			for rows.Next() && len(batch) < 10 {
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
			rows.Close() // release the daemon's single DB connection between chunks
		}

		enrolled := 0
		for _, it := range batch {
			if err := ws.Enroll(ctx, q.typ, it.repo, it.number, it.ghID); err != nil {
				slog.Warn("state-poller: backfill enroll failed", "type", q.typ, "repo", it.repo, "number", it.number, "err", err)
				break // skip rest of this type, try next type
			}
			enrolled++
		}
		if enrolled > 0 {
			slog.Debug("state-poller: backfill enrolled", "type", q.typ, "count", enrolled)
		}
	}
}

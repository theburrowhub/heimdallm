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
	"net"
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
	"github.com/heimdallm/daemon/internal/bus"
	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/discovery"
	"github.com/heimdallm/daemon/internal/executor"
	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/gitops"
	"github.com/heimdallm/daemon/internal/instances"
	"github.com/heimdallm/daemon/internal/keychain"
	"github.com/heimdallm/daemon/internal/mergetrack"
	"github.com/heimdallm/daemon/internal/notify"
	"github.com/heimdallm/daemon/internal/pipeline"
	"github.com/heimdallm/daemon/internal/repoctx"
	"github.com/heimdallm/daemon/internal/scheduler"
	"github.com/heimdallm/daemon/internal/server"
	"github.com/heimdallm/daemon/internal/singleinstance"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/heimdallm/daemon/internal/store"
	"github.com/heimdallm/daemon/internal/worker"
	"github.com/heimdallm/daemon/internal/workgate"
	"github.com/heimdallm/daemon/launchagent"
	"github.com/nats-io/nats.go"
)

// version is overridden via -ldflags "-X main.version=..." at build time.
var version = "dev"

// processLifetimeLock keeps the production lock descriptor reachable until
// os.Exit. Closing it in run's last defer would still leave a tiny handoff gap
// where other goroutines exist between return and os.Exit. Tests use run(),
// which opts into deterministic release so multiple cases can share a process.
var processLifetimeLock *singleinstance.Lock

// processDependencies keeps process-boundary effects injectable for lifecycle
// tests while production uses the concrete defaults below.
type processDependencies struct {
	newGitHubClient    func(string, ...gh.Option) *gh.Client
	listen             func(int, string) (net.Listener, error)
	afterPollerRestart func()
}

var defaultProcessDependencies = processDependencies{
	newGitHubClient: gh.NewClient,
	listen:          server.Listen,
}

// updateServerError keeps the HTTP contract independent from workgate's
// internal error vocabulary. Every updater transition passes through this one
// mapping so prepare/cancel/seal/confirm cannot drift into different status
// codes as new recovery paths are added.
func updateServerError(err error) error {
	switch {
	case errors.Is(err, workgate.ErrLeaseIDRequired):
		return server.ErrUpdateLeaseRequired
	case errors.Is(err, workgate.ErrLeaseIDInvalid):
		return server.ErrUpdateLeaseInvalid
	case errors.Is(err, workgate.ErrLeaseConflict):
		return server.ErrUpdateLeaseConflict
	case errors.Is(err, workgate.ErrWorkActive):
		return server.ErrUpdateNotReady
	case errors.Is(err, workgate.ErrLeaseNotSealed):
		return server.ErrUpdateNotSealed
	case errors.Is(err, workgate.ErrBootstrapNotAuthorized):
		return server.ErrUpdateBootstrapNotAuthorized
	default:
		return err
	}
}

func snapshotUpdatePreparation(snapshot workgate.Snapshot, bootID string) server.UpdatePreparationStatus {
	active := make(map[string]int, len(snapshot.Active))
	for kind, count := range snapshot.Active {
		active[string(kind)] = count
	}
	state := "running"
	if snapshot.Draining {
		state = "draining"
		if snapshot.Total() == 0 {
			state = "ready"
		}
	}
	return server.UpdatePreparationStatus{
		State:               state,
		PID:                 os.Getpid(),
		Version:             versionString(),
		LeaseID:             snapshot.LeaseID,
		Sealed:              snapshot.Sealed,
		BootstrapAuthorized: snapshot.BootstrapAuthorized,
		BootID:              bootID,
		Active:              active,
		ActiveTotal:         snapshot.Total(),
		LeaseExpiresAt:      snapshot.LeaseExpiresAt,
	}
}

// acquireUpdateWork centralizes ownership of update-drain permits. Nested
// operations inherit the outer permit and receive a no-op release, while
// top-level work always gets a matching cleanup function.
func acquireUpdateWork(ctx context.Context, gate *workgate.Gate, kind workgate.Kind) (context.Context, func(), error) {
	ctx, permit, owned, err := gate.AcquireContext(ctx, kind)
	if err != nil {
		return ctx, nil, err
	}
	if !owned {
		return ctx, func() {}, nil
	}
	return ctx, permit.Release, nil
}

// guardUpdateVoidHandler applies the updater admission protocol to message
// handlers that do not return a result. Keeping this boundary outside each
// worker makes it impossible to admit the fetch but forget to carry the permit
// into the nested pipeline.
func guardUpdateVoidHandler[M any](
	gate *workgate.Gate,
	kind workgate.Kind,
	deferredMessage string,
	next func(context.Context, M),
) func(context.Context, M) {
	return func(ctx context.Context, message M) {
		ctx, releaseUpdateWork, err := acquireUpdateWork(ctx, gate, kind)
		if err != nil {
			slog.Debug(deferredMessage, "work", message)
			return
		}
		defer releaseUpdateWork()
		next(ctx, message)
	}
}

// guardUpdateResultHandler is the result-returning variant used by workers
// whose queue protocol distinguishes ack/retry through their return value.
func guardUpdateResultHandler[M, R any](
	gate *workgate.Gate,
	kind workgate.Kind,
	deferredMessage string,
	deferredResult R,
	next func(context.Context, M) R,
) func(context.Context, M) R {
	return func(ctx context.Context, message M) R {
		ctx, releaseUpdateWork, err := acquireUpdateWork(ctx, gate, kind)
		if err != nil {
			slog.Debug(deferredMessage, "work", message)
			return deferredResult
		}
		defer releaseUpdateWork()
		return next(ctx, message)
	}
}

func versionString() string { return version }

// publishBridgeEvents re-publishes every SSE broker event to NATS so the SSE
// handler (which reads from NATS) sees events from all broker publishers.
// Each event is processed under its own recover(): a panic on one event is
// logged with a stack and the loop continues to the next, so a single bad
// event can neither crash the daemon (a panic in a bare goroutine is fatal)
// nor permanently kill the bridge that feeds every SSE client.
func publishBridgeEvents(events <-chan sse.Event, publish func(subject string, data []byte) error) {
	for event := range events {
		// Discovery publishes this event synchronously so NATS confirms it
		// before the related PR can be enqueued. The broker copy remains for
		// legacy subscribers and delivery fallback; bridging a confirmed copy
		// again would duplicate the UI event.
		if event.NATSForwarded {
			continue
		}
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

func newOrderedNATSEventPublisher(conn *nats.Conn) func([]sse.Event) error {
	return func(events []sse.Event) error {
		for _, event := range events {
			if err := conn.Publish(bus.SubjEventPrefix+event.Type, []byte(event.Data)); err != nil {
				return err
			}
		}
		return conn.FlushTimeout(2 * time.Second)
	}
}

func main() {
	os.Exit(runProcess(false))
}

// run is the test-friendly lifecycle entry point. Production calls runProcess
// directly and lets the OS release the instance lock atomically with exit.
func run() int {
	return runProcess(true)
}

// runProcess owns the daemon lifecycle and returns its process exit code.
// Keeping os.Exit in main means every return executes deferred cleanup first,
// including fatal bind and HTTP-serve failures.
func runProcess(releaseLock bool) int {
	return runProcessWithDependencies(releaseLock, defaultProcessDependencies)
}

func runProcessWithDependencies(releaseLock bool, deps processDependencies) int {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "install":
			bin, _ := os.Executable()
			if err := launchagent.Install(bin); err != nil {
				fmt.Fprintf(os.Stderr, "install: %v\n", err)
				return 1
			}
			return 0
		case "uninstall":
			if err := launchagent.Uninstall(); err != nil {
				fmt.Fprintf(os.Stderr, "uninstall: %v\n", err)
				return 1
			}
			return 0
		case "version", "--version", "-version":
			fmt.Println(versionString())
			return 0
		}
	}

	// Resolve the data directory first and acquire a process-lifetime lock before
	// opening even the shared log. Unlike the HTTP port, this ownership token is
	// retained through every deferred worker/store cleanup, closing the teardown
	// window where a replacement could otherwise mutate the same state while the
	// old process was still alive (#646).
	logDir := dataDir()
	instanceLock, err := singleinstance.Acquire(filepath.Join(logDir, "daemon.lock"))
	if err != nil {
		slog.Error("daemon: another instance owns the data directory; exiting", "dir", logDir, "err", err)
		return 1
	}
	if releaseLock {
		defer func() {
			if err := instanceLock.Close(); err != nil {
				slog.Warn("daemon: release instance lock", "err", err)
			}
		}()
	} else {
		processLifetimeLock = instanceLock
	}

	// setupLogging mirrors the daemon's slog output into
	// <dataDir>/heimdallm.log. The web UI's
	// /logs stream reads that file; writing only to stderr (as we used
	// to) left the stream empty under Docker — see #75.
	logCloser := setupLogging(logDir)
	if logCloser != nil {
		// Flush buffered writes on shutdown so the last lines reach
		// disk even when the daemon is killed mid-log.
		defer logCloser.Close()
	}

	// Restore an updater barrier before loading any stateful subsystem. A sealed
	// marker represents the interval in which Sparkle may have replaced the app
	// bundle; until the native app verifies the exact bundled daemon version,
	// startup must not create configuration, open/migrate SQLite, clear claims,
	// start NATS, or prune worktrees.
	const updateLeaseTTL = 2 * time.Minute
	updateDrainPath := filepath.Join(logDir, "update-drain.json")
	recoveredDesktopIntent, err := workgate.RestoreDesktopRecoveryBarrier(
		updateLeaseTTL,
		updateDrainPath,
		filepath.Join(logDir, "app-update-recovery.json"),
	)
	if err != nil {
		slog.Error("desktop update recovery intent could not be restored safely", "err", err)
		return 1
	}
	if recoveredDesktopIntent {
		slog.Info("daemon: sealed barrier restored from desktop recovery intent")
	}
	updateWorkGate, err := workgate.NewPersistent(updateLeaseTTL, updateDrainPath)
	if err != nil {
		slog.Error("update drain state could not be restored safely", "err", err)
		return 1
	}
	restoredUpdateBarrier := updateWorkGate.Status().Draining

	cfgPath := configPath()

	// loadConfig is captured once at startup so the reload path further
	// down cannot drift: both read the same env-var and select the same
	// loader. Docker deployments (HEIMDALLM_DATA_DIR set) use LoadOrCreate
	// so a missing config.toml is not fatal — the daemon rebuilds from env
	// vars. Desktop deployments use Load; the Flutter app is expected to
	// have written the TOML before the daemon starts, so ENOENT is a real
	// error there.
	loadConfig := config.Load
	if os.Getenv("HEIMDALLM_DATA_DIR") != "" && !restoredUpdateBarrier {
		loadConfig = config.LoadOrCreate
	}

	cfg, err := loadConfig(cfgPath)
	if err != nil {
		slog.Error("config load failed", "path", cfgPath, "err", err)
		return 1
	}

	// Claim single-instance ownership before reading credentials, opening
	// SQLite, clearing restart claims or pruning worktrees. The old ordering
	// discovered EADDRINUSE only after those destructive recovery steps, so a
	// losing second instance could corrupt the live daemon before exiting.
	httpListener, err := deps.listen(cfg.Server.Port, cfg.Server.BindAddr)
	if err != nil {
		slog.Error("daemon: cannot bind HTTP port — another instance is probably already running; exiting",
			"port", cfg.Server.Port, "bind", cfg.Server.BindAddr, "err", err)
		return 1
	}
	defer httpListener.Close()

	// Start serving the claimed listener immediately. During the remainder of
	// startup only GET /health is exposed, as a daemon-identifying 503 response;
	// every other route is gated by startupMiddleware until Configure +
	// MarkReady publish the fully wired dependencies. This avoids both failure
	// modes from #646: a losing process cannot mutate shared state, and the
	// winning process never leaves launchers staring at a bound-but-silent port.
	runtimeCtx, runtimeCancel := context.WithCancel(context.Background())
	defer runtimeCancel()

	// Load authentication and restore the updater lease before Serve exposes
	// even the startup router. This lets the updater renew a lease throughout a
	// slow respawn without opening any other mutation endpoint.
	var apiToken string
	if restoredUpdateBarrier {
		apiToken, err = loadExistingAPIToken(dataDir())
	} else {
		apiToken, err = loadOrCreateAPIToken(dataDir())
	}
	if err != nil {
		slog.Error("could not load API token — refusing to start without authentication", "err", err)
		return 1
	}
	updateBootIDBytes := make([]byte, 32)
	if _, err := rand.Read(updateBootIDBytes); err != nil {
		slog.Error("could not create update process identity — refusing unsafe replacement bootstrap", "err", err)
		return 1
	}
	updateBootID := hex.EncodeToString(updateBootIDBytes)
	// Read-only Tier 1 discovery and Core NATS enqueue hints stay outside the
	// gate; their source rows are durable in SQLite and every side-effecting
	// consumer acquires a permit. Brief single-SQLite retention/claim sweeps are
	// serialized by SQLite Close. External, multi-resource, agent, and
	// filesystem-destructive transactions are all protected.
	srv := server.NewWithOptions(nil, nil, nil, apiToken, server.Options{
		Version:      versionString(),
		StartedAt:    time.Now(),
		UpdateBootID: updateBootID,
	})
	srv.SetUpdatePreparationFns(
		func(leaseID string) (server.UpdatePreparationStatus, error) {
			snapshot, err := updateWorkGate.Prepare(leaseID)
			if err != nil {
				return server.UpdatePreparationStatus{}, updateServerError(err)
			}
			return snapshotUpdatePreparation(snapshot, updateBootID), nil
		},
		func(leaseID string) (server.UpdatePreparationStatus, error) {
			snapshot, err := updateWorkGate.Cancel(leaseID)
			if err != nil {
				return server.UpdatePreparationStatus{}, updateServerError(err)
			}
			return snapshotUpdatePreparation(snapshot, updateBootID), nil
		},
	)
	srv.SetUpdateSealFn(func(leaseID string) (server.UpdatePreparationStatus, error) {
		snapshot, err := updateWorkGate.Seal(leaseID)
		if err != nil {
			return server.UpdatePreparationStatus{}, updateServerError(err)
		}
		return snapshotUpdatePreparation(snapshot, updateBootID), nil
	})
	srv.SetUpdateConfirmFn(func(leaseID string) (server.UpdatePreparationStatus, error) {
		snapshot, err := updateWorkGate.ConfirmBootstrap(leaseID)
		if err != nil {
			return server.UpdatePreparationStatus{}, updateServerError(err)
		}
		return snapshotUpdatePreparation(snapshot, updateBootID), nil
	})
	srv.MarkStarting()
	slog.Info("daemon: HTTP listener claimed", "port", cfg.Server.Port, "bind", cfg.Server.BindAddr)
	serveFailed := make(chan error, 1)
	go func() {
		if err := srv.Serve(httpListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			// Cancel every producer immediately. The main goroutine may still be
			// inside a bounded startup operation, but no poller/worker derived from
			// runtimeCtx can remain alive as a headless daemon.
			slog.Error("daemon: HTTP server stopped serving", "err", err)
			select {
			case serveFailed <- err:
			default:
			}
			runtimeCancel()
		}
	}()
	// Early startup failures must also stop the HTTP goroutine. Shutdown is
	// idempotent; the normal path calls it explicitly for graceful draining.
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			slog.Warn("server shutdown failed", "err", err)
		}
	}()
	takeServeFailure := func() error {
		select {
		case err := <-serveFailed:
			return err
		default:
			return nil
		}
	}
	if restoredUpdateBarrier {
		slog.Info("daemon: updater barrier restored; deferring stateful bootstrap until owner verification",
			"sealed", updateWorkGate.Status().Sealed)
		if err := updateWorkGate.WaitUntilBootstrapAuthorized(runtimeCtx); err != nil {
			slog.Error("daemon: updater barrier wait failed", "err", err)
			return 1
		}
		slog.Info("daemon: stateful bootstrap authorized; update admission remains sealed",
			"draining", updateWorkGate.Status().Draining)
	}

	token, err := keychain.Get()
	if err != nil {
		slog.Error("token not found", "err", err)
		return 1
	}

	dbPath := filepath.Join(dataDir(), "heimdallm.db")
	s, err := store.Open(dbPath)
	if err != nil {
		slog.Error("store open failed", "err", err)
		return 1
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

	// ── NATS event bus (core only, no JetStream) ───────────────────────
	eventBus := bus.New(bus.Config{
		MaxConcurrentWorkers: cfg.Server.MaxConcurrentWorkers,
	})
	if err := eventBus.Start(runtimeCtx); err != nil {
		slog.Error("nats bus failed to start", "err", err)
		return 1
	}
	defer eventBus.Stop()

	// ── Watch store (SQLite, replaces JetStream KV) ─────────────────
	watchStore, err := bus.NewWatchStore(s.DB())
	if err != nil {
		slog.Error("watch store failed to initialize", "err", err)
		return 1
	}

	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()

	// ── Multi-instance control plane ────────────────────────────────────
	// Resolve this daemon's stable identity, then build the registry, the
	// org/repo router and (on a hub) the health prober. Everything below is a
	// no-op on a config with no [cluster]: the router then reports that this
	// daemon owns every repo, which is exactly the single-daemon behaviour.
	instanceID, err := ensureInstanceID(cfg, dataDir())
	if err != nil {
		slog.Error("cluster: could not resolve instance identity", "err", err)
		return 1
	}
	cfg.Cluster.InstanceID = instanceID
	ensureSelfInstance(cfg, dataDir())
	clusterSt := newClusterState(cfg, s, broker)
	// Discovery advertises where the server actually answers, not what
	// config.toml asked for. The listener is already bound by this point and
	// nothing rebinds it, so this is the only address a peer can ever reach.
	if httpListener != nil {
		clusterSt.SetServedAddr(httpListener.Addr())
	}
	if cfg.ClusterEnabled() {
		slog.Info("cluster: multi-instance mode active",
			"instance_id", instanceID, "role", cfg.Cluster.Role,
			"instances", len(cfg.Cluster.Instances))
	}

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
			activityCtx, activityCancel := context.WithCancel(runtimeCtx)
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
	ghClient := deps.newGitHubClient(token)
	exec := executor.New()
	repoCtx := repoctx.NewManagerWithOptions(repoctx.ManagerOptions{
		MaxWorktreesPerRepo: cfg.AI.MaxWorktreesPerRepo,
	})

	// Sweep worktrees left behind by a previous daemon process. At
	// startup the manager has no active worktrees, so every directory
	// under `<clone>/.worktrees/` is by definition stale and safe to
	// remove. Mirrors the in-flight DB sweeps above. (#461)
	if err := runStartupWorktreePrune(runtimeCtx, updateWorkGate, func(ctx context.Context) {
		for _, cloneDir := range managedCloneDirs(cfg) {
			if n, err := repoCtx.PruneStaleWorktreesUnder(ctx, cloneDir); err != nil {
				slog.Warn("startup: prune stale worktrees", "dir", cloneDir, "err", err)
			} else if n > 0 {
				slog.Info("startup: pruned stale worktrees", "dir", cloneDir, "count", n)
			}
		}
	}); err != nil {
		if errors.Is(err, workgate.ErrDraining) {
			slog.Debug("startup: stale worktree prune deferred while application update drains")
		} else {
			slog.Warn("startup: stale worktree prune could not acquire update permit", "err", err)
		}
	}

	p := pipeline.New(s, ghClient, exec, &notifyWithSSE{notifier: notifier})
	p.SetWorkGate(updateWorkGate)
	// Wired once at startup, before any review body can be built, so the
	// footer's version suffix (see pipeline.reviewFooter) is never stale.
	pipeline.SetDaemonVersion(versionString())

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

	// Resolve bot login for re-review context filtering.
	var resolvedBotLogin string
	if login, err := ghClient.AuthenticatedUser(); err == nil {
		resolvedBotLogin = login
		p.SetBotLogin(login)
		slog.Info("bot login resolved", "login", login)
	} else {
		slog.Warn("could not resolve bot login; re-review context filtering and the cross-instance duplicate-review guard stay off until the login is resolved lazily", "err", err)
	}
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
	storePollInterval(cfg.ResolvedPollInterval())
	recordPollCompleted := func(_ string, at time.Time) {
		atomic.StoreInt64(&lastPollUnixNano, at.UTC().UnixNano())
	}

	// Publish the dependencies before lifting the startup gate. The atomic
	// MarkReady transition makes the fully configured server visible to HTTP
	// handlers as one lifecycle step.
	srv.ConfigureDependencies(s, broker, p)
	srv.SetNATSConn(eventBus.Conn())
	srv.SetConfigPath(cfgPath)
	// Identity is published on every daemon (a worker has to be recognisable
	// on /health); the /instances and /cluster routes only on a hub.
	wireCluster(srv, clusterSt, s)
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
		ctx, releaseUpdateWork, err := acquireUpdateWork(ctx, updateWorkGate, workgate.KindMaintenance)
		if err != nil {
			return err
		}
		defer releaseUpdateWork()
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
		ctx, releaseUpdateWork, err := acquireUpdateWork(ctx, updateWorkGate, workgate.KindMaintenance)
		if err != nil {
			return err
		}
		defer releaseUpdateWork()
		reconciler := newRenameReconciler(cfg, &cfgMu, srv.TOMLMu(), s, repoCtx, broker, cfgPath)
		return reconciler.Run(ctx, oldRepo, newRepo)
	})
	srv.SetCleanClonesFn(func(ctx context.Context) (int, error) {
		ctx, releaseUpdateWork, err := acquireUpdateWork(ctx, updateWorkGate, workgate.KindMaintenance)
		if err != nil {
			return 0, err
		}
		defer releaseUpdateWork()
		cfgMu.Lock()
		cfgSnap := cfg
		cfgMu.Unlock()
		return purgeAllManagedClones(ctx, repoCtx, cfgSnap)
	})

	runCloneRetention := func(reason string) {
		ctx, cancel := context.WithTimeout(runtimeCtx, 5*time.Minute)
		defer cancel()
		ctx, releaseUpdateWork, err := acquireUpdateWork(ctx, updateWorkGate, workgate.KindMaintenance)
		if err != nil {
			slog.Debug("clone retention deferred while application update drains", "reason", reason)
			return
		}
		defer releaseUpdateWork()
		cfgMu.Lock()
		cfgSnap := cfg
		var discovered []string
		if cfg.GitHub.DiscoveryTopic != "" {
			discovered = discoverySvc.Discovered()
		}
		cfgMu.Unlock()
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
		reviewFailureRepoHourlyLimit := cfg.CircuitBreakerForRepo(pr.Repo).PerReviewFailureRepoHr
		// Convert config.ResolvedReviewGuards to pipeline.GateConfig via same-shape cast.
		// config cannot import pipeline (import cycle), so the helper returns a shadow
		// type that callers cast here.
		guards := pipeline.GateConfig(cfg.ReviewGuards(botLogin))
		cfgMu.Unlock()
		extraFlags := agentCfg.ExtraFlags
		if extraFlags != "" {
			if err := executor.ValidateExtraFlagsForCLI(cli, extraFlags); err != nil {
				slog.Warn("buildRunOpts: extra_flags from config rejected", "err", err)
				extraFlags = ""
			}
		}
		return pipeline.RunOptions{
			Primary:                      aiCfg.Primary,
			Fallback:                     aiCfg.Fallback,
			PromptOverride:               aiCfg.Prompt,
			AgentPromptID:                agentCfg.PromptID,
			ReviewMode:                   aiCfg.ReviewMode,
			InstructionAuthors:           aiCfg.InstructionAuthors,
			NeverApproveWithIssues:       aiCfg.NeverApproveWithIssues != nil && *aiCfg.NeverApproveWithIssues,
			NeverApproveMinSeverity:      aiCfg.NeverApproveMinSeverity,
			ReviewFailureRepoHourlyLimit: reviewFailureRepoHourlyLimit,
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

	runReview := func(ctx context.Context, pr *gh.PullRequest, aiCfg config.RepoAI) *store.Review {
		ctx, releaseUpdateWork, err := acquireUpdateWork(ctx, updateWorkGate, workgate.KindReview)
		if err != nil {
			slog.Debug("review deferred while application update drains", "repo", pr.Repo, "pr", pr.Number)
			return nil
		}
		defer releaseUpdateWork()
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
		runOpts.WorkPermit = workgate.PermitFromContext(ctx)
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
			broker.Publish(sse.Event{
				Type: sse.EventReviewError,
				Data: sseData(reviewErrorEventData(
					s, 0, pr.Repo, pr.Number, pr.Title, err,
				)),
			})
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

	// Shared rate limiter (was Pipeline.limiter).
	limiter := scheduler.NewRateLimiter(4500)

	// Wire the GitHub client → rate limiter so every API response updates the
	// live budget, enabling proactive throttle before a 403 and correct backoff
	// on secondary limits (Retry-After).
	ghClient.SetRateObserver(&rateLimitAdapter{limiter: limiter})

	// tier2Adapter bridges main.go's concrete types to the polling logic.
	adapter := &tier2Adapter{
		ghClient:             ghClient,
		ghToken:              token,
		pipeline:             p,
		repoCtx:              repoCtx,
		store:                s,
		broker:               broker,
		publishOrderedEvents: newOrderedNATSEventPublisher(conn),
		cfgMu:                &cfgMu,
		cfg:                  &cfg,
		loginMu:              &loginMu,
		login:                &cachedLogin,
		runReview:            runReview,
		publishPub:           publishPub,
		watchStore:           watchStore,
		workGate:             updateWorkGate,
		lastSkippedUpdatedAt: make(map[int64]time.Time),
		lastBreakerTrips:     make(map[breakerTripKey]breakerTripDedup),
		owns:                 clusterSt.Owns,
		dispatchPR: func(repo string, ref instances.PRDispatchRef) bool {
			return clusterSt.DispatchPRReview(runtimeCtx, repo, ref)
		},
	}

	// botLoginAccessor wraps the same loginMu / cachedLogin pair the
	// adapter's cachedAuthenticatedUser uses, so locking discipline lives in
	// one closure rather than being duplicated at each callsite.
	botLoginAccessor := func() string {
		loginMu.Lock()
		defer loginMu.Unlock()
		return cachedLogin
	}
	// The peer-review guard scopes its cross-instance claim to this login
	// (#778). Reading it through the accessor, not the SetBotLogin copy taken
	// above, means a failed AuthenticatedUser at boot disables the guard only
	// until cachedLogin is repaired, not until the daemon restarts.
	p.SetBotLoginFunc(botLoginAccessor)
	repoPublisher := bus.NewRepoPublisher(conn)
	prReviewPublisher := bus.NewPRReviewPublisher(conn)

	// reposChan bridges Tier 1 (discovery) → Tier 2 (per-repo polling) via
	// the NATS discovery stream. Tier 1 publishes to NATS, the bridge
	// consumes from NATS and forwards repo lists through this channel.
	reposChan := make(chan []string, 1)

	// Push the [polling] runtime knobs into the client and limiter. No
	// request goes out before the pollers start. Under cfgMu like every other
	// cfg read: a reload can already be in flight by this point.
	cfgMu.Lock()
	startupCfg := cfg
	cfgMu.Unlock()
	applyClientRuntimeConfig(ghClient, limiter, startupCfg)

	// startPollers launches all polling goroutines under the given context.
	// Returns a cancel function and a WaitGroup that completes when all
	// goroutines have exited.
	startPollers := func(ctx context.Context, coldStart bool) (context.CancelFunc, *sync.WaitGroup, error) {
		ctx, cancel := context.WithCancel(ctx)
		var wg sync.WaitGroup

		// Meter the separate 30/min search budget per request. Rewired on every
		// restart so the gate blocks on the CURRENT poller context and a
		// shutdown cannot be held up waiting on a budget nobody will use.
		ghClient.SetSearchGate(func() error {
			return limiter.AcquireResource(ctx, scheduler.TierRepo, scheduler.SearchResource)
		})
		ghClient.SetGraphQLGate(func() error {
			return limiter.AcquireResource(ctx, scheduler.TierRepo, scheduler.GraphQLResource)
		})

		// Core NATS has no persistence. Establish and flush the discovery
		// subscription before Tier 1 can publish its initial snapshot; otherwise
		// a fast producer wins the race and Tier 2 waits a full poll interval.
		bridgeReady := make(chan error, 1)
		wg.Add(1)
		go func() {
			defer wg.Done()
			bridgeDiscovery(ctx, conn, reposChan, bridgeReady)
		}()
		select {
		case err := <-bridgeReady:
			if err != nil {
				cancel()
				wg.Wait()
				return nil, nil, fmt.Errorf("start discovery bridge: %w", err)
			}
		case <-ctx.Done():
			cancel()
			wg.Wait()
			return nil, nil, ctx.Err()
		}

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

		// In-flight claim sweep (#544). Catches reviews_in_flight claims
		// that were leaked at runtime (panic,
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
			// Retry cooldown tops out at 6h. Keeping state for 48h leaves a wide
			// safety margin while bounding rows left by abandoned PR HEADs.
			const reviewRetryMaxAge = 48 * time.Hour
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
					if n, err := s.PruneReviewRetryBackoffs(time.Now().Add(-reviewRetryMaxAge)); err != nil {
						slog.Warn("sweep: prune review retry cooldowns failed", "err", err)
					} else if n > 0 {
						slog.Info("sweep: pruned review retry cooldown rows", "count", n)
					}
					if n, err := s.PruneReviewRetryAttempts(time.Now().Add(-reviewRetryMaxAge)); err != nil {
						slog.Warn("sweep: prune review retry attempt rows failed", "err", err)
					} else if n > 0 {
						slog.Info("sweep: pruned review retry attempt rows", "count", n)
					}
				}
			}
		}()

		// Tier 1: Discovery — publishes to NATS
		// [polling].discovery_interval takes precedence over [github].discovery_interval;
		// both fall back to 5m (ResolvedDiscoveryInterval handles the full cascade).
		cfgMu.Lock()
		discoveryInterval := cfg.ResolvedDiscoveryInterval()
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

		// Instance health probing (hub only). RunProber returns immediately on
		// a worker or a standalone daemon, so this costs one no-op goroutine
		// rather than needing a conditional around the whole block.
		wg.Add(1)
		go func() {
			defer wg.Done()
			clusterSt.RunProber(ctx)
		}()

		// mDNS: answer "which daemons are on this network" (any role), and on
		// a hub, ask it. Both return immediately when cluster.discovery is off,
		// which is the default — same no-op-goroutine trade as the prober.
		// Living on the poller context is what makes a reload that flips
		// discovery on or off take effect: any [cluster] change restarts the
		// pollers, so these are rebuilt with it.
		wg.Add(1)
		go func() {
			defer wg.Done()
			clusterSt.RunAdvertiser(ctx)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			clusterSt.RunDiscoverer(ctx)
		}()

		// Tier 2: PR polling — use the resolved interval which honours
		// [polling].poll_interval > [github].poll_interval > 5m default.
		cfgMu.Lock()
		pollInterval := cfg.ResolvedPollInterval()
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
			runTier2(ctx, adapter, prReviewPublisher, broker, tier2ConfigFn, reposChan, pollInterval, coldStart, recordPollCompleted)
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
				probe := newRenameProbe(ctx, cfg, &cfgMu, srv.TOMLMu(), ghClient, s, repoCtx, broker, cfgPath, renameInterval, updateWorkGate)
				probe.Run(ctx)
			}()
			slog.Info("rename probe: started", "interval", renameInterval)
		} else {
			slog.Info("rename probe: disabled (ai.repo_rename_check_interval=0)")
		}

		// The repos merge tracking acts on, resolved under cfgMu so a reload
		// takes effect on the next tick.
		monitoredReposFn := func() []string {
			cfgMu.Lock()
			repos := discovery.MergeRepos(cfg.GitHub.Repositories, aiRepoKeys(cfg), nil, cfg.GitHub.NonMonitored)
			cfgMu.Unlock()
			// Partition the work: with instances configured, each daemon acts
			// only on the repos routed to it. Discovery deliberately stays
			// global (every instance still learns about every repo, so the GUI
			// sees the whole picture) — it is acting that is narrowed.
			return clusterSt.FilterOwned(repos)
		}
		// Merge tracking: reconciles the PRs the operator authored or is
		// assigned to towards merge. It is always started and gates itself per
		// repo, so a cycle with the feature off
		// everywhere costs nothing — AnyEnabled short-circuits before the first
		// GitHub call.
		// *gitops.GitExec, *executor.Executor and *gh.Client satisfy the
		// mergetrack interfaces directly — no adapters, so there is no
		// untestable delegation layer sitting in main.
		mergeTrackGit := gitops.NewGitExec()
		mergeTrackRunner := mergetrack.NewWorktreeOps(
			&mergeTrackRepoContexts{manager: repoCtx, token: token, cfg: &cfg, cfgMu: &cfgMu},
			mergeTrackGit,
			mergetrack.NewConflictResolver(mergeTrackGit, exec),
			token,
			mergeTrackAgentSpec(&cfg, &cfgMu),
		)

		mergeTrackReconciler := mergetrack.NewReconciler(mergetrack.ReconcilerOptions{
			Gateway:   ghClient,
			Store:     s,
			Publisher: broker,
			Gate:      updateWorkGate,
			Worktree:  mergeTrackRunner,
			ConfigForRepo: func(repo string) config.MergeTrackingConfig {
				cfgMu.Lock()
				defer cfgMu.Unlock()
				return cfg.MergeTrackingForRepo(repo)
			},
			GlobalConfig: func() config.MergeTrackingConfig {
				cfgMu.Lock()
				defer cfgMu.Unlock()
				return cfg.MergeTracking
			},
			Viewer:          botLoginAccessor,
			DefaultCooldown: pollInterval,
		})

		// The Merge tab's own add-a-PR action. Separate from POST /prs/add,
		// which routes through the review pipeline and therefore refuses the
		// operator's own PRs — the very ones merge tracking exists for.
		srv.SetMergeTrackEnrolFn(func(prID int64, repo string, number int) error {
			return mergeTrackReconciler.EnrolExistingPR(prID, repo, number)
		})

		// The on-demand evaluation behind POST /merge-tracking/{prID}/evaluate.
		srv.SetMergeTrackEvaluateFn(func(ctx context.Context, prID int64, dryRun bool) error {
			_, err := mergeTrackReconciler.ReconcilePR(ctx, prID, time.Now().UTC(), dryRun)
			return err
		})

		wg.Add(1)
		go func() {
			defer wg.Done()
			cfgMu.Lock()
			interval := mergeTrackInterval(cfg, pollInterval)
			cfgMu.Unlock()
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			// One cycle immediately: merge tracking is primarily a reporting
			// surface, and a freshly started daemon showing an empty Merge tab
			// for a full poll interval reads as broken. The cycle is a no-op
			// when the feature is off, so this costs nothing by default.
			runMergeTrackCycle := func() {
				stats := mergeTrackReconciler.Tick(ctx, monitoredReposFn())
				if stats.Evaluated > 0 || stats.Actions > 0 || stats.Errors > 0 {
					slog.Info("merge tracking: cycle complete",
						"discovered", stats.Discovered, "evaluated", stats.Evaluated,
						"actions", stats.Actions, "errors", stats.Errors)
				}
			}
			runMergeTrackCycle()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					// Hot reload of the cadence, same pattern as tier 3: a
					// config change must not need a daemon restart.
					cfgMu.Lock()
					next := mergeTrackInterval(cfg, pollInterval)
					cfgMu.Unlock()
					if next != interval {
						interval = next
						ticker.Reset(interval)
					}
					runMergeTrackCycle()
				}
			}
		}()

		// Every other cfg read in startPollers takes cfgMu; these two did not.
		// startPollers re-runs on the restart goroutine after a reload, where a
		// concurrent reload can reassign cfg under the same mutex — a data race
		// that -race would flag.
		cfgMu.Lock()
		etagEnabled := cfg.ETagEnabled()
		rateLimitThreshold := cfg.Polling.RateLimitSafetyThreshold
		cfgMu.Unlock()
		slog.Info("pollers: started",
			"discovery", discoveryInterval,
			"poll", pollInterval,
			"etag_cache", etagEnabled,
			"rate_limit_threshold", rateLimitThreshold)

		return cancel, &wg, nil
	}

	// Initial daemon start → coldStart=true so Tier 2 fires its first tick
	// immediately; operators see polling activity without waiting an entire
	// PollInterval. The reload path below passes false.
	if err := takeServeFailure(); err != nil {
		slog.Error("daemon: aborting startup after HTTP serve failure", "err", err)
		return 1
	}
	const coreWorkerCount = 3 // review, publish, state check
	workerReady := make(chan error, coreWorkerCount)

	// ── NATS PR review worker ───────────────────────────────────────────
	// Consumes PR review requests published by Tier 2 and runs the
	// existing review pipeline. This replaces the goroutine-per-PR
	// pattern that Tier 2 used to use.
	reviewHandlerCore := func(ctx context.Context, msg bus.PRReviewMsg) {
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
		loginMu.Lock()
		botLogin := cachedLogin
		loginMu.Unlock()
		if botLogin != "" && !pr.ReviewRequestedFor(botLogin) {
			// Search is eventually consistent; the fresh Pulls response is the
			// source of truth. Keeping this guard in the worker removes the old
			// duplicate serial hydration without admitting ghost results.
			slog.Debug("review-worker: bot no longer requested, skipping stale search result",
				"repo", msg.Repo, "pr", msg.Number, "bot", botLogin)
			return
		}
		if adapter.PRAlreadyReviewed(pr.ID, pr.Repo, pr.Number, pr.UpdatedAt, pr.Head.SHA) {
			slog.Debug("review-worker: fresh PR already handled, skipping",
				"repo", msg.Repo, "pr", msg.Number, "head_sha", pr.Head.SHA)
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

		rev := runReview(ctx, pr, aiCfg)

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

	reviewHandler := guardUpdateVoidHandler(
		updateWorkGate,
		workgate.KindReview,
		"review-worker: deferred while application update drains",
		reviewHandlerCore,
	)
	reviewWorker := worker.NewReviewWorker(conn, maxWorkers, reviewHandler)
	workerCtx, workerCancel := context.WithCancel(runtimeCtx)
	defer workerCancel()
	go func() {
		if err := reviewWorker.Start(workerCtx, workerReady); err != nil {
			slog.Error("review worker stopped", "err", err)
		}
	}()

	// ── NATS PR publish worker ──────────────────────────────────────────
	// Consumes publish requests and submits stored reviews to GitHub.
	// Replaces the manual retry loop in PublishPending with NATS retry
	// semantics (NakWithDelay for transient GitHub errors).
	publishHandlerCore := func(ctx context.Context, msg bus.PRPublishMsg) error {
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
		// Cross-instance duplicate guard (#765). Runs here, under the atomic
		// publish claim and after the HEAD refresh above, so it sees the same
		// commit the submit below will be anchored to. Returning an error naks
		// the message for retry; the review stays unpublished either way.
		//
		// pendingReviewInvalidReason above already retires this row if
		// snapshot.HeadSHA disagrees with rev.HeadSHA, so the two are the same
		// value by this point — passed explicitly, rather than relying on that
		// invariant silently, so this call site cannot drift from the other two
		// if that upstream check ever changes (#772).
		if skip, err := p.SkipIfPeerPublished(rev, pr.Repo, pr.Number, pr.Title, rev.HeadSHA, snapshot.HeadSHA); skip {
			if err != nil {
				return fmt.Errorf("retire review %d already published by a peer instance: %w", rev.ID, err)
			}
			return nil // ack — the review that matters is already on the PR
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

	publishHandler := guardUpdateResultHandler(
		updateWorkGate,
		workgate.KindPublish,
		"publish-worker: deferred while application update drains",
		error(nil), // PublishPending re-enqueues the still-unpublished row.
		publishHandlerCore,
	)
	publishW := worker.NewPublishWorker(conn, maxWorkers, publishHandler)
	publishWCtx, publishWCancel := context.WithCancel(runtimeCtx)
	defer publishWCancel()
	go func() {
		if err := publishW.Start(publishWCtx, workerReady); err != nil {
			slog.Error("publish worker stopped", "err", err)
		}
	}()

	// ── State check poller ──────────────────────────────────────────────
	// Scans the NATS KV watch bucket on the configured Tier 3 interval and
	// publishes StateCheckMsg for items due for a state check. Replaces the
	// in-memory WatchQueue. Default: 30s (matches previous hardcoded value).
	stateCheckPub := bus.NewStateCheckPublisher(conn)
	statePollerCtx, statePollerCancel := context.WithCancel(runtimeCtx)
	defer statePollerCancel()
	go func() {
		cfgMu.Lock()
		tier3Interval := cfg.ResolvedTier3Interval()
		cfgMu.Unlock()
		ticker := time.NewTicker(tier3Interval)
		defer ticker.Stop()
		for {
			select {
			case <-statePollerCtx.Done():
				return
			case <-ticker.C:
				// This goroutine lives outside startPollers and is only
				// cancelled at shutdown, so a reload that changes
				// tier3_interval would otherwise leave it ticking at the old
				// cadence forever while GET /config reported the new one.
				// Re-read and reset each tick instead.
				cfgMu.Lock()
				wantInterval := cfg.ResolvedTier3Interval()
				cfgMu.Unlock()
				if wantInterval != tier3Interval {
					slog.Info("state-poller: tier3 interval changed, resetting ticker",
						"from", tier3Interval, "to", wantInterval)
					tier3Interval = wantInterval
					ticker.Reset(tier3Interval)
				}

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
	stateW.SetWorkGate(updateWorkGate)
	stateWCtx, stateWCancel := context.WithCancel(runtimeCtx)
	defer stateWCancel()
	go func() {
		if err := stateW.Start(stateWCtx, workerReady); err != nil {
			slog.Error("state worker stopped", "err", err)
		}
	}()

	// All three work subjects use Core NATS. Confirm their subscriptions have
	// reached the server before a cold poll can publish; otherwise startup can
	// lose review messages until the next poll just as discovery could.
	if err := waitForWorkerReadiness(runtimeCtx, workerReady, coreWorkerCount, 5*time.Second); err != nil {
		slog.Error("workers: subscriptions not ready", "err", err)
		return 1
	}

	pollerCancel, pollerWg, err := startPollers(runtimeCtx, true)
	if err != nil {
		slog.Error("pollers: startup failed", "err", err)
		return 1
	}

	// Use a closure so the defer reads the current cancel/wg at shutdown
	// time, not the initial values captured at defer-statement time. After a
	// reload, pollerCancel/pollerWg point to the new goroutines — the bare
	// defer would stop the already-halted original set and leak the
	// post-reload ones.
	defer func() {
		// A reload restart owns restartMu across old-poller teardown and new-
		// poller publication. Cancel the shared parent first, then wait for that
		// critical section before snapshotting, so shutdown can never miss a
		// newly published poller set.
		runtimeCancel()
		restartMu.Lock()
		defer restartMu.Unlock()
		cfgMu.Lock()
		cancel := pollerCancel
		wg := pollerWg
		cfgMu.Unlock()
		cancel()
		wg.Wait()
		slog.Info("pollers: stopped")
	}()

	// Expose live config for GET /config

	// Live GitHub API rate-limit lookup for GET /github/rate_limit. Served from
	// the scheduler's tracker (real X-RateLimit-* headers observed on every
	// API call), falling back to GitHub's GET /rate_limit only for a bucket
	// that hasn't been observed yet. See buildRateLimitView's doc comment for
	// why the tracker — not GitHub's own endpoint — must be the primary source.
	srv.SetRateLimitFn(func() (any, error) {
		return buildRateLimitView(time.Now(), limiter.Snapshots(), ghClient.RateLimit)
	})

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
			"repo_overrides":              repoOverrides,
			"org_overrides":               orgOverrides,
			"agent_configs":               agentConfigs,
			"local_dirs_detected":         localDirsDetected,
			"activity_log_enabled":        ptrBoolOrTrue(c.ActivityLog.Enabled),
			"activity_log_retention_days": ptrIntOr(c.ActivityLog.RetentionDays, 90),
			"clone_dir":                   c.AI.CloneDir,
			"never_approve_with_issues":   c.AI.NeverApproveWithIssues,
			"never_approve_min_severity":  c.AI.NeverApproveMinSeverity,
		}
		// Merge tracking. Without this projection the app PATCHes the section
		// successfully, the daemon honours it, and the settings screen still
		// reads it back as defaults on the next load — the toggle silently
		// resets itself in front of the operator.
		result["merge_tracking"] = mergeTrackingConfigMap(c.MergeTracking)
		result["circuit_breaker"] = map[string]any{
			"per_pr_24h":                 c.CircuitBreaker.PerPR24h,
			"per_repo_hr":                c.CircuitBreaker.PerRepoHr,
			"per_review_failure_repo_hr": c.CircuitBreaker.PerReviewFailureRepoHr,
		}
		result["polling"] = map[string]any{
			"poll_interval":               c.Polling.PollInterval,
			"discovery_interval":          c.Polling.DiscoveryInterval,
			"tier3_interval":              c.Polling.Tier3Interval,
			"rate_limit_safety_threshold": c.Polling.RateLimitSafetyThreshold,
			"use_etag":                    c.ETagEnabled(),
		}
		// Cluster. Without this projection the app can PATCH cluster.role but
		// the Settings screen reads back "standalone" forever, looking like
		// the change never took.
		result["cluster"] = clusterConfigMap(c.Cluster)
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

		// Refresh routing before the restart decision below: the pollers read
		// ownership live on every tick, so a routing-only change must take
		// effect even on the fast path that does not restart them.
		//
		// instanceID may still be "" if [cluster] did not exist when this
		// daemon booted (ensureInstanceID is boot-only otherwise). Re-resolve
		// it here so enabling clustering via a reload — no restart — does not
		// leave selfID empty forever, which made Router.Owns fail open and
		// the hub silently keep every repo it had just routed away.
		resolvedID, idErr := resolveReloadInstanceID(instanceID, newCfg, dataDir())
		if idErr != nil {
			return fmt.Errorf("reload: cluster: could not resolve instance identity: %w", idErr)
		}
		instanceID = resolvedID
		newCfg.Cluster.InstanceID = instanceID
		ensureSelfInstance(newCfg, dataDir())
		proberJustBuilt := clusterSt.Update(newCfg)
		// wireCluster is otherwise boot-only (main.go's call happens once,
		// before Serve). Re-running it here is what lets a worker/standalone
		// daemon promoted to hub purely by this reload get /instances and
		// /cluster mounted without a restart.
		wireCluster(srv, clusterSt, s)
		if proberJustBuilt {
			go clusterSt.RunProber(runtimeCtx)
		}

		// Keep the fleet in sync automatically: every save on the hub pushes
		// the shared config to the other instances instead of waiting for an
		// operator to remember the manual propagate dialog. Backgrounded so a
		// slow or unreachable instance never delays the reload that triggered
		// it; each instance's own PatchConfig/reload cycle applies it.
		//
		// propagatePartition is the separate channel that pushes the
		// ownership partition itself (identity, default_instance,
		// routing.orgs/repos) — cluster.* is deliberately excluded from the
		// general push above, so a worker never gets that from
		// propagateClusterConfig no matter how often it runs. Without this, a
		// worker with no partition of its own fails closed and does nothing
		// (see instances.Router's worker gate) — this is what closes
		// theburrowhub/heimdallm#769.
		if newCfg.IsHub() {
			go propagateClusterConfig(runtimeCtx, cfgPath, clusterSt, broker)
			go propagatePartition(runtimeCtx, clusterSt)
		}

		cfgMu.Lock()
		restartPollers := configReloadRequiresPollerRestart(cfg, newCfg)
		if !restartPollers {
			cfg = newCfg
			cfgMu.Unlock()
			// The kill-switches live on the client and limiter, not the
			// pollers, so they need applying even when nothing restarts.
			applyClientRuntimeConfig(ghClient, limiter, newCfg)
			slog.Info("config reload: applied without poller restart")
			return nil
		}
		cfgMu.Unlock()

		// Apply the new config immediately so config reads (GET /config, the
		// next tier1ConfigFn tick, etc.) reflect it right away.
		cfgMu.Lock()
		cfg = newCfg
		cfgMu.Unlock()
		applyClientRuntimeConfig(ghClient, limiter, newCfg)

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

			if runtimeCtx.Err() != nil {
				slog.Info("config reload: poller restart skipped during shutdown")
				return
			}

			newCancel, newWg, err := startPollers(runtimeCtx, false)
			if err != nil {
				slog.Error("config reload: poller restart failed", "err", err)
				return
			}
			if runtimeCtx.Err() != nil {
				newCancel()
				newWg.Wait()
				slog.Info("config reload: new pollers stopped during shutdown")
				return
			}

			cfgMu.Lock()
			pollerCancel = newCancel
			pollerWg = newWg
			cfgMu.Unlock()

			slog.Info("config reload: pollers restarted")
			if deps.afterPollerRestart != nil {
				deps.afterPollerRestart()
			}
		}()

		return nil
	})

	// Wire the trigger-review callback: re-run pipeline on a single stored PR.
	// In-process per-PR-ID guard for manual triggers. Backstops the persistent
	// in-flight claim for the window where the HEAD SHA lookup fails (see
	// triggerGuard and RunOptions.Force). One instance shared across all
	// trigger invocations via the closure below.
	manualReviewGuard := newTriggerGuard()
	srv.SetCancelReviewFn(func(prID int64) (bool, error) {
		return exec.TerminateExecution(pipeline.ReviewExecutionID(prID))
	})
	srv.SetTriggerReviewFn(func(prID int64) error {
		ctx, releaseUpdateWork, err := acquireUpdateWork(runtimeCtx, updateWorkGate, workgate.KindReview)
		if err != nil {
			slog.Debug("trigger review deferred while application update drains", "pr_id", prID)
			return nil
		}
		defer releaseUpdateWork()
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
			// Never surface the raw store error to the UI: before the cluster
			// dispatch fix (theburrowhub/heimdallm#799), a peer instance
			// receiving an id minted by a different daemon hit exactly this
			// path, and "PR not found: store: scan pr: sql: no rows in result
			// set" is what showed up as the "Review Failed" notification. The
			// full error still goes to the log for diagnosis. By the time
			// this callback runs, the caller (handleTriggerReview,
			// handleAddPR, handleClusterTriggerPRReview) has already
			// validated or just created prID, so reaching this at all means
			// something changed underneath it — no need to distinguish
			// "not found" from other store failures for the operator here.
			slog.Error("trigger review: get pr failed", "pr_id", prID, "err", err)
			publishErr(fmt.Sprintf("PR not found (id %d) on this instance.", prID))
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
		repoHandle, err := acquireRepoContext(ctx, repoCtx, pr.Repo, &aiCfg, localDirBase, token, repoctx.ModeRead, wtTokenFor("pr-review", pr.Number), "", "")
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

		// Persistent in-flight claim: keyed on (GitHub pr_id, head_sha), the
		// same namespace as the poll loop so both paths share one guard across
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
			ok, err := s.ClaimInFlightReview(pr.GithubID, ghPR.Head.SHA)
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
				if err := s.ReleaseInFlightReview(pr.GithubID, ghPR.Head.SHA); err != nil {
					slog.Warn("trigger review: release inflight failed", "err", err,
						"pr_id", pr.GithubID, "head_sha", ghPR.Head.SHA)
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
		runOpts.WorkPermit = workgate.PermitFromContext(ctx)
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
			broker.Publish(sse.Event{
				Type: sse.EventReviewError,
				Data: sseData(reviewErrorEventData(
					s, prID, pr.Repo, pr.Number, pr.Title, err,
				)),
			})
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

	// Manual PR add (Activity view "ADD" action → POST /prs/add): fetch the PR
	// from GitHub by (repo, number) and upsert it into the store. The server
	// handler adds the repo to the monitored list and triggers the review;
	// this callback owns only the GitHub fetch + store write. The base repo
	// from the URL is authoritative for the stored Repo (GetPR would otherwise
	// use head.repo.full_name, which is the fork for cross-repo PRs).
	srv.SetAddPRFn(func(repo string, number int) (*store.PR, error) {
		ghPR, err := ghClient.GetPR(repo, number)
		if err != nil {
			return nil, err
		}
		storePR := &store.PR{
			GithubID:  ghPR.ID,
			Repo:      repo,
			Number:    ghPR.Number,
			Title:     ghPR.Title,
			Author:    ghPR.User.Login,
			URL:       ghPR.HTMLURL,
			State:     ghPR.State,
			UpdatedAt: ghPR.UpdatedAt,
			FetchedAt: time.Now().UTC(),
		}
		id, err := s.UpsertPR(storePR)
		if err != nil {
			return nil, fmt.Errorf("upsert pr: %w", err)
		}
		storePR.ID = id
		slog.Info("manual PR add: stored", "repo", repo, "number", number, "store_pr_id", id, "github_id", ghPR.ID)
		return storePR, nil
	})

	serveDied := false
	if err := takeServeFailure(); err != nil {
		// Workers now exist, so do not return directly: the common shutdown path
		// below must cancel every producer and terminate any child CLI already
		// launched during startup.
		slog.Error("daemon: aborting startup after HTTP serve failure", "err", err)
		serveDied = true
	} else {
		// Every dependency is wired: /health can run its deep checks now. The
		// listener has been bound since the start of run and is actively served,
		// so this log line means what it says — the daemon is reachable and ready.
		srv.MarkReady()
		slog.Info("daemon started", "port", cfg.Server.Port, "bind", cfg.Server.BindAddr)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sig)
	if !serveDied {
		select {
		case received := <-sig:
			slog.Info("shutting down", "signal", received.String())
		case <-shutdownReq:
			slog.Info("shutting down via API")
		case err := <-serveFailed:
			// Losing the listener mid-flight leaves the same headless daemon the
			// bind check above prevents at startup, so treat it the same way:
			// shut down and exit non-zero instead of polling GitHub forever with
			// nobody able to reach us (#646). Already logged in the serve goroutine.
			slog.Error("daemon: shutting down after serve failure", "err", err)
			serveDied = true
		}
	}
	// Keep the HTTP ownership socket open but gate dependency-consuming routes
	// while producers drain. The independent data-dir lock remains held even
	// longer — through every deferred Wait/Close — so a replacement cannot
	// overlap SQLite/worktree cleanup with this process.
	srv.MarkStopping()
	runtimeCancel()
	// Stop the agents this daemon started. Each execution runs in its own
	// process group so a timeout can reach the CLI's grandchildren (#614), which
	// also means a group-directed signal to the daemon no longer reaches them:
	// without this sweep, in-flight agents survive the restart and keep spending
	// provider quota.
	//
	// Every producer of work is cancelled first. The workers and tickers below
	// run on their own contexts,
	// whose cancels are deferred to main's return — i.e. after this point. A
	// worker starting an execution between the sweep's snapshot and process exit
	// would leave exactly the orphan this sweep exists to prevent. CancelFunc is
	// idempotent, so the deferred cancels remain harmless.
	stopProducersThenAgents([]context.CancelFunc{
		func() {
			cfgMu.Lock()
			cancel := pollerCancel
			cfgMu.Unlock()
			cancel()
		},
		workerCancel, publishWCancel, statePollerCancel, stateWCancel,
	}, exec.TerminateAll, producerSettleDelay)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Warn("server shutdown failed", "err", err)
	}
	if serveDied {
		return 1
	}
	return 0
}

// producerSettleDelay is how long the shutdown path waits between cancelling the
// work producers and its second sweep of in-flight agents. Cancelling a context
// does not wait for the worker to act on it.
const producerSettleDelay = 500 * time.Millisecond

// stopProducersThenAgents runs the ordering the shutdown path depends on:
// silence everything that can start an execution, then sweep the executions
// still running. Reversing it leaves a window where a worker starts a CLI the
// sweep has already snapshotted past, and that CLI — in its own process group
// since #656 — outlives the daemon.
//
// It sweeps twice because cancellation is asynchronous: a worker already past its
// context check and about to call cmd.Start() still starts an execution after the
// first snapshot. The settle delay plus a second pass catches that one. This
// narrows the window rather than closing it — closing it needs the workers to
// report completion (a WaitGroup joined with a bounded wait), which is #614's
// "shutdown ... waits for all goroutines" criterion. Nil cancels are skipped so a
// producer that never started is not a special case at the call site.
func stopProducersThenAgents(stopProducers []context.CancelFunc, terminateAgents func(), settle time.Duration) {
	for _, stop := range stopProducers {
		if stop != nil {
			stop()
		}
	}
	if terminateAgents == nil {
		return
	}
	terminateAgents()
	if settle > 0 {
		time.Sleep(settle)
	}
	terminateAgents()
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
	snap.GitHub.ReviewGuards = config.ReviewGuardsConfig{}

	snap.AI.Primary = ""
	snap.AI.Fallback = ""
	snap.AI.ReviewMode = ""
	snap.AI.ExecutionTimeout = ""
	snap.AI.Agents = nil
	snap.AI.Repos = nil
	snap.AI.Orgs = nil
	snap.AI.CloneDir = ""
	snap.AI.NeverApproveWithIssues = false
	snap.AI.NeverApproveMinSeverity = ""

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
// process. Per-agent timeout wins over the global timeout; absent or invalid
// values fall back to the executor's normal 20-minute budget.
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
	return executor.DefaultExecutionTimeout
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
func bridgeDiscovery(ctx context.Context, conn *nats.Conn, out chan<- []string, ready chan<- error) {
	ch := make(chan *nats.Msg, 8)
	sub, err := conn.ChanSubscribe(bus.SubjDiscoveryRepos, ch)
	if err != nil {
		slog.Error("bridge: subscribe to discovery subject failed", "err", err)
		if ready != nil {
			ready <- err
		}
		return
	}
	defer sub.Unsubscribe()
	if err := conn.FlushTimeout(2 * time.Second); err != nil {
		slog.Error("bridge: flush discovery subscription failed", "err", err)
		if ready != nil {
			ready <- err
		}
		return
	}
	if ready != nil {
		ready <- nil
	}

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
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

// waitForWorkerReadiness collects one explicit post-Subscribe+Flush result per
// Core NATS worker. The timeout is only a startup failure bound, never a fixed
// delay on the happy path.
func waitForWorkerReadiness(ctx context.Context, ready <-chan error, count int, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for i := 0; i < count; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("timed out waiting for Core NATS workers: ready %d of %d", i, count)
		case err, ok := <-ready:
			if !ok {
				return fmt.Errorf("Core NATS worker readiness channel closed after %d of %d", i, count)
			}
			if err != nil {
				return fmt.Errorf("Core NATS worker subscription: %w", err)
			}
		}
	}
	return nil
}

// applyClientRuntimeConfig pushes the [polling] kill-switches and safety knobs
// into the live GitHub client, rate limiter and adaptive scheduler.
//
// This must run on every config change, not only at startup. The three
// settings live on long-lived objects that outlive a poller restart, so
// applying them once at boot left the runtime on the old values while
// GET /config and the UI already reported the new ones — a silent divergence
// precisely on the emergency knobs an operator reaches for during an incident.
//
// Safe to call from any goroutine: the client flags are atomics and the
// limiter guards its threshold with its own mutex.
func applyClientRuntimeConfig(ghClient *gh.Client, limiter *scheduler.RateLimiter, cfg *config.Config) {
	// ETag cache (C1): enabled by default; operator can disable via use_etag=false.
	ghClient.SetCacheEnabled(cfg.ETagEnabled())
	// Rate-limit safety threshold: drives how eagerly each tier backs off.
	// Default 100 matches the hardcoded tierSafetyThreshold[TierDiscovery].
	limiter.SetDiscoverySafetyThreshold(cfg.Polling.RateLimitSafetyThreshold)
}

type tier2PRCandidatePublisher interface {
	PublishPRReviewCandidate(ctx context.Context, repo string, number int, githubID int64) error
}

// runTier2 runs the PR polling loop. Replaces the old RunTier2 from
// the scheduler package.
func runTier2(
	ctx context.Context,
	adapter *tier2Adapter,
	prPublisher tier2PRCandidatePublisher,
	ssePub sse.Publisher,
	configFn func() []string,
	reposChan <-chan []string,
	interval time.Duration,
	coldStart bool,
	pollCompletedFn func(kind string, at time.Time),
) {
	var (
		mu                sync.Mutex
		repos             []string
		firstSnapshotOnce sync.Once
		firstSnapshot     = make(chan struct{})
	)

	// Goroutine to receive repo updates from Tier 1
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case r, ok := <-reposChan:
				if !ok {
					return
				}
				// Classify first so live eligibility callbacks can never observe a
				// raw topic result before auto-enable=false records it disabled.
				adapter.upsertDiscoveredFromTopics(r)
				mu.Lock()
				repos = r
				mu.Unlock()
				firstSnapshotOnce.Do(func() { close(firstSnapshot) })
				slog.Info("tier2: received repo list", "count", len(r))
			}
		}
	}()

	// A cold tick is useful only after Tier 1's first snapshot, including an
	// explicitly empty one. Event readiness removes the fixed 2 s delay and,
	// unlike a timer, cannot fire early and defer real work for a full interval.
	if coldStart {
		select {
		case <-firstSnapshot:
		case <-ctx.Done():
			return
		}
	}

	snapshotRepos := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), repos...)
	}

	// runPRTier publishes review requests for every reviewable PR.
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
		// FetchPRsToReview meters each Search page against GitHub's Search
		// resource. A generic core acquire here charged the same operation a
		// second time and could stall a healthy Search budget behind an unrelated
		// core threshold.
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
			// Search candidates normally have no HEAD SHA. Let the worker's
			// single fresh hydration run the SHA-scoped dedup/circuit breaker;
			// calling it here with an empty SHA would broaden a per-head cap to
			// every commit on the PR and could suppress legitimate new work.
			if pr.HeadSHA != "" && adapter.PRAlreadyReviewed(pr.ID, pr.Repo, pr.Number, pr.UpdatedAt, pr.HeadSHA) {
				continue
			}
			prCount++
			if err := prPublisher.PublishPRReviewCandidate(ctx, pr.Repo, pr.Number, pr.ID); err != nil {
				slog.Error("tier2: publish PR review", "repo", pr.Repo, "pr", pr.Number, "err", err)
			}
		}
	}

	// A time.Ticker drops extra ticks when its previous run is still in
	// flight, so the PR tier never runs concurrently against itself.
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
		adapter.PublishPending()
	}
	// time.Ticker's channel is buffered to size 1, so a tick that fires
	// while the previous prTick is still running is silently dropped —
	// the invariant that prevents the tier from running concurrently
	// against itself without needing a mutex.
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	if coldStart {
		prTick()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			prTick()
		}
	}
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
// that is wired up but has no active PRs still stays monitored.
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
		processDiscoveredReposOrdered(added, reposSnap, nonMonSnap, a.store, a.broker, time.Now(), a.publishOrderedEvents)
	}
}

// ── rateLimitAdapter bridges the GitHub client observer → scheduler limiter ──

// rateLimitAdapter implements gh.RateLimitObserver. After every GitHub API
// response it parses the X-RateLimit-* headers and forwards the live budget
// to the scheduler limiter, enabling proactive throttle before hitting 403
// and honoring Retry-After on secondary limits.
type rateLimitAdapter struct {
	limiter *scheduler.RateLimiter
}

func (a *rateLimitAdapter) ObserveResponse(resp *http.Response) {
	parsed, ok := gh.ParseRateLimitHeaders(resp)
	if !ok {
		return
	}
	if parsed.Resource != "" {
		a.limiter.Observe(parsed.Resource, scheduler.RateSnapshot{
			Limit:      parsed.Limit,
			Remaining:  parsed.Remaining,
			Used:       parsed.Used,
			Reset:      parsed.Reset,
			ObservedAt: time.Now(),
		})
	}
	if parsed.RetryAfter > 0 {
		a.limiter.ObserveRetryAfter(parsed.RetryAfter)
	}
}

// ── tier2Adapter bridges main.go's concrete types to Pipeline interfaces ──

type tier2Adapter struct {
	ghClient *gh.Client
	ghToken  string
	pipeline *pipeline.Pipeline
	repoCtx  *repoctx.Manager
	store    *store.Store
	broker   *sse.Broker
	// Synchronous NATS handoff for discovery ordering. Optional in tests.
	publishOrderedEvents func([]sse.Event) error
	cfgMu                *sync.Mutex
	cfg                  **config.Config
	loginMu              *sync.Mutex
	login                *string
	runReview            func(ctx context.Context, pr *gh.PullRequest, aiCfg config.RepoAI) *store.Review
	workGate             *workgate.Gate
	publishPub           *bus.PRPublishPublisher
	watchStore           *bus.WatchStore

	// owns reports whether this daemon should act on a repo. Nil means "yes",
	// which is the single-daemon behaviour and what every test that does not
	// care about clustering gets for free.
	owns func(repo string) bool

	// dispatchPR hands a PR review to the instance repo is routed to. Called
	// only when owns(repo) is false. Returning true means the remote accepted
	// it and this daemon must not also review it; false (including a nil
	// dispatchPR) means fall back to reviewing it locally — a PR must never go
	// unreviewed just because its routed owner is unavailable.
	//
	// ref carries the PR's cluster-stable identity (github_id, repo+number)
	// rather than a store row ID: prs.id is local to each daemon, so this
	// daemon's rowid for the PR has no meaning on the instance it is routed
	// to (theburrowhub/heimdallm#799).
	dispatchPR func(repo string, ref instances.PRDispatchRef) bool

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
	processDiscoveredReposOrdered(added, reposSnap, nonMonSnap, st, broker, now, nil)
}

func processDiscoveredReposOrdered(
	added []string,
	reposSnap []string,
	nonMonSnap []string,
	st discoveryStore,
	broker *sse.Broker,
	now time.Time,
	publishOrdered func([]sse.Event) error,
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

	events := make([]sse.Event, 0, len(added))
	for _, r := range added {
		events = append(events, sse.Event{
			Type: sse.EventRepoDiscovered,
			Data: sseData(map[string]any{"repo": r}),
		})
	}
	if publishOrdered != nil {
		if err := publishOrdered(events); err != nil {
			slog.Warn("poll: publish ordered repo discovery batch failed", "repos", len(events), "err", err)
		} else {
			for i := range events {
				events[i].NATSForwarded = true
			}
		}
	}
	for i, event := range events {
		broker.Publish(event)
		slog.Info("poll: auto-discovered repo", "repo", added[i])
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
	processDiscoveredReposOrdered(added, reposSnap, nonMonSnap, a.store, a.broker, time.Now(), a.publishOrderedEvents)

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
		// Repos routed to another instance: this runs AFTER
		// upsertDiscoveredRepos on purpose, so every instance still discovers
		// every repo (the GUI shows the whole estate) while only the owner
		// acts. Without instances configured this is always true and the loop
		// behaves exactly as it did before.
		//
		// A repo we don't own is not simply skipped: try to dispatch it to
		// its owner first. Only `continue` (skip local review) when that
		// dispatch actually succeeds — dispatchPR being nil, or the owner
		// being down/unreachable/erroring, must fall through to reviewing it
		// here instead. Silently dropping it (the old behaviour) is what let
		// PRs in a routed org disappear from the queue entirely.
		if a.owns != nil && !a.owns(pr.Repo) {
			if a.dispatchPR != nil && a.dispatchPR(pr.Repo, instances.PRDispatchRef{
				GithubID: pr.ID, Repo: pr.Repo, Number: pr.Number, URL: pr.HTMLURL,
			}) {
				continue
			}
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
			HeadSHA:   pr.Head.SHA,
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
	ctx, releaseUpdateWork, err := acquireUpdateWork(ctx, a.workGate, workgate.KindReview)
	if err != nil {
		slog.Debug("tier2 PR deferred while application update drains", "repo", pr.Repo, "pr", pr.Number)
		return nil
	}
	defer releaseUpdateWork()
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
		// Legacy/direct callers may provide HeadSHA here. The current NATS
		// ingestion path hydrates the PullRequest in its worker and calls
		// runReview with that fresh SHA instead.
		Head: gh.Branch{SHA: pr.HeadSHA},
	}
	rev := a.runReview(ctx, ghPR, aiCfg)
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
	// been removed. The review worker now confirms the bot is still in
	// requested_reviewers using its single fresh Pulls API hydration before a
	// PR reaches the pipeline. That check eliminates "ghost" Search results and
	// replication lag — the only scenario the grace protected against. Without
	// the grace, a push + re-request-review within 2 minutes of the last
	// review is picked up immediately instead of being suppressed until the
	// grace expired. See theburrowhub/heimdallm#243 for the original incident
	// and the worker hydration that replaced the duplicate adapter lookup.
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
// against fresh data without a second round-trip.
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
	// PRs are the only watched item type. Anything else is a leftover row
	// from before the issue pipelines were removed; report no change and let
	// the watch store evict it.
	return false, nil, nil
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
		localDirBase := c.GitHub.LocalDirBase
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

		// Reserve the checkout the same way review-worker and tier2 ProcessPR
		// do. Without this the agent ran with no WorkDir and inherited the
		// daemon's cwd — `/` under launchd — which aborts `codex exec` outright
		// (#655). Acquired here, after every guard and the dedup above, so the
		// paths that skip the review do not reserve a worktree they never use.
		repoHandle, err := acquireRepoContext(ctx, a.repoCtx, item.Repo, &aiCfg, localDirBase, a.ghToken, repoctx.ModeRead, wtTokenFor("pr-tier3", item.Number), "", "")
		if err != nil {
			logRepoContextFallback("tier3 PR", item.Repo, err)
			aiCfg.LocalDir = ""
		}
		if repoHandle != nil {
			defer repoHandle.Release()
		}
		// Reserving the checkout can block for a long time — cloning, fetching,
		// or queueing on the per-repo worktree cap — and the operator may disable
		// the repo during that wait. Recheck at the execution boundary, mirroring
		// review-worker's skipIfUnmonitored("post_acquire"), so a disable stops
		// the CLI instead of spending provider quota on an un-monitored repo.
		a.cfgMu.Lock()
		stillMonitored := repoIsMonitored(*a.cfg, item.Repo)
		a.cfgMu.Unlock()
		if !stillMonitored {
			a.broker.Publish(sse.Event{
				Type: sse.EventReviewSkipped,
				Data: sseData(map[string]any{
					"repo":      item.Repo,
					"pr_number": item.Number,
					"pr_title":  title,
					"reason":    string(pipeline.SkipReasonNotMonitored),
				}),
			})
			slog.Info("tier3: repo un-monitored while reserving the checkout, skipping PR",
				"repo", item.Repo, "pr", item.Number)
			return nil
		}

		rev := a.runReview(ctx, ghPR, aiCfg)
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

// loadExistingAPIToken is the recovery-only counterpart to
// loadOrCreateAPIToken. While an updater barrier is restored, rotating or
// creating the credential would strand the native owner that must verify and
// release that barrier. Read the already-established token without mutating
// the filesystem and fail closed if it is missing or invalid.
func loadExistingAPIToken(dir string) (string, error) {
	path := filepath.Join(dir, "api_token")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("api_token: read existing %s: %w", path, err)
	}
	token := strings.TrimSpace(string(data))
	if len(token) < 32 {
		return "", fmt.Errorf("api_token: existing token in %s is invalid", path)
	}
	return token, nil
}

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

func reviewErrorEventData(
	s *store.Store,
	prID int64,
	repo string,
	prNumber int,
	prTitle string,
	err error,
) map[string]any {
	data := map[string]any{
		"repo":      repo,
		"pr_number": prNumber,
		"pr_title":  prTitle,
		"error":     pipeline.UserFacingReviewError(err),
	}
	if errors.Is(err, executor.ErrExecutionCancelled) {
		data["reason"] = "manual_cancelled"
	}
	if prID == 0 && repo != "" && prNumber > 0 {
		if pr, lookupErr := s.GetPRByRepoNumber(repo, prNumber); lookupErr != nil {
			slog.Warn("review error event: PR lookup failed",
				"repo", repo, "pr_number", prNumber, "err", lookupErr)
		} else {
			prID = pr.ID
		}
	}
	if prID == 0 {
		return data
	}
	data["pr_id"] = prID
	status, statusErr := s.LatestReviewExecutionStatusForPR(prID)
	if statusErr != nil {
		slog.Warn("review error event: failure status lookup failed", "pr_id", prID, "err", statusErr)
		return data
	}
	if status != nil {
		data["attempts"] = status.Attempts
		data["failed_at"] = status.FailedAt
		data["retry_at"] = status.RetryAt
	}
	return data
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
// pipeline stage. The prefix names the stage (`pr-review`, `pr-tier2`,
// `pr-tier3`; merge tracking mints its own `merge-*` tokens) so operators can correlate
// `<clone>/.worktrees/<token>/` with the running execution.
func wtTokenFor(prefix string, n int) string {
	return fmt.Sprintf("%s-%d", prefix, n)
}

func ensureRepoContextFullHistory(ctx context.Context, manager *repoctx.Manager, h *repoctx.Handle, token, scope, repo string) {
	if manager == nil || h == nil {
		return
	}
	if err := manager.EnsureFullHistory(ctx, h, token); err != nil {
		slog.Warn(scope+": full git history unavailable; history-dependent steps may fall back",
			"repo", repo, "err", err)
	}
}

func logRepoContextFallback(scope, repo string, err error) {
	slog.Warn(scope+": repo context unavailable; continuing without local checkout",
		"repo", repo, "err", err)
}

// mergeTrackingConfigMap renders the whole [merge_tracking] section for
// GET /config, overrides included.
//
// Without this projection the app PATCHes the section successfully, the daemon
// honours it, and the settings screen still reads it back as defaults on the
// next load — the toggle silently resets itself in front of the operator.
func mergeTrackingConfigMap(c config.MergeTrackingConfig) map[string]any {
	orgs := make(map[string]any, len(c.Orgs))
	for org, o := range c.Orgs {
		orgs[org] = mergeTrackingOverrideMap(o)
	}
	repos := make(map[string]any, len(c.Repos))
	for repo, o := range c.Repos {
		repos[repo] = mergeTrackingOverrideMap(o)
	}
	return map[string]any{
		"enabled":              c.Enabled,
		"enable_auto_merge":    c.EnableAutoMerge,
		"update_branch":        c.UpdateBranch,
		"resolve_conflicts":    c.ResolveConflicts,
		"merge":                c.Merge,
		"merge_method":         c.MergeMethod,
		"include_assigned":     c.IncludeAssigned,
		"require_approval":     c.RequireApproval,
		"poll_interval":        c.PollInterval,
		"max_prs_per_tick":     c.MaxPRsPerTick,
		"max_update_attempts":  c.MaxUpdateAttempts,
		"max_resolve_attempts": c.MaxResolveAttempts,
		"max_merge_attempts":   c.MaxMergeAttempts,
		"action_cooldown":      c.ActionCooldown,
		"resolve_timeout":      c.ResolveTimeout,
		"resolve_effort":       c.ResolveEffort,
		"orgs":                 orgs,
		"repos":                repos,
	}
}

// clusterConfigMap renders the [cluster] section's own-identity fields for
// GET /config.
//
// It deliberately does NOT include Instances: InstanceConfig.Token can hold an
// inline secret, and this is the same generic settings-screen projection used
// for every other section — the registry already has its own token-redacting
// view (instanceView, in internal/server/cluster.go) for anything that needs
// to list instances. A future "just marshal the whole struct" refactor here
// would silently leak a token into the Config screen's response.
//
// Role and Routing.Mode are resolved to their effective defaults (the TOML
// zero value for both is "", meaning standalone/assignment) so the Flutter
// dropdown never receives a value outside its own item list.
func clusterConfigMap(c config.ClusterConfig) map[string]any {
	role := strings.ToLower(strings.TrimSpace(c.Role))
	if role == "" {
		role = config.RoleStandalone
	}
	routingMode := strings.ToLower(strings.TrimSpace(c.Routing.Mode))
	if routingMode == "" {
		routingMode = config.ModeAssignment
	}
	discovery := strings.ToLower(strings.TrimSpace(c.Discovery))
	if discovery == "" {
		discovery = config.DiscoveryOff
	}
	return map[string]any{
		"role":             role,
		"instance_id":      c.InstanceID,
		"instance_name":    c.InstanceName,
		"default_instance": c.DefaultInstance,
		"probe_interval":   c.ProbeInterval,
		"routing_mode":     routingMode,
		"discovery":        discovery,
		// Resolved rather than passed through, for the same reason role and
		// routing_mode are: the TOML zero value means "the default", and a 0
		// reaching the GUI would read as "take over immediately".
		"takeover_after_failed_probes": clusterTakeoverThreshold(&config.Config{Cluster: c}),
	}
}

// mergeTrackingOverrideMap renders one per-org / per-repo override for
// GET /config. Unset pointers are omitted so the client can tell "inherit" from
// "explicitly false".
func mergeTrackingOverrideMap(o config.MergeTrackingOverride) map[string]any {
	out := map[string]any{}
	if o.Enabled != nil {
		out["enabled"] = *o.Enabled
	}
	if o.EnableAutoMerge != nil {
		out["enable_auto_merge"] = *o.EnableAutoMerge
	}
	if o.UpdateBranch != nil {
		out["update_branch"] = *o.UpdateBranch
	}
	if o.ResolveConflicts != nil {
		out["resolve_conflicts"] = *o.ResolveConflicts
	}
	if o.Merge != nil {
		out["merge"] = *o.Merge
	}
	if o.MergeMethod != "" {
		out["merge_method"] = o.MergeMethod
	}
	if o.IncludeAssigned != nil {
		out["include_assigned"] = *o.IncludeAssigned
	}
	if o.RequireApproval != nil {
		out["require_approval"] = *o.RequireApproval
	}
	if o.MaxUpdateAttempts != nil {
		out["max_update_attempts"] = *o.MaxUpdateAttempts
	}
	if o.MaxResolveAttempts != nil {
		out["max_resolve_attempts"] = *o.MaxResolveAttempts
	}
	if o.MaxMergeAttempts != nil {
		out["max_merge_attempts"] = *o.MaxMergeAttempts
	}
	if o.ActionCooldown != "" {
		out["action_cooldown"] = o.ActionCooldown
	}
	if o.ResolveTimeout != "" {
		out["resolve_timeout"] = o.ResolveTimeout
	}
	if o.ResolveEffort != "" {
		out["resolve_effort"] = o.ResolveEffort
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
		CloneDir:                ai.CloneDir,
		NeverApproveWithIssues:  ai.NeverApproveWithIssues,
		NeverApproveMinSeverity: ai.NeverApproveMinSeverity,
	})
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
		CloneDir:                ai.CloneDir,
		NeverApproveWithIssues:  ai.NeverApproveWithIssues,
		NeverApproveMinSeverity: ai.NeverApproveMinSeverity,
	})
	return out
}

type aiOverrideFields struct {
	Prompt                  string
	CloneDir                string
	NeverApproveWithIssues  *bool
	NeverApproveMinSeverity string
}

func addCommonAIOverrideFields(out map[string]any, fields aiOverrideFields) {
	if fields.Prompt != "" {
		out["prompt"] = fields.Prompt
	}
	if fields.CloneDir != "" {
		out["clone_dir"] = fields.CloneDir
	}
	if fields.NeverApproveWithIssues != nil {
		out["never_approve_with_issues"] = *fields.NeverApproveWithIssues
	}
	if fields.NeverApproveMinSeverity != "" {
		out["never_approve_min_severity"] = fields.NeverApproveMinSeverity
	}
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

// runStartupWorktreePrune protects the filesystem-destructive startup sweep.
// A replacement daemon may start while the updater still owns a persistent
// drain lease; in that case it must leave every checkout untouched until the
// verified new application cancels the lease.
func runStartupWorktreePrune(
	ctx context.Context,
	gate *workgate.Gate,
	prune func(context.Context),
) error {
	ctx, releaseUpdateWork, err := acquireUpdateWork(ctx, gate, workgate.KindMaintenance)
	if err != nil {
		return err
	}
	defer releaseUpdateWork()
	prune(ctx)
	return nil
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

// enrollOpenItems enrolls up to 10 open PRs not yet in watch_state.
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

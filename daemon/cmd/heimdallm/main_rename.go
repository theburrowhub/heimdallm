package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/rename"
	"github.com/heimdallm/daemon/internal/repoctx"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/heimdallm/daemon/internal/store"
)

// minRenameProbeInterval is the lower bound enforced on operator
// input. Below 1 minute the probe would burn GitHub rate-limit
// budget (one GET per repo per tick) for no real detection gain —
// renames are by nature rare events. Misconfigurations get clamped
// up with an audit-log warn rather than silently honoured.
const minRenameProbeInterval = time.Minute

// parseRenameProbeInterval parses the operator-facing duration string
// for `ai.repo_rename_check_interval`. Empty / unparseable falls back
// to 1h; literal "0" returns 0 so the caller knows the probe is
// disabled. Returning <= 0 from here is the disable signal. Positive
// values below minRenameProbeInterval are clamped up.
func parseRenameProbeInterval(raw string) time.Duration {
	if raw == "0" {
		return 0
	}
	if raw == "" {
		return rename.DefaultProbeInterval
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		slog.Warn("rename probe: unparseable interval, falling back to default",
			"raw", raw, "default", rename.DefaultProbeInterval)
		return rename.DefaultProbeInterval
	}
	if d < minRenameProbeInterval {
		slog.Warn("rename probe: interval below floor, clamping",
			"raw", raw, "floor", minRenameProbeInterval)
		return minRenameProbeInterval
	}
	return d
}

// tomlPersister adapts config.RenameRepoInTOML to rename.Persister so
// the rename package does not depend on the config package directly
// (keeps the internal/rename surface small and testable with fakes).
type tomlPersister struct{}

func (tomlPersister) Rename(path, oldRepo, newRepo string) error {
	return config.RenameRepoInTOML(path, oldRepo, newRepo)
}

// renameRepoListFn returns the currently-configured monitored repo
// set (excluding non_monitored) under cfgMu so the probe observes
// runtime config reloads.
func renameRepoListFn(cfg *config.Config, cfgMu *sync.Mutex) func() []string {
	return func() []string {
		cfgMu.Lock()
		defer cfgMu.Unlock()
		// Probe operates on the explicitly-configured set only —
		// discovery-only repos are excluded because the probe writes
		// into config TOML on a rename, and rewriting an entry the
		// operator never explicitly added would surprise them. The
		// next discovery cycle re-publishes the canonical name
		// regardless, so coverage isn't lost.
		out := make([]string, 0, len(cfg.GitHub.Repositories))
		out = append(out, cfg.GitHub.Repositories...)
		return out
	}
}

// renameNonMonitoredListFn returns the operator-disabled repo set
// (#493). The probe scans this list separately from the monitored
// one and emits stale-slug warnings without dispatching the
// reconciler — non_monitored entries reflect explicit operator
// choice and must not be auto-rewritten.
func renameNonMonitoredListFn(cfg *config.Config, cfgMu *sync.Mutex) func() []string {
	return func() []string {
		cfgMu.Lock()
		defer cfgMu.Unlock()
		out := make([]string, 0, len(cfg.GitHub.NonMonitored))
		out = append(out, cfg.GitHub.NonMonitored...)
		return out
	}
}

// newRenameProbe constructs the Reconciler + Probe with their real
// daemon deps. It is invoked once per pollers cycle (re-construction
// is safe; the probe is goroutine-local), but called from a single
// goroutine in startPollers so there is no concurrent-construction
// concern.
func newRenameProbe(
	_ context.Context,
	cfg *config.Config,
	cfgMu *sync.Mutex,
	tomlMu *sync.Mutex,
	ghClient *gh.Client,
	s *store.Store,
	repoCtx *repoctx.Manager,
	broker *sse.Broker,
	cfgPath string,
	interval time.Duration,
) *rename.Probe {
	reconciler := newRenameReconciler(cfg, cfgMu, tomlMu, s, repoCtx, broker, cfgPath)
	return rename.NewProbe(rename.ProbeDeps{
		Probe:        ghClient,
		Dispatcher:   reconciler,
		Repos:        renameRepoListFn(cfg, cfgMu),
		NonMonitored: renameNonMonitoredListFn(cfg, cfgMu),
		Publisher:    broker,
		Interval:     interval,
	})
}

// newRenameReconciler is the constructor seam that the admin endpoint
// (step 8) and the probe share — both wire the same dependencies,
// including the shared tomlMu that serializes config-TOML writes with
// the HTTP PATCH handlers (#489 race fix).
//
// CloneDir comes from the global AI default rather than per-repo
// AIForRepo: the rename probe purges the OLD slug whose per-repo
// override is about to be removed, so reading the global default is
// the only path that stays valid across the rename. Operators with
// per-repo CloneDir overrides should rely on next-acquire reclone to
// repopulate under the new override.
func newRenameReconciler(
	cfg *config.Config,
	cfgMu *sync.Mutex,
	tomlMu *sync.Mutex,
	s *store.Store,
	repoCtx *repoctx.Manager,
	broker *sse.Broker,
	cfgPath string,
) *rename.Reconciler {
	cfgMu.Lock()
	cloneDir := cfg.AI.CloneDir
	cfgMu.Unlock()
	if cloneDir == "" {
		cloneDir = repoctx.DefaultCloneDir()
	}
	return rename.NewReconciler(rename.Deps{
		Store:     s,
		Persister: tomlPersister{},
		Purger:    repoCtx,
		Publisher: broker,
		CfgMu:     cfgMu,
		TOMLMu:    tomlMu,
		ApplyConfig: func(oldRepo, newRepo string) {
			cfg.ApplyRename(oldRepo, newRepo)
		},
		CfgPath:  cfgPath,
		CloneDir: cloneDir,
	})
}

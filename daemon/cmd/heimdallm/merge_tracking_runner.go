package main

import (
	"context"
	"sync"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/mergetrack"
	"github.com/heimdallm/daemon/internal/repoctx"
)

// mergeTrackRepoContexts adapts repoctx.Manager to mergetrack.RepoContexts.
//
// This is the only merge-tracking code that has to live in main: reserving a
// worktree needs both repoctx and the per-repo AI config (local_dir, clone_dir,
// local_dir_base), and neither belongs in the reconciler. Everything else lives
// in internal/mergetrack, where it is reachable from a test.
type mergeTrackRepoContexts struct {
	manager *repoctx.Manager
	token   string
	cfg     **config.Config
	cfgMu   *sync.Mutex
}

// AcquireWrite reserves an ephemeral, Heimdallm-managed worktree.
//
// ModeWrite is what keeps the operator's own checkout out of reach: repoctx
// refuses to hand back a local_dir_base mount for a write, so the agent can
// only ever rewrite a clone Heimdallm created.
func (m *mergeTrackRepoContexts) AcquireWrite(ctx context.Context, repo, worktreeToken string) (mergetrack.Worktree, error) {
	m.cfgMu.Lock()
	c := *m.cfg
	aiCfg := c.AIForRepo(repo)
	localDirBase := append([]string(nil), c.GitHub.LocalDirBase...)
	m.cfgMu.Unlock()

	handle, err := acquireRepoContext(ctx, m.manager, repo, &aiCfg, localDirBase, m.token,
		repoctx.ModeWrite, worktreeToken, "", "")
	if err != nil {
		return nil, err
	}
	// A rebase replays commits, so a shallow clone fails the moment it reaches
	// a commit older than the shallow boundary.
	ensureRepoContextFullHistory(ctx, m.manager, handle, m.token, "merge tracking", repo)
	return handle, nil
}

var _ mergetrack.RepoContexts = (*mergeTrackRepoContexts)(nil)

// mergeTrackAgentSpec resolves the per-repo agent configuration for a
// conflict-resolution run.
func mergeTrackAgentSpec(cfg **config.Config, cfgMu *sync.Mutex) func(repo string) mergetrack.AgentSpec {
	return func(repo string) mergetrack.AgentSpec {
		cfgMu.Lock()
		c := *cfg
		aiCfg := c.AIForRepo(repo)
		mt := c.MergeTrackingForRepo(repo)
		globalPrimary, globalFallback := c.AI.Primary, c.AI.Fallback
		cfgMu.Unlock()

		spec := mergetrack.AgentSpec{
			Primary:  firstNonEmptyString(aiCfg.Primary, globalPrimary),
			Fallback: firstNonEmptyString(aiCfg.Fallback, globalFallback),
			Effort:   mt.ResolveEffort,
		}
		if d, err := time.ParseDuration(mt.ResolveTimeout); err == nil && d > 0 {
			spec.Timeout = d
		}
		return spec
	}
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// mergeTrackInterval resolves the reconciler's cadence, falling back to the
// shared poll interval when [merge_tracking].poll_interval is unset.
func mergeTrackInterval(c *config.Config, fallback time.Duration) time.Duration {
	if raw := c.MergeTracking.PollInterval; raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
	}
	return fallback
}

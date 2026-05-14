package rename

import (
	"context"
	"errors"
	"log/slog"
	"time"

	gh "github.com/heimdallm/daemon/internal/github"
)

// DefaultProbeInterval is the cadence the rename probe uses when the
// operator did not configure `ai.repo_rename_check_interval`. Renames
// are rare events; an hour is the right tradeoff between detection
// latency and GitHub rate-limit consumption (one GET per repo per
// tick).
const DefaultProbeInterval = time.Hour

// CanonicalProbe wraps the single GitHub call the probe needs.
// *github.Client satisfies it via GetCanonicalFullName.
type CanonicalProbe interface {
	GetCanonicalFullName(repo string) (string, error)
}

// Dispatcher hides the heavy Reconciler behind a one-method interface
// so the probe can be tested without constructing every reconciler
// dep. *Reconciler satisfies it.
type Dispatcher interface {
	Run(ctx context.Context, oldRepo, newRepo string) error
}

// ProbeDeps configures the Probe. Repos is a function (not a slice)
// because the monitored repository set can change at runtime — the
// probe must observe the current list on every tick.
type ProbeDeps struct {
	Probe      CanonicalProbe
	Dispatcher Dispatcher
	Repos      func() []string

	// Interval defaults to DefaultProbeInterval when zero.
	Interval time.Duration
}

// Probe periodically asks GitHub for the canonical full_name of each
// monitored repo and dispatches the rename reconciler on a mismatch.
type Probe struct {
	deps ProbeDeps
}

// NewProbe constructs a Probe ready to Tick or Run.
func NewProbe(deps ProbeDeps) *Probe {
	if deps.Interval <= 0 {
		deps.Interval = DefaultProbeInterval
	}
	return &Probe{deps: deps}
}

// Tick performs one iteration: probe every repo currently in the
// monitored set, dispatch the reconciler for each mismatch. Errors
// from GitHub (404, transport) skip the repo for this tick without
// failing the whole iteration — the probe is best-effort and the
// next tick retries naturally.
func (p *Probe) Tick(ctx context.Context) {
	repos := p.deps.Repos()
	for _, repo := range repos {
		if ctx.Err() != nil {
			return
		}
		canonical, err := p.deps.Probe.GetCanonicalFullName(repo)
		if err != nil {
			var apiErr *gh.APIError
			if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
				slog.Info("rename probe: repo unreachable (404)", "repo", repo)
				continue
			}
			slog.Debug("rename probe: canonical lookup failed",
				"repo", repo, "err", err)
			continue
		}
		if canonical == "" || canonical == repo {
			continue
		}
		slog.Info("rename probe: detected rename",
			"old_repo", repo, "new_repo", canonical)
		if err := p.deps.Dispatcher.Run(ctx, repo, canonical); err != nil {
			slog.Error("rename probe: dispatcher failed",
				"old_repo", repo, "new_repo", canonical, "err", err)
		}
	}
}

// Run drives the probe at deps.Interval until ctx is done. Each Tick
// is synchronous — a single slow GET (canonical lookup of one repo)
// is bounded by the GitHub client timeout, so a stuck tick cannot
// stack up beyond one interval window.
func (p *Probe) Run(ctx context.Context) {
	ticker := time.NewTicker(p.deps.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.Tick(ctx)
		}
	}
}

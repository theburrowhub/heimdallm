package rename

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/sse"
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

// ProbeDeps configures the Probe. Repos / NonMonitored are functions
// (not slices) because both sets can change at runtime — the probe
// must observe the current lists on every tick.
type ProbeDeps struct {
	Probe      CanonicalProbe
	Dispatcher Dispatcher
	Repos      func() []string

	// NonMonitored is the operator's disabled-repo list. When set,
	// the probe also scans these slugs for renames on every tick,
	// but does NOT dispatch the reconciler — instead it emits a
	// best-effort EventRepoNonMonitoredStale via Publisher so the
	// operator can take manual action. Nil disables this axis
	// entirely (backward compat for callers that pre-date #493).
	NonMonitored func() []string

	// Publisher receives EventRepoNonMonitoredStale events. May be
	// nil when NonMonitored is nil or when callers do not care
	// about the stale-slug surface; the probe then only logs via
	// slog.Warn.
	Publisher Publisher

	// Interval defaults to DefaultProbeInterval when zero.
	Interval time.Duration
}

// Probe periodically asks GitHub for the canonical full_name of each
// monitored repo and dispatches the rename reconciler on a mismatch.
// Optionally also scans non-monitored entries for the stale-slug
// warning surface (#493).
type Probe struct {
	deps ProbeDeps

	// nmWarned remembers (old → last-warned-new) pairs across ticks
	// so the non-monitored scan emits at most one SSE per detected
	// drift, instead of one per probe interval. Reset on daemon
	// restart, which is the right trade-off: a restart is the
	// natural prompt to re-surface known stale entries.
	nmMu     sync.Mutex
	nmWarned map[string]string
}

// NewProbe constructs a Probe ready to Tick or Run.
func NewProbe(deps ProbeDeps) *Probe {
	if deps.Interval <= 0 {
		deps.Interval = DefaultProbeInterval
	}
	return &Probe{
		deps:     deps,
		nmWarned: make(map[string]string),
	}
}

// Tick performs one iteration: probes the monitored set and
// dispatches the reconciler for each mismatch, then optionally
// scans the non-monitored set and emits stale-slug warnings (no
// reconciler dispatch — operator-disabled entries are not
// auto-rewritten).
func (p *Probe) Tick(ctx context.Context) {
	p.tickMonitored(ctx)
	p.tickNonMonitored(ctx)
}

// tickMonitored is the original probe path: detect-and-dispatch.
func (p *Probe) tickMonitored(ctx context.Context) {
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

// tickNonMonitored scans the operator-disabled repo list (#493) and
// surfaces stale slugs without auto-renaming them. The non-monitored
// list reflects deliberate operator choice — auto-rewriting an entry
// the operator explicitly disabled would be a surprise, so we only
// observe and report. Per-(old,new) dedup across ticks keeps the
// signal at one event per detected drift per daemon lifetime.
func (p *Probe) tickNonMonitored(ctx context.Context) {
	if p.deps.NonMonitored == nil {
		return
	}
	current := p.deps.NonMonitored()

	// Garbage-collect warned entries that are no longer in the
	// current non-monitored set. If the operator removes and later
	// re-adds the same stale slug, the re-add must re-warn.
	currentSet := make(map[string]struct{}, len(current))
	for _, repo := range current {
		currentSet[repo] = struct{}{}
	}
	p.nmMu.Lock()
	for old := range p.nmWarned {
		if _, ok := currentSet[old]; !ok {
			delete(p.nmWarned, old)
		}
	}
	p.nmMu.Unlock()

	for _, repo := range current {
		if ctx.Err() != nil {
			return
		}
		canonical, err := p.deps.Probe.GetCanonicalFullName(repo)
		if err != nil {
			var apiErr *gh.APIError
			if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
				slog.Info("rename probe: non-monitored repo unreachable (404)",
					"repo", repo)
				continue
			}
			slog.Debug("rename probe: non-monitored canonical lookup failed",
				"repo", repo, "err", err)
			continue
		}
		if canonical == "" || canonical == repo {
			// Healthy: drop any stale warning state so a future
			// rename re-warns from a clean baseline.
			p.nmMu.Lock()
			delete(p.nmWarned, repo)
			p.nmMu.Unlock()
			continue
		}

		// Mismatch — dedup against the most-recently-warned
		// canonical for this slug.
		p.nmMu.Lock()
		if p.nmWarned[repo] == canonical {
			p.nmMu.Unlock()
			continue
		}
		p.nmWarned[repo] = canonical
		p.nmMu.Unlock()

		slog.Warn("rename probe: non-monitored repo has been renamed; entry is stale",
			"old_repo", repo, "new_repo", canonical)
		if p.deps.Publisher != nil {
			// json.Marshal of a map[string]string is infallible and
			// produces a deterministic key-sorted payload, which
			// matches the SSE consumer expectations on the Flutter
			// side. Preferred over fmt.Sprintf %q on the same shape
			// because the encoder owns the escaping rules.
			payload, _ := json.Marshal(map[string]string{
				"old_repo": repo,
				"new_repo": canonical,
			})
			p.deps.Publisher.Publish(sse.Event{
				Type: sse.EventRepoNonMonitoredStale,
				Data: string(payload),
			})
		}
	}
}

// Run drives the probe at deps.Interval until ctx is done. Each Tick
// is synchronous — a single slow GET (canonical lookup of one repo)
// is bounded by the GitHub client timeout, so a stuck tick cannot
// stack up beyond one interval window.
//
// Fires one Tick immediately on start so the daemon detects renames
// that happened while it was offline without waiting a full
// interval (default 1h) — operator-visible latency after a restart
// would otherwise be unbounded by the probe cadence alone.
func (p *Probe) Run(ctx context.Context) {
	p.Tick(ctx)
	if ctx.Err() != nil {
		return
	}
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

package pipeline

import (
	"sync"
	"sync/atomic"
	"time"
)

// gateVerdictKey identifies one evaluation of the re-review gate. The gate's
// inputs that can flip its outcome are the previous review, the HEAD under
// review and the PR's updated_at: a re-request, a dismissal or a push all
// move updated_at on GitHub, so an identical key means GitHub has nothing new
// to say about whether a re-review was asked for.
type gateVerdictKey struct {
	PrevReviewID int64
	HeadSHA      string
	UpdatedAt    time.Time
}

// maxGateVerdicts bounds the cache. One entry per PR is kept (the latest
// key), so this is a cap on watched PRs, far above any realistic estate;
// overflowing it just drops the cache and costs one extra lookup per PR.
const maxGateVerdicts = 4096

// gateVerdicts remembers the last no-re-review verdict per PR so repeated
// polls of an unchanged PR skip without re-walking the timeline, listing
// published reviews and re-fetching the PR (theburrowhub/heimdallm: "Skipped
// because no rereview request" every few minutes). In memory on purpose: a
// restart costs a single re-evaluation per PR.
type gateVerdicts struct {
	mu      sync.Mutex
	byPR    map[int64]gateVerdictKey
	reasons map[int64]SkipReason
	// lookupFailed counts fail-closed GitHub lookup errors inside the gate.
	// Run only caches a verdict when no lookup failed while it was decided;
	// a concurrent failure on another PR just costs one uncached verdict.
	lookupFailed atomic.Int64
}

func (g *gateVerdicts) lookup(prID int64, key gateVerdictKey) (SkipReason, bool) {
	if key.UpdatedAt.IsZero() {
		// Without updated_at a re-request is indistinguishable from a
		// repeat, so never short-circuit.
		return SkipReasonNone, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	got, ok := g.byPR[prID]
	if !ok || got.PrevReviewID != key.PrevReviewID || got.HeadSHA != key.HeadSHA || !got.UpdatedAt.Equal(key.UpdatedAt) {
		return SkipReasonNone, false
	}
	return g.reasons[prID], true
}

func (g *gateVerdicts) remember(prID int64, key gateVerdictKey, reason SkipReason) {
	if key.UpdatedAt.IsZero() {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.byPR == nil || len(g.byPR) >= maxGateVerdicts {
		g.byPR = make(map[int64]gateVerdictKey)
		g.reasons = make(map[int64]SkipReason)
	}
	g.byPR[prID] = key
	g.reasons[prID] = reason
}

func (g *gateVerdicts) forget(prID int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.byPR, prID)
	delete(g.reasons, prID)
}

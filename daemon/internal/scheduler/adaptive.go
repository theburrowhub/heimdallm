package scheduler

import (
	"sync"
	"time"
)

// repoState tracks the adaptive scheduling state for a single repo.
type repoState struct {
	interval time.Duration
	nextDue  time.Time
}

// AdaptiveScheduler tracks per-repo polling cadence and adjusts it based on
// observed activity. Repos with actionable work are polled at the minimum
// interval; idle repos are backed off exponentially toward the maximum.
//
// The zero value is not usable — always construct via NewAdaptiveScheduler.
//
// All methods are safe for concurrent use.
type AdaptiveScheduler struct {
	factor float64

	// min/max are mutable via SetBounds and are read by every scheduling
	// method, so they live under mu alongside state.
	mu    sync.Mutex
	min   time.Duration
	max   time.Duration
	state map[string]*repoState
}

// NewAdaptiveScheduler creates an AdaptiveScheduler with the given min/max
// bounds and a default growth factor of 2× (interval doubles each idle cycle).
// min must be positive; if max < min, max is clamped to min.
func NewAdaptiveScheduler(min, max time.Duration) *AdaptiveScheduler {
	if min <= 0 {
		min = time.Minute
	}
	if max < min {
		max = min
	}
	return &AdaptiveScheduler{
		min:    min,
		max:    max,
		factor: 2.0,
		state:  make(map[string]*repoState),
	}
}

// SetBounds updates the min/max interval bounds in place.
//
// The scheduler deliberately outlives config reloads so per-repo back-off state
// is not lost, which meant min_interval/max_interval changes never took effect
// — not even with a poller restart, since the scheduler is not recreated there
// — while GET /config and the UI already showed the new values. Updating the
// bounds in place fixes that divergence without discarding the accumulated
// state: existing per-repo intervals are clamped into the new range on their
// next MarkActive/MarkIdle.
//
// Mirrors NewAdaptiveScheduler's guards: a non-positive min is ignored, and max
// is clamped up to min.
func (a *AdaptiveScheduler) SetBounds(min, max time.Duration) {
	if min <= 0 {
		return
	}
	if max < min {
		max = min
	}
	a.mu.Lock()
	a.min, a.max = min, max
	a.mu.Unlock()
}

// Due returns the subset of keys whose next scheduled poll is at or before now.
// Keys that have never been seen are treated as immediately due and are
// initialised with the minimum interval; their nextDue is set to now+min so
// the NEXT call to Due respects the cadence.
func (a *AdaptiveScheduler) Due(now time.Time, keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	out := make([]string, 0, len(keys))
	for _, k := range keys {
		st, exists := a.state[k]
		if !exists {
			// New key: due immediately, schedule next poll at now+min.
			a.state[k] = &repoState{
				interval: a.min,
				nextDue:  now.Add(a.min),
			}
			out = append(out, k)
			continue
		}
		if !now.Before(st.nextDue) {
			out = append(out, k)
		}
	}
	return out
}

// MarkActive records that key had actionable work this cycle. The interval is
// reset to min and nextDue is set to now+min (the repo will be polled again
// quickly).
func (a *AdaptiveScheduler) MarkActive(key string, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()

	st := a.getOrCreate(key, now)
	st.interval = a.min
	st.nextDue = now.Add(a.min)
}

// MarkIdle records that key had no actionable work this cycle. The interval is
// grown by the growth factor (default 2×) and capped at max; nextDue is set to
// now+newInterval.
func (a *AdaptiveScheduler) MarkIdle(key string, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()

	st := a.getOrCreate(key, now)
	next := time.Duration(float64(st.interval) * a.factor)
	if next > a.max {
		next = a.max
	}
	if next < a.min {
		next = a.min
	}
	st.interval = next
	st.nextDue = now.Add(next)
}

// Forget removes key from the scheduler. Subsequent calls to Due will treat it
// as a new, immediately-due key. Use this to keep memory bounded when repos are
// removed from monitoring.
func (a *AdaptiveScheduler) Forget(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.state, key)
}

// PruneAbsent removes any key from the scheduler that is NOT in the provided
// set. This prevents unbounded memory growth when repos are removed from
// monitoring over time. O(len(state) + len(active)).
func (a *AdaptiveScheduler) PruneAbsent(active []string) {
	if len(active) == 0 {
		a.mu.Lock()
		a.state = make(map[string]*repoState)
		a.mu.Unlock()
		return
	}
	set := make(map[string]struct{}, len(active))
	for _, k := range active {
		set[k] = struct{}{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for k := range a.state {
		if _, ok := set[k]; !ok {
			delete(a.state, k)
		}
	}
}

// Interval returns the current scheduled interval for key, or min if unknown.
// Intended for observability / testing only.
func (a *AdaptiveScheduler) Interval(key string) time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	if st, ok := a.state[key]; ok {
		return st.interval
	}
	return a.min
}

// getOrCreate returns the state for key, creating a fresh entry at min interval
// if absent. Caller must hold a.mu.
func (a *AdaptiveScheduler) getOrCreate(key string, now time.Time) *repoState {
	if st, ok := a.state[key]; ok {
		return st
	}
	st := &repoState{
		interval: a.min,
		nextDue:  now.Add(a.min),
	}
	a.state[key] = st
	return st
}

package autonomous

import "sync"

// PhaseGuard enforces single-flight per pipeline phase: at most one task in
// triage, one in refinement, one in development, and one review-fix running
// at a time. Independent phases never block each other.
type PhaseGuard struct {
	mu   sync.Mutex
	busy map[string]bool
}

// NewPhaseGuard builds an empty guard.
func NewPhaseGuard() *PhaseGuard {
	return &PhaseGuard{busy: make(map[string]bool)}
}

// TryEnter attempts to claim a phase. It returns a release func and true on
// success; false (with a no-op release) when the phase is already busy. The
// returned release func is safe to call multiple times.
func (g *PhaseGuard) TryEnter(phase string) (release func(), ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.busy[phase] {
		return func() {}, false
	}
	g.busy[phase] = true
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			delete(g.busy, phase)
			g.mu.Unlock()
		})
	}, true
}

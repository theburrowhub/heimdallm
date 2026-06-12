package autonomous

import (
	"sync"
	"testing"
)

func TestPhaseGuard_OneAtATime(t *testing.T) {
	g := NewPhaseGuard()

	rel, ok := g.TryEnter("development")
	if !ok {
		t.Fatalf("first enter should succeed")
	}
	if _, ok := g.TryEnter("development"); ok {
		t.Errorf("second enter of busy phase must fail")
	}
	if _, ok := g.TryEnter("triage"); !ok {
		t.Errorf("independent phase should enter")
	}
	rel()
	if _, ok := g.TryEnter("development"); !ok {
		t.Errorf("after release, phase should be enterable again")
	}
}

func TestPhaseGuard_ReleaseIdempotent(t *testing.T) {
	g := NewPhaseGuard()
	rel, _ := g.TryEnter("triage")
	rel()
	rel() // second release must be a safe no-op
	if _, ok := g.TryEnter("triage"); !ok {
		t.Errorf("phase should be free after idempotent releases")
	}
}

func TestPhaseGuard_ConcurrentSafe(t *testing.T) {
	g := NewPhaseGuard()
	var wins int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rel, ok := g.TryEnter("triage"); ok {
				mu.Lock()
				wins++
				mu.Unlock()
				rel()
			}
		}()
	}
	wg.Wait()
	if wins == 0 {
		t.Errorf("expected at least one successful enter")
	}
}

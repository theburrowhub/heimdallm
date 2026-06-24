package scheduler_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/scheduler"
)

// ── basic construction ────────────────────────────────────────────────────

func TestAdaptive_NewKeyDueImmediately(t *testing.T) {
	now := time.Now()
	s := scheduler.NewAdaptiveScheduler(time.Minute, 15*time.Minute)

	due := s.Due(now, []string{"org/repo"})
	if len(due) != 1 || due[0] != "org/repo" {
		t.Fatalf("new key: want [org/repo], got %v", due)
	}
}

func TestAdaptive_NewKeyDueImmediately_MultipleKeys(t *testing.T) {
	now := time.Now()
	s := scheduler.NewAdaptiveScheduler(time.Minute, 15*time.Minute)
	repos := []string{"a/1", "b/2", "c/3"}

	due := s.Due(now, repos)
	if len(due) != len(repos) {
		t.Fatalf("want %d new keys due, got %d: %v", len(repos), len(due), due)
	}
}

// ── Due scheduling ─────────────────────────────────────────────────────────

func TestAdaptive_DueReturnsOnlyDueKeys(t *testing.T) {
	t0 := time.UnixMilli(0) // deterministic base
	s := scheduler.NewAdaptiveScheduler(time.Minute, 15*time.Minute)

	// First call: both keys are new → both are due.
	due := s.Due(t0, []string{"a/1", "b/2"})
	if len(due) != 2 {
		t.Fatalf("first call: want 2, got %d", len(due))
	}

	// Second call at the same instant: neither is due yet (nextDue = t0+1m).
	due = s.Due(t0, []string{"a/1", "b/2"})
	if len(due) != 0 {
		t.Fatalf("immediately after: want 0, got %d (%v)", len(due), due)
	}

	// Call at t0+1m: both are due again.
	due = s.Due(t0.Add(time.Minute), []string{"a/1", "b/2"})
	if len(due) != 2 {
		t.Fatalf("at t0+1m: want 2, got %d (%v)", len(due), due)
	}
}

func TestAdaptive_DueAtExactBoundary(t *testing.T) {
	t0 := time.UnixMilli(0)
	min := time.Minute
	s := scheduler.NewAdaptiveScheduler(min, 15*time.Minute)

	// Prime key.
	s.Due(t0, []string{"r/1"})
	// At exactly t0+min, the key should be due (nextDue = t0+min → !now.Before means >=).
	due := s.Due(t0.Add(min), []string{"r/1"})
	if len(due) != 1 {
		t.Fatalf("at boundary: want 1 due, got %d", len(due))
	}
}

// ── MarkActive ─────────────────────────────────────────────────────────────

func TestAdaptive_MarkActive_ResetsToMin(t *testing.T) {
	t0 := time.UnixMilli(0)
	min := time.Minute
	max := 15 * time.Minute
	s := scheduler.NewAdaptiveScheduler(min, max)

	// Prime key and let it go idle a few times (grows interval).
	s.Due(t0, []string{"r/1"})
	s.MarkIdle("r/1", t0)
	s.MarkIdle("r/1", t0)
	s.MarkIdle("r/1", t0)

	if interval := s.Interval("r/1"); interval == min {
		t.Log("MarkIdle didn't grow interval; test will still verify reset")
	}

	// Mark active: interval must reset to min.
	s.MarkActive("r/1", t0)
	if got := s.Interval("r/1"); got != min {
		t.Fatalf("after MarkActive: interval = %v, want %v", got, min)
	}
}

func TestAdaptive_MarkActive_NextDueIsNowPlusMin(t *testing.T) {
	t0 := time.UnixMilli(0)
	min := time.Minute
	s := scheduler.NewAdaptiveScheduler(min, 15*time.Minute)

	s.Due(t0, []string{"r/1"})
	// Grow the interval first.
	s.MarkIdle("r/1", t0)
	s.MarkIdle("r/1", t0)

	s.MarkActive("r/1", t0)

	// Just before t0+min: not due.
	due := s.Due(t0.Add(min-time.Millisecond), []string{"r/1"})
	if len(due) != 0 {
		t.Fatalf("before min: want 0 due, got %d", len(due))
	}
	// At t0+min: due.
	due = s.Due(t0.Add(min), []string{"r/1"})
	if len(due) != 1 {
		t.Fatalf("at t0+min: want 1 due, got %d", len(due))
	}
}

// ── MarkIdle ───────────────────────────────────────────────────────────────

func TestAdaptive_MarkIdle_GrowsInterval(t *testing.T) {
	t0 := time.UnixMilli(0)
	min := time.Minute
	s := scheduler.NewAdaptiveScheduler(min, 15*time.Minute)

	// Prime.
	s.Due(t0, []string{"r/1"})

	s.MarkIdle("r/1", t0)
	after1 := s.Interval("r/1")
	if after1 <= min {
		t.Fatalf("interval should grow after 1 idle: got %v, want > %v", after1, min)
	}

	s.MarkIdle("r/1", t0)
	after2 := s.Interval("r/1")
	if after2 <= after1 {
		t.Fatalf("interval should grow after 2nd idle: got %v, want > %v", after2, after1)
	}
}

func TestAdaptive_MarkIdle_CapsAtMax(t *testing.T) {
	t0 := time.UnixMilli(0)
	min := time.Minute
	max := 4 * time.Minute
	s := scheduler.NewAdaptiveScheduler(min, max)

	s.Due(t0, []string{"r/1"})

	// Drive idle many times — interval must never exceed max.
	for i := 0; i < 20; i++ {
		s.MarkIdle("r/1", t0)
	}
	if got := s.Interval("r/1"); got > max {
		t.Fatalf("interval capped: got %v, want <= %v", got, max)
	}
	if got := s.Interval("r/1"); got != max {
		t.Fatalf("interval at max after many idles: got %v, want %v", got, max)
	}
}

func TestAdaptive_MarkIdle_NextDueExcludesBeforeNewInterval(t *testing.T) {
	t0 := time.UnixMilli(0)
	min := time.Minute
	s := scheduler.NewAdaptiveScheduler(min, 15*time.Minute)

	s.Due(t0, []string{"r/1"})
	s.MarkIdle("r/1", t0) // interval grows to 2m

	got := s.Interval("r/1")
	// Should not be due before now+interval.
	due := s.Due(t0.Add(got-time.Millisecond), []string{"r/1"})
	if len(due) != 0 {
		t.Fatalf("before nextDue: want 0, got %d", len(due))
	}
	// Should be due at now+interval.
	due = s.Due(t0.Add(got), []string{"r/1"})
	if len(due) != 1 {
		t.Fatalf("at nextDue: want 1, got %d", len(due))
	}
}

// ── Forget ─────────────────────────────────────────────────────────────────

func TestAdaptive_Forget_ReturnsKeyToDueImmediately(t *testing.T) {
	t0 := time.UnixMilli(0)
	s := scheduler.NewAdaptiveScheduler(time.Minute, 15*time.Minute)

	// Prime and advance interval.
	s.Due(t0, []string{"r/1"})
	s.MarkIdle("r/1", t0)
	s.MarkIdle("r/1", t0)

	// Not due at t0 (nextDue is in the future).
	due := s.Due(t0, []string{"r/1"})
	if len(due) != 0 {
		t.Fatal("should not be due before forget")
	}

	// Forget: the key is now unknown → due immediately.
	s.Forget("r/1")
	due = s.Due(t0, []string{"r/1"})
	if len(due) != 1 {
		t.Fatalf("after Forget: want 1 due, got %d", len(due))
	}
}

// ── PruneAbsent ────────────────────────────────────────────────────────────

func TestAdaptive_PruneAbsent_RemovesGoneKeys(t *testing.T) {
	t0 := time.UnixMilli(0)
	s := scheduler.NewAdaptiveScheduler(time.Minute, 15*time.Minute)

	s.Due(t0, []string{"a/1", "b/2", "c/3"})
	s.PruneAbsent([]string{"a/1"}) // b/2 and c/3 removed

	// a/1 is still tracked (not due yet at t0+tiny).
	due := s.Due(t0.Add(time.Millisecond), []string{"a/1"})
	if len(due) != 0 {
		t.Fatal("a/1 should not be due immediately after prime+prune")
	}

	// b/2 and c/3 were pruned → new keys → due immediately.
	due = s.Due(t0, []string{"b/2", "c/3"})
	if len(due) != 2 {
		t.Fatalf("pruned keys: want 2 due, got %d", len(due))
	}
}

// ── concurrency ────────────────────────────────────────────────────────────

func TestAdaptive_Concurrent_NoRaceOrDeadlock(t *testing.T) {
	// This test is deliberately run with -race to detect data races.
	s := scheduler.NewAdaptiveScheduler(time.Millisecond, 10*time.Millisecond)
	repos := []string{"r/1", "r/2", "r/3", "r/4", "r/5"}

	const goroutines = 20
	var wg sync.WaitGroup
	var ops atomic.Int64

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			now := time.Now()
			switch id % 4 {
			case 0:
				s.Due(now, repos)
			case 1:
				s.MarkActive(repos[id%len(repos)], now)
			case 2:
				s.MarkIdle(repos[id%len(repos)], now)
			case 3:
				s.PruneAbsent(repos[:3])
			}
			ops.Add(1)
		}(i)
	}
	wg.Wait()
	if ops.Load() != goroutines {
		t.Fatalf("want %d ops, got %d", goroutines, ops.Load())
	}
}

// ── edge cases ─────────────────────────────────────────────────────────────

func TestAdaptive_MinEqualsMax_NeverGrows(t *testing.T) {
	t0 := time.UnixMilli(0)
	fixed := 5 * time.Minute
	s := scheduler.NewAdaptiveScheduler(fixed, fixed)

	s.Due(t0, []string{"r/1"})
	for i := 0; i < 10; i++ {
		s.MarkIdle("r/1", t0)
	}
	if got := s.Interval("r/1"); got != fixed {
		t.Fatalf("min==max: interval = %v, want %v", got, fixed)
	}
}

func TestAdaptive_EmptyKeyList_NoDue(t *testing.T) {
	s := scheduler.NewAdaptiveScheduler(time.Minute, 15*time.Minute)
	due := s.Due(time.Now(), nil)
	if due != nil {
		t.Fatalf("nil keys: want nil, got %v", due)
	}
	due = s.Due(time.Now(), []string{})
	if due != nil {
		t.Fatalf("empty keys: want nil, got %v", due)
	}
}

func TestAdaptive_MaxLessThanMin_ClampedToMin(t *testing.T) {
	// max < min should clamp max to min.
	s := scheduler.NewAdaptiveScheduler(5*time.Minute, time.Minute)
	t0 := time.UnixMilli(0)
	s.Due(t0, []string{"r/1"})
	for i := 0; i < 10; i++ {
		s.MarkIdle("r/1", t0)
	}
	got := s.Interval("r/1")
	if got < 5*time.Minute {
		t.Fatalf("clamped max: interval = %v, want >= 5m", got)
	}
}

// ── filterDueRepos helper ─────────────────────────────────────────────────

// TestFilterDueRepos_* tests the tier2 gating helper function exported for
// testability from cmd/heimdallm (or implemented inline here as a pure function
// for demonstration). The actual gate lives in runIssueTier in main.go and calls
// adaptiveSched.Due — this test verifies the expected filtering behaviour when
// integrated at the seam, using AdaptiveScheduler directly.
func TestAdaptive_IssueGating_OnlyDueReposProcessed(t *testing.T) {
	// Simulate the tier2 adaptive gating decision:
	// - 3 repos: a/1 and b/2 are new (due), c/3 was recently active (not yet due).
	t0 := time.UnixMilli(0)
	min := time.Minute
	s := scheduler.NewAdaptiveScheduler(min, 15*time.Minute)

	// c/3 already primed and marked active (nextDue = t0+min).
	s.Due(t0, []string{"c/3"})
	s.MarkActive("c/3", t0)

	// Now at t0: a/1 and b/2 are new, c/3 is not due yet.
	all := []string{"a/1", "b/2", "c/3"}
	due := s.Due(t0, all)

	// Expected: a/1 and b/2 (new → due), c/3 not due.
	if len(due) != 2 {
		t.Fatalf("want 2 due, got %d: %v", len(due), due)
	}
	dueSet := map[string]bool{}
	for _, k := range due {
		dueSet[k] = true
	}
	if dueSet["c/3"] {
		t.Fatal("c/3 should not be due (MarkActive just set nextDue = t0+min)")
	}

	// After MarkActive on a/1 and MarkIdle on b/2:
	s.MarkActive("a/1", t0)
	s.MarkIdle("b/2", t0)

	// At t0+min: a/1 is due (reset to min), b/2 interval grew to 2m so not due, c/3 due.
	now2 := t0.Add(min)
	due2 := s.Due(now2, all)
	due2Set := map[string]bool{}
	for _, k := range due2 {
		due2Set[k] = true
	}
	if !due2Set["a/1"] {
		t.Error("a/1: MarkActive → due at t0+min")
	}
	if due2Set["b/2"] {
		t.Error("b/2: MarkIdle 2x → interval=2m, should not be due at t0+min")
	}
	if !due2Set["c/3"] {
		t.Error("c/3: MarkActive at t0 → due at t0+min")
	}
}

// TestAdaptiveScheduler_UpdateBounds verifies that UpdateBounds changes the
// min/max applied to subsequent MarkActive/MarkIdle while preserving the
// existing per-repo state map (no reset).
func TestAdaptiveScheduler_UpdateBounds(t *testing.T) {
	s := scheduler.NewAdaptiveScheduler(time.Minute, 10*time.Minute)
	t0 := time.Unix(1_000_000, 0).UTC()

	// Seed a repo (becomes known) and grow it idle.
	s.Due(t0, []string{"r/1"})
	s.MarkIdle("r/1", t0)

	// Tighten bounds; existing state must survive (not pruned).
	s.UpdateBounds(5*time.Second, 20*time.Second)

	// MarkActive now resets to the NEW min (5s), not the old 1m.
	s.MarkActive("r/1", t0)
	if due := s.Due(t0.Add(6*time.Second), []string{"r/1"}); len(due) != 1 {
		t.Errorf("after UpdateBounds(min=5s)+MarkActive, repo should be due at t0+6s, got %v", due)
	}
	if due := s.Due(t0.Add(4*time.Second), []string{"r/1"}); len(due) != 0 {
		t.Errorf("repo should NOT be due at t0+4s (new min=5s), got %v", due)
	}
}

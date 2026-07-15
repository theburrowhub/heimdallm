package main

import (
	"sync"
	"testing"
)

// TestTriggerGuard_RejectsConcurrentSamePR verifies the in-process guard the
// manual "Re-review" trigger relies on: a second acquire for the same PR ID
// while the first is held must be rejected (no double-run), and a release must
// allow a subsequent acquire (a fresh click after the review completes).
func TestTriggerGuard_RejectsConcurrentSamePR(t *testing.T) {
	g := newTriggerGuard()

	if !g.tryAcquire(42) {
		t.Fatal("first acquire must succeed")
	}
	if g.tryAcquire(42) {
		t.Error("second concurrent acquire for the same PR must be rejected")
	}
	// A different PR is independent.
	if !g.tryAcquire(43) {
		t.Error("acquire for a different PR must succeed")
	}
	// Release PR 42; a fresh acquire must now succeed.
	g.release(42)
	if !g.tryAcquire(42) {
		t.Error("acquire after release must succeed")
	}
}

// TestTriggerGuard_ConcurrentAcquireExactlyOneWinner hammers a single PR ID
// from many goroutines and asserts exactly one acquire wins at a time — the
// property that stops two rapid Re-review clicks from both running when the
// SHA lookup fails and the persistent claim can't engage.
func TestTriggerGuard_ConcurrentAcquireExactlyOneWinner(t *testing.T) {
	g := newTriggerGuard()
	const goroutines = 50

	var wins int64
	var winsMu sync.Mutex
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if g.tryAcquire(7) {
				winsMu.Lock()
				wins++
				winsMu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()

	if wins != 1 {
		t.Errorf("exactly one goroutine must win the guard, got %d", wins)
	}
}

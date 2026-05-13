package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestProcessReposInParallel_AllReposProcessed asserts that the
// worker pool processes every repo exactly once and aggregates the
// returned counts.
func TestProcessReposInParallel_AllReposProcessed(t *testing.T) {
	repos := []string{"a/1", "a/2", "a/3", "b/1", "b/2"}
	var seen sync.Map
	total := processReposInParallel(context.Background(), repos, 2, func(_ context.Context, repo string) (int, error) {
		seen.Store(repo, true)
		return 1, nil
	})
	if total != len(repos) {
		t.Fatalf("total = %d, want %d", total, len(repos))
	}
	for _, r := range repos {
		if _, ok := seen.Load(r); !ok {
			t.Errorf("repo %q never processed", r)
		}
	}
}

// TestProcessReposInParallel_RespectsConcurrencyCap pins the cap: the
// number of in-flight workers never exceeds the configured value.
func TestProcessReposInParallel_RespectsConcurrencyCap(t *testing.T) {
	const cap = 2
	const repos = 8

	repoList := make([]string, repos)
	for i := range repoList {
		repoList[i] = "org/repo"
	}

	var inFlight, peak int64
	processReposInParallel(context.Background(), repoList, cap, func(_ context.Context, _ string) (int, error) {
		cur := atomic.AddInt64(&inFlight, 1)
		for {
			p := atomic.LoadInt64(&peak)
			if cur <= p || atomic.CompareAndSwapInt64(&peak, p, cur) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt64(&inFlight, -1)
		return 0, nil
	})

	if peak > cap {
		t.Fatalf("observed peak concurrency %d, cap was %d", peak, cap)
	}
	if peak < 2 {
		t.Fatalf("observed peak concurrency %d, want >= 2 (parallelism not happening)", peak)
	}
}

// Note: parallelism is already pinned by
// TestProcessReposInParallel_RespectsConcurrencyCap via the peak
// in-flight counter (peak must be >= 2). A wall-clock test would be
// strictly redundant and flaky on loaded CI runners.

// TestProcessReposInParallel_AggregatesErrors_ContinuesAfterFailure
// asserts a failing repo does not stop the others. Errors are
// logged via the caller-provided closure; the helper itself does
// not abort.
func TestProcessReposInParallel_ContinuesAfterFailure(t *testing.T) {
	repos := []string{"good/1", "bad/1", "good/2"}
	var processed int64
	total := processReposInParallel(context.Background(), repos, 2, func(_ context.Context, repo string) (int, error) {
		atomic.AddInt64(&processed, 1)
		if repo == "bad/1" {
			return 0, errors.New("boom")
		}
		return 1, nil
	})
	if processed != 3 {
		t.Fatalf("processed = %d, want 3 (failure must not abort siblings)", processed)
	}
	if total != 2 {
		t.Fatalf("total = %d, want 2 (failed repo contributes 0)", total)
	}
}

// TestProcessReposInParallel_RespectsContextCancellation asserts that
// in-flight workers see the cancel and the helper returns promptly.
func TestProcessReposInParallel_RespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	repos := []string{"r/1", "r/2", "r/3", "r/4"}
	start := time.Now()
	done := make(chan struct{})
	go func() {
		processReposInParallel(ctx, repos, 4, func(ctx context.Context, _ string) (int, error) {
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			case <-time.After(time.Second):
				return 0, nil
			}
		})
		close(done)
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("helper did not return within 500ms after cancel (elapsed %v)", time.Since(start))
	}
}

// TestProcessReposInParallel_CancelStopsScheduling pins the fix for
// the bug spotted in PR review: with cap < len(repos), the old code
// only `break`ed the select (not the for loop) and then tried to
// `sem <- struct{}{}` without observing ctx.Done — the helper could
// keep queuing repos and block waiting for slots that would never
// arrive promptly when workers themselves were stuck.
func TestProcessReposInParallel_CancelStopsScheduling(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	// 20 repos, cap=2 — the scheduling loop is forced to gate on
	// the semaphore between dispatches. A worker that ignores ctx
	// would mean every queued repo eventually runs.
	repos := make([]string, 20)
	for i := range repos {
		repos[i] = "org/r"
	}

	var processed int64
	// Cancel after the first two workers start.
	startedTwo := make(chan struct{}, 2)
	done := make(chan struct{})

	go func() {
		processReposInParallel(ctx, repos, 2, func(ctx context.Context, _ string) (int, error) {
			select {
			case startedTwo <- struct{}{}:
			default:
			}
			atomic.AddInt64(&processed, 1)
			// Worker honours ctx — returns promptly when cancelled.
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			case <-time.After(80 * time.Millisecond):
				return 0, nil
			}
		})
		close(done)
	}()

	// Wait for the first two workers to start, then cancel.
	<-startedTwo
	<-startedTwo
	cancel()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("processReposInParallel did not return within 500ms after cancel — scheduling loop leaked")
	}

	// At most a handful of workers should have run — definitely
	// not all 20. The exact bound depends on timing, but a healthy
	// fix means most repos never get dispatched.
	if got := atomic.LoadInt64(&processed); got >= int64(len(repos)) {
		t.Fatalf("processed %d/%d repos after cancel — scheduling loop did not honour ctx", got, len(repos))
	}
}

// TestProcessReposInParallel_NilOrEmptyReposNoOp asserts the helper
// is a no-op for empty input and never spawns workers.
func TestProcessReposInParallel_NilOrEmptyReposNoOp(t *testing.T) {
	if n := processReposInParallel(context.Background(), nil, 4, func(_ context.Context, _ string) (int, error) {
		t.Fatal("workFn should not be invoked for nil repos")
		return 0, nil
	}); n != 0 {
		t.Errorf("nil repos returned %d, want 0", n)
	}
	if n := processReposInParallel(context.Background(), []string{}, 4, func(_ context.Context, _ string) (int, error) {
		t.Fatal("workFn should not be invoked for empty repos")
		return 0, nil
	}); n != 0 {
		t.Errorf("empty repos returned %d, want 0", n)
	}
}

// TestProcessReposInParallel_NonPositiveCapFallsBack asserts cap<=0
// falls back to a sensible default so a misconfiguration cannot
// deadlock the daemon.
func TestProcessReposInParallel_NonPositiveCapFallsBack(t *testing.T) {
	repos := []string{"r/1", "r/2"}
	for _, cap := range []int{0, -1, -100} {
		var processed int64
		n := processReposInParallel(context.Background(), repos, cap, func(_ context.Context, _ string) (int, error) {
			atomic.AddInt64(&processed, 1)
			return 1, nil
		})
		if n != 2 || processed != 2 {
			t.Errorf("cap=%d: n=%d processed=%d, want both 2", cap, n, processed)
		}
	}
}

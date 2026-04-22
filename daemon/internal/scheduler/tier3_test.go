package scheduler_test

import (
	"context"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/scheduler"
)

// fakeChecker is a Tier3ItemChecker double for TestRunTier3_*.
type fakeChecker struct {
	changed     bool
	snap        *scheduler.ItemSnapshot
	err         error
	handledSnap *scheduler.ItemSnapshot
}

func (f *fakeChecker) CheckItem(ctx context.Context, item *scheduler.WatchItem) (bool, *scheduler.ItemSnapshot, error) {
	return f.changed, f.snap, f.err
}

func (f *fakeChecker) HandleChange(ctx context.Context, item *scheduler.WatchItem, snap *scheduler.ItemSnapshot) error {
	f.handledSnap = snap
	return nil
}

// TestRunTier3_PassesSnapshotToHandleChange verifies that when CheckItem reports
// a change with a non-nil snapshot, RunTier3 threads that exact snapshot into
// HandleChange. This closes the stale-state hole that caused closed/merged PRs
// to be re-reviewed at Tier 3.
func TestRunTier3_PassesSnapshotToHandleChange(t *testing.T) {
	q := scheduler.NewWatchQueue()
	// Set NextCheck in the past so the item is immediately ready for the first tick.
	q.Push(&scheduler.WatchItem{
		Type:      "pr",
		Repo:      "org/r",
		Number:    1,
		GithubID:  1,
		NextCheck: time.Now().Add(-time.Second),
	})

	want := &scheduler.ItemSnapshot{State: "open", Draft: true, Author: "alice"}
	checker := &fakeChecker{changed: true, snap: want}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	deps := scheduler.Tier3Deps{
		Limiter:  scheduler.NewRateLimiter(100),
		Queue:    q,
		Checker:  checker,
		Interval: 10 * time.Millisecond,
	}
	go scheduler.RunTier3(ctx, deps)
	time.Sleep(150 * time.Millisecond)

	if checker.handledSnap == nil {
		t.Fatalf("HandleChange never received a snapshot")
	}
	if checker.handledSnap.Author != want.Author || checker.handledSnap.State != want.State || !checker.handledSnap.Draft {
		t.Errorf("snap not threaded: got %+v, want %+v", checker.handledSnap, want)
	}
}

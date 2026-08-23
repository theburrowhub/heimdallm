package worker_test

import (
	"context"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/bus"
	"github.com/heimdallm/daemon/internal/worker"
)

func waitForWatchEntry(t *testing.T, watch *bus.WatchStore, key string, predicate func(*bus.WatchEntry) bool) *bus.WatchEntry {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		entry, err := watch.Get(context.Background(), key)
		if err == nil && predicate(entry) {
			return entry
		}
		time.Sleep(10 * time.Millisecond)
	}
	entry, err := watch.Get(context.Background(), key)
	t.Fatalf("watch %s never reached expected state: entry=%+v err=%v", key, entry, err)
	return nil
}

func TestStateWorkerRecoversHandlerPanicAndIncreasesBackoff(t *testing.T) {
	b := newTestBus(t)
	watch := newTestWatch(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := watch.Enroll(ctx, "pr", "acme/widgets", 17, 7001); err != nil {
		t.Fatalf("enroll: %v", err)
	}

	called := make(chan struct{}, 1)
	w := worker.NewStateWorker(b.Conn(), 0, watch, func(context.Context, bus.StateCheckMsg) (bool, error) {
		called <- struct{}{}
		panic("synthetic handler panic")
	})
	started := make(chan error, 1)
	go func() { started <- w.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-started; err != nil {
			t.Errorf("state worker exit: %v", err)
		}
	})
	time.Sleep(50 * time.Millisecond)

	data, err := bus.Encode(bus.StateCheckMsg{
		Type: "pr", Repo: "acme/widgets", Number: 17, GithubID: 7001,
	})
	if err != nil {
		t.Fatalf("encode state check: %v", err)
	}
	if err := b.Conn().Publish(bus.SubjStateCheck, data); err != nil {
		t.Fatalf("publish state check: %v", err)
	}
	if err := b.Conn().Flush(); err != nil {
		t.Fatalf("flush state check: %v", err)
	}
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("panic handler was not called")
	}
	waitForWatchEntry(t, watch, "pr.7001", func(entry *bus.WatchEntry) bool {
		return entry.Backoff() > bus.InitialBackoff
	})
}

func TestStateWorkerResetsBackoffAfterChangeAndSurvivesMalformedMessage(t *testing.T) {
	b := newTestBus(t)
	watch := newTestWatch(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	oldSeen := time.Now().Add(-time.Hour).UTC()
	if err := watch.ForceUpdate(ctx, &bus.WatchEntry{
		Type:      "issue",
		Repo:      "acme/widgets",
		Number:    23,
		GithubID:  8002,
		NextCheck: time.Now(),
		BackoffNs: int64(8 * bus.InitialBackoff),
		LastSeen:  oldSeen,
	}); err != nil {
		t.Fatalf("seed watch: %v", err)
	}

	called := make(chan struct{}, 1)
	w := worker.NewStateWorker(b.Conn(), 1, watch, func(context.Context, bus.StateCheckMsg) (bool, error) {
		called <- struct{}{}
		return true, nil
	})
	started := make(chan error, 1)
	go func() { started <- w.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-started; err != nil {
			t.Errorf("state worker exit: %v", err)
		}
	})
	time.Sleep(50 * time.Millisecond)

	if err := b.Conn().Publish(bus.SubjStateCheck, []byte("not-json")); err != nil {
		t.Fatalf("publish malformed state check: %v", err)
	}
	data, err := bus.Encode(bus.StateCheckMsg{
		Type: "issue", Repo: "acme/widgets", Number: 23, GithubID: 8002,
	})
	if err != nil {
		t.Fatalf("encode state check: %v", err)
	}
	if err := b.Conn().Publish(bus.SubjStateCheck, data); err != nil {
		t.Fatalf("publish valid state check: %v", err)
	}
	if err := b.Conn().Flush(); err != nil {
		t.Fatalf("flush state checks: %v", err)
	}
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("valid message after malformed input was not handled")
	}
	entry := waitForWatchEntry(t, watch, "issue.8002", func(entry *bus.WatchEntry) bool {
		return entry.Backoff() == bus.InitialBackoff && entry.LastSeen.After(oldSeen)
	})
	if err := ctx.Err(); err != nil {
		t.Fatalf("worker context ended unexpectedly after malformed message: %v", err)
	}
	if entry.Backoff() != bus.InitialBackoff {
		t.Fatalf("backoff = %s, want %s", entry.Backoff(), bus.InitialBackoff)
	}
}

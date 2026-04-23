// daemon/internal/bus/watch_test.go
package bus_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/bus"
	"github.com/nats-io/nats.go/jetstream"
)

func TestWatchKV_EnrollAndGet(t *testing.T) {
	b := newTestBus(t)
	w := b.WatchKV()
	ctx := context.Background()

	err := w.Enroll(ctx, "pr", "owner/repo", 42, 999)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	entry, err := w.Get(ctx, "pr.999")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if entry.Type != "pr" {
		t.Errorf("Type = %q, want %q", entry.Type, "pr")
	}
	if entry.Repo != "owner/repo" {
		t.Errorf("Repo = %q, want %q", entry.Repo, "owner/repo")
	}
	if entry.Number != 42 {
		t.Errorf("Number = %d, want 42", entry.Number)
	}
	if entry.GithubID != 999 {
		t.Errorf("GithubID = %d, want 999", entry.GithubID)
	}
	if entry.Backoff() != bus.InitialBackoff {
		t.Errorf("Backoff = %v, want %v", entry.Backoff(), bus.InitialBackoff)
	}
	if entry.Key() != "pr.999" {
		t.Errorf("Key = %q, want %q", entry.Key(), "pr.999")
	}
}

func TestWatchKV_ResetBackoff(t *testing.T) {
	b := newTestBus(t)
	w := b.WatchKV()
	ctx := context.Background()

	if err := w.Enroll(ctx, "pr", "owner/repo", 1, 100); err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	// Increase backoff a few times.
	for range 3 {
		if err := w.IncreaseBackoff(ctx, "pr.100"); err != nil {
			t.Fatalf("IncreaseBackoff: %v", err)
		}
	}

	// Verify backoff is larger than initial.
	entry, err := w.Get(ctx, "pr.100")
	if err != nil {
		t.Fatalf("Get after increase: %v", err)
	}
	if entry.Backoff() <= bus.InitialBackoff {
		t.Fatalf("expected increased backoff, got %v", entry.Backoff())
	}

	// Reset and verify.
	now := time.Now()
	if err := w.ResetBackoff(ctx, "pr.100", now); err != nil {
		t.Fatalf("ResetBackoff: %v", err)
	}
	entry, err = w.Get(ctx, "pr.100")
	if err != nil {
		t.Fatalf("Get after reset: %v", err)
	}
	if entry.Backoff() != bus.InitialBackoff {
		t.Errorf("Backoff after reset = %v, want %v", entry.Backoff(), bus.InitialBackoff)
	}
	if entry.LastSeen.Before(now.Add(-time.Second)) {
		t.Errorf("LastSeen not updated: %v", entry.LastSeen)
	}
}

func TestWatchKV_IncreaseBackoff_CapsAtMax(t *testing.T) {
	b := newTestBus(t)
	w := b.WatchKV()
	ctx := context.Background()

	if err := w.Enroll(ctx, "issue", "org/repo", 7, 200); err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	for range 20 {
		if err := w.IncreaseBackoff(ctx, "issue.200"); err != nil {
			t.Fatalf("IncreaseBackoff: %v", err)
		}
	}

	entry, err := w.Get(ctx, "issue.200")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if entry.Backoff() > bus.MaxBackoff {
		t.Errorf("Backoff = %v, exceeds MaxBackoff %v", entry.Backoff(), bus.MaxBackoff)
	}
	if entry.Backoff() != bus.MaxBackoff {
		t.Errorf("Backoff = %v, want MaxBackoff %v", entry.Backoff(), bus.MaxBackoff)
	}
}

func TestWatchKV_ScanReady(t *testing.T) {
	b := newTestBus(t)
	w := b.WatchKV()
	ctx := context.Background()

	// Enroll an item — its NextCheck is in the future, so not ready.
	if err := w.Enroll(ctx, "pr", "owner/repo", 10, 300); err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	ready, err := w.ScanReady(ctx)
	if err != nil {
		t.Fatalf("ScanReady: %v", err)
	}
	if len(ready) != 0 {
		t.Fatalf("expected 0 ready entries, got %d", len(ready))
	}

	// Force the entry to be ready (NextCheck in the past).
	entry, err := w.Get(ctx, "pr.300")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	entry.NextCheck = time.Now().Add(-5 * time.Minute)
	if err := w.ForceUpdate(ctx, entry); err != nil {
		t.Fatalf("ForceUpdate: %v", err)
	}

	ready, err = w.ScanReady(ctx)
	if err != nil {
		t.Fatalf("ScanReady after update: %v", err)
	}
	if len(ready) != 1 {
		t.Fatalf("expected 1 ready entry, got %d", len(ready))
	}
	if ready[0].GithubID != 300 {
		t.Errorf("ready entry GithubID = %d, want 300", ready[0].GithubID)
	}
}

func TestWatchKV_EvictStale(t *testing.T) {
	b := newTestBus(t)
	w := b.WatchKV()
	ctx := context.Background()

	if err := w.Enroll(ctx, "pr", "owner/repo", 20, 400); err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	// Force LastSeen to be old enough for eviction.
	entry, err := w.Get(ctx, "pr.400")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	entry.LastSeen = time.Now().Add(-2 * bus.EvictAfter)
	if err := w.ForceUpdate(ctx, entry); err != nil {
		t.Fatalf("ForceUpdate: %v", err)
	}

	evicted, err := w.EvictStale(ctx)
	if err != nil {
		t.Fatalf("EvictStale: %v", err)
	}
	if evicted != 1 {
		t.Errorf("evicted = %d, want 1", evicted)
	}

	// Verify the entry is gone.
	_, err = w.Get(ctx, "pr.400")
	if err == nil {
		t.Fatal("expected error after eviction, got nil")
	}
	if !errors.Is(err, jetstream.ErrKeyNotFound) {
		t.Errorf("expected ErrKeyNotFound, got %v", err)
	}
}

func TestWatchKV_Delete(t *testing.T) {
	b := newTestBus(t)
	w := b.WatchKV()
	ctx := context.Background()

	if err := w.Enroll(ctx, "issue", "org/repo", 5, 500); err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	// Verify it exists.
	if _, err := w.Get(ctx, "issue.500"); err != nil {
		t.Fatalf("Get before delete: %v", err)
	}

	if err := w.Delete(ctx, "issue.500"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Verify it's gone.
	_, err := w.Get(ctx, "issue.500")
	if err == nil {
		t.Fatal("expected error after Delete, got nil")
	}
	if !errors.Is(err, jetstream.ErrKeyNotFound) {
		t.Errorf("expected ErrKeyNotFound, got %v", err)
	}
}

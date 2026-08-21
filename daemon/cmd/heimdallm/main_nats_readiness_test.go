package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/bus"
)

func TestBridgeDiscoveryReadyBeforeInitialPublish(t *testing.T) {
	conn := newInProcessNATS(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	out := make(chan []string, 1)
	ready := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		bridgeDiscovery(ctx, conn, out, ready)
	}()

	if err := <-ready; err != nil {
		t.Fatalf("bridge readiness: %v", err)
	}
	pub := bus.NewRepoPublisher(conn)
	if err := pub.PublishRepos(ctx, []string{"org/repo"}); err != nil {
		t.Fatalf("publish initial repos: %v", err)
	}
	if err := conn.Flush(); err != nil {
		t.Fatalf("flush publish: %v", err)
	}

	select {
	case got := <-out:
		if len(got) != 1 || got[0] != "org/repo" {
			t.Fatalf("forwarded repos = %v, want [org/repo]", got)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("initial snapshot was lost after bridge reported ready")
	}
	cancel()
	<-done
}

func TestWaitForWorkerReadinessCollectsEveryWorker(t *testing.T) {
	ready := make(chan error, 2)
	ready <- nil
	ready <- nil
	if err := waitForWorkerReadiness(context.Background(), ready, 2, time.Second); err != nil {
		t.Fatalf("waitForWorkerReadiness: %v", err)
	}
}

func TestWaitForWorkerReadinessFailures(t *testing.T) {
	t.Run("default timeout with no workers", func(t *testing.T) {
		if err := waitForWorkerReadiness(context.Background(), make(chan error), 0, 0); err != nil {
			t.Fatalf("waitForWorkerReadiness() = %v, want nil", err)
		}
	})

	t.Run("worker error", func(t *testing.T) {
		ready := make(chan error, 1)
		ready <- errors.New("subscribe failed")
		if err := waitForWorkerReadiness(context.Background(), ready, 1, time.Second); err == nil || !strings.Contains(err.Error(), "subscribe failed") {
			t.Fatalf("waitForWorkerReadiness() = %v, want wrapped worker error", err)
		}
	})

	t.Run("closed channel", func(t *testing.T) {
		ready := make(chan error)
		close(ready)
		if err := waitForWorkerReadiness(context.Background(), ready, 1, time.Second); err == nil || !strings.Contains(err.Error(), "channel closed") {
			t.Fatalf("waitForWorkerReadiness() = %v, want closed-channel error", err)
		}
	})

	t.Run("context cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := waitForWorkerReadiness(ctx, make(chan error), 1, time.Second); !errors.Is(err, context.Canceled) {
			t.Fatalf("waitForWorkerReadiness() = %v, want context.Canceled", err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		if err := waitForWorkerReadiness(context.Background(), make(chan error), 1, time.Millisecond); err == nil || !strings.Contains(err.Error(), "timed out") {
			t.Fatalf("waitForWorkerReadiness() = %v, want timeout error", err)
		}
	})
}

func TestBridgeDiscoveryReportsSubscribeFailure(t *testing.T) {
	conn := newInProcessNATS(t)
	conn.Close()
	ready := make(chan error, 1)

	bridgeDiscovery(context.Background(), conn, make(chan []string), ready)

	if err := <-ready; err == nil {
		t.Fatal("bridge readiness = nil, want subscription error")
	}
}

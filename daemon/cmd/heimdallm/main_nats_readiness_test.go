package main

import (
	"context"
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

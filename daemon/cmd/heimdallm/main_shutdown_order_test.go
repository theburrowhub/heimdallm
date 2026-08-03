package main

import (
	"context"
	"testing"
)

// TestStopProducersThenAgentsCancelsProducersFirst pins the shutdown ordering.
// srv.Shutdown only stops HTTP traffic: the workers and tickers run on their own
// contexts, whose cancels are deferred to main's return. If the agent sweep ran
// while they were still live, a worker could start an execution between the
// sweep's snapshot and process exit — and because each execution now has its own
// process group, that CLI no longer receives a group-directed signal and would
// survive the restart spending provider quota (#614's symptom in miniature).
// Producers must therefore stop before the sweep, not after.
func TestStopProducersThenAgentsCancelsProducersFirst(t *testing.T) {
	var order []string

	producer := func(name string) context.CancelFunc {
		return func() { order = append(order, name) }
	}
	stopProducersThenAgents(
		[]context.CancelFunc{producer("worker"), producer("triage"), producer("state")},
		func() { order = append(order, "sweep") },
		0,
	)

	// Two sweeps: cancelling a context does not wait for the worker to notice.
	// One that is already past its context check and about to call cmd.Start()
	// would start an execution the first sweep has snapshotted past, so a second
	// pass after a settle delay is what catches it.
	want := []string{"worker", "triage", "state", "sweep", "sweep"}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}

// TestStopProducersThenAgentsToleratesNilCancels keeps the call site safe if a
// producer was never started (its cancel is nil).
func TestStopProducersThenAgentsToleratesNilCancels(t *testing.T) {
	swept := false
	sweeps := 0
	stopProducersThenAgents([]context.CancelFunc{nil}, func() { sweeps++; swept = true }, 0)
	if !swept {
		t.Error("sweep did not run when a producer cancel was nil")
	}
	if sweeps != 2 {
		t.Errorf("sweeps = %d, want 2", sweeps)
	}
}

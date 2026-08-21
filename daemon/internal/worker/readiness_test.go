package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/heimdallm/daemon/internal/bus"
)

func closedTestConn(t *testing.T) *bus.Bus {
	t.Helper()
	b := bus.New(bus.Config{MaxConcurrentWorkers: 1})
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("start test bus: %v", err)
	}
	t.Cleanup(b.Stop)
	b.Conn().Close()
	return b
}

func TestReportSubscriptionReadyWithoutListenerReturnsOriginalError(t *testing.T) {
	want := errors.New("subscribe failed")
	if got := reportSubscriptionReady(nil, nil, want); !errors.Is(got, want) {
		t.Fatalf("reportSubscriptionReady() = %v, want %v", got, want)
	}
}

func TestReportSubscriptionReadyReportsFlushFailure(t *testing.T) {
	b := closedTestConn(t)
	ready := make(chan error, 1)

	got := reportSubscriptionReady(b.Conn(), []chan<- error{ready}, nil)
	if got == nil {
		t.Fatal("reportSubscriptionReady() = nil, want closed-connection error")
	}
	if reported := <-ready; !errors.Is(reported, got) {
		t.Fatalf("reported error = %v, return error = %v", reported, got)
	}
}

func TestWorkersReportSubscriptionFailure(t *testing.T) {
	b := closedTestConn(t)
	conn := b.Conn()
	ctx := context.Background()

	tests := []struct {
		name  string
		start func(context.Context, ...chan<- error) error
	}{
		{
			name:  "review",
			start: NewReviewWorker(conn, 1, func(context.Context, bus.PRReviewMsg) {}).Start,
		},
		{
			name: "publish",
			start: NewPublishWorker(conn, 1, func(context.Context, bus.PRPublishMsg) error {
				return nil
			}).Start,
		},
		{
			name:  "triage",
			start: NewTriageWorker(conn, 1, func(context.Context, bus.IssueMsg) {}).Start,
		},
		{
			name:  "refinement",
			start: NewRefinementWorker(conn, 1, func(context.Context, bus.IssueMsg) {}).Start,
		},
		{
			name:  "implement",
			start: NewImplementWorker(conn, 1, func(context.Context, bus.IssueMsg) {}).Start,
		},
		{
			name: "state",
			start: NewStateWorker(conn, 1, nil, func(context.Context, bus.StateCheckMsg) (bool, error) {
				return false, nil
			}).Start,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ready := make(chan error, 1)
			got := tt.start(ctx, ready)
			if got == nil {
				t.Fatal("Start() = nil, want subscription error")
			}
			if reported := <-ready; !errors.Is(reported, got) {
				t.Fatalf("reported error = %v, return error = %v", reported, got)
			}
		})
	}
}

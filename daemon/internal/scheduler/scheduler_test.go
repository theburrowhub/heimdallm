package scheduler_test

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/scheduler"
)

func TestScheduler_Ticks(t *testing.T) {
	var count atomic.Int32
	s := scheduler.New(100*time.Millisecond, func() {
		count.Add(1)
	})
	s.Start()
	time.Sleep(350 * time.Millisecond)
	s.Stop()

	n := count.Load()
	if n < 2 || n > 5 {
		t.Errorf("expected 2-5 ticks in 350ms at 100ms interval, got %d", n)
	}
}

func TestScheduler_StopsCleanly(t *testing.T) {
	s := scheduler.New(10*time.Millisecond, func() {})
	s.Start()
	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Error("scheduler did not stop within 1s")
	}
}

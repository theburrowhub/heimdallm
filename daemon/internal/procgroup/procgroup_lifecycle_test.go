package procgroup

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

// TestClosedGroupNeverSignalsItsReleasedPGID proves the safety property that a
// return value alone cannot demonstrate: after sentinel reap releases the
// numeric PGID, late shutdown/cancellation calls do not enter the kernel at
// all. This keeps a recycled group belonging to another process out of reach.
func TestClosedGroupNeverSignalsItsReleasedPGID(t *testing.T) {
	original := sendGroupSignal
	calls := 0
	sendGroupSignal = func(int, syscall.Signal) error {
		calls++
		return nil
	}
	defer func() { sendGroupSignal = original }()

	group := &ownedGroup{pgid: 12345, closed: true}
	if err := group.terminate(); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("late terminate = %v, want os.ErrProcessDone", err)
	}
	if err := group.kill(); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("late kill = %v, want os.ErrProcessDone", err)
	}
	if calls != 0 {
		t.Fatalf("closed group issued %d signal syscalls, want 0", calls)
	}
}

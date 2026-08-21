package procgroup

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
)

func TestNilProcessOperationsAreSafe(t *testing.T) {
	var process *Process
	if got := process.ID(); got != 0 {
		t.Fatalf("nil process ID = %d, want 0", got)
	}
	if err := process.Terminate(); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("nil process Terminate = %v, want os.ErrProcessDone", err)
	}
	if err := process.Kill(); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("nil process Kill = %v, want os.ErrProcessDone", err)
	}
}

func TestOwnedGroupTerminationCachesFirstSignalResult(t *testing.T) {
	want := errors.New("synthetic signal failure")
	group := &ownedGroup{termSent: true, termErr: want}
	if err := group.terminate(); !errors.Is(err, want) {
		t.Fatalf("second terminate = %v, want cached %v", err, want)
	}
}

func TestSignalGroupRejectsInvalidAndMissingGroups(t *testing.T) {
	if err := signalGroup(0, syscall.SIGTERM); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("signalGroup(0) = %v, want os.ErrProcessDone", err)
	}
	if err := signalGroup(1<<30, syscall.SIGTERM); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("signalGroup(missing) = %v, want os.ErrProcessDone", err)
	}
	if err := signalGroup(syscall.Getpgrp(), syscall.Signal(-1)); !errors.Is(err, syscall.EINVAL) {
		t.Fatalf("signalGroup(invalid signal) = %v, want EINVAL", err)
	}
}

func TestReapSentinelHandlesNonExitWaitError(t *testing.T) {
	sentinel := exec.CommandContext(context.Background(), "sh", "-c", "exit 0")
	if err := sentinel.Run(); err != nil {
		t.Fatalf("run sentinel fixture: %v", err)
	}
	readEnd, hold, err := os.Pipe()
	if err != nil {
		t.Fatalf("create hold pipe: %v", err)
	}
	_ = readEnd.Close()
	group := &ownedGroup{
		pgid:       sentinel.Process.Pid,
		sentinel:   sentinel,
		hold:       hold,
		sentinelCh: make(chan struct{}),
	}
	group.reapSentinel()
	if !group.closed {
		t.Fatal("sentinel was not marked closed after a repeated Wait")
	}
}

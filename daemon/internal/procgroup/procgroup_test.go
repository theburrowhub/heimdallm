package procgroup_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/procgroup"
)

// readPID reads one newline-terminated PID from r. The child prints its own PID
// rather than the test reading cmd.Process, because procgroup.Run owns Start and
// touching that field from the test goroutine would be a data race.
func readPID(t *testing.T, r io.Reader) int {
	t.Helper()
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil {
		t.Fatalf("read pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("parse pid %q: %v", line, err)
	}
	return pid
}

// TestRun_KillsGrandchildOnTimeout is the regression test for
// theburrowhub/heimdallm#665. `sh` stands in for git and the backgrounded
// `sleep` for the `ssh` child git forks for SSH remotes. Under plain cmd.Run
// only the direct child is signalled, so the sleep survives, is reparented to
// PID 1 and becomes a zombie there once it exits with nobody to wait() on it.
// Cancelling must take the whole group down.
func TestRun_KillsGrandchildOnTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	// An inherited pipe is the liveness probe. fd 3 reaches both the shell and
	// the sleep it backgrounds, and the kernel reports EOF on the read end only
	// once every process holding the write end has exited.
	//
	// A PID existence check (kill(pid, 0)) is unusable here: it also succeeds
	// for a zombie, which is exactly the state this issue is about — the first
	// version of this test passed a live grandchild and failed a correctly
	// killed one for that reason. The pipe distinguishes "running" from
	// "exited but not yet reaped"; kill(pid, 0) cannot.
	probeR, probeW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer probeR.Close()

	// Background a long sleep, announce its PID, then block. The parent must not
	// exit by itself, otherwise the test would pass without cancellation having
	// done any work.
	cmd := exec.CommandContext(ctx, "sh", "-c", `sleep 60 & echo $!; wait`)
	cmd.ExtraFiles = []*os.File{probeW}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}

	// Run in a goroutine so the PID can be read while the child is still alive:
	// Wait closes the stdout pipe once the command exits.
	runErrCh := make(chan error, 1)
	go func() { runErrCh <- procgroup.Run(cmd) }()

	grandchild := readPID(t, stdout)
	// The parent must drop its own copy or EOF can never arrive.
	if err := probeW.Close(); err != nil {
		t.Fatalf("close probe write end: %v", err)
	}

	select {
	case runErr := <-runErrCh:
		if runErr == nil {
			t.Fatal("expected a cancellation error from a command that outlives its context")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run never returned after the context expired")
	}

	eof := make(chan error, 1)
	go func() {
		b := make([]byte, 1)
		_, readErr := probeR.Read(b)
		eof <- readErr
	}()
	select {
	case readErr := <-eof:
		if !errors.Is(readErr, io.EOF) {
			t.Fatalf("probe read returned %v, want io.EOF", readErr)
		}
	case <-time.After(10 * time.Second):
		_ = syscall.Kill(grandchild, syscall.SIGKILL) // don't leak a stray process
		t.Fatalf("grandchild %d still holds the inherited pipe — it outlived "+
			"cancellation, so the process group was not signalled", grandchild)
	}
}

// TestRun_ChildLeadsItsOwnGroup pins the precondition that makes the fix work:
// the child must lead its own group, so kill(-pid) reaches its descendants. If
// Setpgid were dropped the child would inherit the daemon's group and a
// negated-PID signal would be aimed at the daemon itself.
func TestRun_ChildLeadsItsOwnGroup(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "sh", "-c", `echo $$; exec sleep 60`)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}

	runErrCh := make(chan error, 1)
	go func() { runErrCh <- procgroup.Run(cmd) }()

	// `exec` replaces the shell in place, so this PID is also the sleep's.
	child := readPID(t, stdout)

	pgid, err := syscall.Getpgid(child)
	if err != nil {
		t.Fatalf("getpgid(%d): %v", child, err)
	}
	if pgid != child {
		t.Errorf("pgid = %d, want %d (the child must lead its own group)", pgid, child)
	}
	if pgid == syscall.Getpgrp() {
		t.Error("child shares the test process group; kill(-pgid) would signal the daemon itself")
	}

	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill group: %v", err)
	}
	select {
	case <-runErrCh:
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return after the group was killed")
	}
}

// TestRun_SucceedsAndPropagatesOutput guards the happy path: wrapping Start and
// Wait must not change the observable behaviour of a command that completes
// normally, since every git call in the daemon now goes through here.
func TestRun_SucceedsAndPropagatesOutput(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "sh", "-c", "printf hello")
	var out strings.Builder
	cmd.Stdout = &out

	if err := procgroup.Run(cmd); err != nil {
		t.Fatalf("run: %v", err)
	}
	if out.String() != "hello" {
		t.Errorf("stdout = %q, want %q", out.String(), "hello")
	}
}

// TestRun_PropagatesExitError keeps the error contract identical to cmd.Run:
// the git wrappers unwrap *exec.ExitError to report the exit status, so losing
// the error type would silently degrade their messages.
func TestRun_PropagatesExitError(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "sh", "-c", "exit 3")
	err := procgroup.Run(cmd)
	if err == nil {
		t.Fatal("expected a non-nil error for exit status 3")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("error %v (%T) is not an *exec.ExitError", err, err)
	}
	if got := exitErr.ExitCode(); got != 3 {
		t.Errorf("exit code = %d, want 3", got)
	}
}

// TestRun_ReportsStartFailure covers the early return, which must also close the
// escalation channel rather than leave a goroutine parked on it.
func TestRun_ReportsStartFailure(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "/nonexistent/heimdallm-procgroup-test")
	if err := procgroup.Run(cmd); err == nil {
		t.Fatal("expected an error when the binary cannot be started")
	}
}

// TestRun_PreservesExistingSysProcAttr makes sure Run augments rather than
// replaces a SysProcAttr the caller had already configured.
func TestRun_PreservesExistingSysProcAttr(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "sh", "-c", "exit 0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: false, Foreground: false}
	if err := procgroup.Run(cmd); err != nil {
		t.Fatalf("run: %v", err)
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Error("Setpgid was not applied to the caller's existing SysProcAttr")
	}
}

// TestRun_RejectsCommandWithoutContext documents the one precondition callers
// must honour: os/exec refuses to start a command that has a Cancel hook but
// was not created with CommandContext. Both git call sites use CommandContext,
// and the failure is immediate and self-describing rather than silent — but pin
// it so nobody "fixes" the requirement out of the doc comment by accident.
func TestRun_RejectsCommandWithoutContext(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 0") //nolint:noctx // deliberate misuse
	err := procgroup.Run(cmd)
	if err == nil {
		t.Fatal("expected Run to fail for a command not created with CommandContext")
	}
	if !strings.Contains(err.Error(), "CommandContext") {
		t.Errorf("error %q does not name the missing precondition", err)
	}
}

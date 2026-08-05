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
//
// r must be an ExtraFiles pipe, never cmd.StdoutPipe(): os/exec closes the
// StdoutPipe read end from Wait, so a test racing a read against cancellation
// would intermittently get os.ErrClosed instead of the PID and fail for reasons
// unrelated to the behaviour under test.
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

	// Two inherited pipes, both on ExtraFiles so neither races os/exec's
	// handling of the stdout pipe:
	//   fd 3 (pidW)   — the child announces the grandchild's PID here
	//   fd 4 (probeW) — liveness probe; the kernel reports EOF on the read end
	//                   only once every process holding the write end has exited
	//
	// A PID existence check (kill(pid, 0)) is unusable as the probe: it also
	// succeeds for a zombie, which is exactly the state this issue is about — an
	// earlier version of this test passed a live grandchild and failed a
	// correctly killed one for that reason.
	pidR, pidW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pid pipe: %v", err)
	}
	defer pidR.Close()
	probeR, probeW, err := os.Pipe()
	if err != nil {
		t.Fatalf("probe pipe: %v", err)
	}
	defer probeR.Close()

	// Background a long sleep, announce its PID on fd 3, then block. The parent
	// must not exit by itself, otherwise the test would pass without
	// cancellation having done any work.
	cmd := exec.CommandContext(ctx, "sh", "-c", `sleep 60 & echo $! >&3; wait`)
	cmd.ExtraFiles = []*os.File{pidW, probeW}

	runErrCh := make(chan error, 1)
	go func() { runErrCh <- procgroup.Run(cmd) }()

	// The parent must drop its own copies or EOF can never arrive. Closing after
	// Start has certainly happened — which reading the PID guarantees — keeps
	// the descriptors valid for the child.
	grandchild := readPID(t, pidR)
	if err := pidW.Close(); err != nil {
		t.Fatalf("close pid write end: %v", err)
	}
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

// TestRun_EscalatesToSIGKILLWhenGroupIgnoresSIGTERM covers the escalation path,
// which every other test skips because their children die on the first signal.
// The whole tree traps SIGTERM, so only the SIGKILL aimed at the group can end
// it; removing the escalation makes this test fail.
//
// What it does NOT pin: the termGrace-vs-waitDelay ordering. When those timers
// are equal the outcome is a genuine race between this package's escalation
// goroutine and os/exec's own WaitDelay expiry, and which side wins is
// scheduling-dependent — verified by running this test against termGrace ==
// waitDelay == 2s, where it still passes because the escalation happens to win.
// The ordering is enforced by the invariant documented on the constants, not by
// a test, because a probabilistic failure cannot be asserted reliably.
func TestRun_EscalatesToSIGKILLWhenGroupIgnoresSIGTERM(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	pidR, pidW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pid pipe: %v", err)
	}
	defer pidR.Close()
	probeR, probeW, err := os.Pipe()
	if err != nil {
		t.Fatalf("probe pipe: %v", err)
	}
	defer probeR.Close()

	// BOTH levels must ignore SIGTERM for this to test what it claims. Trapping
	// only in the parent shell is not enough: the group SIGTERM would still kill
	// the grandchild directly, the probe would reach EOF, and the test would pass
	// whether or not the escalation ever fired (an earlier version of this test
	// did exactly that and passed against the buggy timers).
	//
	// os/exec's own WaitDelay always kills the DIRECT child, so the parent dies
	// either way. The escalation is the only thing that can reach a grandchild
	// which ignores SIGTERM — and SIGKILL cannot be trapped.
	cmd := exec.CommandContext(ctx, "sh", "-c",
		`trap '' TERM; sh -c "trap '' TERM; exec sleep 60" & echo $! >&3; wait`)
	cmd.ExtraFiles = []*os.File{pidW, probeW}

	runErrCh := make(chan error, 1)
	go func() { runErrCh <- procgroup.Run(cmd) }()

	grandchild := readPID(t, pidR)
	if err := pidW.Close(); err != nil {
		t.Fatalf("close pid write end: %v", err)
	}
	if err := probeW.Close(); err != nil {
		t.Fatalf("close probe write end: %v", err)
	}

	select {
	case <-runErrCh:
	case <-time.After(20 * time.Second):
		t.Fatal("Run never returned; the SIGKILL escalation did not fire")
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
		_ = syscall.Kill(grandchild, syscall.SIGKILL)
		t.Fatalf("grandchild %d survived a SIGTERM-ignoring group — the SIGKILL "+
			"escalation never reached the process group", grandchild)
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

// TestRun_ReportsSuccessWhenCleanExitLeavesADescendantHoldingThePipes is the
// regression test for the second review finding on #667.
//
// `git gc --auto` (spawned in the background after fetch/commit/merge) and an
// ssh ControlMaster/ControlPersist mux both outlive the git command that started
// them while still holding its inherited stdout/stderr. With WaitDelay armed,
// os/exec surfaces exec.ErrWaitDelay from Wait even though the command exited 0.
// Propagating that would report a `git push` that actually succeeded as a
// failure and throw away its output, so the pipeline would retry or fail an
// operation that already applied.
func TestRun_ReportsSuccessWhenCleanExitLeavesADescendantHoldingThePipes(t *testing.T) {
	// The shell writes its output and exits 0 immediately; the backgrounded
	// sleep inherits stdout and holds it open well past waitDelay.
	cmd := exec.CommandContext(context.Background(), "sh", "-c",
		`sleep 30 & printf complete-output; exit 0`)
	var out strings.Builder
	cmd.Stdout = &out

	start := time.Now()
	err := procgroup.Run(cmd)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("run returned %v; a command that exited 0 must be reported as "+
			"successful even when a descendant holds the pipes", err)
	}
	if out.String() != "complete-output" {
		t.Errorf("stdout = %q, want %q — the buffered output must survive", out.String(), "complete-output")
	}
	// Confirms the test actually exercised the WaitDelay path rather than
	// finishing before it could fire.
	if elapsed < waitDelayForTest {
		t.Errorf("returned after %s, faster than waitDelay — the ErrWaitDelay "+
			"branch was not exercised, so this test proves nothing", elapsed)
	}
	if code := cmd.ProcessState.ExitCode(); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}

// waitDelayForTest mirrors procgroup's unexported waitDelay. Kept as a separate
// constant on purpose: if the production value shrinks below this, the timing
// assertion above starts failing loudly instead of silently passing without
// having exercised the branch.
const waitDelayForTest = 3 * time.Second

// TestRun_RejectsCommandWithoutContext documents the one precondition callers
// must honour: os/exec refuses to start a command that has a Cancel hook but was
// not created with CommandContext. Both git call sites use CommandContext, and
// the failure is immediate and self-describing rather than silent.
//
// Asserts only that Start failed. The review of #667 claimed the stdlib message
// is "exec: command with Cancel requires a Context"; it is actually "exec:
// command with a non-nil Cancel was not created with CommandContext"
// (go1.25 os/exec/exec.go:692). Either way the text is Go's to reword, so
// matching on it would be a fragile assertion — the precondition is what
// matters, not its wording.
func TestRun_RejectsCommandWithoutContext(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 0") //nolint:noctx // deliberate misuse
	if err := procgroup.Run(cmd); err == nil {
		t.Fatal("expected Run to fail for a command not created with CommandContext")
	}
}

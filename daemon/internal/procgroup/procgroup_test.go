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

// TestRun_ChildJoinsAnIsolatedSentinelGroup pins both parts of the ownership
// invariant: the child must not share the daemon's group, and it must not be
// the leader. The separate leader is the sentinel whose unreaped PID reserves
// the PGID until cleanup is complete.
func TestRun_ChildJoinsAnIsolatedSentinelGroup(t *testing.T) {
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
	if pgid == child {
		t.Errorf("pgid = child PID %d; want a distinct sentinel leader", child)
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

	start := time.Now()
	if err := procgroup.Run(cmd); err != nil {
		t.Fatalf("run: %v", err)
	}
	elapsed := time.Since(start)
	if out.String() != "hello" {
		t.Errorf("stdout = %q, want %q", out.String(), "hello")
	}
	if elapsed >= procgroup.TermGrace {
		t.Errorf("clean command took %s, want less than TermGrace %s; normal completion must not wait for escalation",
			elapsed, procgroup.TermGrace)
	}
}

// TestRun_CleansDetachedDescendantAfterOrdinarySuccess covers the case that
// does not produce exec.ErrWaitDelay: a background process can close its
// stdout/stderr and still outlive the command. Run must drain the owned group
// on every return, not only when os/exec reports an inherited pipe.
func TestRun_CleansDetachedDescendantAfterOrdinarySuccess(t *testing.T) {
	pidR, pidW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pid pipe: %v", err)
	}
	defer pidR.Close()

	cmd := exec.CommandContext(context.Background(), "sh", "-c",
		`sleep 30 >/dev/null 2>&1 & echo $! >&3; exit 0`)
	cmd.ExtraFiles = []*os.File{pidW}
	if err := procgroup.Run(cmd); err != nil {
		t.Fatalf("run: %v", err)
	}
	if err := pidW.Close(); err != nil {
		t.Fatalf("close pid write end: %v", err)
	}
	descendant := readPID(t, pidR)
	if processRunning(descendant) {
		_ = syscall.Kill(descendant, syscall.SIGKILL)
		t.Fatalf("detached descendant %d was still running when Run returned", descendant)
	}
}

// TestProcessGroupOwnerCrashHelper is launched as a separate test process by
// TestStart_DaemonSIGKILLCleansOnlyItsOwnedGroup. It deliberately never calls
// Wait: killing this process models a daemon crash at the exact point where an
// AI command is still running.
func TestProcessGroupOwnerCrashHelper(t *testing.T) {
	if os.Getenv("HEIMDALLM_PROCGROUP_CRASH_HELPER") != "1" {
		t.Skip("subprocess helper")
	}

	pidW := os.NewFile(3, "command-pid")
	probeW := os.NewFile(4, "command-liveness")
	if pidW == nil || probeW == nil {
		t.Fatal("expected inherited helper descriptors 3 and 4")
	}

	cmd := exec.CommandContext(context.Background(), "sh", "-c",
		`trap '' TERM; echo $$ >&3; exec sleep 60`)
	cmd.ExtraFiles = []*os.File{pidW, probeW}
	if _, err := procgroup.Start(cmd); err != nil {
		t.Fatalf("start owned command: %v", err)
	}
	// Only the command keeps these descriptors now. The helper retains the
	// procgroup sentinel's private hold pipe until the outer test SIGKILLs it.
	_ = pidW.Close()
	_ = probeW.Close()

	select {}
}

// TestStart_DaemonSIGKILLCleansOnlyItsOwnedGroup covers the shutdown path that
// no in-process cleanup can handle. The sentinel must observe EOF on its
// private pipe when the daemon is SIGKILLed, TERM and then KILL only its own
// reserved group, and leave an unrelated process group alive.
func TestStart_DaemonSIGKILLCleansOnlyItsOwnedGroup(t *testing.T) {
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

	helper := exec.Command(os.Args[0], "-test.run=^TestProcessGroupOwnerCrashHelper$") //nolint:noctx // killed explicitly below
	helper.Env = append(os.Environ(), "HEIMDALLM_PROCGROUP_CRASH_HELPER=1")
	helper.ExtraFiles = []*os.File{pidW, probeW}
	if err := helper.Start(); err != nil {
		t.Fatalf("start daemon-crash helper: %v", err)
	}
	helperDone := make(chan error, 1)
	go func() { helperDone <- helper.Wait() }()

	commandPID := readPID(t, pidR)
	if err := pidW.Close(); err != nil {
		t.Fatalf("close pid write end: %v", err)
	}
	if err := probeW.Close(); err != nil {
		t.Fatalf("close probe write end: %v", err)
	}

	unrelated := exec.Command("sleep", "60") //nolint:noctx // cleaned up exactly by PID below
	unrelated.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := unrelated.Start(); err != nil {
		_ = helper.Process.Kill()
		<-helperDone
		t.Fatalf("start unrelated process: %v", err)
	}
	defer func() {
		_ = unrelated.Process.Kill()
		_ = unrelated.Wait()
	}()
	if pgid, err := syscall.Getpgid(unrelated.Process.Pid); err != nil || pgid != unrelated.Process.Pid {
		t.Fatalf("unrelated process group = %d, %v; want its own PGID %d",
			pgid, err, unrelated.Process.Pid)
	}

	if err := helper.Process.Kill(); err != nil {
		t.Fatalf("SIGKILL daemon-crash helper: %v", err)
	}
	select {
	case waitErr := <-helperDone:
		if waitErr == nil {
			t.Fatal("daemon-crash helper exited cleanly; want SIGKILL")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("daemon-crash helper did not exit after SIGKILL")
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
			t.Fatalf("command probe read returned %v, want io.EOF", readErr)
		}
	case <-time.After(10 * time.Second):
		_ = syscall.Kill(commandPID, syscall.SIGKILL)
		t.Fatalf("owned command %d survived its daemon's SIGKILL", commandPID)
	}

	if !processRunning(unrelated.Process.Pid) {
		t.Fatal("sentinel cleanup signalled an unrelated process group")
	}
}

// TestProcess_LateSignalsAfterWaitAreNoOps pins the other half of PGID
// ownership. Wait reaps the sentinel and releases the numeric ID, so later
// Terminate/Kill calls must return the result of the already-delivered signals
// without issuing a new syscall against a potentially recycled group.
func TestProcess_LateSignalsAfterWaitAreNoOps(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "sh", "-c", "exit 0")
	process, err := procgroup.Start(cmd)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := process.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if err := process.Terminate(); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("late Terminate = %v, want os.ErrProcessDone without a syscall", err)
	}
	if err := process.Kill(); err != nil {
		t.Fatalf("late Kill = %v, want the cached successful result", err)
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
	pidR, pidW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pid pipe: %v", err)
	}
	defer pidR.Close()

	// The shell writes its output and exits 0 immediately; the backgrounded
	// sleep inherits stdout and holds it open well past waitDelay. fd 3 records
	// the descendant so the test can assert that Run cleaned it before return.
	cmd := exec.CommandContext(context.Background(), "sh", "-c",
		`sleep 30 & echo $! >&3; printf complete-output; exit 0`)
	cmd.ExtraFiles = []*os.File{pidW}
	var out strings.Builder
	cmd.Stdout = &out

	start := time.Now()
	err = procgroup.Run(cmd)
	elapsed := time.Since(start)
	if closeErr := pidW.Close(); closeErr != nil {
		t.Fatalf("close pid write end: %v", closeErr)
	}
	descendant := readPID(t, pidR)

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
	if processRunning(descendant) {
		_ = syscall.Kill(descendant, syscall.SIGKILL)
		t.Fatalf("descendant %d was still running when Run returned", descendant)
	}
}

// waitDelayForTest mirrors procgroup.WaitDelay. Kept as a separate constant on
// purpose: if the production value shrinks below this, the timing
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

// processRunning treats a zombie as dead. The Docker test environment's PID 1
// may not reap an orphan immediately, but a zombie cannot execute work or keep
// an update drain unsafe.
func processRunning(pid int) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err == nil {
		if idx := strings.LastIndex(string(data), ")"); idx >= 0 {
			rest := strings.Fields(string(data)[idx+1:])
			if len(rest) > 0 {
				return rest[0] != "Z"
			}
		}
		return true
	}
	if os.IsNotExist(err) {
		// /proc is not mounted on macOS. Ask ps for the state so a zombie is
		// not mistaken for a running process by kill(pid, 0).
		out, psErr := exec.Command("ps", "-o", "state=", "-p", strconv.Itoa(pid)).Output() //nolint:noctx // bounded local probe
		if psErr == nil {
			state := strings.TrimSpace(string(out))
			return state != "" && !strings.HasPrefix(strings.ToUpper(state), "Z")
		}
		var exitErr *exec.ExitError
		if errors.As(psErr, &exitErr) {
			return false
		}
	}
	return syscall.Kill(pid, 0) == nil
}

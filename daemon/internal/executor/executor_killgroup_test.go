package executor_test

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"github.com/heimdallm/daemon/internal/executor"
)

// TestKillGroupReportsProcessDoneForVanishedGroup pins the contract os/exec
// relies on. cmd.Cancel's error reaches Wait, and os/exec only ignores it when
// it is equivalent to os.ErrProcessDone — a raw ESRCH ("no such process") is
// surfaced instead, so a cancelled execution whose group had already exited
// reported "executor: run <cli>: no such process" and hid the fact that the real
// cause was the timeout. That is precisely the diagnosis this work is meant to
// improve, so a group that is already gone must read as done, not as a failure.
func TestKillGroupReportsProcessDoneForVanishedGroup(t *testing.T) {
	// Take a real PID and let it exit, so the group is guaranteed to be gone.
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	pgid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait helper: %v", err)
	}

	err := executor.KillGroupForTest(pgid, syscall.SIGTERM)
	if !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("KillGroup on a vanished group = %v, want os.ErrProcessDone", err)
	}
}

// TestKillGroupSignalsLiveGroup keeps the happy path honest: a real group must
// be signalled and report no error.
func TestKillGroupSignalsLiveGroup(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "sleep 30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	pgid := cmd.Process.Pid
	defer func() {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		_ = cmd.Wait()
	}()

	if err := executor.KillGroupForTest(pgid, syscall.SIGKILL); err != nil {
		t.Fatalf("KillGroup on a live group = %v, want nil", err)
	}
}

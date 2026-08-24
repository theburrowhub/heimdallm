package executor

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/procgroup"
)

func TestExecuteRawHonorsCancellationWhileProcessIsStarting(t *testing.T) {
	defer ResetLoginPathCacheForTest()()

	binDir := t.TempDir()
	workDir := t.TempDir()
	pidFile := filepath.Join(workDir, "review.pid")
	script := `#!/bin/sh
if [ "$1" = "--help" ]; then
  printf 'Usage: claude\n'
  exit 0
fi
echo $$ > "$PWD/review.pid"
sleep 600
`
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake CLI: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	started := make(chan error, 1)
	releaseStart := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseStart) }) }

	e := New()
	defer e.TerminateAll()
	defer func() {
		// Mark a still-pending execution before releasing the blocked Start;
		// attachGroup will then terminate it even if cleanup wins the race.
		_, _ = e.TerminateExecution("pr-review:1")
		release()
	}()
	// Hold Start after the child exists but before ExecuteRaw can attach its
	// process group. Cancellation must target the logical registration and be
	// applied as soon as this hook returns.
	e.startProcess = func(cmd *exec.Cmd) (*procgroup.Process, error) {
		process, err := procgroup.Start(cmd)
		started <- err
		<-releaseStart
		return process, err
	}

	done := make(chan error, 1)
	go func() {
		_, err := e.ExecuteRaw("claude", "prompt", ExecOptions{
			WorkDir: workDir, Timeout: 10 * time.Minute, ExecutionID: "pr-review:1",
		})
		done <- err
	}()

	if err := <-started; err != nil {
		release()
		t.Fatalf("start fake CLI: %v", err)
	}
	pid := waitForStartedPID(t, pidFile)
	if active, err := e.TerminateExecution("pr-review:1"); err != nil || !active {
		release()
		t.Fatalf("pending execution cancellation = active %v, error %v", active, err)
	}
	release()

	select {
	case err := <-done:
		if !errors.Is(err, ErrExecutionCancelled) {
			t.Fatalf("execution error = %v, want ErrExecutionCancelled", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("startup-cancelled execution did not return")
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("startup-cancelled process %d still exists (signal probe: %v)", pid, err)
	}
}

func TestExecuteRawUntracksExecutionWhenStartFails(t *testing.T) {
	startErr := errors.New("forced start failure")
	e := New()
	e.startProcess = func(*exec.Cmd) (*procgroup.Process, error) {
		return nil, startErr
	}

	_, err := e.ExecuteRaw("claude", "prompt", ExecOptions{ExecutionID: "pr-review:failed-start"})
	if !errors.Is(err, startErr) {
		t.Fatalf("ExecuteRaw error = %v, want wrapped start failure", err)
	}
	if active, cancelErr := e.TerminateExecution("pr-review:failed-start"); cancelErr != nil || active {
		t.Fatalf("failed start remained cancellable: active %v, error %v", active, cancelErr)
	}
}

func waitForStartedPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, convErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if convErr == nil && pid > 0 {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("process never recorded its PID in %s", path)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

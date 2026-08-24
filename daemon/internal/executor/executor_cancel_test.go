package executor_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/executor"
)

func TestTerminateExecutionCancelsOnlyMatchingProcessGroup(t *testing.T) {
	defer executor.ResetLoginPathCacheForTest()()

	binDir := t.TempDir()
	firstDir := t.TempDir()
	secondDir := t.TempDir()
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

	e := executor.New()
	defer e.TerminateAll()
	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	go func() {
		_, err := e.ExecuteRaw("claude", "prompt", executor.ExecOptions{
			WorkDir: firstDir, Timeout: 10 * time.Minute, ExecutionID: "pr-review:1",
		})
		firstDone <- err
	}()
	go func() {
		_, err := e.ExecuteRaw("claude", "prompt", executor.ExecOptions{
			WorkDir: secondDir, Timeout: 10 * time.Minute, ExecutionID: "pr-review:2",
		})
		secondDone <- err
	}()

	firstPID := readPIDFile(t, filepath.Join(firstDir, "review.pid"))
	secondPID := readPIDFile(t, filepath.Join(secondDir, "review.pid"))
	if active, err := e.TerminateExecution("pr-review:999"); err != nil || active {
		t.Fatalf("unknown execution cancellation = active %v, error %v", active, err)
	}
	if active, err := e.TerminateExecution("pr-review:1"); err != nil || !active {
		t.Fatalf("first execution cancellation = active %v, error %v", active, err)
	}

	select {
	case err := <-firstDone:
		if !errors.Is(err, executor.ErrExecutionCancelled) {
			t.Fatalf("first execution error = %v, want ErrExecutionCancelled", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("matching execution did not return after cancellation")
	}
	if processRunning(firstPID) {
		t.Fatalf("matching process %d survived cancellation", firstPID)
	}
	if !processRunning(secondPID) {
		t.Fatalf("unrelated process %d was killed by scoped cancellation", secondPID)
	}
	select {
	case err := <-secondDone:
		t.Fatalf("unrelated execution returned after cancelling first: %v", err)
	default:
	}

	if active, err := e.TerminateExecution("pr-review:2"); err != nil || !active {
		t.Fatalf("cleanup cancellation = active %v, error %v", active, err)
	}
	select {
	case err := <-secondDone:
		if !errors.Is(err, executor.ErrExecutionCancelled) {
			t.Fatalf("second execution error = %v, want ErrExecutionCancelled", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("second execution did not return after cancellation")
	}
}

func TestExecuteRawClassifiesConfiguredDeadline(t *testing.T) {
	defer executor.ResetLoginPathCacheForTest()()

	binDir := t.TempDir()
	script := `#!/bin/sh
if [ "$1" = "--help" ]; then
  printf 'Usage: claude\n'
  exit 0
fi
sleep 600
`
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake CLI: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err := executor.New().ExecuteRaw("claude", "prompt", executor.ExecOptions{
		Timeout: 50 * time.Millisecond,
	})
	if !errors.Is(err, executor.ErrExecutionTimedOut) {
		t.Fatalf("deadline error = %v, want ErrExecutionTimedOut", err)
	}
	if errors.Is(err, executor.ErrExecutionCancelled) {
		t.Fatalf("deadline error was misclassified as manual cancellation: %v", err)
	}
}

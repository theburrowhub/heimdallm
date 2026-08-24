package executor_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/executor"
)

func TestTerminateExecutionCancelsEveryMatchingProcessGroupOnly(t *testing.T) {
	defer executor.ResetLoginPathCacheForTest()()

	binDir := t.TempDir()
	firstDir := t.TempDir()
	secondDir := t.TempDir()
	unrelatedDir := t.TempDir()
	script := `#!/bin/sh
if [ "$1" = "--help" ]; then
  printf 'Usage: claude\n'
  exit 0
fi
printf 'partial cancellation diagnostic\n' >&2
echo $$ > "$PWD/review.pid"
sleep 600
`
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake CLI: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	e := executor.New()
	defer e.TerminateAll()
	if active, err := e.TerminateExecution(" \t "); err != nil || active {
		t.Fatalf("blank execution cancellation = active %v, error %v", active, err)
	}
	start := func(dir, executionID string) <-chan error {
		done := make(chan error, 1)
		go func() {
			_, err := e.ExecuteRaw("claude", "prompt", executor.ExecOptions{
				WorkDir: dir, Timeout: 10 * time.Minute, ExecutionID: executionID,
			})
			done <- err
		}()
		return done
	}

	// Two independent review runs intentionally share the same PR key. This
	// happens when poll-triggered and manual review paths overlap.
	firstDone := start(firstDir, "pr-review:1")
	secondDone := start(secondDir, "pr-review:1")
	unrelatedDone := start(unrelatedDir, "pr-review:2")

	firstPID := readPIDFile(t, filepath.Join(firstDir, "review.pid"))
	secondPID := readPIDFile(t, filepath.Join(secondDir, "review.pid"))
	unrelatedPID := readPIDFile(t, filepath.Join(unrelatedDir, "review.pid"))
	if active, err := e.TerminateExecution("pr-review:999"); err != nil || active {
		t.Fatalf("unknown execution cancellation = active %v, error %v", active, err)
	}
	if active, err := e.TerminateExecution("pr-review:1"); err != nil || !active {
		t.Fatalf("matching executions cancellation = active %v, error %v", active, err)
	}

	for label, done := range map[string]<-chan error{
		"first":  firstDone,
		"second": secondDone,
	} {
		select {
		case err := <-done:
			if !errors.Is(err, executor.ErrExecutionCancelled) {
				t.Fatalf("%s matching execution error = %v, want ErrExecutionCancelled", label, err)
			}
			if !strings.Contains(err.Error(), "(output: partial cancellation diagnostic)") {
				t.Fatalf("%s matching execution error lost diagnostic output: %v", label, err)
			}
		case <-time.After(15 * time.Second):
			t.Fatalf("%s matching execution did not return after cancellation", label)
		}
	}
	if processRunning(firstPID) {
		t.Fatalf("first matching process %d survived cancellation", firstPID)
	}
	if processRunning(secondPID) {
		t.Fatalf("second matching process %d survived cancellation", secondPID)
	}
	if !processRunning(unrelatedPID) {
		t.Fatalf("unrelated process %d was killed by scoped cancellation", unrelatedPID)
	}
	select {
	case err := <-unrelatedDone:
		t.Fatalf("unrelated execution returned after cancelling first: %v", err)
	default:
	}

	if active, err := e.TerminateExecution("pr-review:2"); err != nil || !active {
		t.Fatalf("cleanup cancellation = active %v, error %v", active, err)
	}
	select {
	case err := <-unrelatedDone:
		if !errors.Is(err, executor.ErrExecutionCancelled) {
			t.Fatalf("unrelated execution cleanup error = %v, want ErrExecutionCancelled", err)
		}
		if !strings.Contains(err.Error(), "(output: partial cancellation diagnostic)") {
			t.Fatalf("unrelated execution cleanup error lost diagnostic output: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("unrelated execution did not return after cleanup cancellation")
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

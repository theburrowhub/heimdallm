package executor_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/executor"
)

// TestTerminateAllKillsInFlightExecutionGroups covers the regression that
// Setpgid introduces. Putting each execution in its own process group is what
// lets a timeout reach the grandchild, but it also detaches the CLI from any
// group-directed signal the daemon receives: ExecuteRaw's context comes from
// context.Background() and is not wired to the SIGINT/SIGTERM handler, so
// launchd terminating the job — or Ctrl-C on a foreground daemon — no longer
// reaches in-flight agents. They would survive a restart and keep spending
// provider quota, which is the very symptom #614 describes. The executor must
// therefore expose a way for the shutdown path to sweep what it started.
func TestTerminateAllKillsInFlightExecutionGroups(t *testing.T) {
	defer executor.ResetLoginPathCacheForTest()()

	binDir := t.TempDir()
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	// Detach from the inherited pipes so this test is about process lifetime,
	// and sleep far past its deadline so "alive" can only mean "not signalled".
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--help\" ]; then\n" +
		"  printf 'Usage: claude\\n'\n" +
		"  exit 0\n" +
		"fi\n" +
		"sh -c 'echo $$ > " + shellQuote(pidFile) + "; exec sleep 600' >/dev/null 2>&1 &\n" +
		"sleep 600\n"
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake CLI: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	e := executor.New()
	done := make(chan struct{})
	go func() {
		defer close(done)
		// A long timeout: the execution must be ended by the shutdown sweep,
		// not by its own deadline.
		_, _ = e.ExecuteRaw("claude", "prompt", executor.ExecOptions{Timeout: 10 * time.Minute})
	}()

	pid := readPIDFile(t, pidFile)

	e.TerminateAll()

	deadline := time.Now().Add(15 * time.Second)
	for processRunning(pid) {
		if time.Now().After(deadline) {
			if data, readErr := os.ReadFile(pidFile); readErr == nil {
				killPID(string(data))
			}
			t.Fatalf("grandchild %d survived TerminateAll", pid)
		}
		time.Sleep(100 * time.Millisecond)
	}

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("ExecuteRaw did not return after TerminateAll")
	}
}

// TestTerminateAllIsSafeWithNoExecutions pins that the shutdown path can call
// the sweep unconditionally, including before anything has run.
func TestTerminateAllIsSafeWithNoExecutions(t *testing.T) {
	executor.New().TerminateAll()
}

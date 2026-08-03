package executor_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/executor"
)

// TestExecuteRawKeepsOutputWhenDescendantHoldsPipesAfterSuccess covers the
// regression that bounding the wait introduces. With cmd.WaitDelay set, os/exec
// also bounds the case where the command already exited but its inherited
// stdout/stderr are still open: Wait closes the pipes and returns
// exec.ErrWaitDelay *even for an exit code 0*. Treating every non-nil Wait error
// as a failure therefore throws away a complete review — and it happens exactly
// in the scenario this work is about, a launcher CLI leaving a descendant
// holding the pipes. A successful run must still return its output.
func TestExecuteRawKeepsOutputWhenDescendantHoldsPipesAfterSuccess(t *testing.T) {
	defer executor.ResetLoginPathCacheForTest()()

	binDir := t.TempDir()
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	// The CLI prints its result and exits 0 straight away, but leaves a
	// descendant holding the inherited pipes open well past WaitDelay.
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--help\" ]; then\n" +
		"  printf 'Usage: claude\\n'\n" +
		"  exit 0\n" +
		"fi\n" +
		"sh -c 'echo $$ > " + shellQuote(pidFile) + "; exec sleep 600' &\n" +
		"printf 'agent output\\n'\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake CLI: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	e := executor.New()
	out, err := e.ExecuteRaw("claude", "prompt", executor.ExecOptions{Timeout: time.Minute})

	// Clean up the descendant regardless of the outcome.
	t.Cleanup(func() {
		if data, readErr := os.ReadFile(pidFile); readErr == nil {
			killPID(strings.TrimSpace(string(data)))
		}
	})

	if err != nil {
		t.Fatalf("ExecuteRaw: %v — a CLI that exited 0 must not fail because a descendant held the pipes", err)
	}
	if got := strings.TrimSpace(string(out)); got != "agent output" {
		t.Errorf("stdout = %q, want %q", got, "agent output")
	}
}

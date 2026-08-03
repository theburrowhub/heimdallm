package executor_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/executor"
)

// TestExecuteRawTimeoutKillsGrandchildren covers the process leak seen in
// production (theburrowhub/heimdallm#614): exec.CommandContext signals only the
// direct child, and every supported AI CLI is a launcher — codex ships a node
// shim that spawns a native binary, and that grandchild is what holds the
// provider connection. When the execution timeout fired, the shim died and the
// grandchild was reparented to init, kept running and kept spending provider
// quota until killed by hand. The executor must put each execution in its own
// process group and signal the whole group.
func TestExecuteRawTimeoutKillsGrandchildren(t *testing.T) {
	defer executor.ResetLoginPathCacheForTest()()

	binDir := t.TempDir()
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	// The grandchild outlives its parent on purpose: it records its own PID and
	// then sleeps far past both the executor timeout and this test's deadline,
	// so "still running" can only mean the group was never signalled — never
	// that it happened to exit on its own. It also detaches from the inherited
	// stdout/stderr pipes, keeping this test about process lifetime alone;
	// TestExecuteRawReturnsPromptlyWhenGrandchildHoldsPipes covers the pipes.
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
	_, err := e.ExecuteRaw("claude", "prompt", executor.ExecOptions{Timeout: time.Second})
	if err == nil {
		t.Fatal("ExecuteRaw returned nil error, want a timeout failure")
	}

	pid := readPIDFile(t, pidFile)

	// Poll: the group kill and the kernel's teardown are not instantaneous.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if !processRunning(pid) {
			return // grandchild is gone — the group was signalled
		}
		if time.Now().After(deadline) {
			// Do not leak the process into the rest of the suite.
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("grandchild %d survived the execution timeout", pid)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// processRunning reports whether pid is a live process. A reaped-but-unwaited
// zombie must count as dead: the test container's PID 1 is a plain shell that
// never reaps orphans, so a killed grandchild lingers as a zombie and signal 0
// keeps succeeding on it — which would make the group-kill assertion pass or
// fail for the wrong reason. On Linux the state comes from /proc; elsewhere fall
// back to signal 0, where the test's own shell reaps promptly.
func processRunning(pid int) bool {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err == nil {
		// Fields: pid (comm) state ... — comm may contain spaces and parens,
		// so parse the state from after the final ')'.
		if idx := strings.LastIndex(string(data), ")"); idx >= 0 {
			rest := strings.Fields(string(data)[idx+1:])
			if len(rest) > 0 {
				return rest[0] != "Z"
			}
		}
		return true
	}
	if os.IsNotExist(err) {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// TestExecuteRawReturnsPromptlyWhenGrandchildHoldsPipes covers the second half
// of the production symptom: a grandchild inherits stdout/stderr, so Wait blocks
// until it closes them — a review that timed out at 5 minutes kept the pipeline
// stalled for as long as the leaked process chose to run (observed: ~12 minutes
// wall clock for a 5-minute timeout). Cancellation must be bounded.
func TestExecuteRawReturnsPromptlyWhenGrandchildHoldsPipes(t *testing.T) {
	defer executor.ResetLoginPathCacheForTest()()

	binDir := t.TempDir()
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	// No redirection: the grandchild keeps the inherited pipes open while it
	// sleeps far longer than any deadline in this test.
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--help\" ]; then\n" +
		"  printf 'Usage: claude\\n'\n" +
		"  exit 0\n" +
		"fi\n" +
		"sh -c 'echo $$ > " + shellQuote(pidFile) + "; exec sleep 600' &\n" +
		"sleep 600\n"
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake CLI: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	done := make(chan error, 1)
	go func() {
		e := executor.New()
		_, err := e.ExecuteRaw("claude", "prompt", executor.ExecOptions{Timeout: time.Second})
		done <- err
	}()

	// Generous ceiling: the point is bounded-vs-unbounded, not the exact grace.
	const ceiling = 30 * time.Second
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("ExecuteRaw returned nil error, want a timeout failure")
		}
	case <-time.After(ceiling):
		if pid, readErr := os.ReadFile(pidFile); readErr == nil {
			if n, convErr := strconv.Atoi(strings.TrimSpace(string(pid))); convErr == nil {
				_ = syscall.Kill(n, syscall.SIGKILL)
			}
		}
		t.Fatalf("ExecuteRaw still blocked %s after a 1s timeout: cancellation is unbounded", ceiling)
	}
}

// TestExecuteRawStillReturnsOutputWithProcessGroup pins the happy path: putting
// the child in its own process group must not disturb normal stdin/stdout
// plumbing.
func TestExecuteRawStillReturnsOutputWithProcessGroup(t *testing.T) {
	defer executor.ResetLoginPathCacheForTest()()

	binDir := t.TempDir()
	capturePrompt := filepath.Join(t.TempDir(), "prompt.txt")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--help\" ]; then\n" +
		"  printf 'Usage: claude\\n'\n" +
		"  exit 0\n" +
		"fi\n" +
		"cat > " + shellQuote(capturePrompt) + "\n" +
		"printf 'agent output\\n'\n"
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake CLI: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	e := executor.New()
	out, err := e.ExecuteRaw("claude", "review this PR", executor.ExecOptions{})
	if err != nil {
		t.Fatalf("ExecuteRaw: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "agent output" {
		t.Errorf("stdout = %q, want %q", got, "agent output")
	}
	promptBytes, err := os.ReadFile(capturePrompt)
	if err != nil {
		t.Fatalf("read captured prompt: %v", err)
	}
	if got := string(promptBytes); got != "review this PR" {
		t.Errorf("prompt = %q, want %q", got, "review this PR")
	}
}

func readPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(data))); convErr == nil && pid > 0 {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("grandchild never recorded its PID in %s", path)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// killPID best-effort kills a PID captured by a fake CLI, keeping leaked test
// descendants out of the rest of the suite.
func killPID(pid string) {
	if n, err := strconv.Atoi(strings.TrimSpace(pid)); err == nil && n > 0 {
		_ = syscall.Kill(n, syscall.SIGKILL)
	}
}

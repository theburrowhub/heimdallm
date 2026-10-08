package executor_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heimdallm/daemon/internal/executor"
)

// fakeAgentBin installs a fake CLI named bin that records its args, cwd and
// stdin, then prints a review.
func fakeAgentBin(t *testing.T, bin string) (argsFile, cwdFile, stdinFile string) {
	t.Helper()
	binDir := t.TempDir()
	out := t.TempDir()
	argsFile = filepath.Join(out, "args")
	cwdFile = filepath.Join(out, "cwd")
	stdinFile = filepath.Join(out, "stdin")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--help\" ]; then printf 'Usage\\n'; exit 0; fi\n" +
		"printf '%s\\n' \"$*\" > " + shellQuote(argsFile) + "\n" +
		"printf '%s\\n' \"$PWD\" > " + shellQuote(cwdFile) + "\n" +
		"cat > " + shellQuote(stdinFile) + "\n" +
		"printf '{\"summary\":\"ok\",\"issues\":[],\"severity\":\"low\"}'\n"
	if err := os.WriteFile(filepath.Join(binDir, bin), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// Prepend, not replace: the script needs the system `cat` to capture stdin.
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func TestExecute_CopilotIsReadOnlyAndReadsStdin(t *testing.T) {
	args, cwd, stdin := fakeAgentBin(t, "copilot")
	workDir := t.TempDir()
	res, err := executor.New().Execute("copilot", "review this", executor.ExecOptions{
		Model: "gpt-5.5", Effort: "high", WorkDir: workDir, ExtraFlags: "--context long_context",
	})
	if err != nil || res.Summary != "ok" {
		t.Fatalf("Execute: %+v %v", res, err)
	}
	got := mustRead(t, args)
	for _, want := range []string{"-s", "--stream off", "--disable-builtin-mcps", "--deny-tool shell", "--deny-tool write",
		"--model gpt-5.5", "--reasoning-effort high", "--context long_context"} {
		if !strings.Contains(got, want) {
			t.Errorf("args %q missing %q", got, want)
		}
	}
	if strings.Contains(got, "--add-dir") || strings.Contains(got, "--allow-all") {
		t.Errorf("args %q must not trust or widen the checkout", got)
	}
	requireSameDir(t, mustRead(t, cwd), workDir)
	if mustRead(t, stdin) != "review this" {
		t.Errorf("prompt must arrive on stdin, got %q", mustRead(t, stdin))
	}
}

func TestExecute_CursorNeverSeesTheCheckout(t *testing.T) {
	args, cwd, stdin := fakeAgentBin(t, "cursor-agent")
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "secret.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := executor.New().Execute("cursor_cli", "review this", executor.ExecOptions{Model: "gpt-5.2", WorkDir: workDir})
	if err != nil || res.Summary != "ok" {
		t.Fatalf("Execute: %+v %v", res, err)
	}
	got := mustRead(t, args)
	for _, want := range []string{"-p", "--mode ask", "--output-format text", "--model gpt-5.2", "--trust", "--workspace"} {
		if !strings.Contains(got, want) {
			t.Errorf("args %q missing %q", got, want)
		}
	}
	ran := mustRead(t, cwd)
	if strings.Contains(got, workDir) || cleanResolvedPath(ran) == cleanResolvedPath(workDir) {
		t.Errorf("cursor must run in its own workspace, not the checkout: args=%q cwd=%q", got, ran)
	}
	if !strings.Contains(ran, "heimdallm-cursor-ws-") {
		t.Errorf("cwd = %q, want the throwaway workspace", ran)
	}
	if _, err := os.Stat(ran); !os.IsNotExist(err) {
		t.Errorf("throwaway workspace %q must be removed after the run", ran)
	}
	if mustRead(t, stdin) != "review this" {
		t.Errorf("prompt must arrive on stdin, got %q", mustRead(t, stdin))
	}
}

func TestCursorFallsBackToAgentBinary(t *testing.T) {
	args, _, _ := fakeAgentBin(t, "agent")
	if _, err := executor.New().Execute("cursor_cli", "p", executor.ExecOptions{}); err != nil {
		t.Fatalf("Execute via `agent`: %v", err)
	}
	if !strings.Contains(mustRead(t, args), "--mode ask") {
		t.Error("the agent alias must run with the Cursor arguments")
	}
	if p := executor.ResolveCLIPath("cursor_cli"); !strings.HasSuffix(p, "/agent") {
		t.Errorf("ResolveCLIPath = %q", p)
	}
	if executor.ResolveCLIPath("rm -rf /") != "" {
		t.Error("unknown agents must not resolve")
	}
}

func TestNewAgentsExtraFlagPolicy(t *testing.T) {
	if err := executor.ValidateExtraFlagsForCLI("copilot", "--allow-all-tools"); err == nil {
		t.Error("copilot --allow-all-tools must be rejected")
	}
	if err := executor.ValidateExtraFlagsForCLI("copilot", "--excluded-tools fetch --log-level error"); err != nil {
		t.Errorf("copilot capability-removing flags must pass: %v", err)
	}
	for _, f := range []string{"--force", "--yolo", "--trust", "--workspace /", "--sandbox disabled", "--approve-mcps"} {
		if err := executor.ValidateExtraFlagsForCLI("cursor_cli", f); err == nil {
			t.Errorf("cursor_cli %s must be rejected", f)
		}
	}
	got := strings.Join(executor.SupportedCLIs(), ",")
	if got != "claude,codex,copilot,cursor_cli,gemini,opencode,openrouter" {
		t.Errorf("SupportedCLIs = %s", got)
	}
	if err := executor.ValidateCLIName("nope"); err == nil || !strings.Contains(err.Error(), "cursor_cli") {
		t.Errorf("error should list the supported agents: %v", err)
	}
	if env := executor.CommandEnv("/opt/x/bin/tool"); !strings.Contains(strings.Join(env, "\n"), "/opt/x/bin") {
		t.Error("CommandEnv must put the binary's directory on PATH")
	}
}

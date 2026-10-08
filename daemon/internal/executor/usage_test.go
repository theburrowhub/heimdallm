package executor_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heimdallm/daemon/internal/executor"
)

// fakeClaude writes a fake `claude` that records its args and prints out.
func fakeClaude(t *testing.T, out string) (argsFile string) {
	t.Helper()
	binDir := t.TempDir()
	argsFile = filepath.Join(t.TempDir(), "args.txt")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--help\" ]; then printf 'Usage: claude\\n'; exit 0; fi\n" +
		"printf '%s\\n' \"$*\" > " + shellQuote(argsFile) + "\n" +
		"cat >/dev/null\n" +
		"printf '%s' " + shellQuote(out) + "\n"
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	// Prepend, not replace: the executor caches the first environment it
	// builds, and a PATH without system dirs would leak into later tests.
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsFile
}

func readArgs(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	return string(b)
}

func TestExecute_ClaudeUsageEnvelope(t *testing.T) {
	envelope := `{"type":"result","subtype":"success","is_error":false,` +
		`"result":"{\"summary\":\"ok\",\"issues\":[],\"severity\":\"low\"}",` +
		`"total_cost_usd":0.0123,` +
		`"usage":{"input_tokens":100,"cache_creation_input_tokens":20,"cache_read_input_tokens":300,"output_tokens":40}}`
	args := fakeClaude(t, envelope)

	res, err := executor.New().Execute("claude", "prompt", executor.ExecOptions{ReportUsage: true})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(readArgs(t, args), "--output-format json") {
		t.Errorf("args = %q, want --output-format json", readArgs(t, args))
	}
	if res.Summary != "ok" || res.Severity != "low" {
		t.Errorf("result = %+v", res)
	}
	u := res.Usage
	if u == nil || u.InputTokens != 120 || u.OutputTokens != 40 || u.CacheReadTokens != 300 ||
		u.CacheWriteTokens != 20 || u.CostUSD != 0.0123 || u.Estimated {
		t.Fatalf("usage = %+v", u)
	}
}

func TestExecute_ClaudeEnvelopeErrorFailsTheRun(t *testing.T) {
	fakeClaude(t, `{"type":"result","subtype":"error_max_turns","is_error":true}`)
	_, err := executor.New().Execute("claude", "prompt", executor.ExecOptions{ReportUsage: true})
	if err == nil || !strings.Contains(err.Error(), "error_max_turns") {
		t.Fatalf("err = %v, want error_max_turns", err)
	}
}

// An older claude, or extra_flags choosing another format, prints the answer
// directly: it must still parse, just without usage.
func TestExecute_ClaudePlainOutputStillParses(t *testing.T) {
	args := fakeClaude(t, `{"summary":"plain","issues":[],"severity":"medium"}`)
	res, err := executor.New().Execute("claude", "prompt", executor.ExecOptions{
		ReportUsage: true,
		ExtraFlags:  "--output-format text",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Summary != "plain" || res.Usage != nil {
		t.Errorf("result = %+v usage=%+v", res, res.Usage)
	}
	if got := readArgs(t, args); strings.Count(got, "--output-format") != 1 {
		t.Errorf("args = %q: an operator-chosen output format must not be doubled", got)
	}

	fakeClaude(t, `{"summary":"plain","issues":[],"severity":"medium"}`)
	res, err = executor.New().Execute("claude", "prompt", executor.ExecOptions{ReportUsage: true})
	if err != nil || res.Summary != "plain" || res.Usage != nil {
		t.Fatalf("a non-envelope answer must parse without usage: res=%+v err=%v", res, err)
	}
}

func TestExecute_NoReportUsageKeepsArgs(t *testing.T) {
	args := fakeClaude(t, `{"summary":"x","issues":[],"severity":"low"}`)
	if _, err := executor.New().Execute("claude", "prompt", executor.ExecOptions{}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(readArgs(t, args), "--output-format") {
		t.Errorf("args = %q, want no --output-format without ReportUsage", readArgs(t, args))
	}
}

func TestEstimateUsage(t *testing.T) {
	u := executor.EstimateUsage(strings.Repeat("a", 401), &executor.ReviewResult{Summary: "s"})
	if !u.Estimated || u.InputTokens != 101 || u.OutputTokens == 0 {
		t.Errorf("EstimateUsage = %+v", u)
	}
	if u := executor.EstimateUsage("", nil); u.InputTokens != 0 || u.OutputTokens != 0 {
		t.Errorf("empty estimate = %+v", u)
	}
}

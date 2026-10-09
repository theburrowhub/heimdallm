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

// A turn cap set by limit_exploration (SoftMaxTurns) must not fail a review
// that needs more turns: the run is retried once without the cap. An
// operator's own max_turns still fails the run.
func TestExecute_SoftMaxTurnsRetriesWithoutTheCap(t *testing.T) {
	binDir := t.TempDir()
	calls := filepath.Join(t.TempDir(), "calls.txt")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--help\" ]; then printf 'Usage: claude\\n'; exit 0; fi\n" +
		"printf '%s\\n' \"$*\" >> " + shellQuote(calls) + "\n" +
		"case \"$*\" in *--max-turns*) printf '%s' '{\"type\":\"result\",\"subtype\":\"error_max_turns\",\"is_error\":true,\"total_cost_usd\":0.5,\"usage\":{\"input_tokens\":1000,\"output_tokens\":100}}'; exit 0;; esac\n" +
		"printf '%s' " + shellQuote(`{"type":"result","subtype":"success","is_error":false,"result":"{\"summary\":\"ok\",\"issues\":[],\"severity\":\"low\"}","total_cost_usd":0.25,"usage":{"input_tokens":500,"output_tokens":50}}`) + "\n"
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)

	res, err := executor.New().Execute("claude", "prompt", executor.ExecOptions{ReportUsage: true, MaxTurns: 20, SoftMaxTurns: true})
	if err != nil || res.Summary != "ok" {
		t.Fatalf("soft cap: res=%+v err=%v", res, err)
	}
	lines := strings.Split(strings.TrimSpace(readArgs(t, calls)), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "--max-turns 20") || strings.Contains(lines[1], "--max-turns") {
		t.Fatalf("runs = %q, want a capped run then an uncapped retry", lines)
	}
	// Both runs were billed, so the review reports both.
	if u := res.Usage; u == nil || u.InputTokens != 1500 || u.OutputTokens != 150 || u.CostUSD != 0.75 || u.TurnCapRetries != 1 {
		t.Fatalf("usage = %+v, want both runs and one retry", u)
	}

	// The retry only gets what is left of the review's timeout. A capped run
	// that used it all fails instead of starting a second full run.
	if err := os.Remove(calls); err != nil {
		t.Fatal(err)
	}
	_, err = executor.New().Execute("claude", "prompt", executor.ExecOptions{ReportUsage: true, MaxTurns: 20, SoftMaxTurns: true, Timeout: 5 * time.Second})
	if !errors.Is(err, executor.ErrClaudeMaxTurns) || !strings.Contains(err.Error(), "no time left") {
		t.Fatalf("err = %v, want ErrClaudeMaxTurns with no time left to retry", err)
	}
	if runs := strings.Split(strings.TrimSpace(readArgs(t, calls)), "\n"); len(runs) != 1 {
		t.Fatalf("runs = %q, want no retry", runs)
	}

	_, err = executor.New().Execute("claude", "prompt", executor.ExecOptions{ReportUsage: true, MaxTurns: 20})
	if !errors.Is(err, executor.ErrClaudeMaxTurns) {
		t.Fatalf("operator cap: err = %v, want ErrClaudeMaxTurns", err)
	}
	if got := executor.OptionsForSelectedCLI("claude", "codex", executor.ExecOptions{MaxTurns: 20, SoftMaxTurns: true}); got.SoftMaxTurns {
		t.Error("a fallback agent must not inherit SoftMaxTurns")
	}
}

// fakeCappedClaude writes a claude that runs out of turns whenever it gets
// --max-turns and otherwise runs uncapped as the given shell lines.
func fakeCappedClaude(t *testing.T, uncapped string) {
	t.Helper()
	binDir := t.TempDir()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--help\" ]; then printf 'Usage: claude\\n'; exit 0; fi\n" +
		"cat >/dev/null\n" +
		"case \"$*\" in *--max-turns*) printf '%s' '{\"type\":\"result\",\"subtype\":\"error_max_turns\",\"is_error\":true,\"usage\":{\"input_tokens\":700,\"output_tokens\":70}}'; exit 0;; esac\n" +
		uncapped + "\n"
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// The capped run is counted even when the retry reports no usage, and a retry
// that fails fails the review.
func TestExecute_SoftMaxTurnsRetryOutcomes(t *testing.T) {
	soft := executor.ExecOptions{ReportUsage: true, MaxTurns: 20, SoftMaxTurns: true}

	fakeCappedClaude(t, `printf '%s' '{"summary":"plain","issues":[],"severity":"low"}'`)
	res, err := executor.New().Execute("claude", "prompt", soft)
	if err != nil || res.Summary != "plain" {
		t.Fatalf("retry with a plain answer: res=%+v err=%v", res, err)
	}
	if u := res.Usage; u == nil || u.InputTokens != 700 || u.OutputTokens != 70 || u.TurnCapRetries != 1 {
		t.Fatalf("usage = %+v, want the capped run's usage and one retry", u)
	}

	fakeCappedClaude(t, `echo 'boom' >&2; exit 3`)
	if _, err := executor.New().Execute("claude", "prompt", soft); err == nil || errors.Is(err, executor.ErrClaudeMaxTurns) {
		t.Fatalf("a failed retry must fail the review with its own error, got %v", err)
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

	// Without the envelope a run that ran out of turns cannot be detected and
	// retried, so limit_exploration's default cap is not applied at all.
	args = fakeClaude(t, `{"summary":"plain","issues":[],"severity":"medium"}`)
	if _, err := executor.New().Execute("claude", "prompt", executor.ExecOptions{
		ReportUsage: true, ExtraFlags: "--output-format text", MaxTurns: 20, SoftMaxTurns: true,
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := readArgs(t, args); strings.Contains(got, "--max-turns") {
		t.Errorf("args = %q: a soft cap must not be passed when it cannot be retried", got)
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

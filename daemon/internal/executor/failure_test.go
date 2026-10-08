package executor_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/heimdallm/daemon/internal/executor"
)

type rlErr struct{ rl bool }

func (e rlErr) Error() string     { return "provider error" }
func (e rlErr) RateLimited() bool { return e.rl }

func TestIsQuotaError(t *testing.T) {
	yes := []error{
		errors.New("executor: run claude: exit status 1 (output: Claude AI usage limit reached|1760000000)"),
		errors.New("executor: claude run ended with error_during_execution: You've hit your usage limit"),
		errors.New("HTTP 429 Too Many Requests"),
		errors.New("RESOURCE_EXHAUSTED: Quota exceeded for quota metric"),
		errors.New("Your credit balance is too low to access the Anthropic API"),
		fmt.Errorf("executor: openrouter: %w", rlErr{rl: true}),
	}
	for _, err := range yes {
		if !executor.IsQuotaError(err) {
			t.Errorf("IsQuotaError(%v) = false", err)
		}
	}
	no := []error{
		nil,
		errors.New("executor: parse JSON result: invalid character"),
		fmt.Errorf("x: %w", rlErr{rl: false}),
		fmt.Errorf("executor: run claude: %w (output: usage limit)", executor.ErrExecutionCancelled),
	}
	for _, err := range no {
		if executor.IsQuotaError(err) {
			t.Errorf("IsQuotaError(%v) = true", err)
		}
	}
}

func TestWriteCapable(t *testing.T) {
	for cli, want := range map[string]bool{"claude": true, "codex": true, "gemini": true, "opencode": true, "copilot": false, "cursor_cli": false, "openrouter": false, "nope": false} {
		if got := executor.WriteCapable(cli); got != want {
			t.Errorf("WriteCapable(%s) = %v", cli, got)
		}
	}
}

func TestClaudeEnvelopeErrorKeepsMessage(t *testing.T) {
	fakeClaude(t, `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"Claude AI usage limit reached|1760000000"}`)
	_, err := executor.New().Execute("claude", "p", executor.ExecOptions{ReportUsage: true})
	if err == nil || !executor.IsQuotaError(err) {
		t.Fatalf("err = %v, want a quota error", err)
	}
}

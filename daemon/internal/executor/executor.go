package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const executionTimeout = 5 * time.Minute

// ReviewResult is the parsed JSON response from the AI CLI.
type ReviewResult struct {
	Summary     string   `json:"summary"`
	Issues      []Issue  `json:"issues"`
	Suggestions []string `json:"suggestions"`
	Severity    string   `json:"severity"`
}

// Issue represents a single code issue found by the AI reviewer.
type Issue struct {
	File        string `json:"file"`
	Line        int    `json:"line"`
	Description string `json:"description"`
	Severity    string `json:"severity"`
}

// ExecOptions controls how the AI CLI is invoked.
type ExecOptions struct {
	// Model sets --model <value> for CLIs that support it.
	Model string
	// MaxTurns sets --max-turns <n> for Claude (0 = not set).
	MaxTurns int
	// ApprovalMode sets --approval-mode <value> for Codex.
	ApprovalMode string
	// ExtraFlags is a free-form string of additional CLI flags (split on spaces).
	ExtraFlags string
	// WorkDir is the working directory for the CLI process.
	// When set, the CLI runs inside the local repo directory, giving it
	// access to all project files for deeper analysis (missing tests, side effects, etc.).
	WorkDir string

	// Claude-specific flags
	Effort               string // --effort low|medium|high|max
	PermissionMode       string // --permission-mode <value>
	Bare                 bool   // --bare
	DangerouslySkipPerms bool   // --dangerously-skip-permissions
	NoSessionPersistence bool   // --no-session-persistence
}

// Executor runs AI CLI tools for code review.
type Executor struct{}

// New creates a new Executor.
func New() *Executor {
	return &Executor{}
}

// Detect returns the first available CLI (primary → fallback).
func (e *Executor) Detect(primary, fallback string) (string, error) {
	if primary != "" {
		if path, err := exec.LookPath(primary); err == nil && path != "" {
			return primary, nil
		}
	}
	if fallback != "" {
		if path, err := exec.LookPath(fallback); err == nil && path != "" {
			return fallback, nil
		}
	}
	return "", fmt.Errorf("executor: no AI CLI available (tried %q, %q)", primary, fallback)
}

// Execute runs the AI CLI with the given prompt and options, returning the parsed result.
func (e *Executor) Execute(cli, prompt string, opts ExecOptions) (*ReviewResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), executionTimeout)
	defer cancel()

	args := buildArgs(cli, opts)
	cmd := exec.CommandContext(ctx, cli, args...)
	cmd.Stdin = strings.NewReader(prompt)
	if opts.WorkDir != "" {
		cmd.Dir = opts.WorkDir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("executor: run %s: %w (stderr: %s)", cli, err, stderr.String())
	}

	return parseResult(stdout.Bytes())
}

// buildArgs constructs the CLI argument list based on the CLI name and options.
func buildArgs(cli string, opts ExecOptions) []string {
	var args []string

	switch cli {
	case "codex":
		if opts.Model != "" {
			args = append(args, "--model", opts.Model)
		}
		if opts.ApprovalMode != "" {
			args = append(args, "--approval-mode", opts.ApprovalMode)
		}
	default:
		// claude, gemini: stdin mode
		args = append(args, "-p", "-")
		if opts.Model != "" {
			args = append(args, "--model", opts.Model)
		}
		if cli == "claude" {
			if opts.MaxTurns > 0 {
				args = append(args, "--max-turns", fmt.Sprintf("%d", opts.MaxTurns))
			}
			if opts.Effort != "" {
				args = append(args, "--effort", opts.Effort)
			}
			if opts.PermissionMode != "" {
				args = append(args, "--permission-mode", opts.PermissionMode)
			}
			if opts.Bare {
				args = append(args, "--bare")
			}
			if opts.DangerouslySkipPerms {
				args = append(args, "--dangerously-skip-permissions")
			}
			if opts.NoSessionPersistence {
				args = append(args, "--no-session-persistence")
			}
		}
	}

	// Append free-form extra flags (split on whitespace)
	if opts.ExtraFlags != "" {
		args = append(args, strings.Fields(opts.ExtraFlags)...)
	}

	return args
}

func parseResult(data []byte) (*ReviewResult, error) {
	s := strings.TrimSpace(string(data))
	// Strip potential markdown code fences
	if strings.HasPrefix(s, "```") {
		lines := strings.Split(s, "\n")
		if len(lines) > 2 {
			s = strings.Join(lines[1:len(lines)-1], "\n")
		}
	}

	// Find first { to last } in case there's surrounding text
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start >= 0 && end > start {
		s = s[start : end+1]
	}

	var result ReviewResult
	if err := json.Unmarshal([]byte(s), &result); err != nil {
		return nil, fmt.Errorf("executor: parse JSON result: %w (raw: %.200s)", err, s)
	}
	if result.Severity == "" {
		result.Severity = "low"
	}
	return &result, nil
}

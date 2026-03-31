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

// Execute runs the AI CLI with the prompt via stdin and returns the parsed result.
func (e *Executor) Execute(cli, prompt string) (*ReviewResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), executionTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, cli, "-p", "-")
	cmd.Stdin = strings.NewReader(prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("executor: run %s: %w (stderr: %s)", cli, err, stderr.String())
	}

	return parseResult(stdout.Bytes())
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

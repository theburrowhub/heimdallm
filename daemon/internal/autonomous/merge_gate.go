package autonomous

import (
	"context"
	"fmt"
)

// Merger merges a PR. Backed by github.Client.MergePR in production.
type Merger interface {
	MergePR(repo string, number int, method string) error
}

// MergeResult records what the gate did, for SSE/audit.
type MergeResult int

const (
	MergeSkippedDisabled MergeResult = iota // auto_merge=false (the default)
	MergeDone
)

func (r MergeResult) String() string {
	if r == MergeDone {
		return "merged"
	}
	return "skipped_disabled"
}

// MergeGate performs the final merge when, and only when, auto_merge is
// enabled for the repo. With the default (disabled) it is a safe no-op that
// reports MergeSkippedDisabled so the caller can emit an audit event.
type MergeGate struct {
	merger  Merger
	enabled bool
	method  string
}

// NewMergeGate builds a gate from resolved autonomous config.
func NewMergeGate(merger Merger, enabled bool, method string) *MergeGate {
	if method == "" {
		method = "squash"
	}
	return &MergeGate{merger: merger, enabled: enabled, method: method}
}

// Run merges the PR if enabled; otherwise returns MergeSkippedDisabled.
func (g *MergeGate) Run(ctx context.Context, repo string, number int) (MergeResult, error) {
	if err := ctx.Err(); err != nil {
		return MergeSkippedDisabled, err
	}
	if !g.enabled {
		return MergeSkippedDisabled, nil
	}
	if err := g.merger.MergePR(repo, number, g.method); err != nil {
		return MergeSkippedDisabled, fmt.Errorf("autonomous: merge gate: %w", err)
	}
	return MergeDone, nil
}

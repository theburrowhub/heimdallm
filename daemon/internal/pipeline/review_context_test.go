package pipeline

import (
	"strings"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/github"
)

func TestPartitionComments_SplitsByTimestamp(t *testing.T) {
	cutoff := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	comments := []github.Comment{
		{Author: "reviewer", Body: "old comment", CreatedAt: cutoff.Add(-1 * time.Hour)},
		{Author: "author", Body: "old response", CreatedAt: cutoff.Add(-30 * time.Minute)},
		{Author: "author", Body: "new push", CreatedAt: cutoff.Add(1 * time.Hour)},
		{Author: "other", Body: "new feedback", CreatedAt: cutoff.Add(2 * time.Hour)},
	}

	before, after := partitionComments(comments, cutoff)
	if len(before) != 2 {
		t.Errorf("before = %d, want 2", len(before))
	}
	if len(after) != 2 {
		t.Errorf("after = %d, want 2", len(after))
	}
}

func TestPartitionComments_AllBefore(t *testing.T) {
	cutoff := time.Now()
	comments := []github.Comment{
		{Author: "a", Body: "old", CreatedAt: cutoff.Add(-1 * time.Hour)},
	}
	before, after := partitionComments(comments, cutoff)
	if len(before) != 1 || len(after) != 0 {
		t.Errorf("before=%d after=%d, want 1/0", len(before), len(after))
	}
}

func TestPartitionComments_AllAfter(t *testing.T) {
	cutoff := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	comments := []github.Comment{
		{Author: "a", Body: "new", CreatedAt: time.Now()},
	}
	before, after := partitionComments(comments, cutoff)
	if len(before) != 0 || len(after) != 1 {
		t.Errorf("before=%d after=%d, want 0/1", len(before), len(after))
	}
}

func TestPartitionComments_Empty(t *testing.T) {
	before, after := partitionComments(nil, time.Now())
	if len(before) != 0 || len(after) != 0 {
		t.Error("expected empty slices for nil input")
	}
}

func TestBuildReviewContext_WithPreviousReview(t *testing.T) {
	prevIssues := `[{"file":"handler.go","line":42,"description":"Missing error handling","severity":"high"}]`
	lastReviewAt := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)

	comments := []github.Comment{
		{Author: "heimdallm-bot", Body: "Review comment", CreatedAt: lastReviewAt.Add(-1 * time.Minute)},
		{Author: "author", Body: "Fixed the error handling in latest commit", CreatedAt: lastReviewAt.Add(1 * time.Hour)},
		{Author: "other-reviewer", Body: "Looks good now", CreatedAt: lastReviewAt.Add(2 * time.Hour)},
	}

	ctx := buildReviewContext(prevIssues, "high", lastReviewAt, comments, "heimdallm-bot", "author")

	if ctx == "" {
		t.Fatal("expected non-empty review context")
	}
	if !strings.Contains(ctx, "RE-REVIEW") {
		t.Error("missing RE-REVIEW instruction")
	}
	if !strings.Contains(ctx, "Missing error handling") {
		t.Error("missing previous finding")
	}
	if !strings.Contains(ctx, "Fixed the error handling") {
		t.Error("missing author response in discussion")
	}
	// Bot's own comment should NOT appear in "Discussion since last review"
	if strings.Contains(ctx, "Review comment") {
		t.Error("bot's own comment should be filtered from new discussion")
	}
}

func TestBuildReviewContext_NoPreviousReview(t *testing.T) {
	ctx := buildReviewContext("", "", time.Time{}, nil, "bot", "")
	if ctx != "" {
		t.Errorf("expected empty context for first review, got: %q", ctx)
	}
}

func TestBuildReviewContext_PreviousReviewNoNewComments(t *testing.T) {
	prevIssues := `[{"file":"main.go","line":10,"description":"Unused import","severity":"low"}]`
	lastReviewAt := time.Now()

	ctx := buildReviewContext(prevIssues, "low", lastReviewAt, nil, "bot", "")

	if !strings.Contains(ctx, "RE-REVIEW") {
		t.Error("missing RE-REVIEW instruction")
	}
	if !strings.Contains(ctx, "Unused import") {
		t.Error("missing previous finding")
	}
	if strings.Contains(ctx, "Discussion since") {
		t.Error("should not have discussion section with no new comments")
	}
}

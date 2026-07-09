package issues

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

func TestBuildTriageContext_WithPreviousTriage(t *testing.T) {
	prevTriage := `{"severity":"high","category":"bug","suggested_assignee":"alice"}`
	prevNextSteps := `["reproduce locally","check auth migration"]`
	prevSummary := "User cannot log in after upgrade."
	lastTriageAt := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)

	comments := []github.Comment{
		{Author: "heimdallm-bot", Body: "Triage comment", CreatedAt: lastTriageAt.Add(-1 * time.Minute)},
		{Author: "author", Body: "Actually we want to support subdirectories long-term", CreatedAt: lastTriageAt.Add(1 * time.Hour)},
		{Author: "heimdallm-bot", Body: "Bot follow-up", CreatedAt: lastTriageAt.Add(90 * time.Minute)},
		{Author: "reviewer", Body: "Agreed, the template needs to handle glob patterns", CreatedAt: lastTriageAt.Add(2 * time.Hour)},
	}

	ctx := buildTriageContext(prevTriage, prevNextSteps, prevSummary, "high", lastTriageAt, comments, "heimdallm-bot")

	if ctx == "" {
		t.Fatal("expected non-empty triage context")
	}
	if !strings.Contains(ctx, "RE-TRIAGE") {
		t.Error("missing RE-TRIAGE instruction")
	}
	if !strings.Contains(ctx, "severity: high") {
		t.Error("missing previous severity")
	}
	if !strings.Contains(ctx, "Category: bug") {
		t.Error("missing previous category")
	}
	if !strings.Contains(ctx, "@alice") {
		t.Error("missing previous suggested assignee")
	}
	if !strings.Contains(ctx, "reproduce locally") {
		t.Error("missing previous next step")
	}
	if !strings.Contains(ctx, "User cannot log in") {
		t.Error("missing previous summary")
	}
	if !strings.Contains(ctx, "support subdirectories") {
		t.Error("missing author's new comment in discussion")
	}
	if !strings.Contains(ctx, "glob patterns") {
		t.Error("missing reviewer's new comment in discussion")
	}
	if strings.Contains(ctx, "Triage comment") {
		t.Error("bot's own pre-triage comment should not appear in new discussion")
	}
	if strings.Contains(ctx, "Bot follow-up") {
		t.Error("bot's own post-triage comment should be filtered from new discussion")
	}
}

func TestBuildTriageContext_NoPreviousTriage(t *testing.T) {
	ctx := buildTriageContext("", "", "", "", time.Time{}, nil, "bot")
	if ctx != "" {
		t.Errorf("expected empty context for first triage, got: %q", ctx)
	}
}

func TestBuildTriageContext_PreviousTriageNoNewComments(t *testing.T) {
	prevTriage := `{"severity":"low","category":"question","suggested_assignee":""}`
	prevNextSteps := `["clarify requirements"]`
	lastTriageAt := time.Now()

	ctx := buildTriageContext(prevTriage, prevNextSteps, "Unclear request", "low", lastTriageAt, nil, "bot")

	if !strings.Contains(ctx, "RE-TRIAGE") {
		t.Error("missing RE-TRIAGE instruction")
	}
	if !strings.Contains(ctx, "Category: question") {
		t.Error("missing previous category")
	}
	if !strings.Contains(ctx, "clarify requirements") {
		t.Error("missing previous next step")
	}
	if strings.Contains(ctx, "New discussion since") {
		t.Error("should not have discussion section with no new comments")
	}
}

func TestBuildTriageContext_EmptyTriageJSON(t *testing.T) {
	ctx := buildTriageContext("{}", "[]", "", "medium", time.Now(), nil, "bot")
	if !strings.Contains(ctx, "RE-TRIAGE") {
		t.Error("missing RE-TRIAGE instruction even with empty triage JSON")
	}
	if !strings.Contains(ctx, "severity: medium") {
		t.Error("should still show severity from prevSeverity param")
	}
}

func TestBuildTriageContext_EmptyBotLogin(t *testing.T) {
	lastTriageAt := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	comments := []github.Comment{
		{Author: "someone", Body: "new info", CreatedAt: lastTriageAt.Add(1 * time.Hour)},
	}

	ctx := buildTriageContext("{}", "[]", "", "low", lastTriageAt, comments, "")
	if !strings.Contains(ctx, "new info") {
		t.Error("with empty botLogin, all new comments should appear")
	}
}

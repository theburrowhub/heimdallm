package pipeline

import (
	"testing"

	"github.com/heimdallm/daemon/internal/executor"
	"github.com/heimdallm/daemon/internal/github"
)

func TestExtractCommentSignals_Empty(t *testing.T) {
	signals := ExtractCommentSignals(nil, "author")
	if signals.HasBlockerKeywords || signals.UnresolvedConcerns != 0 || signals.Urgency != 0 {
		t.Errorf("expected zero signals for nil input, got %+v", signals)
	}
}

func TestExtractCommentSignals_AuthorOnlyComments(t *testing.T) {
	comments := []github.Comment{
		{Author: "author", Body: "I'll fix this"},
		{Author: "author", Body: "Done, pushed changes"},
	}
	signals := ExtractCommentSignals(comments, "author")
	if signals.UnresolvedConcerns != 0 {
		t.Errorf("author-only comments should produce 0 unresolved, got %d", signals.UnresolvedConcerns)
	}
	if signals.Urgency != 0 {
		t.Errorf("expected urgency 0, got %d", signals.Urgency)
	}
}

func TestExtractCommentSignals_BlockerKeyword(t *testing.T) {
	comments := []github.Comment{
		{Author: "reviewer", Body: "This is a blocker — the API key is exposed"},
	}
	signals := ExtractCommentSignals(comments, "author")
	if !signals.HasBlockerKeywords {
		t.Error("expected HasBlockerKeywords=true for 'blocker'")
	}
	if signals.Urgency != 3 {
		t.Errorf("expected urgency 3 for blocker keyword, got %d", signals.Urgency)
	}
}

func TestExtractCommentSignals_DoNotMerge(t *testing.T) {
	comments := []github.Comment{
		{Author: "lead", Body: "Do not merge this until the migration is ready"},
	}
	signals := ExtractCommentSignals(comments, "author")
	if !signals.HasBlockerKeywords {
		t.Error("expected HasBlockerKeywords=true for 'do not merge'")
	}
	if signals.Urgency != 3 {
		t.Errorf("expected urgency 3, got %d", signals.Urgency)
	}
}

func TestExtractCommentSignals_MustFix(t *testing.T) {
	comments := []github.Comment{
		{Author: "reviewer", Body: "You must fix the null pointer dereference"},
	}
	signals := ExtractCommentSignals(comments, "author")
	if !signals.HasBlockerKeywords {
		t.Error("expected HasBlockerKeywords=true for 'must fix'")
	}
}

func TestExtractCommentSignals_UnresolvedConcerns(t *testing.T) {
	comments := []github.Comment{
		{Author: "reviewer1", Body: "This function is too complex"},
		{Author: "reviewer2", Body: "Missing error handling here"},
		{Author: "reviewer1", Body: "Also the naming is confusing"},
	}
	signals := ExtractCommentSignals(comments, "author")
	if signals.UnresolvedConcerns != 3 {
		t.Errorf("expected 3 unresolved concerns, got %d", signals.UnresolvedConcerns)
	}
	if signals.Urgency != 2 {
		t.Errorf("expected urgency 2 for 3+ unresolved, got %d", signals.Urgency)
	}
}

func TestExtractCommentSignals_ConcernsResolvedByAuthor(t *testing.T) {
	comments := []github.Comment{
		{Author: "reviewer", Body: "This function is too complex"},
		{Author: "reviewer", Body: "Missing error handling here"},
		{Author: "author", Body: "Fixed both issues in latest push"},
	}
	signals := ExtractCommentSignals(comments, "author")
	if signals.UnresolvedConcerns != 0 {
		t.Errorf("expected 0 unresolved after author reply, got %d", signals.UnresolvedConcerns)
	}
	if signals.Urgency != 0 {
		t.Errorf("expected urgency 0 after resolution, got %d", signals.Urgency)
	}
}

func TestExtractCommentSignals_PartialResolution(t *testing.T) {
	comments := []github.Comment{
		{Author: "reviewer", Body: "Issue A"},
		{Author: "author", Body: "Fixed A"},
		{Author: "reviewer", Body: "Issue B"},
		{Author: "reviewer", Body: "Issue C"},
	}
	signals := ExtractCommentSignals(comments, "author")
	if signals.UnresolvedConcerns != 2 {
		t.Errorf("expected 2 unresolved (B and C after author reply), got %d", signals.UnresolvedConcerns)
	}
	if signals.Urgency != 1 {
		// 2 concerns → urgency 1 (need 3+ for urgency 2)
		t.Errorf("expected urgency 1, got %d", signals.Urgency)
	}
}

func TestExtractCommentSignals_CaseInsensitiveAuthor(t *testing.T) {
	comments := []github.Comment{
		{Author: "reviewer", Body: "Needs work"},
		{Author: "Author", Body: "Done"}, // different case
	}
	signals := ExtractCommentSignals(comments, "author")
	if signals.UnresolvedConcerns != 0 {
		t.Errorf("expected case-insensitive author match, got %d unresolved", signals.UnresolvedConcerns)
	}
}

func TestExtractCommentSignals_BlockerOverridesCount(t *testing.T) {
	// Even with only 1 comment, blocker keyword → urgency 3
	comments := []github.Comment{
		{Author: "reviewer", Body: "This is a security issue"},
	}
	signals := ExtractCommentSignals(comments, "author")
	if signals.Urgency != 3 {
		t.Errorf("blocker keyword should set urgency 3 regardless of count, got %d", signals.Urgency)
	}
}

func TestExtractCommentSignals_NoCasualFalsePositive(t *testing.T) {
	// Normal discussion should not trigger blockers
	comments := []github.Comment{
		{Author: "reviewer", Body: "Nice work, looks good overall"},
		{Author: "reviewer", Body: "Minor nit: rename this variable"},
	}
	signals := ExtractCommentSignals(comments, "author")
	if signals.HasBlockerKeywords {
		t.Error("casual comments should not trigger blocker keywords")
	}
}

// --- Tests for ReconcileSeverity ---

func TestReconcileSeverity_Consistent(t *testing.T) {
	tests := []struct {
		name     string
		severity string
		issues   []Issue
		want     string
	}{
		{"high matches", "high", []Issue{{Severity: "high"}}, "high"},
		{"medium matches", "medium", []Issue{{Severity: "medium"}}, "medium"},
		{"low matches", "low", []Issue{{Severity: "low"}}, "low"},
		{"no issues low", "low", nil, "low"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Convert pipeline.Issue to executor.Issue for the function
			result := makeResult(tt.severity, tt.issues)
			got := ReconcileSeverity(result)
			if got != tt.want {
				t.Errorf("ReconcileSeverity() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReconcileSeverity_EscalatesFromLowToHigh(t *testing.T) {
	result := makeResult("low", []Issue{
		{Severity: "low"},
		{Severity: "high"},
	})
	got := ReconcileSeverity(result)
	if got != "high" {
		t.Errorf("expected reconciled severity 'high', got %q", got)
	}
}

func TestReconcileSeverity_EscalatesFromLowToMedium(t *testing.T) {
	result := makeResult("low", []Issue{
		{Severity: "medium"},
	})
	got := ReconcileSeverity(result)
	if got != "medium" {
		t.Errorf("expected reconciled severity 'medium', got %q", got)
	}
}

func TestReconcileSeverity_NeverLowers(t *testing.T) {
	// AI says "high" but all issues are "low" — we trust the AI's higher assessment
	result := makeResult("high", []Issue{
		{Severity: "low"},
		{Severity: "low"},
	})
	got := ReconcileSeverity(result)
	if got != "high" {
		t.Errorf("reconcile should never lower severity, got %q", got)
	}
}

// --- Tests for SeverityToEvent with CommentSignals ---

func TestSeverityToEvent_HighAlwaysRequestsChanges(t *testing.T) {
	got := SeverityToEvent("high", CommentSignals{})
	if got != "REQUEST_CHANGES" {
		t.Errorf("high severity should always REQUEST_CHANGES, got %q", got)
	}
}

func TestSeverityToEvent_LowApproves(t *testing.T) {
	got := SeverityToEvent("low", CommentSignals{})
	if got != "APPROVE" {
		t.Errorf("low severity without signals should APPROVE, got %q", got)
	}
}

func TestSeverityToEvent_MediumApprovesNormally(t *testing.T) {
	got := SeverityToEvent("medium", CommentSignals{Urgency: 1})
	if got != "APPROVE" {
		t.Errorf("medium with low urgency should APPROVE, got %q", got)
	}
}

func TestSeverityToEvent_MediumEscalatesWithBlocker(t *testing.T) {
	signals := CommentSignals{
		HasBlockerKeywords: true,
		Urgency:            3,
	}
	got := SeverityToEvent("medium", signals)
	if got != "REQUEST_CHANGES" {
		t.Errorf("medium + blocker signals should REQUEST_CHANGES, got %q", got)
	}
}

func TestSeverityToEvent_LowDoesNotEscalateWithBlocker(t *testing.T) {
	// Design choice: "low" severity is not elevated even with blocker keywords.
	// Only "medium" can be escalated — keeps Heimdallm non-aggressive.
	signals := CommentSignals{
		HasBlockerKeywords: true,
		Urgency:            3,
	}
	got := SeverityToEvent("low", signals)
	if got != "APPROVE" {
		t.Errorf("low severity should not escalate even with blockers, got %q", got)
	}
}

// --- Helper ---

// Issue mirrors executor.Issue for test readability.
type Issue struct {
	Severity string
}

func makeResult(severity string, issues []Issue) *executor.ReviewResult {
	var execIssues []executor.Issue
	for _, iss := range issues {
		execIssues = append(execIssues, executor.Issue{Severity: iss.Severity})
	}
	return &executor.ReviewResult{
		Severity: severity,
		Issues:   execIssues,
	}
}

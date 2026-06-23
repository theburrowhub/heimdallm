package pipeline

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/heimdallm/daemon/internal/executor"
	"github.com/heimdallm/daemon/internal/github"
)

// --- Tests for ExtractCommentSignals ---

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

// --- Fix 2: Word-boundary matching prevents substring false positives ---

func TestExtractCommentSignals_NonBlockingNotFalsePositive(t *testing.T) {
	comments := []github.Comment{
		{Author: "reviewer", Body: "This is non-blocking, just a nit"},
	}
	signals := ExtractCommentSignals(comments, "author")
	if signals.HasBlockerKeywords {
		t.Error("'non-blocking' should NOT trigger blocker detection")
	}
	if signals.Urgency == 3 {
		t.Error("'non-blocking' should not produce urgency 3")
	}
}

func TestExtractCommentSignals_NotABlockerNotFalsePositive(t *testing.T) {
	comments := []github.Comment{
		{Author: "reviewer", Body: "Not a blocker, but consider refactoring"},
	}
	signals := ExtractCommentSignals(comments, "author")
	if signals.HasBlockerKeywords {
		t.Error("'not a blocker' should NOT trigger blocker detection")
	}
}

func TestExtractCommentSignals_NegatedMustFixNotFalsePositive(t *testing.T) {
	comments := []github.Comment{
		{Author: "reviewer", Body: "This is not a must fix, just a suggestion"},
	}
	signals := ExtractCommentSignals(comments, "author")
	if signals.HasBlockerKeywords {
		t.Error("'not a must fix' should NOT trigger blocker detection")
	}
}

func TestExtractCommentSignals_KnackNotFalsePositive(t *testing.T) {
	// "nack" as a pattern should not match inside other words
	comments := []github.Comment{
		{Author: "reviewer", Body: "You have a knack for clean code"},
	}
	signals := ExtractCommentSignals(comments, "author")
	if signals.HasBlockerKeywords {
		t.Error("'knack' should NOT trigger 'nack' blocker detection")
	}
}

func TestExtractCommentSignals_RealBlockerStillDetected(t *testing.T) {
	// After adding negation handling, real blockers must still work
	comments := []github.Comment{
		{Author: "reviewer", Body: "This is definitely a blocker for the release"},
	}
	signals := ExtractCommentSignals(comments, "author")
	if !signals.HasBlockerKeywords {
		t.Error("real 'blocker' keyword should still be detected")
	}
}

func TestExtractCommentSignals_RealBlockingStillDetected(t *testing.T) {
	comments := []github.Comment{
		{Author: "reviewer", Body: "This bug is blocking the deploy pipeline"},
	}
	signals := ExtractCommentSignals(comments, "author")
	if !signals.HasBlockerKeywords {
		t.Error("real 'blocking' keyword should still be detected")
	}
}

// --- Fix 3: HasBlockerKeywords resets when author resolves ---

func TestExtractCommentSignals_BlockerResolvedByAuthor(t *testing.T) {
	comments := []github.Comment{
		{Author: "reviewer", Body: "This is a blocker — exposed credentials"},
		{Author: "author", Body: "Fixed, removed the credentials"},
	}
	signals := ExtractCommentSignals(comments, "author")
	if signals.HasBlockerKeywords {
		t.Error("blocker resolved by author reply should clear HasBlockerKeywords")
	}
	if signals.UnresolvedConcerns != 0 {
		t.Errorf("expected 0 unresolved after author addresses blocker, got %d", signals.UnresolvedConcerns)
	}
	if signals.Urgency != 0 {
		t.Errorf("resolved blocker should produce urgency 0, got %d", signals.Urgency)
	}
}

func TestExtractCommentSignals_BlockerResolvedThenNewConcern(t *testing.T) {
	comments := []github.Comment{
		{Author: "reviewer", Body: "This is a blocker"},
		{Author: "author", Body: "Fixed"},
		{Author: "reviewer", Body: "New minor concern, not a big deal"},
	}
	signals := ExtractCommentSignals(comments, "author")
	if signals.HasBlockerKeywords {
		t.Error("old blocker was resolved; new comment has no blocker keyword")
	}
	if signals.UnresolvedConcerns != 1 {
		t.Errorf("expected 1 unresolved concern (new one), got %d", signals.UnresolvedConcerns)
	}
	if signals.Urgency != 1 {
		t.Errorf("expected urgency 1 (1 unresolved, no blocker), got %d", signals.Urgency)
	}
}

func TestExtractCommentSignals_UnresolvedBlockerPersists(t *testing.T) {
	// If the author never replies to a blocker, it persists
	comments := []github.Comment{
		{Author: "reviewer", Body: "This is a blocker"},
		{Author: "other-reviewer", Body: "I agree, must fix this"},
	}
	signals := ExtractCommentSignals(comments, "author")
	if !signals.HasBlockerKeywords {
		t.Error("unresolved blocker should persist HasBlockerKeywords")
	}
	if signals.Urgency != 3 {
		t.Errorf("expected urgency 3 for unresolved blocker, got %d", signals.Urgency)
	}
}

// --- Tests for ReconcileSeverity ---

func TestReconcileSeverity_Consistent(t *testing.T) {
	tests := []struct {
		name     string
		severity string
		issues   []testIssue
		want     string
	}{
		{"high matches", "high", []testIssue{{Severity: "high"}}, "high"},
		{"medium matches", "medium", []testIssue{{Severity: "medium"}}, "medium"},
		{"low matches", "low", []testIssue{{Severity: "low"}}, "low"},
		{"no issues low", "low", nil, "low"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := makeResult(tt.severity, tt.issues)
			got := ReconcileSeverity(result)
			if got != tt.want {
				t.Errorf("ReconcileSeverity() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReconcileSeverity_EscalatesFromLowToHigh(t *testing.T) {
	result := makeResult("low", []testIssue{
		{Severity: "low"},
		{Severity: "high"},
	})
	got := ReconcileSeverity(result)
	if got != "high" {
		t.Errorf("expected reconciled severity 'high', got %q", got)
	}
}

func TestReconcileSeverity_EscalatesFromLowToMedium(t *testing.T) {
	result := makeResult("low", []testIssue{
		{Severity: "medium"},
	})
	got := ReconcileSeverity(result)
	if got != "medium" {
		t.Errorf("expected reconciled severity 'medium', got %q", got)
	}
}

func TestReconcileSeverity_NeverLowers(t *testing.T) {
	// AI says "high" but all issues are "low" — we trust the AI's higher assessment
	result := makeResult("high", []testIssue{
		{Severity: "low"},
		{Severity: "low"},
	})
	got := ReconcileSeverity(result)
	if got != "high" {
		t.Errorf("reconcile should never lower severity, got %q", got)
	}
}

func TestReconcileSeverity_NonCanonicalTopLevelFailsSafeToHigh(t *testing.T) {
	// A model that escalates with an out-of-vocabulary label (or emits garbage)
	// must NOT be silently downgraded to low and APPROVEd. Top-level
	// non-canonical → high.
	for _, sev := range []string{"critical", "blocker", "severe", "CRITICAL", "🔥", "definitely-bad"} {
		got := ReconcileSeverity(makeResult(sev, nil))
		if got != "high" {
			t.Errorf("ReconcileSeverity(top-level %q) = %q, want high (fail-safe)", sev, got)
		}
	}
}

func TestReconcileSeverity_NonCanonicalPerIssueStaysTolerant(t *testing.T) {
	// Stray below-the-bar issue labels must not escalate the whole review.
	result := makeResult("low", []testIssue{
		{Severity: "info"},
		{Severity: "nit"},
		{Severity: "trivial"},
	})
	if got := ReconcileSeverity(result); got != "low" {
		t.Errorf("non-canonical per-issue severities should not escalate; got %q, want low", got)
	}
}

func TestReconcileSeverity_CaseInsensitiveCanonicalNotEscalated(t *testing.T) {
	// "High"/"Medium" are canonical (case-insensitive) and must keep their
	// own rank, not be treated as non-canonical.
	if got := ReconcileSeverity(makeResult("High", nil)); got != "high" {
		t.Errorf(`ReconcileSeverity("High") = %q, want high`, got)
	}
	if got := ReconcileSeverity(makeResult("Medium", nil)); got != "medium" {
		t.Errorf(`ReconcileSeverity("Medium") = %q, want medium`, got)
	}
}

func TestReconcileSeverity_EmptyTopLevelStaysLow(t *testing.T) {
	// Empty top-level severity is the documented no-issues default (parseResult
	// coerces a missing severity to "low" upstream). It must NOT fail-safe to
	// high — that would over-block legitimately clean reviews.
	if got := ReconcileSeverity(makeResult("", nil)); got != "low" {
		t.Errorf(`ReconcileSeverity("") = %q, want low (no-issues default, not fail-safe)`, got)
	}
	// A real per-issue severity still escalates an empty top-level.
	if got := ReconcileSeverity(makeResult("", []testIssue{{Severity: "high"}})); got != "high" {
		t.Errorf(`empty top-level with a high issue should escalate; got %q, want high`, got)
	}
}

func TestReconcileSeverity_WhitespacePaddedCanonicalNotEscalated(t *testing.T) {
	// Padded canonical values must rank by their trimmed value, not be treated
	// as non-canonical (which would wrongly escalate " low " to high).
	for in, want := range map[string]string{" high ": "high", " low ": "low", "  medium": "medium"} {
		if got := ReconcileSeverity(makeResult(in, nil)); got != want {
			t.Errorf("ReconcileSeverity(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReconcileSeverity_NonCanonicalTopLevelNeverCaps(t *testing.T) {
	// The fail-safe only ever raises to high; combined with any mix of per-issue
	// severities it must stay high, never be capped below it.
	for _, issues := range [][]testIssue{
		nil,
		{{Severity: "low"}, {Severity: "low"}},
		{{Severity: "high"}},
		{{Severity: "medium"}, {Severity: "info"}},
	} {
		if got := ReconcileSeverity(makeResult("critical", issues)); got != "high" {
			t.Errorf("non-canonical top-level must stay high regardless of issues %v; got %q", issues, got)
		}
	}
}

func TestReconcileSeverity_ExtraAttrsAppendedToWarnings(t *testing.T) {
	// extraAttrs let callers attach a correlation id (repo + PR number) so a
	// fail-safe / reconciliation warning can be tied back to the review that
	// triggered it (#557). Both warn paths must carry the fields.
	cases := []struct {
		name   string
		result *executor.ReviewResult
	}{
		{
			// Non-canonical top-level → "failing safe to high" warning.
			"non-canonical fail-safe",
			makeResult("critical", nil),
		},
		{
			// Canonical top-level raised by a higher per-issue severity →
			// "severity reconciled (AI inconsistency)" warning.
			"ai inconsistency",
			makeResult("low", []testIssue{{Severity: "high"}}),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
			defer slog.SetDefault(prev)

			ReconcileSeverity(tc.result, "repo", "owner/heimdallm", "pr_number", 42)

			out := buf.String()
			if !strings.Contains(out, "level=WARN") {
				t.Fatalf("expected a WARN line, got: %q", out)
			}
			if !strings.Contains(out, "repo=owner/heimdallm") {
				t.Errorf("warn line missing repo correlation field; got: %q", out)
			}
			if !strings.Contains(out, "pr_number=42") {
				t.Errorf("warn line missing pr_number correlation field; got: %q", out)
			}
		})
	}
}

func TestReconcileThenEvent_CriticalBlocks(t *testing.T) {
	// End-to-end gate: a "critical" top-level severity must drive REQUEST_CHANGES,
	// not a silent APPROVE (the bug in #547).
	reconciled := ReconcileSeverity(makeResult("critical", nil))
	if event := SeverityToEvent(reconciled); event != "REQUEST_CHANGES" {
		t.Errorf("critical severity must block: SeverityToEvent(%q) = %q, want REQUEST_CHANGES", reconciled, event)
	}
}

// --- Tests for ApplySignalEscalation ---

func TestApplySignalEscalation_MediumWithBlocker(t *testing.T) {
	got := ApplySignalEscalation("medium", CommentSignals{HasBlockerKeywords: true, Urgency: 3})
	if got != "high" {
		t.Errorf("medium + blocker should escalate to high, got %q", got)
	}
}

func TestApplySignalEscalation_MediumWithoutBlocker(t *testing.T) {
	got := ApplySignalEscalation("medium", CommentSignals{Urgency: 2})
	if got != "medium" {
		t.Errorf("medium + urgency 2 should stay medium, got %q", got)
	}
}

func TestApplySignalEscalation_LowWithBlocker(t *testing.T) {
	// Low is never escalated by signals — conservative design
	got := ApplySignalEscalation("low", CommentSignals{HasBlockerKeywords: true, Urgency: 3})
	if got != "low" {
		t.Errorf("low should never be escalated by signals, got %q", got)
	}
}

func TestApplySignalEscalation_HighUnchanged(t *testing.T) {
	got := ApplySignalEscalation("high", CommentSignals{})
	if got != "high" {
		t.Errorf("high should stay high, got %q", got)
	}
}

// --- Tests for SeverityToEvent ---

func TestSeverityToEvent_HighRequestsChanges(t *testing.T) {
	got := SeverityToEvent("high")
	if got != "REQUEST_CHANGES" {
		t.Errorf("high severity should REQUEST_CHANGES, got %q", got)
	}
}

func TestSeverityToEvent_MediumApproves(t *testing.T) {
	got := SeverityToEvent("medium")
	if got != "APPROVE" {
		t.Errorf("medium severity should APPROVE, got %q", got)
	}
}

func TestSeverityToEvent_LowApproves(t *testing.T) {
	got := SeverityToEvent("low")
	if got != "APPROVE" {
		t.Errorf("low severity should APPROVE, got %q", got)
	}
}

// --- Fix 1: Verify escalation is persisted correctly (integration scenario) ---

func TestEscalationPersistenceScenario(t *testing.T) {
	// Simulate the pipeline flow: medium severity + blocker signals → stored as high
	signals := ExtractCommentSignals([]github.Comment{
		{Author: "reviewer", Body: "This is a blocker for release"},
	}, "author")
	reconciled := "medium" // AI said medium
	finalSeverity := ApplySignalEscalation(reconciled, signals)

	// This is what gets stored in the DB
	if finalSeverity != "high" {
		t.Fatalf("expected stored severity 'high', got %q", finalSeverity)
	}

	// On retry, PublishPending uses stored severity with no signals
	event := SeverityToEvent(finalSeverity)
	if event != "REQUEST_CHANGES" {
		t.Errorf("retry should reproduce REQUEST_CHANGES from stored severity, got %q", event)
	}
}

// --- Helper ---

type testIssue struct {
	Severity string
}

func makeResult(severity string, issues []testIssue) *executor.ReviewResult {
	var execIssues []executor.Issue
	for _, iss := range issues {
		execIssues = append(execIssues, executor.Issue{Severity: iss.Severity})
	}
	return &executor.ReviewResult{
		Severity: severity,
		Issues:   execIssues,
	}
}

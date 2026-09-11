package pipeline

import (
	"strings"
	"testing"

	"github.com/heimdallm/daemon/internal/executor"
)

func TestBuildIssueCommentPreservesSeverityAndOptionalLocation(t *testing.T) {
	tests := []struct {
		name      string
		issue     executor.Issue
		fragments []string
		absent    []string
	}{
		{
			name: "high with file and line",
			issue: executor.Issue{
				Severity: "high", File: "internal/auth.go", Line: 41,
				Description: "token validation is bypassed",
			},
			fragments: []string{"🔴 HIGH Issue", "token validation is bypassed", "`internal/auth.go` line 41", "🔴 *Severity: **HIGH***"},
		},
		{
			name: "low with file only",
			issue: executor.Issue{
				Severity: "low", File: "README.md", Description: "example is stale",
			},
			fragments: []string{"🟡 LOW Issue", "`README.md`", "🟡 *Severity: **LOW***"},
			absent:    []string{" line 0"},
		},
		{
			name: "default severity without location",
			issue: executor.Issue{
				Severity: "unknown", Description: "review manually",
			},
			fragments: []string{"⚠️ MEDIUM Issue", "review manually", "⚠️ *Severity: **MEDIUM***"},
			absent:    []string{"**Location:**"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := buildIssueComment(tc.issue)
			for _, fragment := range tc.fragments {
				if !strings.Contains(body, fragment) {
					t.Errorf("comment missing %q:\n%s", fragment, body)
				}
			}
			for _, fragment := range tc.absent {
				if strings.Contains(body, fragment) {
					t.Errorf("comment unexpectedly contains %q:\n%s", fragment, body)
				}
			}
			if !strings.Contains(body, "Reviewed by [Heimdallm]") {
				t.Errorf("comment missing provenance footer:\n%s", body)
			}
			if strings.Contains(body, "Heimdallm AI Review") {
				t.Errorf("comment should not carry the old heading:\n%s", body)
			}
		})
	}
}

func TestBuildMultiSummaryBodyReportsIssueCountAndSeverity(t *testing.T) {
	result := &executor.ReviewResult{
		Summary:  "Two actionable findings.",
		Severity: "high",
		Issues: []executor.Issue{
			{Description: "first"},
			{Description: "second"},
		},
	}
	body := buildMultiSummaryBody(result)
	for _, fragment := range []string{"Two actionable findings.", "2 issue(s) found", "🔴 *Severity: **HIGH***", "Reviewed by [Heimdallm]"} {
		if !strings.Contains(body, fragment) {
			t.Errorf("summary missing %q:\n%s", fragment, body)
		}
	}
	if strings.Contains(body, "Heimdallm AI Review") {
		t.Errorf("summary should not carry the old heading:\n%s", body)
	}

	withoutIssues := buildMultiSummaryBody(&executor.ReviewResult{Summary: "Clean.", Severity: "low"})
	if strings.Contains(withoutIssues, "issue(s) found") {
		t.Fatalf("zero-issue summary reported findings:\n%s", withoutIssues)
	}
}

func TestSeverityIconAndLabel(t *testing.T) {
	cases := []struct {
		severity string
		icon     string
		label    string
	}{
		{"high", "🔴", "HIGH"},
		{"HIGH", "🔴", "HIGH"}, // case-insensitive
		{"medium", "⚠️", "MEDIUM"},
		{"low", "🟡", "LOW"},
		{"", "⚠️", "MEDIUM"},         // missing severity defaults to medium
		{"critical", "⚠️", "MEDIUM"}, // non-canonical value defaults to medium
	}
	for _, tc := range cases {
		if got := severityIcon(tc.severity); got != tc.icon {
			t.Errorf("severityIcon(%q) = %q, want %q", tc.severity, got, tc.icon)
		}
		if got := severityLabel(tc.severity); got != tc.label {
			t.Errorf("severityLabel(%q) = %q, want %q", tc.severity, got, tc.label)
		}
	}
}

func TestReviewFooter(t *testing.T) {
	// Empty severity (LGTM bodies) omits the severity line entirely.
	lgtmFooter := reviewFooter("")
	if !strings.Contains(lgtmFooter, "Reviewed by [Heimdallm](https://theburrowhub.github.io/heimdallm/)") {
		t.Errorf("footer missing attribution link: %q", lgtmFooter)
	}
	if strings.Contains(lgtmFooter, "Severity:") {
		t.Errorf("empty-severity footer should omit the severity line: %q", lgtmFooter)
	}

	highFooter := reviewFooter("high")
	if !strings.HasSuffix(highFooter, "🔴 *Severity: **HIGH***") {
		t.Errorf("high footer should end with the red severity badge: %q", highFooter)
	}
	if !strings.Contains(highFooter, "---\n🤖 *Reviewed by") {
		t.Errorf("footer should start with the rule and attribution line: %q", highFooter)
	}
}

// TestReviewFooterVersionSuffix locks in the three shapes versionSuffix must
// produce: a bare semver (what goreleaser's {{.Version}} and the Makefile's
// GIT_VERSION both stamp) gets its "v" back, a non-numeric build tag (the
// Makefile's "dev" default, or a git-describe-style "main-<sha>") is shown
// verbatim, and no wiring at all (SetDaemonVersion never called) omits the
// suffix rather than guessing.
//
// NOTE: this mutates the package-global daemonVersion via SetDaemonVersion.
// Safe only because no test in this package calls t.Parallel() — see
// comment_signals_test.go's TestReconcileSeverity_LogAttrsThreadedIntoWarnLines
// for the same caveat with slog's default logger. defer restores the
// zero-value state every other test in this file expects.
func TestReviewFooterVersionSuffix(t *testing.T) {
	defer SetDaemonVersion("")

	SetDaemonVersion("")
	if got := reviewFooter(""); !strings.HasSuffix(got, heimdallmURL+")*") {
		t.Errorf("no version wired: footer should end right after the link, with no version suffix, got %q", got)
	}

	SetDaemonVersion("0.8.22")
	if got := reviewFooter(""); !strings.Contains(got, "Reviewed by [Heimdallm](https://theburrowhub.github.io/heimdallm/) (v0.8.22)*") {
		t.Errorf("semver should be reported as v0.8.22 right after the link, got %q", got)
	}

	SetDaemonVersion("0.8.22-3-gab12cd-dirty")
	if got := reviewFooter(""); !strings.Contains(got, "(v0.8.22-3-gab12cd-dirty)") {
		t.Errorf("a dirty git-describe version must be reported verbatim (with the v prefix), got %q", got)
	}

	SetDaemonVersion("dev")
	if got := reviewFooter(""); !strings.Contains(got, "(dev)") {
		t.Errorf("a non-numeric build tag should be reported verbatim, without an invented v prefix, got %q", got)
	}
}

// TestReviewFooterVersionAppearsInAllPublishedBodyShapes guards that the
// version suffix reaches every body Heimdallm posts — not just the review
// footer helper itself — since buildIssueComment and buildMultiSummaryBody
// are separate call sites a future refactor could bypass.
func TestReviewFooterVersionAppearsInAllPublishedBodyShapes(t *testing.T) {
	defer SetDaemonVersion("")
	SetDaemonVersion("0.8.22")

	issueBody := buildIssueComment(executor.Issue{Severity: "high", Description: "x"})
	if !strings.Contains(issueBody, "(v0.8.22)") {
		t.Errorf("buildIssueComment should carry the version suffix, got %q", issueBody)
	}

	summaryBody := buildMultiSummaryBody(&executor.ReviewResult{Summary: "s", Severity: "high"})
	if !strings.Contains(summaryBody, "(v0.8.22)") {
		t.Errorf("buildMultiSummaryBody should carry the version suffix, got %q", summaryBody)
	}
}

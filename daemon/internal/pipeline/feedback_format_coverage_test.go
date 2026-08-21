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
			fragments: []string{"🔴 HIGH Issue", "token validation is bypassed", "`internal/auth.go` line 41"},
		},
		{
			name: "low with file only",
			issue: executor.Issue{
				Severity: "low", File: "README.md", Description: "example is stale",
			},
			fragments: []string{"🟡 LOW Issue", "`README.md`"},
			absent:    []string{" line 0"},
		},
		{
			name: "default severity without location",
			issue: executor.Issue{
				Severity: "unknown", Description: "review manually",
			},
			fragments: []string{"⚠️ MEDIUM Issue", "review manually"},
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
			if !strings.Contains(body, "Posted by Heimdallm AI Review") {
				t.Errorf("comment missing provenance footer:\n%s", body)
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
	for _, fragment := range []string{"Two actionable findings.", "2 issue(s) found", "Severity: **HIGH**"} {
		if !strings.Contains(body, fragment) {
			t.Errorf("summary missing %q:\n%s", fragment, body)
		}
	}

	withoutIssues := buildMultiSummaryBody(&executor.ReviewResult{Summary: "Clean.", Severity: "low"})
	if strings.Contains(withoutIssues, "issue(s) found") {
		t.Fatalf("zero-issue summary reported findings:\n%s", withoutIssues)
	}
}

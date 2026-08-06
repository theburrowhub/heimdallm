package tui

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/theburrowhub/heimdallm/cli/internal/api"
)

func TestBuildPRDetailLinesIncludesReviewDetails(t *testing.T) {
	updatedAt := time.Date(2026, time.August, 6, 10, 11, 0, 0, time.UTC)
	reviewedAt := time.Date(2026, time.August, 6, 10, 20, 0, 0, time.UTC)
	pr := api.PR{
		Number:    42,
		Repo:      "acme/widgets",
		Title:     "Make retries observable",
		Author:    "alice",
		State:     "open",
		URL:       "https://github.example/acme/widgets/pull/42",
		UpdatedAt: updatedAt,
		LatestReview: &api.Review{
			Severity:    "high",
			CreatedAt:   reviewedAt,
			CLIUsed:     "codex",
			HeadSHA:     "abcdef0123456789",
			Summary:     "summary words that demonstrate wrapping across lines",
			Issues:      "first issue\nsecond issue",
			Suggestions: "add a regression test",
		},
	}

	out := strings.Join(buildPRDetailLines(pr, 30), "\n")

	assertDetailContains(t, out,
		"PR #42 — acme/widgets",
		"Title:       Make retries observable",
		"Author:      alice",
		"State:       open",
		"URL:         https://github.example/acme/widgets/pull/42",
		"Updated:     2026-08-06 10:11",
		"Latest Review",
		"Severity:    high",
		"Reviewed:    2026-08-06 10:20",
		"CLI:         codex",
		"SHA:         abcdef01",
		"Summary",
		"    summary words that demonstrate",
		"    wrapping across lines",
		"Issues",
		"    first issue",
		"    second issue",
		"Suggestions",
		"    add a regression test",
	)
	if strings.Contains(out, pr.LatestReview.HeadSHA) {
		t.Fatalf("detail contains the full SHA instead of its short form:\n%s", out)
	}
}

func TestBuildPRDetailLinesOmitsAbsentReview(t *testing.T) {
	pr := api.PR{
		Number:    7,
		Repo:      "acme/api",
		Title:     "Document rate limits",
		Author:    "bob",
		State:     "closed",
		URL:       "https://github.example/acme/api/pull/7",
		UpdatedAt: time.Date(2026, time.July, 1, 8, 30, 0, 0, time.UTC),
	}

	out := strings.Join(buildPRDetailLines(pr, 100), "\n")

	assertDetailContains(t, out,
		"PR #7 — acme/api",
		"Title:       Document rate limits",
		"Updated:     2026-07-01 08:30",
	)
	assertDetailOmits(t, out, "Latest Review", "Severity:", "Reviewed:", "Summary", "Issues", "Suggestions")
}

func TestBuildIssueDetailLinesIncludesReviewAndTruncatesBodyByRune(t *testing.T) {
	createdAt := time.Date(2026, time.June, 2, 9, 15, 0, 0, time.UTC)
	reviewedAt := time.Date(2026, time.June, 3, 14, 45, 0, 0, time.UTC)
	visibleBody := strings.Repeat("á", 499) + "界"
	issue := api.Issue{
		Number:    81,
		Repo:      "acme/mobile",
		Title:     "Handle offline startup",
		Body:      visibleBody + "SECRET",
		Author:    "carol",
		Labels:    json.RawMessage(`["bug","help wanted"]`),
		State:     "open",
		CreatedAt: createdAt,
		Dismissed: true,
		LatestReview: &api.IssueReview{
			CLIUsed:     "claude",
			Summary:     "implementation is safe to schedule",
			Triage:      json.RawMessage(`{"severity":"high","owner":"platform","score":4}`),
			ActionTaken: "auto_implement",
			PRCreated:   77,
			CreatedAt:   reviewedAt,
		},
	}

	out := strings.Join(buildIssueDetailLines(issue, 30), "\n")

	assertDetailContains(t, out,
		"Issue #81 — acme/mobile",
		"Title:       Handle offline startup",
		"Author:      carol",
		"State:       open",
		"Created:     2026-06-02 09:15",
		"Dismissed:   yes",
		"Labels:      bug, help wanted",
		"Description",
		visibleBody+"…",
		"Latest Review",
		"Action:      → PR #77",
		"Reviewed:    2026-06-03 14:45",
		"CLI:         claude",
		"PR Created:  #77",
		"Triage",
		"severity:        high",
		"owner:           platform",
		"score:           4",
		"Summary",
		"    implementation is safe to schedule",
	)
	if strings.Contains(out, "SECRET") {
		t.Fatalf("description contains content after the 500-rune limit:\n%s", out)
	}
}

func TestBuildIssueDetailLinesOmitsAbsentOptionalFields(t *testing.T) {
	issue := api.Issue{
		Number:    9,
		Repo:      "acme/backend",
		Title:     "Clarify health response",
		Author:    "dave",
		State:     "closed",
		CreatedAt: time.Date(2026, time.May, 4, 12, 0, 0, 0, time.UTC),
	}

	out := strings.Join(buildIssueDetailLines(issue, 100), "\n")

	assertDetailContains(t, out,
		"Issue #9 — acme/backend",
		"Title:       Clarify health response",
		"Created:     2026-05-04 12:00",
	)
	assertDetailOmits(t, out, "Dismissed:", "Labels:", "Description", "Latest Review", "Triage", "Summary")
}

func TestBuildIssueDetailLinesIgnoresInvalidTriage(t *testing.T) {
	issue := api.Issue{
		Number: 10,
		Repo:   "acme/backend",
		Title:  "Malformed metadata",
		LatestReview: &api.IssueReview{
			ActionTaken: "refinement",
			Triage:      json.RawMessage(`{"severity":`),
		},
	}

	out := strings.Join(buildIssueDetailLines(issue, 80), "\n")

	assertDetailContains(t, out, "Latest Review", "Action:      Refined")
	assertDetailOmits(t, out, "Triage", "severity:")
}

func TestWrapText(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		width int
		want  []string
	}{
		{
			name:  "non-positive width leaves text intact",
			text:  "alpha beta",
			width: 0,
			want:  []string{"alpha beta"},
		},
		{
			name:  "preserves blank paragraph",
			text:  "alpha\n\nbeta",
			width: 10,
			want:  []string{"alpha", "", "beta"},
		},
		{
			name:  "normalizes whitespace-only paragraph to blank",
			text:  "alpha\n   \nbeta",
			width: 10,
			want:  []string{"alpha", "", "beta"},
		},
		{
			name:  "keeps words at exact limit and wraps following words",
			text:  "one two three four",
			width: 7,
			want:  []string{"one two", "three", "four"},
		},
		{
			name:  "does not split an overlong word",
			text:  "abcdefgh next",
			width: 4,
			want:  []string{"abcdefgh", "next"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := wrapText(tt.text, tt.width); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("wrapText(%q, %d) = %#v, want %#v", tt.text, tt.width, got, tt.want)
			}
		})
	}
}

func assertDetailContains(t *testing.T, detail string, want ...string) {
	t.Helper()
	for _, fragment := range want {
		if !strings.Contains(detail, fragment) {
			t.Errorf("detail does not contain %q:\n%s", fragment, detail)
		}
	}
}

func assertDetailOmits(t *testing.T, detail string, unwanted ...string) {
	t.Helper()
	for _, fragment := range unwanted {
		if strings.Contains(detail, fragment) {
			t.Errorf("detail unexpectedly contains %q:\n%s", fragment, detail)
		}
	}
}

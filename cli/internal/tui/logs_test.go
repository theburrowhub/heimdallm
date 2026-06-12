package tui

import (
	"testing"

	"github.com/theburrowhub/heimdallm/cli/internal/api"
)

func TestHumanize(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"review_started", "Review started"},
		{"issue_implemented", "Issue implemented"},
		{"pr_detected", "Pr detected"},
		{"DETECTED", "Detected"},
		{"review", "Review"},
		{"", ""},
		{"a", "A"},
		{"polling_completed", "Polling completed"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := humanize(tc.in)
			if got != tc.want {
				t.Fatalf("humanize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestAppendDetail(t *testing.T) {
	if got := appendDetail("", "severity=high"); got != "severity=high" {
		t.Fatalf("appendDetail empty: got %q", got)
	}
	if got := appendDetail("by @alice", "severity=high"); got != "by @alice  severity=high" {
		t.Fatalf("appendDetail join: got %q", got)
	}
}

func TestSseToLogLine_HumanizedActions(t *testing.T) {
	cases := []struct {
		name       string
		event      api.SSEEvent
		wantAction string
		wantBadge  string
	}{
		{"pr_detected", api.SSEEvent{Type: "pr_detected", Data: `{"repo":"acme/foo","pr_number":1}`}, "Detected", "PR"},
		{"review_started", api.SSEEvent{Type: "review_started", Data: `{"repo":"acme/foo","pr_number":1}`}, "Review ▶", "PR"},
		{"review_completed", api.SSEEvent{Type: "review_completed", Data: `{"repo":"acme/foo","pr_number":1,"severity":"high"}`}, "Review ✓", "PR"},
		{"review_error", api.SSEEvent{Type: "review_error", Data: `{"repo":"acme/foo","pr_number":1,"error":"timeout"}`}, "Review ✗", "PR"},
		{"issue_detected", api.SSEEvent{Type: "issue_detected", Data: `{"repo":"acme/foo","issue_number":7}`}, "Detected", "ISSUE"},
		{"issue_promoted", api.SSEEvent{Type: "issue_promoted", Data: `{"repo":"acme/foo","issue_number":7,"from_label":"a","to_label":"b"}`}, "Promoted", "ISSUE"},
		{"repo_discovered", api.SSEEvent{Type: "repo_discovered", Data: `{"repo":"acme/foo"}`}, "Discovered", "REPO"},
		{"unknown_type", api.SSEEvent{Type: "polling_started", Data: `{"kind":"prs","repos":["a"]}`}, "Polling started", "EVENT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sseToLogLine(tc.event)
			if got.Action != tc.wantAction {
				t.Errorf("action: got %q want %q", got.Action, tc.wantAction)
			}
			if got.Badge != tc.wantBadge {
				t.Errorf("badge: got %q want %q", got.Badge, tc.wantBadge)
			}
		})
	}
}

func TestSseToLogLine_ShowsTitle(t *testing.T) {
	evt := api.SSEEvent{
		Type: "review_completed",
		Data: `{"repo":"acme/web","pr_number":42,"pr_title":"Fix login bug","severity":"low"}`,
	}
	got := sseToLogLine(evt)
	if got.Target != "acme/web #42 Fix login bug" {
		t.Fatalf("target with title: got %q", got.Target)
	}
}

func TestSseToLogLine_ShowsIssueTitle(t *testing.T) {
	evt := api.SSEEvent{
		Type: "issue_detected",
		Data: `{"repo":"acme/web","issue_number":7,"issue_title":"Refactor auth"}`,
	}
	got := sseToLogLine(evt)
	if got.Target != "acme/web #7 Refactor auth" {
		t.Fatalf("target with issue title: got %q", got.Target)
	}
}

func TestSseToLogLine_ShowsAuthor(t *testing.T) {
	evt := api.SSEEvent{
		Type: "pr_detected",
		Data: `{"repo":"acme/web","pr_number":42,"author":"alice"}`,
	}
	got := sseToLogLine(evt)
	if got.Details != "by @alice" {
		t.Fatalf("author in details: got %q", got.Details)
	}
}

func TestSseToLogLine_AuthorBeforeExistingDetails(t *testing.T) {
	evt := api.SSEEvent{
		Type: "review_completed",
		Data: `{"repo":"acme/web","pr_number":42,"severity":"high","author":"bob"}`,
	}
	got := sseToLogLine(evt)
	if got.Details != "by @bob  severity=high" {
		t.Fatalf("author+details: got %q", got.Details)
	}
}

func TestActivityToLogLine_HumanizedAction(t *testing.T) {
	entry := api.ActivityEntry{
		TS:         "2024-01-15T10:30:00Z",
		Repo:       "acme/web",
		ItemType:   "pr",
		ItemNumber: 42,
		ItemTitle:  "Fix login",
		Action:     "review",
		Outcome:    "success",
		Details:    map[string]any{"severity": "high"},
	}
	got := activityToLogLine(entry)
	if got.Action != "Review ✓" {
		t.Fatalf("action: got %q want %q", got.Action, "Review ✓")
	}
	if got.Target != "acme/web #42 Fix login" {
		t.Fatalf("target: got %q", got.Target)
	}
}

func TestActivityToLogLine_ShowsAuthorFromDetails(t *testing.T) {
	entry := api.ActivityEntry{
		TS:         "2024-01-15T10:30:00Z",
		Repo:       "acme/web",
		ItemType:   "issue",
		ItemNumber: 7,
		Action:     "triage",
		Outcome:    "completed",
		Details:    map[string]any{"author": "carol", "severity": "medium"},
	}
	got := activityToLogLine(entry)
	if got.Details != "by @carol  severity=medium" {
		t.Fatalf("details with author: got %q", got.Details)
	}
}

func TestActivityToLogLine_TitleTruncated(t *testing.T) {
	entry := api.ActivityEntry{
		TS:         "2024-01-15T10:30:00Z",
		Repo:       "acme/web",
		ItemType:   "pr",
		ItemNumber: 1,
		ItemTitle:  "This is an extremely long title that exceeds the truncation limit for display purposes",
		Action:     "review",
		Outcome:    "success",
		Details:    map[string]any{},
	}
	got := activityToLogLine(entry)
	if len([]rune(got.Target)) > len("acme/web #1 ")+40 {
		t.Fatalf("target too long: %q", got.Target)
	}
}

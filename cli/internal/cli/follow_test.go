package cli

import (
	"strings"
	"testing"
)

func TestEventCategory(t *testing.T) {
	tests := []struct {
		eventType string
		want      string
	}{
		{"pr_detected", "pr"},
		{"pr_state_changed", "pr"},
		{"review_started", "pr"},
		{"review_completed", "pr"},
		{"review_error", "pr"},
		{"review_skipped", "pr"},
		{"circuit_breaker_tripped", "pr"},
		{"issue_detected", "issue"},
		{"issue_review_started", "issue"},
		{"issue_review_completed", "issue"},
		{"issue_implemented", "issue"},
		{"issue_review_error", "issue"},
		{"issue_promoted", "issue"},
		{"issue_state_changed", "issue"},
		{"repo_discovered", "system"},
		{"unknown_event", "system"},
	}
	for _, tt := range tests {
		if got := eventCategory(tt.eventType); got != tt.want {
			t.Errorf("eventCategory(%q) = %q, want %q", tt.eventType, got, tt.want)
		}
	}
}

func TestExtractRepo(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{"valid repo", `{"repo":"org/name","pr_number":1}`, "org/name"},
		{"no repo field", `{"pr_number":1}`, ""},
		{"invalid json", `not json`, ""},
		{"empty data", `{}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractRepo(tt.data); got != tt.want {
				t.Errorf("extractRepo() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatEventData(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string // substring that must appear
	}{
		{
			"pr review completed",
			`{"repo":"acme/web","pr_number":42,"pr_title":"Fix login","severity":"high"}`,
			"acme/web",
		},
		{
			"pr number shown",
			`{"repo":"acme/web","pr_number":42,"pr_title":"Fix login"}`,
			"#42",
		},
		{
			"pr title shown",
			`{"repo":"acme/web","pr_number":42,"pr_title":"Fix login"}`,
			"Fix login",
		},
		{
			"issue number shown",
			`{"repo":"acme/web","issue_number":7,"issue_title":"Bug report"}`,
			"#7",
		},
		{
			"issue title shown",
			`{"repo":"acme/web","issue_number":7,"issue_title":"Bug report"}`,
			"Bug report",
		},
		{
			"severity shown",
			`{"repo":"acme/web","pr_number":1,"severity":"medium"}`,
			"[medium]",
		},
		{
			"error shown",
			`{"repo":"acme/web","pr_number":1,"error":"timeout"}`,
			"timeout",
		},
		{
			"promotion labels shown",
			`{"repo":"acme/web","issue_number":3,"from_label":"blocked","to_label":"ready"}`,
			"→",
		},
		{
			"chosen action shown",
			`{"repo":"acme/web","issue_number":5,"chosen_action":"implement"}`,
			"implement",
		},
		{
			"invalid json returns raw",
			`not json at all`,
			"not json at all",
		},
		{
			"empty object returns raw",
			`{}`,
			"{}",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatEventData(tt.data)
			if !strings.Contains(got, tt.want) {
				t.Errorf("formatEventData() = %q, want substring %q", got, tt.want)
			}
		})
	}
}

func TestToInt(t *testing.T) {
	tests := []struct {
		name string
		v    any
		want int
	}{
		{"float64", float64(42), 42},
		{"int", int(7), 7},
		{"int64", int64(99), 99},
		{"string", "nope", 0},
		{"nil", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toInt(tt.v); got != tt.want {
				t.Errorf("toInt(%v) = %d, want %d", tt.v, got, tt.want)
			}
		})
	}
}


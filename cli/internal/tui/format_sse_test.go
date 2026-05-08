package tui

import "testing"

func TestFormatSSEData(t *testing.T) {
	tests := []struct {
		name      string
		eventType string
		data      string
		wantType  string
		wantInfo  string
	}{
		{
			name:      "review_completed with repo + pr_number + severity",
			eventType: "review_completed",
			data:      `{"repo":"acme/foo","pr_number":42,"severity":"high"}`,
			wantType:  "pr",
			wantInfo:  "acme/foo PR #42 [high]",
		},
		{
			name:      "issue_review_completed with repo + issue_number",
			eventType: "issue_review_completed",
			data:      `{"repo":"acme/foo","issue_number":7}`,
			wantType:  "issue",
			wantInfo:  "acme/foo Issue #7",
		},
		{
			name:      "polling_started renders kind + repo count",
			eventType: "polling_started",
			data:      `{"kind":"prs","repos":["acme/foo","acme/bar"]}`,
			wantType:  "",
			wantInfo:  "prs (2 repos)",
		},
		{
			name:      "polling_completed renders kind + count + duration",
			eventType: "polling_completed",
			data:      `{"kind":"issues","count":5,"duration_ms":800}`,
			wantType:  "",
			wantInfo:  "issues 5 items in 800ms",
		},
		{
			name:      "polling_started with empty repos list",
			eventType: "polling_started",
			data:      `{"kind":"prs","repos":[]}`,
			wantType:  "",
			wantInfo:  "prs (0 repos)",
		},
		{
			name:      "unknown event with no recognizable fields falls back to raw",
			eventType: "mystery",
			data:      `{"foo":"bar"}`,
			wantType:  "",
			wantInfo:  `{"foo":"bar"}`,
		},
		{
			name:      "malformed JSON returns raw data",
			eventType: "polling_started",
			data:      "not json",
			wantType:  "",
			wantInfo:  "not json",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotType, gotInfo := formatSSEData(tt.eventType, tt.data)
			if gotType != tt.wantType {
				t.Errorf("type: got %q want %q", gotType, tt.wantType)
			}
			if gotInfo != tt.wantInfo {
				t.Errorf("info: got %q want %q", gotInfo, tt.wantInfo)
			}
		})
	}
}

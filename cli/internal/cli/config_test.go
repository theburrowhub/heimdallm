package cli

import (
	"strings"
	"testing"
)

func TestCfgEmpty(t *testing.T) {
	tests := []struct {
		name string
		val  any
		want bool
	}{
		{"nil", nil, true},
		{"empty string", "", true},
		{"non-empty string", "hello", false},
		{"zero float", float64(0), true},
		{"non-zero float", float64(42), false},
		{"false bool", false, true},
		{"true bool", true, false},
		{"empty slice", []any{}, true},
		{"non-empty slice", []any{"a"}, false},
		{"empty map", map[string]any{}, true},
		{"non-empty map", map[string]any{"k": "v"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cfgEmpty(tt.val); got != tt.want {
				t.Errorf("cfgEmpty(%v) = %v, want %v", tt.val, got, tt.want)
			}
		})
	}
}

func TestCfgFmtVal(t *testing.T) {
	tests := []struct {
		val  any
		want string
	}{
		{float64(7842), "7842"},
		{float64(1.5), "1.5"},
		{"5m", "5m"},
		{true, "true"},
		{false, "false"},
	}
	for _, tt := range tests {
		if got := cfgFmtVal(tt.val); got != tt.want {
			t.Errorf("cfgFmtVal(%v) = %q, want %q", tt.val, got, tt.want)
		}
	}
}

func TestCfgServerLines(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	cfg := map[string]any{
		"server_port":                 float64(7842),
		"poll_interval":               "5m",
		"retention_days":              float64(90),
		"activity_log_enabled":        true,
		"activity_log_retention_days": float64(90),
	}
	lines := cfgServerLines(cfg)
	if len(lines) != 5 {
		t.Fatalf("got %d lines, want 5", len(lines))
	}
	if !strings.Contains(lines[0], "Port:") || !strings.Contains(lines[0], "7842") {
		t.Errorf("line[0] = %q, want Port and 7842", lines[0])
	}
	if !strings.Contains(lines[1], "Poll interval:") || !strings.Contains(lines[1], "5m") {
		t.Errorf("line[1] = %q, want Poll interval and 5m", lines[1])
	}
}

func TestCfgServerLinesHidesEmpty(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	cfg := map[string]any{
		"server_port":                 float64(7842),
		"poll_interval":               "",
		"retention_days":              float64(0),
		"activity_log_enabled":        false,
		"activity_log_retention_days": float64(0),
	}
	lines := cfgServerLines(cfg)
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1 (only port): %v", len(lines), lines)
	}
}

func TestCfgRepoLinesWithOverrides(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	cfg := map[string]any{
		"repositories":   []any{"org/repo1", "org/repo2"},
		"local_dir_base": []any{},
		"repo_overrides": map[string]any{
			"org/repo1": map[string]any{
				"primary":     "claude",
				"fallback":    "",
				"review_mode": "",
				"local_dir":   "/repos/repo1",
			},
		},
		"local_dirs_detected": map[string]any{},
	}
	lines := cfgRepoLines(cfg)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "org/repo1") {
		t.Error("missing repo1 in output")
	}
	if !strings.Contains(joined, "org/repo2") {
		t.Error("missing repo2 in monitored list")
	}
	if !strings.Contains(joined, "claude") {
		t.Error("missing primary=claude in repo override")
	}
	if strings.Contains(joined, "Fallback") {
		t.Error("empty fallback should be hidden")
	}
}

func TestCfgRepoLinesAutoDetected(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	cfg := map[string]any{
		"repositories":   []any{"org/myrepo"},
		"local_dir_base": []any{},
		"repo_overrides": map[string]any{},
		"local_dirs_detected": map[string]any{
			"org/myrepo": "/home/user/repos/myrepo",
		},
	}
	lines := cfgRepoLines(cfg)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Local dir (auto)") {
		t.Error("missing auto-detected local dir")
	}
	if !strings.Contains(joined, "/home/user/repos/myrepo") {
		t.Error("missing auto-detected path")
	}
}

func TestCfgAILines(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	cfg := map[string]any{
		"ai_primary":       "claude",
		"ai_fallback":      "gemini",
		"review_mode":      "single",
		"issue_prompt":     "",
		"implement_prompt": "",
		"agent_configs": map[string]any{
			"claude": map[string]any{
				"model":           "claude-sonnet-4-6",
				"max_turns":       float64(0),
				"approval_mode":   "",
				"extra_flags":     "",
				"prompt":          "",
				"effort":          "high",
				"permission_mode": "auto",
			},
		},
	}
	lines := cfgAILines(cfg)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "claude") {
		t.Error("missing primary=claude")
	}
	if !strings.Contains(joined, "gemini") {
		t.Error("missing fallback=gemini")
	}
	if !strings.Contains(joined, "Agent: claude") {
		t.Error("missing agent header")
	}
	if !strings.Contains(joined, "claude-sonnet-4-6") {
		t.Error("missing agent model")
	}
	if strings.Contains(joined, "Max turns") {
		t.Error("zero max_turns should be hidden")
	}
}

func TestCfgAILinesPRMetadata(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	cfg := map[string]any{
		"ai_primary":       "claude",
		"ai_fallback":      "",
		"review_mode":      "single",
		"issue_prompt":     "",
		"implement_prompt": "",
		"agent_configs":    map[string]any{},
		"pr_metadata": map[string]any{
			"reviewers":   []any{"alice", "bob"},
			"labels":      []any{"ai-review"},
			"pr_assignee": "charlie",
		},
	}
	lines := cfgAILines(cfg)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "alice") {
		t.Error("missing PR reviewer alice")
	}
	if !strings.Contains(joined, "ai-review") {
		t.Error("missing PR label")
	}
	if !strings.Contains(joined, "charlie") {
		t.Error("missing PR assignee")
	}
}

func TestCfgOrgLines(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	cfg := map[string]any{
		"org_overrides": map[string]any{
			"myorg": map[string]any{
				"primary":      "gemini",
				"review_mode":  "multi",
				"pr_reviewers": []any{"alice"},
				"issue_tracking": map[string]any{
					"enabled":         true,
					"develop_enabled": false,
					"develop_labels":  []any{"ready"},
				},
			},
		},
	}
	lines := cfgOrgLines(cfg)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "myorg") {
		t.Error("missing org header")
	}
	if !strings.Contains(joined, "gemini") {
		t.Error("missing org primary")
	}
	if !strings.Contains(joined, "ready") {
		t.Error("missing org issue tracking labels")
	}
	if !strings.Contains(joined, "Develop enabled") || !strings.Contains(joined, "false") {
		t.Error("missing org issue tracking develop_enabled=false")
	}
}

func TestCfgIssueTrackingLinesDisabled(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	cfg := map[string]any{
		"issue_tracking": map[string]any{
			"enabled":            false,
			"filter_mode":        "",
			"organizations":      []any{},
			"assignees":          []any{},
			"develop_labels":     []any{},
			"review_only_labels": []any{},
			"skip_labels":        []any{},
			"blocked_labels":     []any{},
			"promote_to_label":   "",
			"default_action":     "",
		},
	}
	lines := cfgIssueTrackingLines(cfg)
	if len(lines) != 0 {
		t.Errorf("disabled issue tracking should produce no lines, got %d: %v", len(lines), lines)
	}
}

func TestCfgIssueTrackingLinesEnabled(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	cfg := map[string]any{
		"issue_tracking": map[string]any{
			"enabled":        true,
			"filter_mode":    "exclusive",
			"default_action": "ignore",
			"develop_labels": []any{"implement"},
			"organizations":  []any{},
			"assignees":      []any{},
		},
	}
	lines := cfgIssueTrackingLines(cfg)
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 lines, got %d: %v", len(lines), lines)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "true") {
		t.Error("missing enabled=true")
	}
	if !strings.Contains(joined, "exclusive") {
		t.Error("missing filter_mode")
	}
	if !strings.Contains(joined, "implement") {
		t.Error("missing develop label")
	}
}

func TestCfgDiscoveryLinesEmpty(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	cfg := map[string]any{
		"non_monitored": []any{},
	}
	lines := cfgDiscoveryLines(cfg)
	if len(lines) != 0 {
		t.Errorf("empty non_monitored should produce no lines, got %d", len(lines))
	}
}

func TestCfgDiscoveryLinesPopulated(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	cfg := map[string]any{
		"non_monitored": []any{"org/old-repo", "org/archived"},
	}
	lines := cfgDiscoveryLines(cfg)
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines (header + 2 items), got %d: %v", len(lines), lines)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "org/old-repo") {
		t.Error("missing org/old-repo")
	}
	if !strings.Contains(joined, "org/archived") {
		t.Error("missing org/archived")
	}
}

func TestCfgSortedKeys(t *testing.T) {
	m := map[string]any{"charlie": 3, "alpha": 1, "bravo": 2}
	got := cfgSortedKeys(m)
	want := []string{"alpha", "bravo", "charlie"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

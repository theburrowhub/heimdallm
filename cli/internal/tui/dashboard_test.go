package tui

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/theburrowhub/heimdallm/cli/internal/api"
)

func TestDashboardWatchdogHealthyReset(t *testing.T) {
	d := NewDashboard("http://localhost:0", "", "test")
	oldCtx := d.sseCtx
	oldEvents := d.sseEvents
	oldSessionID := d.sseSessionID
	lastEventAt := time.Now().Add(-61 * time.Second)
	d.lastSSEEvent = lastEventAt

	model, cmd := d.Update(sseWatchdogMsg(time.Now()))
	d = model.(*Dashboard)
	if cmd == nil {
		t.Fatal("watchdog should schedule a health check")
	}
	if !d.sseStale || !d.sseHealthChecking {
		t.Fatalf("watchdog state: stale=%v healthChecking=%v", d.sseStale, d.sseHealthChecking)
	}

	model, cmd = d.Update(healthCheckMsg{lastEventAt: lastEventAt})
	d = model.(*Dashboard)
	if cmd == nil {
		t.Fatal("healthy stale stream should schedule SSE reconnect")
	}
	if oldCtx.Err() == nil {
		t.Fatal("old SSE context was not cancelled")
	}
	if d.sseEvents == oldEvents {
		t.Fatal("SSE channel was not replaced")
	}
	if d.sseSessionID != oldSessionID+1 {
		t.Fatalf("session id: got %d want %d", d.sseSessionID, oldSessionID+1)
	}
	if d.sseStale || d.sseHealthChecking {
		t.Fatalf("reset state: stale=%v healthChecking=%v", d.sseStale, d.sseHealthChecking)
	}
}

func TestDashboardDropsStaleHealthProbeAfterHeartbeat(t *testing.T) {
	d := NewDashboard("http://localhost:0", "", "test")
	d.connected = true
	lastEventAt := time.Now().Add(-61 * time.Second)
	d.lastSSEEvent = lastEventAt

	model, _ := d.Update(sseWatchdogMsg(time.Now()))
	d = model.(*Dashboard)
	if !d.sseHealthChecking {
		t.Fatal("watchdog did not start health check")
	}

	model, _ = d.Update(sseMsg{
		sessionID: d.sseSessionID,
		event:     api.SSEEvent{Type: "heartbeat", Data: "{}"},
	})
	d = model.(*Dashboard)
	if d.sseStale {
		t.Fatal("heartbeat did not clear stale state")
	}

	model, _ = d.Update(healthCheckMsg{err: errors.New("health failed"), lastEventAt: lastEventAt})
	d = model.(*Dashboard)
	if d.err != nil {
		t.Fatalf("stale health result should be ignored, got err %v", d.err)
	}
	if !d.connected {
		t.Fatal("stale health result should not mark dashboard disconnected")
	}
	if d.sseHealthChecking {
		t.Fatal("health check flag was not cleared")
	}
}

func TestDashboardDropsStaleReconnectAfterWatchdogReset(t *testing.T) {
	d := NewDashboard("http://localhost:0", "", "test")
	oldSessionID := d.sseSessionID

	model, cmd := d.Update(sseDisconnectMsg{sessionID: oldSessionID, err: errors.New("stream closed")})
	d = model.(*Dashboard)
	if cmd == nil {
		t.Fatal("disconnect should schedule delayed reconnect")
	}

	d.resetSSE()
	if d.sseSessionID == oldSessionID {
		t.Fatal("reset did not bump SSE session")
	}

	model, cmd = d.Update(sseReconnectMsg{sessionID: oldSessionID})
	d = model.(*Dashboard)
	if cmd != nil {
		t.Fatal("stale reconnect tick should be ignored")
	}
}

func TestClampScrollOffset(t *testing.T) {
	cases := []struct {
		name                   string
		offset, total, visible int
		want                   int
	}{
		{"viewport larger than content keeps offset at 0", 0, 5, 20, 0},
		{"viewport larger than content clamps non-zero offset to 0", 7, 5, 20, 0},
		{"viewport exactly fits content keeps offset at 0", 0, 10, 10, 0},
		{"offset within bounds passes through", 3, 20, 10, 3},
		{"offset at upper bound passes through", 10, 20, 10, 10},
		{"offset past upper bound clamps to upper", 18, 20, 10, 10},
		{"negative offset clamps to 0", -5, 20, 10, 0},
		{"zero visible treated as 1 (last line reachable)", 5, 3, 0, 2},
		{"negative visible treated as 1", 5, 3, -4, 2},
		{"empty content always 0", 0, 0, 10, 0},
		{"empty content with positive offset clamps to 0", 9, 0, 10, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := clampScrollOffset(tc.offset, tc.total, tc.visible)
			if got != tc.want {
				t.Fatalf("clampScrollOffset(%d, %d, %d) = %d, want %d",
					tc.offset, tc.total, tc.visible, got, tc.want)
			}
		})
	}
}

func TestIsScrollOffsetTab(t *testing.T) {
	d := NewDashboard("http://localhost:0", "", "test")
	want := map[tab]bool{
		tabActivity: false,
		tabPRs:      false,
		tabIssues:   false,
		tabConfig:   true,
		tabStats:    true,
		tabServer:   false,
	}
	for tb, expected := range want {
		d.activeTab = tb
		if got := d.isScrollOffsetTab(); got != expected {
			t.Fatalf("isScrollOffsetTab(tab=%d) = %v, want %v", tb, got, expected)
		}
	}
}

func TestTabItemCountActivityIsZero(t *testing.T) {
	// Activity scrolls via logOffset, never via cursor. Returning the
	// length of logLines would mislead callers — assert the contract.
	d := NewDashboard("http://localhost:0", "", "test")
	d.activeTab = tabActivity
	d.logLines = []logLine{{}, {}, {}}
	if got := d.tabItemCount(); got != 0 {
		t.Fatalf("tabItemCount(tabActivity) = %d, want 0 (cursor is unused for Activity)", got)
	}
}

func TestPRsSortedByReviewDate(t *testing.T) {
	d := NewDashboard("http://localhost:0", "", "test")
	d.width = 120
	d.height = 40

	old := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	mid := time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)

	msg := dataMsg{
		prs: []api.PR{
			{ID: 1, Number: 10, LatestReview: &api.Review{CreatedAt: old}},
			{ID: 2, Number: 20, LatestReview: &api.Review{CreatedAt: recent}},
			{ID: 3, Number: 30, LatestReview: &api.Review{CreatedAt: mid}},
		},
		config: map[string]any{},
		stats:  &api.Stats{},
	}

	model, _ := d.Update(msg)
	d = model.(*Dashboard)

	if len(d.prs) != 3 {
		t.Fatalf("expected 3 PRs, got %d", len(d.prs))
	}
	if d.prs[0].Number != 20 {
		t.Fatalf("expected first PR #20 (most recent), got #%d", d.prs[0].Number)
	}
	if d.prs[1].Number != 30 {
		t.Fatalf("expected second PR #30, got #%d", d.prs[1].Number)
	}
	if d.prs[2].Number != 10 {
		t.Fatalf("expected third PR #10 (oldest), got #%d", d.prs[2].Number)
	}
}

func TestVisiblePRsFilter(t *testing.T) {
	d := NewDashboard("http://localhost:0", "", "test")
	d.prs = []api.PR{
		{ID: 1, Repo: "org/repo-a", LatestReview: &api.Review{}},
		{ID: 2, Repo: "org/repo-b", LatestReview: &api.Review{}},
		{ID: 3, Repo: "org/repo-a", LatestReview: &api.Review{}},
	}

	visible := d.visiblePRs()
	if len(visible) != 3 {
		t.Fatalf("no filter: expected 3, got %d", len(visible))
	}

	d.prRepoFilter = "org/repo-a"
	visible = d.visiblePRs()
	if len(visible) != 2 {
		t.Fatalf("filter repo-a: expected 2, got %d", len(visible))
	}
	for _, pr := range visible {
		if pr.Repo != "org/repo-a" {
			t.Fatalf("expected repo-a, got %s", pr.Repo)
		}
	}

	d.prRepoFilter = "org/repo-c"
	visible = d.visiblePRs()
	if len(visible) != 0 {
		t.Fatalf("filter repo-c: expected 0, got %d", len(visible))
	}
}

func TestCycleRepoFilter(t *testing.T) {
	d := NewDashboard("http://localhost:0", "", "test")
	d.activeTab = tabPRs
	d.prs = []api.PR{
		{Repo: "org/alpha", LatestReview: &api.Review{}},
		{Repo: "org/beta", LatestReview: &api.Review{}},
		{Repo: "org/alpha", LatestReview: &api.Review{}},
	}

	if d.prRepoFilter != "" {
		t.Fatal("initial filter should be empty")
	}

	d.cycleRepoFilter()
	if d.prRepoFilter != "org/alpha" {
		t.Fatalf("first cycle: expected org/alpha, got %s", d.prRepoFilter)
	}

	d.cycleRepoFilter()
	if d.prRepoFilter != "org/beta" {
		t.Fatalf("second cycle: expected org/beta, got %s", d.prRepoFilter)
	}

	d.cycleRepoFilter()
	if d.prRepoFilter != "" {
		t.Fatalf("third cycle: expected empty, got %s", d.prRepoFilter)
	}
}

func TestTabItemCountRespectsFilter(t *testing.T) {
	d := NewDashboard("http://localhost:0", "", "test")
	d.activeTab = tabPRs
	d.prs = []api.PR{
		{Repo: "org/a", LatestReview: &api.Review{}},
		{Repo: "org/b", LatestReview: &api.Review{}},
		{Repo: "org/a", LatestReview: &api.Review{}},
	}

	if got := d.tabItemCount(); got != 3 {
		t.Fatalf("no filter: expected 3, got %d", got)
	}

	d.prRepoFilter = "org/a"
	if got := d.tabItemCount(); got != 2 {
		t.Fatalf("filter org/a: expected 2, got %d", got)
	}
}

func TestHumanizeAction(t *testing.T) {
	cases := []struct {
		name   string
		review *api.IssueReview
		want   string
	}{
		{"nil review", nil, "---"},
		{"review_only", &api.IssueReview{ActionTaken: "review_only"}, "Triaged"},
		{"refinement", &api.IssueReview{ActionTaken: "refinement"}, "Refined"},
		{"auto_implement no PR", &api.IssueReview{ActionTaken: "auto_implement"}, "Implemented"},
		{"auto_implement with PR", &api.IssueReview{ActionTaken: "auto_implement", PRCreated: 157}, "→ PR #157"},
		{"unknown action", &api.IssueReview{ActionTaken: "custom_action"}, "custom_action"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := humanizeAction(tc.review)
			if got != tc.want {
				t.Fatalf("humanizeAction() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTimeAgo(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		t    time.Time
		want string
	}{
		{"zero time", time.Time{}, "---"},
		{"30 seconds ago", now.Add(-30 * time.Second), "just now"},
		{"5 minutes ago", now.Add(-5 * time.Minute), "5m ago"},
		{"3 hours ago", now.Add(-3 * time.Hour), "3h ago"},
		{"10 days ago", now.Add(-10 * 24 * time.Hour), "10d ago"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := timeAgo(tc.t)
			if got != tc.want {
				t.Fatalf("timeAgo() = %q, want %q", got, tc.want)
			}
		})
	}
	old := now.Add(-60 * 24 * time.Hour)
	got := timeAgo(old)
	if got != old.Format("Jan 02") {
		t.Fatalf("timeAgo(60 days) = %q, want %q", got, old.Format("Jan 02"))
	}
}

func TestParseLabels(t *testing.T) {
	cases := []struct {
		name string
		raw  json.RawMessage
		want []string
	}{
		{"nil", nil, nil},
		{"empty", json.RawMessage(""), nil},
		{"string array", json.RawMessage(`["bug","enhancement"]`), []string{"bug", "enhancement"}},
		{"object array", json.RawMessage(`[{"name":"bug"},{"name":"help wanted"}]`), []string{"bug", "help wanted"}},
		{"invalid json", json.RawMessage(`{invalid`), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseLabels(tc.raw)
			if len(got) != len(tc.want) {
				t.Fatalf("parseLabels() = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("parseLabels()[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestVisibleIssuesFilter(t *testing.T) {
	d := NewDashboard("http://localhost:0", "", "test")
	d.issues = []api.Issue{
		{Number: 1, Repo: "org/alpha", LatestReview: &api.IssueReview{ActionTaken: "review_only"}},
		{Number: 2, Repo: "org/beta", LatestReview: &api.IssueReview{ActionTaken: "auto_implement"}},
		{Number: 3, Repo: "org/alpha", LatestReview: &api.IssueReview{ActionTaken: "auto_implement"}},
	}

	if got := len(d.visibleIssues()); got != 3 {
		t.Fatalf("no filter: got %d, want 3", got)
	}

	d.issueRepoFilter = "org/alpha"
	if got := len(d.visibleIssues()); got != 2 {
		t.Fatalf("repo filter: got %d, want 2", got)
	}

	d.issueRepoFilter = ""
	d.issueActionFilter = "auto_implement"
	if got := len(d.visibleIssues()); got != 2 {
		t.Fatalf("action filter: got %d, want 2", got)
	}

	d.issueRepoFilter = "org/alpha"
	d.issueActionFilter = "auto_implement"
	vis := d.visibleIssues()
	if len(vis) != 1 || vis[0].Number != 3 {
		t.Fatalf("combined filter: got %v, want [#3]", vis)
	}
}

func TestCycleIssueRepoFilter(t *testing.T) {
	d := NewDashboard("http://localhost:0", "", "test")
	d.issues = []api.Issue{
		{Repo: "org/beta"},
		{Repo: "org/alpha"},
	}

	d.cycleIssueRepoFilter()
	if d.issueRepoFilter != "org/alpha" {
		t.Fatalf("first cycle: got %q, want org/alpha", d.issueRepoFilter)
	}
	d.cycleIssueRepoFilter()
	if d.issueRepoFilter != "org/beta" {
		t.Fatalf("second cycle: got %q, want org/beta", d.issueRepoFilter)
	}
	d.cycleIssueRepoFilter()
	if d.issueRepoFilter != "" {
		t.Fatalf("third cycle: got %q, want empty (all)", d.issueRepoFilter)
	}
}

func TestBuildConfigLinesPollingSection(t *testing.T) {
	d := NewDashboard("http://localhost:0", "", "test")
	d.config = map[string]any{
		"polling": map[string]any{
			"adaptive":                    true,
			"poll_interval":               "30s",
			"min_interval":                "10s",
			"max_interval":                "5m",
			"discovery_interval":          "15m",
			"tier3_interval":              "1h",
			"rate_limit_safety_threshold": float64(20),
			"use_etag":                    true,
			"use_graphql":                 false,
		},
	}

	lines := d.buildConfigLines()

	// Collect all lines into a single string for easy substring checks.
	combined := ""
	for _, l := range lines {
		combined += l + "\n"
	}

	expectations := []string{
		"Polling / Rate Limit",
		"Adaptive",
		"true",
		"Poll interval",
		"30s",
		"Min interval",
		"10s",
		"Max interval",
		"5m",
		"Discovery interval",
		"15m",
		"Tier3 interval",
		"1h",
		"Rate-limit safety threshold",
		"20",
		"ETag/304 caching",
		"GraphQL batching",
		"false",
	}
	for _, want := range expectations {
		if !strings.Contains(combined, want) {
			t.Fatalf("buildConfigLines() missing %q in Polling section.\nGot:\n%s", want, combined)
		}
	}
}

func TestBuildConfigLinesPollingAbsentWhenMissing(t *testing.T) {
	d := NewDashboard("http://localhost:0", "", "test")
	d.config = map[string]any{} // no "polling" key

	lines := d.buildConfigLines()
	for _, l := range lines {
		if strings.Contains(l, "Polling / Rate Limit") {
			t.Fatal("Polling section should not appear when polling key is absent from config")
		}
	}
}

func TestIssuesSortedByDate(t *testing.T) {
	d := NewDashboard("http://localhost:0", "", "test")
	now := time.Now()
	msg := dataMsg{
		issues: []api.Issue{
			{Number: 1, LatestReview: &api.IssueReview{CreatedAt: now.Add(-3 * time.Hour)}},
			{Number: 2, LatestReview: &api.IssueReview{CreatedAt: now.Add(-1 * time.Hour)}},
			{Number: 3, LatestReview: &api.IssueReview{CreatedAt: now.Add(-2 * time.Hour)}},
			{Number: 4},
		},
	}
	d.Update(msg)
	if len(d.issues) != 3 {
		t.Fatalf("expected 3 issues with reviews, got %d", len(d.issues))
	}
	if d.issues[0].Number != 2 || d.issues[1].Number != 3 || d.issues[2].Number != 1 {
		t.Fatalf("issues not sorted by date desc: got #%d, #%d, #%d",
			d.issues[0].Number, d.issues[1].Number, d.issues[2].Number)
	}
}

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

func TestBuildConfigLinesAutonomousAndCircuitBreaker(t *testing.T) {
	d := NewDashboard("http://localhost:0", "", "test")
	d.config = map[string]any{
		"autonomous": map[string]any{
			"enabled":           true,
			"auto_merge":        false,
			"merge_method":      "squash",
			"dev_max_turns":     float64(20),
			"dev_effort":        "high",
			"dev_timeout":       "30m",
			"claim_lease":       "5m",
			"take_others_tasks": true,
			"reassign_on_take":  false,
		},
		"circuit_breaker": map[string]any{
			"per_pr_24h":        float64(10),
			"per_repo_hr":       float64(5),
			"per_issue_24h":     float64(8),
			"per_issue_repo_hr": float64(3),
			"per_impl_repo_hr":  float64(2),
		},
	}

	lines := d.buildConfigLines()

	joined := ""
	for _, l := range lines {
		joined += l + "\n"
	}

	checks := []string{
		"Autonomous Mode",
		"Circuit Breaker",
		"squash",
		"20",
		"high",
		"30m",
		"5m",
		"Per PR / 24h",
		"Per repo / hr",
		"Per issue / 24h",
		"Per issue-repo / hr",
		"Per impl-repo / hr",
	}
	for _, want := range checks {
		if !strings.Contains(joined, want) {
			t.Errorf("buildConfigLines(): expected output to contain %q", want)
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

// serverRow returns the value of the Server section's "<label>" row. Anchored on
// the label at the start of the trimmed line rather than a substring search, so
// renaming one row cannot silently make another row's assertion vacuous.
func serverRow(t *testing.T, out, label string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == label {
			return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), label))
		}
	}
	t.Fatalf("Server section has no %q row:\n%s", label, out)
	return ""
}

// The Server section describes the daemon, so its Daemon row must report the
// version from /health — not the CLI binary's own build version, which gets its
// own row.
func TestServerSectionShowsDaemonVersionNotCLIVersion(t *testing.T) {
	d := NewDashboard("http://localhost:0", "", "9.9.9-cli")
	d.width = 120
	d.height = 40
	d.daemonVersion = "0.8.0"

	out := d.renderServer(40)
	if got := serverRow(t, out, "Daemon"); !strings.Contains(got, "0.8.0") {
		t.Errorf("Daemon row = %q, want the daemon version 0.8.0", got)
	}
	if got := serverRow(t, out, "Daemon"); strings.Contains(got, "9.9.9-cli") {
		t.Errorf("Daemon row reports the CLI version: %q", got)
	}
	if got := serverRow(t, out, "CLI"); !strings.Contains(got, "9.9.9-cli") {
		t.Errorf("CLI row = %q, want the CLI version 9.9.9-cli", got)
	}
}

func TestServerSectionUnknownDaemonVersion(t *testing.T) {
	d := NewDashboard("http://localhost:0", "", "0.8.0")
	d.width = 120
	d.height = 40
	d.daemonVersion = ""

	out := d.renderServer(40)
	if got := serverRow(t, out, "Daemon"); !strings.Contains(got, "(unknown)") {
		t.Errorf("Daemon row = %q, want (unknown)", got)
	}
}

func TestDataMsgStoresDaemonVersionAndStart(t *testing.T) {
	d := NewDashboard("http://localhost:0", "", "test")
	d.width = 120
	d.height = 40
	started := time.Date(2026, 8, 4, 9, 20, 26, 0, time.UTC)

	msg := dataMsg{
		config: map[string]any{},
		stats:  &api.Stats{},
		health: &api.Health{Status: "ok", Version: "0.8.0", StartedAt: started},
	}
	model, _ := d.Update(msg)
	d = model.(*Dashboard)

	if d.daemonVersion != "0.8.0" {
		t.Errorf("daemonVersion = %q, want 0.8.0", d.daemonVersion)
	}
	if !d.daemonStartedAt.Equal(started) {
		t.Errorf("daemonStartedAt = %v, want %v", d.daemonStartedAt, started)
	}
}

// A degraded daemon answers 503 but still reports its version, so the Daemon row
// must show it — the operator needs the build precisely when things are broken.
func TestServerSectionShowsVersionWhenDegraded(t *testing.T) {
	d := NewDashboard("http://localhost:0", "", "test")
	d.width = 120
	d.height = 40

	model, _ := d.Update(dataMsg{
		config: map[string]any{},
		stats:  &api.Stats{},
		health: &api.Health{Status: "degraded", Version: "0.8.0"},
	})
	d = model.(*Dashboard)

	if got := serverRow(t, d.renderServer(40), "Daemon"); !strings.Contains(got, "0.8.0") {
		t.Errorf("Daemon row = %q, want 0.8.0 for a degraded daemon", got)
	}
}

// Pairing a last-known version with a "stopped" badge misreports what is
// running, so an unreachable daemon clears the version instead of keeping it.
func TestUnreachableDaemonClearsVersion(t *testing.T) {
	d := NewDashboard("http://localhost:0", "", "test")
	d.width = 120
	d.height = 40

	model, _ := d.Update(dataMsg{
		config: map[string]any{},
		stats:  &api.Stats{},
		health: &api.Health{Version: "0.8.0", StartedAt: time.Now()},
	})
	d = model.(*Dashboard)
	if d.daemonVersion == "" {
		t.Fatal("precondition: version should be set after a successful fetch")
	}

	model, _ = d.Update(dataMsg{healthErr: errors.New("connection refused"), err: errors.New("connection refused")})
	d = model.(*Dashboard)

	if d.daemonVersion != "" {
		t.Errorf("daemonVersion = %q after the daemon became unreachable, want cleared", d.daemonVersion)
	}
	if !d.daemonStartedAt.IsZero() {
		t.Errorf("daemonStartedAt = %v, want zero", d.daemonStartedAt)
	}
	out := d.renderServer(40)
	if got := serverRow(t, out, "Daemon"); !strings.Contains(got, "(unknown)") {
		t.Errorf("Daemon row = %q, want (unknown)", got)
	}
	// The swallowed error used to leave no trail; surface it on the row.
	if !strings.Contains(out, "connection refused") {
		t.Errorf("Server section gives no diagnostic for the failed health fetch:\n%s", out)
	}
}

// Uptime in the Server section is the daemon's, derived from /health started_at,
// not the CLI process's own age.
func TestServerSectionUptimeIsDaemonUptime(t *testing.T) {
	d := NewDashboard("http://localhost:0", "", "test")
	d.width = 120
	d.height = 40
	d.startTime = time.Now().Add(-99 * time.Hour) // CLI running far longer
	d.daemonStartedAt = time.Now().Add(-90 * time.Minute)

	got := serverRow(t, d.renderServer(40), "Uptime")
	if strings.Contains(got, "99h") {
		t.Errorf("Uptime row = %q, reports the CLI's uptime instead of the daemon's", got)
	}
	if !strings.Contains(got, "1h30m") {
		t.Errorf("Uptime row = %q, want the daemon's 1h30m", got)
	}
}

func TestServerSectionUptimeUnknownWithoutStartedAt(t *testing.T) {
	d := NewDashboard("http://localhost:0", "", "test")
	d.width = 120
	d.height = 40
	d.daemonStartedAt = time.Time{}

	if got := serverRow(t, d.renderServer(40), "Uptime"); !strings.Contains(got, "(unknown)") {
		t.Errorf("Uptime row = %q, want (unknown) when the daemon reports no started_at", got)
	}
}

// api.Health.Status was parsed but never shown: a degraded daemon (503) rendered
// a plain "● running" badge, because connectedness only tracks the authenticated
// endpoints. The badge now reflects what the daemon says about itself.
func TestServerStatusBadgeReflectsDegraded(t *testing.T) {
	d := NewDashboard("http://localhost:0", "", "test")
	d.width = 120
	d.height = 40
	d.connected = true

	model, _ := d.Update(dataMsg{
		config: map[string]any{},
		stats:  &api.Stats{},
		health: &api.Health{Status: "degraded", Version: "0.8.0"},
	})
	d = model.(*Dashboard)

	got := serverRow(t, d.renderServer(40), "Status")
	if !strings.Contains(got, "degraded") {
		t.Errorf("Status row = %q, want it to report degraded", got)
	}
	if strings.Contains(got, "running") {
		t.Errorf("Status row = %q, still claims running for a degraded daemon", got)
	}
}

func TestServerStatusBadgeRunningWhenOK(t *testing.T) {
	d := NewDashboard("http://localhost:0", "", "test")
	d.width = 120
	d.height = 40
	d.connected = true

	model, _ := d.Update(dataMsg{
		config: map[string]any{},
		stats:  &api.Stats{},
		health: &api.Health{Status: "ok", Version: "0.8.0"},
	})
	d = model.(*Dashboard)

	if got := serverRow(t, d.renderServer(40), "Status"); !strings.Contains(got, "running") {
		t.Errorf("Status row = %q, want running for a healthy daemon", got)
	}
}

// A daemon on another host with a clock ahead of ours yields a started_at in the
// future; a raw time.Since would render a negative duration.
func TestServerSectionUptimeNeverNegative(t *testing.T) {
	d := NewDashboard("http://localhost:0", "", "test")
	d.width = 120
	d.height = 40
	d.daemonStartedAt = time.Now().Add(2 * time.Hour)

	got := serverRow(t, d.renderServer(40), "Uptime")
	if strings.Contains(got, "-") {
		t.Errorf("Uptime row = %q, want no negative duration for a skewed clock", got)
	}
}

// /health answering at all proves the daemon is up, so its own word outranks
// connectedness — which only tracks the authenticated endpoints. fetchData can
// read a 503 "degraded" payload and then fail on ListPRs/config/stats, which
// sets connected=false while daemonStatus stays "degraded"; reporting "stopped"
// there hides a known-degraded daemon behind a wrong state.
func TestServerStatusBadgeDegradedOutranksDisconnected(t *testing.T) {
	d := NewDashboard("http://localhost:0", "", "test")
	d.width = 120
	d.height = 40
	d.connected = true

	model, _ := d.Update(dataMsg{
		health: &api.Health{Status: "degraded", Version: "0.8.0"},
		err:    errors.New("GET /prs failed: 500"),
	})
	d = model.(*Dashboard)

	if d.connected {
		t.Fatal("precondition: a dataMsg error should mark the dashboard disconnected")
	}
	got := serverRow(t, d.renderServer(40), "Status")
	if !strings.Contains(got, "degraded") {
		t.Errorf("Status row = %q, want degraded (the daemon answered /health)", got)
	}
	if strings.Contains(got, "stopped") {
		t.Errorf("Status row = %q, reports stopped for a daemon that answered /health", got)
	}
}

// When /health itself fails there is no evidence the daemon is up, so the
// disconnected badge is still correct.
func TestServerStatusBadgeStoppedWhenHealthUnreachable(t *testing.T) {
	d := NewDashboard("http://localhost:0", "", "test")
	d.width = 120
	d.height = 40
	d.connected = true

	model, _ := d.Update(dataMsg{
		healthErr: errors.New("connection refused"),
		err:       errors.New("connection refused"),
	})
	d = model.(*Dashboard)

	if got := serverRow(t, d.renderServer(40), "Status"); !strings.Contains(got, "stopped") {
		t.Errorf("Status row = %q, want stopped when /health is unreachable", got)
	}
}

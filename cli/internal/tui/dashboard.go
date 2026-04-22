package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/theburrowhub/heimdallm/cli/internal/api"
)

type tab int

const (
	tabActivity tab = iota
	tabPRs
	tabIssues
	tabConfig
	tabLogs
)

var tabNames = []string{"Activity", "PRs", "Issues", "Config", "Logs"}

type Dashboard struct {
	client *api.Client
	width  int
	height int

	activeTab tab
	cursor    int

	prs      []api.PR
	issues   []api.Issue
	config   map[string]any
	stats    *api.Stats
	activity []activityLine

	logLines  []logLine
	logFollow bool
	logOffset int
	logSeeded bool

	err       error
	connected bool
	startTime time.Time

	sseEvents chan api.SSEEvent
	sseCancel context.CancelFunc
}

type activityLine struct {
	Time     string
	Event    string
	Info     string
	ItemType string // "pr" or "issue"
}

type tickMsg time.Time
type dataMsg struct {
	prs      []api.PR
	issues   []api.Issue
	config   map[string]any
	stats    *api.Stats
	activity *api.ActivityResponse
	err      error
}
type sseMsg api.SSEEvent

func NewDashboard(host, token string) *Dashboard {
	return &Dashboard{
		client:    api.New(host, token),
		startTime: time.Now(),
		sseEvents: make(chan api.SSEEvent, 32),
		logFollow: true,
	}
}

func (d *Dashboard) Init() tea.Cmd {
	return tea.Batch(
		d.fetchData,
		d.connectSSE,
		d.listenSSE(),
		tickCmd(),
	)
}

func tickCmd() tea.Cmd {
	return tea.Tick(10*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (d *Dashboard) fetchData() tea.Msg {
	msg := dataMsg{}

	prs, err := d.client.ListPRs()
	if err != nil {
		msg.err = err
		return msg
	}
	msg.prs = prs

	issues, err := d.client.ListIssues()
	if err != nil {
		msg.err = err
		return msg
	}
	msg.issues = issues

	cfg, err := d.client.GetConfig()
	if err != nil {
		msg.err = err
		return msg
	}
	msg.config = cfg

	stats, err := d.client.GetStats()
	if err != nil {
		msg.err = err
		return msg
	}
	msg.stats = stats

	activity, err := d.client.GetActivity()
	if err != nil {
		msg.err = err
		return msg
	}
	msg.activity = activity

	return msg
}

func (d *Dashboard) connectSSE() tea.Msg {
	ctx, cancel := context.WithCancel(context.Background())
	d.sseCancel = cancel
	go func() {
		_ = d.client.StreamEvents(ctx, d.sseEvents)
	}()
	return nil
}

func (d *Dashboard) listenSSE() tea.Cmd {
	return func() tea.Msg {
		event, ok := <-d.sseEvents
		if !ok {
			return nil
		}
		return sseMsg(event)
	}
}

func (d *Dashboard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return d.handleKey(msg)

	case tea.WindowSizeMsg:
		d.width = msg.Width
		d.height = msg.Height
		d.clampCursor()
		return d, nil

	case tea.MouseMsg:
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			if d.activeTab == tabLogs {
				for i := 0; i < 3; i++ {
					d.scrollLogsUp()
				}
			} else if d.cursor > 0 {
				d.cursor -= 3
				if d.cursor < 0 {
					d.cursor = 0
				}
			}
		case tea.MouseButtonWheelDown:
			if d.activeTab == tabLogs {
				for i := 0; i < 3; i++ {
					d.scrollLogsDown()
				}
			} else {
				d.cursor += 3
				d.clampCursor()
			}
		}
		return d, nil

	case tickMsg:
		return d, tea.Batch(d.fetchData, tickCmd())

	case dataMsg:
		if msg.err != nil {
			d.err = msg.err
			d.connected = false
		} else {
			d.err = nil
			d.connected = true
			d.prs = nil
			for _, pr := range msg.prs {
				if pr.LatestReview != nil {
					d.prs = append(d.prs, pr)
				}
			}
			d.issues = nil
			for _, iss := range msg.issues {
				if iss.LatestReview != nil {
					d.issues = append(d.issues, iss)
				}
			}
			d.config = msg.config
			d.stats = msg.stats
			if msg.activity != nil {
				d.activity = make([]activityLine, 0, len(msg.activity.Entries))
				for _, e := range msg.activity.Entries {
					d.activity = append(d.activity, activityLine{
						Time:     formatActivityTime(e.TS),
						Event:    e.Action,
						Info:     formatActivityInfo(e.Repo, e.ItemType, e.ItemNumber),
						ItemType: e.ItemType,
					})
				}
				if !d.logSeeded {
					entries := msg.activity.Entries
					d.logLines = make([]logLine, 0, len(entries))
					for i := len(entries) - 1; i >= 0; i-- {
						d.logLines = append(d.logLines, activityToLogLine(entries[i]))
					}
					d.logSeeded = true
				}
			}
		}
		return d, nil

	case sseMsg:
		itemType, info := formatSSEData(msg.Data)
		line := activityLine{
			Time:     time.Now().Format("15:04"),
			Event:    msg.Type,
			Info:     info,
			ItemType: itemType,
		}
		d.activity = append([]activityLine{line}, d.activity...)
		if len(d.activity) > 100 {
			d.activity = d.activity[:100]
		}
		d.logLines = append(d.logLines, sseToLogLine(api.SSEEvent(msg)))
		if len(d.logLines) > 1000 {
			excess := len(d.logLines) - 1000
			d.logLines = d.logLines[excess:]
			if !d.logFollow {
				d.logOffset -= excess
				if d.logOffset < 0 {
					d.logOffset = 0
				}
			}
		}
		return d, d.listenSSE()
	}

	return d, nil
}

func (d *Dashboard) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		if d.sseCancel != nil {
			d.sseCancel()
		}
		return d, tea.Quit
	case "tab", "l", "right":
		d.activeTab = (d.activeTab + 1) % tab(len(tabNames))
		d.cursor = 0
	case "shift+tab", "h", "left":
		d.activeTab = (d.activeTab - 1 + tab(len(tabNames))) % tab(len(tabNames))
		d.cursor = 0
	case "j", "down":
		if d.activeTab == tabLogs {
			d.scrollLogsDown()
		} else {
			d.cursor++
			d.clampCursor()
		}
	case "k", "up":
		if d.activeTab == tabLogs {
			d.scrollLogsUp()
		} else {
			if d.cursor > 0 {
				d.cursor--
			}
		}
	case "pgdown":
		if d.activeTab == tabLogs {
			for i := 0; i < d.contentHeight(); i++ {
				d.scrollLogsDown()
			}
		} else {
			d.cursor += d.contentHeight()
			d.clampCursor()
		}
	case "pgup":
		if d.activeTab == tabLogs {
			for i := 0; i < d.contentHeight(); i++ {
				d.scrollLogsUp()
			}
		} else {
			d.cursor -= d.contentHeight()
			if d.cursor < 0 {
				d.cursor = 0
			}
		}
	case "home":
		if d.activeTab == tabLogs {
			d.logOffset = 0
			d.logFollow = false
		} else {
			d.cursor = 0
		}
	case "end":
		if d.activeTab == tabLogs {
			d.logFollow = true
		} else {
			max := d.tabItemCount()
			if max > 0 {
				d.cursor = max - 1
			}
		}
	case "G":
		if d.activeTab == tabLogs {
			d.logFollow = true
		}
	case "r":
		return d, d.fetchData
	case "1":
		d.activeTab = tabActivity
		d.cursor = 0
	case "2":
		d.activeTab = tabPRs
		d.cursor = 0
	case "3":
		d.activeTab = tabIssues
		d.cursor = 0
	case "4":
		d.activeTab = tabConfig
		d.cursor = 0
	case "5":
		d.activeTab = tabLogs
		d.cursor = 0
	}
	return d, nil
}

func (d *Dashboard) clampCursor() {
	max := d.tabItemCount()
	if max > 0 && d.cursor >= max {
		d.cursor = max - 1
	}
	if d.cursor < 0 {
		d.cursor = 0
	}
}

func (d *Dashboard) View() string {
	if d.width == 0 {
		return "Loading..."
	}

	var b strings.Builder

	// Title bar
	title := titleStyle.Render("⚡ Heimdallm Dashboard")
	status := d.renderStatus()
	titleBar := lipgloss.JoinHorizontal(lipgloss.Top, title, "  ", status)
	b.WriteString(titleBar)
	b.WriteString("\n\n")

	// Tab bar
	b.WriteString(d.renderTabs())
	b.WriteString("\n\n")

	// Status bar
	b.WriteString(d.renderStatusBar())
	b.WriteString("\n\n")

	// Content area
	b.WriteString(d.renderContent(d.contentHeight()))

	// Help bar
	b.WriteString("\n")
	b.WriteString(d.renderHelp())

	return b.String()
}

func (d *Dashboard) renderStatus() string {
	if d.connected {
		return statusOnline.Render("● online")
	}
	if d.err != nil {
		return statusError.Render("● offline")
	}
	return lipgloss.NewStyle().Foreground(colorMuted).Render("● connecting...")
}

func (d *Dashboard) renderTabs() string {
	tabs := make([]string, len(tabNames))
	for i, name := range tabNames {
		if tab(i) == d.activeTab {
			tabs[i] = activeTabStyle.Render(name)
		} else {
			tabs[i] = inactiveTabStyle.Render(name)
		}
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, tabs...)
}

func (d *Dashboard) renderStatusBar() string {
	uptime := time.Since(d.startTime).Truncate(time.Second)
	prCount := len(d.prs)
	issueCount := len(d.issues)
	repoCount := 0
	if d.config != nil {
		if repos, ok := d.config["repositories"]; ok {
			if arr, ok := repos.([]any); ok {
				repoCount = len(arr)
			}
		}
	}

	parts := []string{
		fmt.Sprintf("Repos: %d", repoCount),
		fmt.Sprintf("PRs: %d", prCount),
		fmt.Sprintf("Issues: %d", issueCount),
		fmt.Sprintf("Uptime: %s", uptime),
	}

	if d.stats != nil {
		parts = append(parts, fmt.Sprintf("Reviews: %d", d.stats.TotalReviews))
	}

	return headerStyle.Render(strings.Join(parts, "  │  "))
}

func (d *Dashboard) renderContent(height int) string {
	switch d.activeTab {
	case tabActivity:
		return d.renderActivity(height)
	case tabPRs:
		return d.renderPRs(height)
	case tabIssues:
		return d.renderIssues(height)
	case tabConfig:
		return d.renderConfig(height)
	case tabLogs:
		return d.renderLogs(height)
	}
	return ""
}

func (d *Dashboard) renderActivity(height int) string {
	if len(d.activity) == 0 {
		return lipgloss.NewStyle().Foreground(colorMuted).Render("  No activity yet. Events will appear here in real-time.")
	}

	var b strings.Builder
	header := fmt.Sprintf("  %-7s %-25s %s", "TIME", "EVENT", "INFO")
	b.WriteString(headerStyle.Render(header))
	b.WriteString("\n")
	b.WriteString("  " + strings.Repeat("─", 70))
	b.WriteString("\n")

	maxVisible := height - 2
	if maxVisible < 1 {
		maxVisible = 1
	}
	start, end := visibleRange(d.cursor, len(d.activity), maxVisible)

	for i := start; i < end; i++ {
		a := d.activity[i]
		info := itemTypeStyle(a.ItemType).Render(a.Info)
		line := fmt.Sprintf("  %-7s %-25s %s", a.Time, a.Event, info)
		if i == d.cursor {
			b.WriteString(selectedRowStyle.Render(line))
		} else {
			b.WriteString(line)
		}
		b.WriteString("\n")
	}

	if ind := scrollIndicator(start, end, len(d.activity)); ind != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(ind))
	}
	return b.String()
}

func (d *Dashboard) renderPRs(height int) string {
	if len(d.prs) == 0 {
		return lipgloss.NewStyle().Foreground(colorMuted).Render("  No PRs found.")
	}

	var b strings.Builder
	header := fmt.Sprintf("  %-6s %-25s %-35s %-8s %-8s", "ID", "REPO", "TITLE", "SEVERITY", "STATE")
	b.WriteString(headerStyle.Render(header))
	b.WriteString("\n")
	b.WriteString("  " + strings.Repeat("─", 86))
	b.WriteString("\n")

	maxVisible := height - 2
	if maxVisible < 1 {
		maxVisible = 1
	}
	start, end := visibleRange(d.cursor, len(d.prs), maxVisible)

	for i := start; i < end; i++ {
		pr := d.prs[i]
		sev := "---"
		if pr.LatestReview != nil {
			sev = pr.LatestReview.Severity
		}
		title := truncateRunes(pr.Title, 33)
		repo := truncateRunes(pr.Repo, 23)
		sevRendered := severityStyle(sev).Render(fmt.Sprintf("%-8s", sev))
		line := fmt.Sprintf("  %-6d %-25s %-35s %s %-8s", pr.ID, repo, title, sevRendered, pr.State)

		if i == d.cursor {
			b.WriteString(selectedRowStyle.Render(line))
		} else {
			b.WriteString(line)
		}
		b.WriteString("\n")
	}

	if ind := scrollIndicator(start, end, len(d.prs)); ind != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(ind))
	}
	return b.String()
}

func (d *Dashboard) renderIssues(height int) string {
	if len(d.issues) == 0 {
		return lipgloss.NewStyle().Foreground(colorMuted).Render("  No issues found.")
	}

	var b strings.Builder
	header := fmt.Sprintf("  %-6s %-25s %-35s %-8s %-12s", "ID", "REPO", "TITLE", "SEVERITY", "ACTION")
	b.WriteString(headerStyle.Render(header))
	b.WriteString("\n")
	b.WriteString("  " + strings.Repeat("─", 90))
	b.WriteString("\n")

	maxVisible := height - 2
	if maxVisible < 1 {
		maxVisible = 1
	}
	start, end := visibleRange(d.cursor, len(d.issues), maxVisible)

	for i := start; i < end; i++ {
		iss := d.issues[i]
		sev := "---"
		action := "---"
		if iss.LatestReview != nil {
			sev = extractSeverity(iss.LatestReview.Triage)
			action = iss.LatestReview.ActionTaken
		}
		title := truncateRunes(iss.Title, 33)
		repo := truncateRunes(iss.Repo, 23)
		sevRendered := severityStyle(sev).Render(fmt.Sprintf("%-8s", sev))
		line := fmt.Sprintf("  %-6d %-25s %-35s %s %-12s", iss.ID, repo, title, sevRendered, action)

		if i == d.cursor {
			b.WriteString(selectedRowStyle.Render(line))
		} else {
			b.WriteString(line)
		}
		b.WriteString("\n")
	}

	if ind := scrollIndicator(start, end, len(d.issues)); ind != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(ind))
	}
	return b.String()
}

func (d *Dashboard) renderConfig(height int) string {
	if d.config == nil {
		return lipgloss.NewStyle().Foreground(colorMuted).Render("  No configuration loaded.")
	}

	var b strings.Builder
	b.WriteString(headerStyle.Render("  Configuration"))
	b.WriteString("\n")
	b.WriteString("  " + strings.Repeat("─", 60))
	b.WriteString("\n")

	lines := d.buildConfigLines()
	if len(lines) == 0 {
		return b.String()
	}

	maxVisible := height - 2
	if maxVisible < 1 {
		maxVisible = 1
	}
	start, end := visibleRange(d.cursor, len(lines), maxVisible)
	for i := start; i < end; i++ {
		b.WriteString(lines[i])
		b.WriteString("\n")
	}

	if ind := scrollIndicator(start, end, len(lines)); ind != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(ind))
	}
	return b.String()
}

func (d *Dashboard) renderHelp() string {
	return helpStyle.Render("[q]uit  [r]efresh  [tab]switch  [j/k]scroll  [pgup/pgdn]page  [1-5]jump  [G]follow")
}

func (d *Dashboard) contentHeight() int {
	h := d.height - 10
	if h < 5 {
		h = 5
	}
	return h
}

func (d *Dashboard) tabItemCount() int {
	switch d.activeTab {
	case tabActivity:
		return len(d.activity)
	case tabPRs:
		return len(d.prs)
	case tabIssues:
		return len(d.issues)
	case tabConfig:
		return len(d.buildConfigLines())
	default:
		return 0
	}
}

func (d *Dashboard) buildConfigLines() []string {
	if d.config == nil {
		return nil
	}
	keys := []string{
		"poll_interval", "repositories", "ai_primary", "ai_fallback",
		"review_mode", "retention_days", "issue_tracking",
	}
	var lines []string
	for _, key := range keys {
		val, ok := d.config[key]
		if !ok {
			continue
		}
		valStr := formatConfigValue(val)
		for _, part := range strings.Split(fmt.Sprintf("  %-20s %s", key+":", valStr), "\n") {
			lines = append(lines, part)
		}
	}
	return lines
}

func visibleRange(cursor, total, maxVisible int) (int, int) {
	if total <= maxVisible {
		return 0, total
	}
	start := cursor - maxVisible + 1
	if start < 0 {
		start = 0
	}
	end := start + maxVisible
	if end > total {
		end = total
		start = total - maxVisible
	}
	return start, end
}

func scrollIndicator(start, end, total int) string {
	var parts []string
	if start > 0 {
		parts = append(parts, fmt.Sprintf("%d above", start))
	}
	if end < total {
		parts = append(parts, fmt.Sprintf("%d below", total-end))
	}
	if len(parts) == 0 {
		return ""
	}
	return fmt.Sprintf("  ── %s ──", strings.Join(parts, " | "))
}

func formatConfigValue(v any) string {
	switch val := v.(type) {
	case []any:
		if len(val) == 0 {
			return "[]"
		}
		parts := make([]string, len(val))
		for i, item := range val {
			parts[i] = fmt.Sprintf("%v", item)
		}
		return strings.Join(parts, ", ")
	case map[string]any:
		b, _ := json.MarshalIndent(val, "                      ", "  ")
		return string(b)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func formatActivityTime(ts string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	return t.Format("15:04")
}

func formatSSEData(data string) (itemType string, info string) {
	var m map[string]any
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		return "", data
	}

	parts := make([]string, 0)
	if repo, ok := m["repo"]; ok {
		parts = append(parts, fmt.Sprintf("%v", repo))
	}
	if num, ok := m["pr_number"]; ok {
		itemType = "pr"
		n := toInt(num)
		if n != 0 {
			parts = append(parts, fmt.Sprintf("PR #%d", n))
		}
	}
	if num, ok := m["issue_number"]; ok {
		itemType = "issue"
		n := toInt(num)
		if n != 0 {
			parts = append(parts, fmt.Sprintf("Issue #%d", n))
		}
	}
	if sev, ok := m["severity"]; ok {
		parts = append(parts, fmt.Sprintf("[%v]", sev))
	}

	if len(parts) > 0 {
		return itemType, strings.Join(parts, " ")
	}
	return itemType, data
}

func formatActivityInfo(repo, itemType string, itemNumber int) string {
	if itemNumber == 0 {
		return repo
	}
	switch itemType {
	case "pr":
		return fmt.Sprintf("%s PR #%d", repo, itemNumber)
	case "issue":
		return fmt.Sprintf("%s Issue #%d", repo, itemNumber)
	default:
		return fmt.Sprintf("%s #%d", repo, itemNumber)
	}
}

func itemTypeStyle(itemType string) lipgloss.Style {
	switch itemType {
	case "pr":
		return lipgloss.NewStyle().Foreground(colorPR)
	case "issue":
		return lipgloss.NewStyle().Foreground(colorIssue)
	default:
		return lipgloss.NewStyle()
	}
}

func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	default:
		return 0
	}
}

func extractSeverity(triage json.RawMessage) string {
	if len(triage) == 0 {
		return "---"
	}
	var t map[string]any
	if err := json.Unmarshal(triage, &t); err != nil {
		return "---"
	}
	if sev, ok := t["severity"]; ok {
		return fmt.Sprintf("%v", sev)
	}
	return "---"
}

// truncateRunes returns s truncated to at most maxLen runes, appending an
// ellipsis if truncated. This avoids corrupting multi-byte UTF-8 characters.
func truncateRunes(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-1]) + "\u2026"
}

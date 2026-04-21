package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/heimdallm/cli/internal/api"
)

type tab int

const (
	tabActivity tab = iota
	tabPRs
	tabIssues
	tabConfig
)

var tabNames = []string{"Activity", "PRs", "Issues", "Config"}

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

	err       error
	connected bool
	startTime time.Time

	sseEvents chan api.SSEEvent
	sseDone   chan struct{}
}

type activityLine struct {
	Time  string
	Event string
	Info  string
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
		sseDone:   make(chan struct{}),
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
	go func() {
		_ = d.client.StreamEvents(d.sseEvents, d.sseDone)
	}()
	return nil
}

func (d *Dashboard) listenSSE() tea.Cmd {
	return func() tea.Msg {
		select {
		case event, ok := <-d.sseEvents:
			if !ok {
				return nil
			}
			return sseMsg(event)
		case <-d.sseDone:
			return nil
		}
	}
}

func (d *Dashboard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return d.handleKey(msg)

	case tea.WindowSizeMsg:
		d.width = msg.Width
		d.height = msg.Height
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
			d.prs = msg.prs
			d.issues = msg.issues
			d.config = msg.config
			d.stats = msg.stats
			if msg.activity != nil {
				d.activity = make([]activityLine, 0, len(msg.activity.Entries))
				for _, e := range msg.activity.Entries {
					d.activity = append(d.activity, activityLine{
						Time:  formatActivityTime(e.TS),
						Event: e.Action,
						Info:  fmt.Sprintf("%s #%d", e.Repo, e.ItemNumber),
					})
				}
			}
		}
		return d, nil

	case sseMsg:
		line := activityLine{
			Time:  time.Now().Format("15:04"),
			Event: msg.Type,
			Info:  formatSSEData(msg.Data),
		}
		d.activity = append([]activityLine{line}, d.activity...)
		if len(d.activity) > 100 {
			d.activity = d.activity[:100]
		}
		return d, d.listenSSE()
	}

	return d, nil
}

func (d *Dashboard) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		close(d.sseDone)
		return d, tea.Quit
	case "tab", "l", "right":
		d.activeTab = (d.activeTab + 1) % tab(len(tabNames))
		d.cursor = 0
	case "shift+tab", "h", "left":
		d.activeTab = (d.activeTab - 1 + tab(len(tabNames))) % tab(len(tabNames))
		d.cursor = 0
	case "j", "down":
		d.cursor++
		d.clampCursor()
	case "k", "up":
		if d.cursor > 0 {
			d.cursor--
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
	}
	return d, nil
}

func (d *Dashboard) clampCursor() {
	max := 0
	switch d.activeTab {
	case tabActivity:
		max = len(d.activity)
	case tabPRs:
		max = len(d.prs)
	case tabIssues:
		max = len(d.issues)
	}
	if max > 0 && d.cursor >= max {
		d.cursor = max - 1
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
	contentHeight := d.height - 10
	if contentHeight < 5 {
		contentHeight = 5
	}
	b.WriteString(d.renderContent(contentHeight))

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

	visible := d.activity
	if len(visible) > height-2 {
		visible = visible[:height-2]
	}

	for i, a := range visible {
		line := fmt.Sprintf("  %-7s %-25s %s", a.Time, a.Event, a.Info)
		if i == d.cursor {
			b.WriteString(selectedRowStyle.Render(line))
		} else {
			b.WriteString(line)
		}
		b.WriteString("\n")
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

	visible := d.prs
	if len(visible) > height-2 {
		start := 0
		if d.cursor > height-3 {
			start = d.cursor - (height - 3)
		}
		end := start + height - 2
		if end > len(visible) {
			end = len(visible)
		}
		visible = visible[start:end]
	}

	for i, pr := range d.prs {
		if i >= height-2 {
			break
		}
		sev := "---"
		if pr.LatestReview != nil {
			sev = pr.LatestReview.Severity
		}
		title := pr.Title
		if len(title) > 33 {
			title = title[:30] + "..."
		}
		repo := pr.Repo
		if len(repo) > 23 {
			repo = repo[:20] + "..."
		}
		sevRendered := severityStyle(sev).Render(fmt.Sprintf("%-8s", sev))
		line := fmt.Sprintf("  %-6d %-25s %-35s %s %-8s", pr.ID, repo, title, sevRendered, pr.State)

		if i == d.cursor {
			b.WriteString(selectedRowStyle.Render(line))
		} else {
			b.WriteString(line)
		}
		b.WriteString("\n")
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

	for i, iss := range d.issues {
		if i >= height-2 {
			break
		}
		sev := "---"
		action := "---"
		if iss.LatestReview != nil {
			sev = extractSeverity(iss.LatestReview.Triage)
			action = iss.LatestReview.ActionTaken
		}
		title := iss.Title
		if len(title) > 33 {
			title = title[:30] + "..."
		}
		repo := iss.Repo
		if len(repo) > 23 {
			repo = repo[:20] + "..."
		}
		sevRendered := severityStyle(sev).Render(fmt.Sprintf("%-8s", sev))
		line := fmt.Sprintf("  %-6d %-25s %-35s %s %-12s", iss.ID, repo, title, sevRendered, action)

		if i == d.cursor {
			b.WriteString(selectedRowStyle.Render(line))
		} else {
			b.WriteString(line)
		}
		b.WriteString("\n")
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

	keys := []string{
		"poll_interval", "repositories", "ai_primary", "ai_fallback",
		"review_mode", "retention_days", "issue_tracking",
	}

	lines := 0
	for _, key := range keys {
		if lines >= height-2 {
			break
		}
		val, ok := d.config[key]
		if !ok {
			continue
		}
		valStr := formatConfigValue(val)
		b.WriteString(fmt.Sprintf("  %-20s %s\n", key+":", valStr))
		lines++
	}
	return b.String()
}

func (d *Dashboard) renderHelp() string {
	return helpStyle.Render("[q]uit  [r]efresh  [tab]switch  [j/k]navigate  [1-4]jump to tab")
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

func formatSSEData(data string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		return data
	}

	parts := make([]string, 0)
	if repo, ok := m["repo"]; ok {
		parts = append(parts, fmt.Sprintf("%v", repo))
	}
	if num, ok := m["pr_number"]; ok {
		parts = append(parts, fmt.Sprintf("#%v", num))
	}
	if num, ok := m["issue_number"]; ok {
		parts = append(parts, fmt.Sprintf("#%v", num))
	}
	if sev, ok := m["severity"]; ok {
		parts = append(parts, fmt.Sprintf("[%v]", sev))
	}

	if len(parts) > 0 {
		return strings.Join(parts, " ")
	}
	return data
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

package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
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
	tabStats
	tabLogs
	tabServer
)

var tabNames = []string{"Activity", "PRs", "Issues", "Config", "Stats", "Logs", "Server"}

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

	err               error
	connected         bool
	sseStale          bool
	sseHealthChecking bool
	refreshing        bool
	confirmShutdown   bool
	shutdownInFlight  bool
	shutdownMessage   string
	sseStatusMessage  string
	startTime         time.Time
	lastUpdate        time.Time
	lastSSEEvent      time.Time
	version           string

	sseEvents    chan api.SSEEvent
	sseCtx       context.Context
	sseCancel    context.CancelFunc
	sseSessionID int64

	showDetail   bool
	detailScroll int
	detailLines  []string
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
type sseMsg struct {
	sessionID int64
	event     api.SSEEvent
}
type sseDisconnectMsg struct {
	sessionID int64
	err       error
}
type sseReconnectMsg struct{ sessionID int64 }
type sseWatchdogMsg time.Time
type healthCheckMsg struct {
	err         error
	lastEventAt time.Time
}
type shutdownMsg struct{ err error }
type promoteIssueMsg struct {
	id  int64
	err error
}

func NewDashboard(host, token, version string) *Dashboard {
	ctx, cancel := context.WithCancel(context.Background())
	return &Dashboard{
		client:       api.New(host, token),
		version:      version,
		startTime:    time.Now(),
		lastSSEEvent: time.Now(),
		sseEvents:    make(chan api.SSEEvent, 32),
		logFollow:    true,
		sseCtx:       ctx,
		sseCancel:    cancel,
		sseSessionID: 1,
	}
}

func (d *Dashboard) Init() tea.Cmd {
	connect, listen := d.sseCommands()
	return tea.Batch(
		d.fetchData,
		connect,
		listen,
		tickCmd(),
		sseWatchdogCmd(),
	)
}

func tickCmd() tea.Cmd {
	return tea.Tick(10*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func sseWatchdogCmd() tea.Cmd {
	return tea.Tick(10*time.Second, func(t time.Time) tea.Msg {
		return sseWatchdogMsg(t)
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

func (d *Dashboard) sseCommands() (tea.Cmd, tea.Cmd) {
	return d.connectSSE(d.sseSessionID, d.sseCtx, d.sseEvents),
		d.listenSSE(d.sseSessionID, d.sseCtx, d.sseEvents)
}

func (d *Dashboard) connectSSE(sessionID int64, ctx context.Context, events chan<- api.SSEEvent) tea.Cmd {
	return func() tea.Msg {
		err := d.client.StreamEvents(ctx, events)
		if ctx.Err() != nil {
			return nil
		}
		return sseDisconnectMsg{sessionID: sessionID, err: err}
	}
}

func (d *Dashboard) listenSSE(sessionID int64, ctx context.Context, events <-chan api.SSEEvent) tea.Cmd {
	return func() tea.Msg {
		select {
		case event, ok := <-events:
			if !ok {
				return nil
			}
			return sseMsg{sessionID: sessionID, event: event}
		case <-ctx.Done():
			return nil
		}
	}
}

func (d *Dashboard) resetSSE() {
	if d.sseCancel != nil {
		d.sseCancel()
	}
	// The StreamEvents producer owns the send side and exits through ctx.Done().
	// Closing the channel here would race with a send and can panic.
	ctx, cancel := context.WithCancel(context.Background())
	d.sseCtx = ctx
	d.sseCancel = cancel
	d.sseEvents = make(chan api.SSEEvent, 32)
	d.sseSessionID++
	d.lastSSEEvent = time.Now()
}

func (d *Dashboard) shutdownDaemon() tea.Cmd {
	return func() tea.Msg {
		return shutdownMsg{err: d.client.Shutdown()}
	}
}

func (d *Dashboard) promoteIssue(id int64) tea.Cmd {
	return func() tea.Msg {
		return promoteIssueMsg{id: id, err: d.client.PromoteIssue(id)}
	}
}

func (d *Dashboard) checkHealth(lastEventAt time.Time) tea.Cmd {
	return func() tea.Msg {
		return healthCheckMsg{err: d.client.Health(), lastEventAt: lastEventAt}
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
			if d.showDetail {
				d.scrollDetailUp()
			} else if d.activeTab == tabLogs {
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
			if d.showDetail {
				d.scrollDetailDown()
			} else if d.activeTab == tabLogs {
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

	case sseWatchdogMsg:
		cmds := []tea.Cmd{sseWatchdogCmd()}
		if !d.shutdownInFlight && !d.sseHealthChecking && !d.lastSSEEvent.IsZero() && time.Since(d.lastSSEEvent) > time.Minute {
			d.sseStale = true
			d.sseHealthChecking = true
			d.sseStatusMessage = "SSE stream stale; checking daemon health..."
			cmds = append(cmds, d.checkHealth(d.lastSSEEvent))
		}
		return d, tea.Batch(cmds...)

	case dataMsg:
		d.refreshing = false
		if msg.err != nil {
			d.err = msg.err
			d.connected = false
		} else {
			d.err = nil
			d.connected = true
			d.lastUpdate = time.Now()
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
		if msg.sessionID != d.sseSessionID {
			return d, nil
		}
		event := msg.event
		d.lastSSEEvent = time.Now()
		d.sseStale = false
		if !d.sseHealthChecking {
			d.sseStatusMessage = ""
			d.connected = true
			d.err = nil
		}
		if event.Type == "heartbeat" {
			// Heartbeats are liveness-only and intentionally skipped in Activity/Logs.
			return d, d.listenSSE(d.sseSessionID, d.sseCtx, d.sseEvents)
		}
		itemType, info := formatSSEData(event.Type, event.Data)
		line := activityLine{
			Time:     time.Now().Format("15:04"),
			Event:    event.Type,
			Info:     info,
			ItemType: itemType,
		}
		d.activity = append([]activityLine{line}, d.activity...)
		if len(d.activity) > 100 {
			d.activity = d.activity[:100]
		}
		d.logLines = append(d.logLines, sseToLogLine(event))
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
		return d, d.listenSSE(d.sseSessionID, d.sseCtx, d.sseEvents)

	case sseDisconnectMsg:
		if msg.sessionID != d.sseSessionID {
			return d, nil
		}
		d.connected = false
		d.err = msg.err
		d.sseStale = false
		sessionID := msg.sessionID
		return d, tea.Tick(5*time.Second, func(t time.Time) tea.Msg {
			return sseReconnectMsg{sessionID: sessionID}
		})

	case sseReconnectMsg:
		if msg.sessionID != d.sseSessionID {
			return d, nil
		}
		connect, _ := d.sseCommands()
		return d, connect

	case healthCheckMsg:
		d.sseHealthChecking = false
		if !d.sseStale || !msg.lastEventAt.Equal(d.lastSSEEvent) {
			return d, nil
		}
		if msg.err != nil {
			d.connected = false
			d.err = msg.err
			d.sseStatusMessage = "Daemon health check failed"
			return d, nil
		}
		d.resetSSE()
		connect, listen := d.sseCommands()
		d.connected = false
		d.err = nil
		d.sseStale = false
		d.sseStatusMessage = "Reconnecting SSE stream..."
		return d, tea.Batch(connect, listen)

	case shutdownMsg:
		d.shutdownInFlight = false
		d.confirmShutdown = false
		if msg.err != nil {
			d.err = msg.err
			d.connected = false
			d.shutdownMessage = fmt.Sprintf("Shutdown failed: %v", msg.err)
			return d, nil
		}
		d.connected = false
		d.err = fmt.Errorf("daemon shutdown requested")
		d.shutdownMessage = "Shutdown requested"
		if d.sseCancel != nil {
			d.sseCancel()
		}
		return d, nil

	case promoteIssueMsg:
		d.refreshing = false
		if msg.err != nil {
			d.err = msg.err
			d.shutdownMessage = fmt.Sprintf("Promotion failed: %v", msg.err)
			return d, nil
		}
		d.shutdownMessage = "Promotion requested"
		return d, d.fetchData
	}

	return d, nil
}

func (d *Dashboard) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if d.showDetail {
		return d.handleDetailKey(msg)
	}
	if d.confirmShutdown {
		switch msg.String() {
		case "y", "Y":
			d.shutdownInFlight = true
			d.shutdownMessage = "Requesting shutdown..."
			return d, d.shutdownDaemon()
		case "n", "N", "esc":
			d.confirmShutdown = false
			d.shutdownMessage = ""
			return d, nil
		case "q", "ctrl+c":
			if d.sseCancel != nil {
				d.sseCancel()
			}
			return d, tea.Quit
		default:
			return d, nil
		}
	}
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
		} else {
			max := d.tabItemCount()
			if max > 0 {
				d.cursor = max - 1
			}
		}
	case "enter":
		if d.activeTab == tabPRs && d.cursor < len(d.prs) {
			d.openDetail()
		} else if d.activeTab == tabIssues && d.cursor < len(d.issues) {
			d.openDetail()
		}
	case "p", "P":
		if cmd := d.promoteSelectedIssue(); cmd != nil {
			return d, cmd
		}
	case "r":
		d.refreshing = true
		return d, d.fetchData
	case "s", "S":
		if !d.shutdownInFlight {
			d.confirmShutdown = true
			d.shutdownMessage = "Stop daemon? y/n"
		}
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
		d.activeTab = tabStats
		d.cursor = 0
	case "6":
		d.activeTab = tabLogs
		d.cursor = 0
	case "7":
		d.activeTab = tabServer
		d.cursor = 0
	}
	return d, nil
}

func (d *Dashboard) handleDetailKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		if d.sseCancel != nil {
			d.sseCancel()
		}
		return d, tea.Quit
	case "esc", "enter":
		d.showDetail = false
	case "p", "P":
		if cmd := d.promoteSelectedIssue(); cmd != nil {
			return d, cmd
		}
	case "j", "down":
		d.scrollDetailDown()
	case "k", "up":
		d.scrollDetailUp()
	case "pgdown":
		for i := 0; i < d.contentHeight(); i++ {
			d.scrollDetailDown()
		}
	case "pgup":
		for i := 0; i < d.contentHeight(); i++ {
			d.scrollDetailUp()
		}
	case "home":
		d.detailScroll = 0
	case "end", "G":
		maxOffset := len(d.detailLines) - d.contentHeight()
		if maxOffset > 0 {
			d.detailScroll = maxOffset
		}
	}
	return d, nil
}

func (d *Dashboard) promoteSelectedIssue() tea.Cmd {
	if d.activeTab != tabIssues || d.cursor >= len(d.issues) {
		return nil
	}
	issue := d.issues[d.cursor]
	if !canPromoteIssue(issue) {
		return nil
	}
	d.refreshing = true
	d.shutdownMessage = fmt.Sprintf("Promoting issue #%d...", issue.Number)
	return d.promoteIssue(issue.ID)
}

func (d *Dashboard) openDetail() {
	d.showDetail = true
	d.detailScroll = 0
	switch d.activeTab {
	case tabPRs:
		d.detailLines = buildPRDetailLines(d.prs[d.cursor], d.width)
	case tabIssues:
		d.detailLines = buildIssueDetailLines(d.issues[d.cursor], d.width)
	}
}

func (d *Dashboard) renderDetail(height int) string {
	if len(d.detailLines) == 0 {
		return lipgloss.NewStyle().Foreground(colorMuted).Render("  No details available.")
	}

	var b strings.Builder
	start := d.detailScroll
	end := start + height
	if end > len(d.detailLines) {
		end = len(d.detailLines)
	}
	if start > end {
		start = end
	}

	for _, line := range d.detailLines[start:end] {
		b.WriteString(line)
		b.WriteString("\n")
	}

	if end < len(d.detailLines) {
		remaining := len(d.detailLines) - end
		b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(
			fmt.Sprintf("  ── %d more lines below ──", remaining)))
	}

	return b.String()
}

func (d *Dashboard) scrollDetailDown() {
	maxOffset := len(d.detailLines) - d.contentHeight()
	if maxOffset < 0 {
		maxOffset = 0
	}
	if d.detailScroll < maxOffset {
		d.detailScroll++
	}
}

func (d *Dashboard) scrollDetailUp() {
	if d.detailScroll > 0 {
		d.detailScroll--
	}
}

// isScrollOffsetTab reports whether the active tab uses the cursor as a
// scroll offset (first visible line) rather than a selected-row index.
// Read-only informational tabs (Stats, Config) have no selectable rows,
// so j/k/arrows scroll the viewport directly instead of moving a highlight.
func (d *Dashboard) isScrollOffsetTab() bool {
	return d.activeTab == tabStats || d.activeTab == tabConfig
}

func (d *Dashboard) clampCursor() {
	max := d.tabItemCount()
	if d.isScrollOffsetTab() {
		// cursor is a scroll offset: clamp so the last line is visible
		// at the bottom of the viewport.
		visible := d.contentHeight()
		if d.activeTab == tabConfig {
			visible -= 2 // header + separator
		}
		if visible < 1 {
			visible = 1
		}
		upper := max - visible
		if upper < 0 {
			upper = 0
		}
		if d.cursor > upper {
			d.cursor = upper
		}
	} else {
		if max > 0 && d.cursor >= max {
			d.cursor = max - 1
		}
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
	if d.shutdownInFlight {
		return lipgloss.NewStyle().Foreground(colorWarning).Bold(true).Render("● stopping...")
	}
	if d.refreshing {
		return lipgloss.NewStyle().Foreground(colorWarning).Bold(true).Render("● refreshing...")
	}
	if d.sseStale {
		return lipgloss.NewStyle().Foreground(colorWarning).Bold(true).Render("● unresponsive...")
	}
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
	if !d.lastUpdate.IsZero() {
		parts = append(parts, fmt.Sprintf("Updated: %s", d.lastUpdate.Format("15:04:05")))
	}
	if d.version != "" {
		parts = append(parts, "v"+d.version)
	}
	if d.confirmShutdown {
		parts = append(parts, "Confirm stop: y/n")
	} else if d.shutdownMessage != "" {
		parts = append(parts, truncateRunes(d.shutdownMessage, 80))
	}
	if d.sseStatusMessage != "" {
		parts = append(parts, truncateRunes(d.sseStatusMessage, 80))
	}

	return headerStyle.Render(strings.Join(parts, "  │  "))
}

func (d *Dashboard) renderContent(height int) string {
	if d.showDetail {
		return d.renderDetail(height)
	}
	switch d.activeTab {
	case tabActivity:
		return d.renderActivity(height)
	case tabPRs:
		return d.renderPRs(height)
	case tabIssues:
		return d.renderIssues(height)
	case tabConfig:
		return d.renderConfig(height)
	case tabStats:
		return d.renderStats(height)
	case tabLogs:
		return d.renderLogs(height)
	case tabServer:
		return d.renderServer(height)
	}
	return ""
}

func (d *Dashboard) renderActivity(height int) string {
	if len(d.activity) == 0 {
		return lipgloss.NewStyle().Foreground(colorMuted).Render("  No activity yet. Events will appear here in real-time.")
	}

	var b strings.Builder
	header := fmt.Sprintf("  %-7s %-7s %-25s %s", "TIME", "TYPE", "EVENT", "INFO")
	b.WriteString(headerStyle.Render(header))
	b.WriteString("\n")
	b.WriteString("  " + strings.Repeat("─", 78))
	b.WriteString("\n")

	maxVisible := height - 2
	if maxVisible < 1 {
		maxVisible = 1
	}
	start, end := visibleRange(d.cursor, len(d.activity), maxVisible)

	for i := start; i < end; i++ {
		a := d.activity[i]
		badge := activityBadge(a.ItemType)
		info := itemTypeStyle(a.ItemType).Render(a.Info)
		line := fmt.Sprintf("  %-7s %s %-25s %s", a.Time, badge, a.Event, info)
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

	maxVisible := height - 2 // header + separator
	if maxVisible < 1 {
		maxVisible = 1
	}

	// cursor is a scroll offset (first visible line).
	start := d.cursor
	if start > len(lines)-maxVisible {
		start = len(lines) - maxVisible
	}
	if start < 0 {
		start = 0
	}
	end := start + maxVisible
	if end > len(lines) {
		end = len(lines)
	}

	for i := start; i < end; i++ {
		b.WriteString(lines[i])
		b.WriteString("\n")
	}

	if ind := scrollIndicator(start, end, len(lines)); ind != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(ind))
	}
	return b.String()
}

func (d *Dashboard) serverStatusBadge() string {
	if d.shutdownInFlight {
		return lipgloss.NewStyle().Foreground(colorWarning).Bold(true).Render("● stopping...")
	}
	if d.sseStale {
		return lipgloss.NewStyle().Foreground(colorWarning).Bold(true).Render("● unresponsive")
	}
	if !d.connected {
		return lipgloss.NewStyle().Foreground(colorMuted).Render("● stopped")
	}
	return lipgloss.NewStyle().Foreground(colorSuccess).Bold(true).Render("● running")
}

func (d *Dashboard) renderServer(height int) string {
	var b strings.Builder

	b.WriteString(headerStyle.Render("  Server"))
	b.WriteString("\n")
	b.WriteString("  " + strings.Repeat("─", 64))
	b.WriteString("\n")

	mutedNote := lipgloss.NewStyle().Foreground(colorMuted)

	// Status row
	b.WriteString(fmt.Sprintf("  %-10s %s\n", "Status", d.serverStatusBadge()))

	// Version row — d.version is the build-time version passed into NewDashboard
	version := d.version
	if version == "" {
		version = mutedNote.Render("(unknown)")
	}
	b.WriteString(fmt.Sprintf("  %-10s %s\n", "Version", version))

	// Uptime row
	uptime := time.Since(d.startTime).Truncate(time.Second).String()
	b.WriteString(fmt.Sprintf("  %-10s %s\n", "Uptime", uptime))

	// Bind addr / port — sourced from d.config (last successful /config fetch)
	bindAddr := mutedNote.Render("(unavailable)")
	port := mutedNote.Render("(unavailable)")
	if d.config != nil {
		if v, ok := d.config["bind_addr"].(string); ok && v != "" {
			bindAddr = v
		} else {
			bindAddr = mutedNote.Render("(default: 127.0.0.1)")
		}
		if n := toInt(d.config["server_port"]); n != 0 {
			port = fmt.Sprintf("%d", n)
		}
	}

	b.WriteString(fmt.Sprintf("  %-10s %s   %s\n", "Bind addr", bindAddr,
		mutedNote.Render("(read-only — edit ~/.config/heimdallm/config.toml)")))
	b.WriteString(fmt.Sprintf("  %-10s %s   %s\n", "Port", port,
		mutedNote.Render("(read-only)")))

	b.WriteString("\n")

	// Help line — only show Stop hint when daemon is up.
	if d.connected && !d.shutdownInFlight {
		b.WriteString("  " + helpStyle.Render("[s] Stop daemon   [r] Refresh"))
	} else if d.shutdownInFlight {
		b.WriteString("  " + helpStyle.Render("[r] Refresh"))
	} else {
		b.WriteString("  " + helpStyle.Render("[r] Refresh   (start the daemon from your shell)"))
	}
	b.WriteString("\n\n")

	b.WriteString("  ")
	b.WriteString(mutedNote.Render(
		"Restarting requires running heimdalld again from your shell"))
	b.WriteString("\n  ")
	b.WriteString(mutedNote.Render(
		"or service manager (TUI cannot spawn the daemon)."))
	b.WriteString("\n")

	return b.String()
}

func (d *Dashboard) renderStats(height int) string {
	if d.stats == nil {
		return lipgloss.NewStyle().Foreground(colorMuted).Render("  No statistics loaded.")
	}

	lines := d.buildStatsLines()
	if len(lines) == 0 {
		return ""
	}

	var b strings.Builder
	maxVisible := height
	if maxVisible < 1 {
		maxVisible = 1
	}

	// cursor is a scroll offset (first visible line).
	start := d.cursor
	if start > len(lines)-maxVisible {
		start = len(lines) - maxVisible
	}
	if start < 0 {
		start = 0
	}
	end := start + maxVisible
	if end > len(lines) {
		end = len(lines)
	}

	for i := start; i < end; i++ {
		b.WriteString(lines[i])
		b.WriteString("\n")
	}

	if ind := scrollIndicator(start, end, len(lines)); ind != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(ind))
	}
	return b.String()
}

func (d *Dashboard) buildStatsLines() []string {
	if d.stats == nil {
		return nil
	}
	s := d.stats
	var lines []string

	lines = append(lines, headerStyle.Render("  Overview"))
	lines = append(lines, fmt.Sprintf("    Total reviews:     %d", s.TotalReviews))
	lines = append(lines, fmt.Sprintf("    Activity (24h):    %d", s.ActivityCount24h))
	lines = append(lines, fmt.Sprintf("    Avg issues/review: %.1f", s.AvgIssuesPerReview))
	lines = append(lines, "")

	if len(s.BySeverity) > 0 {
		lines = append(lines, headerStyle.Render("  By Severity"))
		sevKeys := make([]string, 0, len(s.BySeverity))
		for sev := range s.BySeverity {
			sevKeys = append(sevKeys, sev)
		}
		sort.Strings(sevKeys)
		for _, sev := range sevKeys {
			sevRendered := severityStyle(sev).Render(fmt.Sprintf("%-8s", sev))
			lines = append(lines, fmt.Sprintf("    %s %d", sevRendered, s.BySeverity[sev]))
		}
		lines = append(lines, "")
	}

	if len(s.ByCLI) > 0 {
		lines = append(lines, headerStyle.Render("  By CLI"))
		cliKeys := make([]string, 0, len(s.ByCLI))
		for k := range s.ByCLI {
			cliKeys = append(cliKeys, k)
		}
		sort.Strings(cliKeys)
		for _, k := range cliKeys {
			lines = append(lines, fmt.Sprintf("    %-10s %d", k, s.ByCLI[k]))
		}
		lines = append(lines, "")
	}

	if len(s.TopRepos) > 0 {
		lines = append(lines, headerStyle.Render("  Top Repos"))
		for _, rc := range s.TopRepos {
			lines = append(lines, fmt.Sprintf("    %-30s %d reviews", rc.Repo, rc.Count))
		}
		lines = append(lines, "")
	}

	if len(s.ReviewsLast7Days) > 0 {
		lines = append(lines, headerStyle.Render("  Reviews (last 7 days)"))
		maxBar := 30
		for _, dc := range s.ReviewsLast7Days {
			barLen := dc.Count
			if barLen > maxBar {
				barLen = maxBar
			}
			bar := strings.Repeat("█", barLen)
			lines = append(lines, fmt.Sprintf("    %s  %s (%d)", dc.Day, bar, dc.Count))
		}
		lines = append(lines, "")
	}

	if s.ReviewTiming.SampleCount > 0 {
		t := s.ReviewTiming
		lines = append(lines, headerStyle.Render("  Review Timing"))
		lines = append(lines, fmt.Sprintf("    Samples:            %d", t.SampleCount))
		lines = append(lines, fmt.Sprintf("    Avg:                %.1fs", t.AvgSeconds))
		lines = append(lines, fmt.Sprintf("    Median:             %.1fs", t.MedianSeconds))
		lines = append(lines, fmt.Sprintf("    Range:              %.1fs – %.1fs", t.MinSeconds, t.MaxSeconds))
		lines = append(lines, fmt.Sprintf("    Fast (<30s):        %d", t.BucketFast))
		lines = append(lines, fmt.Sprintf("    Medium (30-120s):   %d", t.BucketMedium))
		lines = append(lines, fmt.Sprintf("    Slow (120-300s):    %d", t.BucketSlow))
		lines = append(lines, fmt.Sprintf("    Very slow (>300s):  %d", t.BucketVerySlow))
	}

	return lines
}

func (d *Dashboard) renderHelp() string {
	if d.confirmShutdown {
		return helpStyle.Render("Stop daemon and disconnect clients?  [y]es  [n/esc]cancel")
	}
	if d.shutdownInFlight {
		return helpStyle.Render("Stopping daemon...")
	}
	if d.showDetail {
		if d.activeTab == tabIssues && d.cursor < len(d.issues) && canPromoteIssue(d.issues[d.cursor]) {
			return helpStyle.Render("[esc]close  [p]romote  [j/k]scroll  [pgup/pgdn]page  [q]uit")
		}
		return helpStyle.Render("[esc]close  [j/k]scroll  [pgup/pgdn]page  [q]uit")
	}
	if d.activeTab == tabIssues {
		return helpStyle.Render("[q]uit  [r]efresh  [s]top  [enter]detail  [p]romote  [tab]switch  [j/k]scroll  [pgup/pgdn]page  [1-7]jump")
	}
	if d.activeTab == tabPRs {
		return helpStyle.Render("[q]uit  [r]efresh  [s]top  [enter]detail  [tab]switch  [j/k]scroll  [pgup/pgdn]page  [1-7]jump")
	}
	return helpStyle.Render("[q]uit  [r]efresh  [s]top  [tab]switch  [j/k]scroll  [pgup/pgdn]page  [1-7]jump  [G]follow")
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
	case tabStats:
		return len(d.buildStatsLines())
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
		return truncateRunes(strings.Join(parts, ", "), 60)
	case map[string]any:
		b, _ := json.Marshal(val)
		return truncateRunes(string(b), 60)
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

func formatSSEData(eventType, data string) (itemType string, info string) {
	var m map[string]any
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		return "", data
	}

	switch eventType {
	case "polling_started":
		kind, _ := m["kind"].(string)
		repos, _ := m["repos"].([]any)
		return "", fmt.Sprintf("%s (%d repos)", kind, len(repos))
	case "polling_completed":
		kind, _ := m["kind"].(string)
		count := toInt(m["count"])
		ms := toInt(m["duration_ms"])
		return "", fmt.Sprintf("%s %d items in %dms", kind, count, ms)
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

func activityBadge(itemType string) string {
	switch itemType {
	case "pr":
		return logBadgeStyleFn("PR").Render(fmt.Sprintf("[%-5s]", "PR"))
	case "issue":
		return logBadgeStyleFn("ISSUE").Render(fmt.Sprintf("[%-5s]", "ISSUE"))
	default:
		return fmt.Sprintf("%-7s", "")
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

func canPromoteIssue(issue api.Issue) bool {
	if issue.LatestReview == nil {
		return false
	}
	switch issue.LatestReview.ActionTaken {
	case "review_only", "refinement":
		return true
	default:
		return false
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
	return string(runes[:maxLen-1]) + "…"
}

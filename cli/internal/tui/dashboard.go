package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
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
	tabServer
)

var tabNames = []string{"Activity", "PRs", "Issues", "Config", "Stats", "Server"}

type Dashboard struct {
	client *api.Client
	width  int
	height int

	activeTab tab
	cursor    int

	prRepoFilter string

	prs    []api.PR
	issues []api.Issue
	config map[string]any
	stats  *api.Stats

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
	// version is this CLI binary's build version; daemonVersion is the
	// server's, from /health. They are independent — a stamped CLI says
	// nothing about the daemon it happens to be pointed at.
	version         string
	daemonVersion   string
	daemonStartedAt time.Time
	// The daemon's own view of its health ("ok", "degraded"). Distinct from
	// connected, which only tracks whether the authenticated endpoints answered:
	// a degraded daemon answers everything and would otherwise look healthy.
	daemonStatus string
	// Last /health failure. Kept so the Server section can say why the version
	// is unknown: the fetch is best-effort and would otherwise leave no trail.
	daemonHealthErr error

	sseEvents    chan api.SSEEvent
	sseCtx       context.Context
	sseCancel    context.CancelFunc
	sseSessionID int64

	showDetail   bool
	detailScroll int
	detailLines  []string

	issueRepoFilter   string
	issueActionFilter string
}

type tickMsg time.Time
type dataMsg struct {
	prs      []api.PR
	issues   []api.Issue
	config   map[string]any
	stats    *api.Stats
	activity *api.ActivityResponse
	health   *api.Health
	// Set when the /health fetch itself failed. Distinct from health == nil,
	// which also covers "not fetched yet"; the difference decides whether a
	// previously known version is kept or cleared.
	healthErr error
	err       error
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
type openURLMsg struct{ err error }

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

	// Fetched first and best-effort: /health needs no token, so the Server tab
	// can still report the daemon's version when the authenticated endpoints
	// below fail with 401. A health failure is left to those endpoints to
	// report rather than blanking the whole dashboard — but it is recorded, so
	// the Server section can explain an unknown version instead of going quiet.
	if health, err := d.client.GetHealth(); err == nil {
		msg.health = health
	} else {
		msg.healthErr = err
	}

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
			} else if d.activeTab == tabActivity {
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
			} else if d.activeTab == tabActivity {
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
		// Applied before the error branch: health is fetched best-effort and
		// unauthenticated, so the daemon version survives a 401 on the rest.
		// A failed fetch clears both — reporting a last-known version next to a
		// stopped badge would misstate which build is running, and a daemon that
		// restarted on a different version would keep showing the old one.
		if msg.health != nil {
			d.daemonVersion = msg.health.DisplayVersion()
			d.daemonStartedAt = msg.health.StartedAt
			d.daemonStatus = msg.health.DisplayStatus()
			d.daemonHealthErr = nil
		} else if msg.healthErr != nil {
			d.daemonVersion = ""
			d.daemonStartedAt = time.Time{}
			d.daemonStatus = ""
			d.daemonHealthErr = msg.healthErr
		}
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
			sort.Slice(d.prs, func(i, j int) bool {
				return d.prs[i].LatestReview.CreatedAt.After(d.prs[j].LatestReview.CreatedAt)
			})
			d.issues = nil
			for _, iss := range msg.issues {
				if iss.LatestReview != nil {
					d.issues = append(d.issues, iss)
				}
			}
			sort.Slice(d.issues, func(i, j int) bool {
				return d.issues[i].LatestReview.CreatedAt.After(d.issues[j].LatestReview.CreatedAt)
			})
			d.config = msg.config
			d.stats = msg.stats
			if msg.activity != nil {
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
		d.clampCursor()
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
			// Heartbeats are liveness-only and intentionally skipped in Activity.
			return d, d.listenSSE(d.sseSessionID, d.sseCtx, d.sseEvents)
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

	case openURLMsg:
		if msg.err != nil {
			d.shutdownMessage = fmt.Sprintf("Cannot open URL: %v", msg.err)
		}
		return d, nil
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
		if d.activeTab == tabActivity {
			d.scrollLogsDown()
		} else {
			d.cursor++
			d.clampCursor()
		}
	case "k", "up":
		if d.activeTab == tabActivity {
			d.scrollLogsUp()
		} else {
			if d.cursor > 0 {
				d.cursor--
			}
		}
	case "pgdown":
		if d.activeTab == tabActivity {
			for i := 0; i < d.contentHeight(); i++ {
				d.scrollLogsDown()
			}
		} else {
			d.cursor += d.contentHeight()
			d.clampCursor()
		}
	case "pgup":
		if d.activeTab == tabActivity {
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
		if d.activeTab == tabActivity {
			d.logOffset = 0
			d.logFollow = false
		} else {
			d.cursor = 0
		}
	case "end":
		if d.activeTab == tabActivity {
			d.logFollow = true
		} else {
			max := d.tabItemCount()
			if max > 0 {
				d.cursor = max - 1
			}
		}
	case "G":
		if d.activeTab == tabActivity {
			d.logFollow = true
		} else {
			max := d.tabItemCount()
			if max > 0 {
				d.cursor = max - 1
			}
		}
	case "o":
		if d.activeTab == tabPRs {
			prs := d.visiblePRs()
			if d.cursor < len(prs) {
				return d, openURLCmd(prs[d.cursor].URL)
			}
		}
	case "f":
		switch d.activeTab {
		case tabPRs:
			d.cycleRepoFilter()
		case tabIssues:
			d.cycleIssueRepoFilter()
			d.cursor = 0
		}
	case "F":
		if d.activeTab == tabIssues {
			d.cycleIssueActionFilter()
			d.cursor = 0
		}
	case "enter":
		if d.activeTab == tabPRs && d.cursor < len(d.visiblePRs()) {
			d.openDetail()
		} else if d.activeTab == tabIssues && d.cursor < len(d.visibleIssues()) {
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
	case "o":
		if d.activeTab == tabPRs {
			prs := d.visiblePRs()
			if d.cursor < len(prs) {
				return d, openURLCmd(prs[d.cursor].URL)
			}
		}
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
	visible := d.visibleIssues()
	if d.activeTab != tabIssues || d.cursor >= len(visible) {
		return nil
	}
	issue := visible[d.cursor]
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
		d.detailLines = buildPRDetailLines(d.visiblePRs()[d.cursor], d.width)
	case tabIssues:
		d.detailLines = buildIssueDetailLines(d.visibleIssues()[d.cursor], d.width)
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

// clampScrollOffset returns offset clamped to [0, max(0, total - visible)].
// A viewport of `visible` lines starting at the returned offset still
// shows the last line of `total`-length content. Shared by clampCursor
// (when the active tab uses cursor as a scroll offset) and by renderConfig
// / renderStats (which compute their viewport `start` the same way).
func clampScrollOffset(offset, total, visible int) int {
	if visible < 1 {
		visible = 1
	}
	upper := total - visible
	if upper < 0 {
		upper = 0
	}
	if offset < 0 {
		return 0
	}
	if offset > upper {
		return upper
	}
	return offset
}

func (d *Dashboard) clampCursor() {
	max := d.tabItemCount()
	if d.isScrollOffsetTab() {
		d.cursor = clampScrollOffset(d.cursor, max, d.contentHeight())
		return
	}
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
		return d.renderLogs(height)
	case tabPRs:
		return d.renderPRs(height)
	case tabIssues:
		return d.renderIssues(height)
	case tabConfig:
		return d.renderConfig(height)
	case tabStats:
		return d.renderStats(height)
	case tabServer:
		return d.renderServer(height)
	}
	return ""
}

func (d *Dashboard) renderPRs(height int) string {
	prs := d.visiblePRs()
	if len(prs) == 0 {
		if d.prRepoFilter != "" {
			return lipgloss.NewStyle().Foreground(colorMuted).Render(
				fmt.Sprintf("  No PRs found (filtered: %s). Press [f] to cycle filter.", d.prRepoFilter))
		}
		return lipgloss.NewStyle().Foreground(colorMuted).Render("  No PRs found.")
	}

	var b strings.Builder
	header := fmt.Sprintf("  %-7s %-20s %-25s %-12s %-8s %-12s %-10s", "PR", "REPO", "TITLE", "AUTHOR", "SEVERITY", "REVIEWED", "STATE")
	b.WriteString(headerStyle.Render(header))
	b.WriteString("\n")
	b.WriteString("  " + strings.Repeat("─", 100))
	b.WriteString("\n")

	maxVisible := height - 2
	if d.prRepoFilter != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(colorSecondary).Render(
			fmt.Sprintf("  filter: %s", d.prRepoFilter)))
		b.WriteString("\n")
		maxVisible--
	}
	if maxVisible < 1 {
		maxVisible = 1
	}
	start, end := visibleRange(d.cursor, len(prs), maxVisible)

	for i := start; i < end; i++ {
		pr := prs[i]
		sev := "---"
		reviewed := "---"
		if pr.LatestReview != nil {
			sev = pr.LatestReview.Severity
			reviewed = pr.LatestReview.CreatedAt.Format("2006-01-02")
		}
		title := truncateRunes(pr.Title, 23)
		repo := truncateRunes(pr.Repo, 18)
		author := truncateRunes(pr.Author, 10)
		prNum := fmt.Sprintf("#%d", pr.Number)
		sevRendered := severityStyle(sev).Render(fmt.Sprintf("%-8s", sev))

		stateStr := pr.State
		var stateRendered string
		if pr.Dismissed {
			stateStr = "dismissed"
			stateRendered = lipgloss.NewStyle().Foreground(colorWarning).Render(fmt.Sprintf("%-10s", stateStr))
		} else {
			stateRendered = fmt.Sprintf("%-10s", stateStr)
		}

		line := fmt.Sprintf("  %-7s %-20s %-25s %-12s %s %-12s %s", prNum, repo, title, author, sevRendered, reviewed, stateRendered)

		if i == d.cursor {
			b.WriteString(selectedRowStyle.Render(line))
		} else {
			b.WriteString(line)
		}
		b.WriteString("\n")
	}

	if ind := scrollIndicator(start, end, len(prs)); ind != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(ind))
	}
	return b.String()
}

func (d *Dashboard) renderIssues(height int) string {
	issues := d.visibleIssues()
	if len(issues) == 0 {
		msg := "  No issues found."
		if d.issueRepoFilter != "" || d.issueActionFilter != "" {
			msg = "  No issues match the current filter."
		}
		return lipgloss.NewStyle().Foreground(colorMuted).Render(msg)
	}

	var b strings.Builder

	filterInfo := ""
	if d.issueRepoFilter != "" {
		filterInfo += fmt.Sprintf(" repo:%s", d.issueRepoFilter)
	}
	if d.issueActionFilter != "" {
		filterInfo += fmt.Sprintf(" action:%s", d.issueActionFilter)
	}
	if filterInfo != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(colorWarning).Render(fmt.Sprintf("  Filter:%s", filterInfo)))
		b.WriteString("\n")
	}

	header := fmt.Sprintf("  %-7s %-20s %-28s %-12s %-8s %-18s %-10s", "#", "REPO", "TITLE", "AUTHOR", "SEVERITY", "ACTION", "DATE")
	b.WriteString(headerStyle.Render(header))
	b.WriteString("\n")
	b.WriteString("  " + strings.Repeat("─", 107))
	b.WriteString("\n")

	extraLines := 2
	if filterInfo != "" {
		extraLines = 3
	}
	maxVisible := height - extraLines
	if maxVisible < 1 {
		maxVisible = 1
	}
	start, end := visibleRange(d.cursor, len(issues), maxVisible)

	for i := start; i < end; i++ {
		iss := issues[i]
		sev := "---"
		action := "---"
		dateStr := "---"
		if iss.LatestReview != nil {
			sev = extractSeverity(iss.LatestReview.Triage)
			action = humanizeAction(iss.LatestReview)
			dateStr = timeAgo(iss.LatestReview.CreatedAt)
		}

		number := fmt.Sprintf("#%d", iss.Number)
		if iss.Dismissed {
			number += " D"
		}
		title := truncateRunes(iss.Title, 26)
		repo := truncateRunes(iss.Repo, 18)
		author := truncateRunes(iss.Author, 10)
		sevRendered := severityStyle(sev).Render(fmt.Sprintf("%-8s", sev))
		line := fmt.Sprintf("  %-7s %-20s %-28s %-12s %s %-18s %-10s", number, repo, title, author, sevRendered, action, dateStr)

		if i == d.cursor {
			b.WriteString(selectedRowStyle.Render(line))
		} else {
			b.WriteString(line)
		}
		b.WriteString("\n")
	}

	if ind := scrollIndicator(start, end, len(issues)); ind != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(ind))
	}
	return b.String()
}

func (d *Dashboard) renderConfig(height int) string {
	if d.config == nil {
		return lipgloss.NewStyle().Foreground(colorMuted).Render("  No configuration loaded.")
	}

	lines := d.buildConfigLines()
	if len(lines) == 0 {
		return ""
	}

	var b strings.Builder
	start := clampScrollOffset(d.cursor, len(lines), height)
	end := start + height
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
	// /health answering at all is proof the daemon is up, and its own word
	// outranks connectedness, which only tracks the authenticated endpoints. This
	// is checked BEFORE !connected on purpose: fetchData can read a 503
	// "degraded" payload and then fail on /prs, /config or /stats, and reporting
	// "stopped" for a daemon that just answered would hide the degradation behind
	// an outright wrong state.
	if d.daemonHealthErr == nil && d.daemonStatus != "" {
		if d.daemonStatus != "ok" {
			return lipgloss.NewStyle().Foreground(colorWarning).Bold(true).
				Render("● " + d.daemonStatus)
		}
		return lipgloss.NewStyle().Foreground(colorSuccess).Bold(true).Render("● running")
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

	// Daemon row — the daemon's own version, reported by /health. Labelled in
	// parallel with the CLI row below: under a "Server" header a bare "Version"
	// reads as the whole product's, which is the ambiguity this pair resolves.
	// Empty means the daemon was unreachable, has not been reached yet, or
	// predates version stamping (older builds omit the field).
	daemonVersion := d.daemonVersion
	if daemonVersion == "" {
		daemonVersion = mutedNote.Render("(unknown)")
		if d.daemonHealthErr != nil {
			daemonVersion += mutedNote.Render(
				" — " + api.DisplayText(d.daemonHealthErr.Error(), 60))
		}
	}
	b.WriteString(fmt.Sprintf("  %-10s %s\n", "Daemon", daemonVersion))

	// CLI row — this binary's build version, shown separately so a mismatch
	// against the daemon is visible instead of being conflated with it.
	cliVersion := d.version
	if cliVersion == "" {
		cliVersion = mutedNote.Render("(unknown)")
	}
	b.WriteString(fmt.Sprintf("  %-10s %s\n", "CLI", cliVersion))

	// Uptime row — the daemon's, from /health started_at. The CLI's own uptime
	// belongs to the footer status bar; reporting it here next to a server-side
	// version row would read as the server's. Unknown for daemons that omit
	// started_at, and while unreachable.
	// Clamped at zero: a daemon on another host whose clock runs ahead of ours
	// reports a started_at in the future, which would render as a negative age.
	uptime := mutedNote.Render("(unknown)")
	if !d.daemonStartedAt.IsZero() {
		age := time.Since(d.daemonStartedAt).Truncate(time.Second)
		if age < 0 {
			age = 0
		}
		uptime = age.String()
	}
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
	start := clampScrollOffset(d.cursor, len(lines), maxVisible)
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
		if d.activeTab == tabPRs {
			return helpStyle.Render("[esc]close  [o]pen in browser  [j/k]scroll  [pgup/pgdn]page  [q]uit")
		}
		visible := d.visibleIssues()
		if d.activeTab == tabIssues && d.cursor < len(visible) && canPromoteIssue(visible[d.cursor]) {
			return helpStyle.Render("[esc]close  [p]romote  [j/k]scroll  [pgup/pgdn]page  [q]uit")
		}
		return helpStyle.Render("[esc]close  [j/k]scroll  [pgup/pgdn]page  [q]uit")
	}
	if d.activeTab == tabIssues {
		return helpStyle.Render("[q]uit  [r]efresh  [s]top  [enter]detail  [p]romote  [f]ilter repo  [F]ilter action  [tab]switch  [j/k]scroll  [1-6]jump")
	}
	if d.activeTab == tabPRs {
		return helpStyle.Render("[q]uit  [r]efresh  [s]top  [enter]detail  [o]pen  [f]ilter repo  [tab]switch  [j/k]scroll  [pgup/pgdn]page  [1-6]jump")
	}
	if d.activeTab == tabActivity {
		return helpStyle.Render("[q]uit  [r]efresh  [s]top  [tab]switch  [j/k]scroll  [pgup/pgdn]page  [1-6]jump  [G]follow")
	}
	return helpStyle.Render("[q]uit  [r]efresh  [s]top  [tab]switch  [j/k]scroll  [pgup/pgdn]page  [1-6]jump")
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
		// Activity scrolls via d.logOffset / d.logFollow, not d.cursor.
		// clampCursor never reads this value for tabActivity, and the
		// cursor-bound branches in handleKey skip tabActivity outright.
		// Return 0 so any accidental future caller treats it as empty.
		return 0
	case tabPRs:
		return len(d.visiblePRs())
	case tabIssues:
		return len(d.visibleIssues())
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

	keyStyle := lipgloss.NewStyle().Foreground(colorMuted)
	valStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#E5E7EB"))

	var lines []string

	section := func(title string) {
		label := fmt.Sprintf("── %s ", title)
		pad := 50 - len([]rune(label))
		if pad < 0 {
			pad = 0
		}
		lines = append(lines, headerStyle.Render(label+strings.Repeat("─", pad)))
	}

	kv := func(key, val string) {
		if val == "" {
			val = "—"
		}
		lines = append(lines, fmt.Sprintf("  %s %s",
			keyStyle.Render(fmt.Sprintf("%-22s", key+":")),
			valStyle.Render(val)))
	}

	bulletList := func(items []string) {
		for _, item := range items {
			lines = append(lines, valStyle.Render("    • "+item))
		}
	}

	blank := func() { lines = append(lines, "") }

	str := func(key string) string {
		if v, ok := d.config[key].(string); ok && v != "" {
			return v
		}
		return "—"
	}

	boolStr := func(key string) string {
		if v, ok := d.config[key].(bool); ok {
			if v {
				return "true"
			}
			return "false"
		}
		return "—"
	}

	// ── Server ──
	section("Server")
	if n := toInt(d.config["server_port"]); n != 0 {
		kv("Port", fmt.Sprintf("%d", n))
	}
	if v, ok := d.config["bind_addr"].(string); ok && v != "" {
		kv("Bind", v)
	}
	kv("Poll interval", str("poll_interval"))
	blank()

	// ── Repositories ──
	repos := configStringSlice(d.config["repositories"])
	section(fmt.Sprintf("Repositories (%d)", len(repos)))
	if len(repos) == 0 {
		lines = append(lines, lipgloss.NewStyle().Foreground(colorMuted).Render("    (none)"))
	} else {
		bulletList(repos)
	}
	blank()

	nonMon := configStringSlice(d.config["non_monitored"])
	if len(nonMon) > 0 {
		section(fmt.Sprintf("Non-Monitored (%d)", len(nonMon)))
		bulletList(nonMon)
		blank()
	}

	// ── AI ──
	section("AI")
	kv("Primary", str("ai_primary"))
	kv("Fallback", str("ai_fallback"))
	kv("Mode", str("review_mode"))
	kv("Refinement timeout", str("refinement_timeout"))
	kv("Triage owner", str("triage_owner"))
	kv("Clone dir", str("clone_dir"))
	kv("Generate PR desc", boolStr("generate_pr_description"))
	if _, ok := d.config["auto_promote_triage"]; ok {
		kv("Auto promote triage", boolStr("auto_promote_triage"))
	}
	if _, ok := d.config["auto_promote_refinement"]; ok {
		kv("Auto promote refine", boolStr("auto_promote_refinement"))
	}
	blank()

	// ── Issue Tracking ──
	if itRaw, ok := d.config["issue_tracking"]; ok {
		section("Issue Tracking")
		if it, ok := itRaw.(map[string]any); ok {
			if v, ok := it["enabled"].(bool); ok {
				kv("Enabled", fmt.Sprintf("%v", v))
			}
			if v, ok := it["filter_mode"].(string); ok && v != "" {
				kv("Filter mode", v)
			}
			for _, pair := range [][2]string{
				{"develop_labels", "Develop"},
				{"refinement_labels", "Refinement"},
				{"review_only_labels", "Review only"},
				{"skip_labels", "Skip"},
				{"blocked_labels", "Blocked"},
			} {
				if labels := configStringSlice(it[pair[0]]); len(labels) > 0 {
					kv(pair[1], strings.Join(labels, ", "))
				}
			}
			if v, ok := it["promote_to_label"].(string); ok && v != "" {
				kv("Promote to label", v)
			}
			if v, ok := it["default_action"].(string); ok && v != "" {
				kv("Default action", v)
			}
			if orgs := configStringSlice(it["organizations"]); len(orgs) > 0 {
				kv("Organizations", strings.Join(orgs, ", "))
			}
			if assignees := configStringSlice(it["assignees"]); len(assignees) > 0 {
				kv("Assignees", strings.Join(assignees, ", "))
			}
		}
		blank()
	}

	// ── PR Metadata (defaults) ──
	if pmRaw, ok := d.config["pr_metadata"]; ok {
		if pm, ok := pmRaw.(map[string]any); ok && len(pm) > 0 {
			section("PR Metadata (defaults)")
			if reviewers := configStringSlice(pm["reviewers"]); len(reviewers) > 0 {
				kv("Reviewers", strings.Join(reviewers, ", "))
			}
			if labels := configStringSlice(pm["labels"]); len(labels) > 0 {
				kv("Labels", strings.Join(labels, ", "))
			}
			if v, ok := pm["pr_assignee"].(string); ok && v != "" {
				kv("Assignee", v)
			}
			if v, ok := pm["pr_draft"].(bool); ok {
				kv("Draft", fmt.Sprintf("%v", v))
			}
			blank()
		}
	}

	// ── Agent Configs ──
	if acRaw, ok := d.config["agent_configs"]; ok {
		if ac, ok := acRaw.(map[string]any); ok && len(ac) > 0 {
			section(fmt.Sprintf("Agent Configs (%d)", len(ac)))
			for _, name := range configSortedKeys(ac) {
				lines = append(lines, valStyle.Render("    "+name))
				if agent, ok := ac[name].(map[string]any); ok {
					for _, field := range []string{"model", "max_turns", "approval_mode", "effort", "permission_mode", "prompt"} {
						if v, ok := agent[field]; ok {
							s := fmt.Sprintf("%v", v)
							if s != "" && s != "0" && s != "false" {
								lines = append(lines, fmt.Sprintf("      %s %s",
									keyStyle.Render(fmt.Sprintf("%-20s", field+":")),
									valStyle.Render(s)))
							}
						}
					}
				}
			}
			blank()
		}
	}

	// ── Repo Overrides ──
	if roRaw, ok := d.config["repo_overrides"]; ok {
		if ro, ok := roRaw.(map[string]any); ok && len(ro) > 0 {
			section(fmt.Sprintf("Repo Overrides (%d)", len(ro)))
			for _, name := range configSortedKeys(ro) {
				lines = append(lines, valStyle.Render("    "+name))
				if over, ok := ro[name].(map[string]any); ok {
					for _, k := range configSortedKeys(over) {
						s := configFlatValue(over[k])
						if s != "" {
							lines = append(lines, fmt.Sprintf("      %s %s",
								keyStyle.Render(fmt.Sprintf("%-22s", k+":")),
								valStyle.Render(s)))
						}
					}
				}
			}
			blank()
		}
	}

	// ── Org Overrides ──
	if ooRaw, ok := d.config["org_overrides"]; ok {
		if oo, ok := ooRaw.(map[string]any); ok && len(oo) > 0 {
			section(fmt.Sprintf("Org Overrides (%d)", len(oo)))
			for _, name := range configSortedKeys(oo) {
				lines = append(lines, valStyle.Render("    "+name))
				if over, ok := oo[name].(map[string]any); ok {
					for _, k := range configSortedKeys(over) {
						s := configFlatValue(over[k])
						if s != "" {
							lines = append(lines, fmt.Sprintf("      %s %s",
								keyStyle.Render(fmt.Sprintf("%-22s", k+":")),
								valStyle.Render(s)))
						}
					}
				}
			}
			blank()
		}
	}

	// ── Local Directories ──
	localDirBase := configStringSlice(d.config["local_dir_base"])
	ldMap, hasDetected := d.config["local_dirs_detected"].(map[string]any)
	if len(localDirBase) > 0 || (hasDetected && len(ldMap) > 0) {
		section("Local Directories")
		if len(localDirBase) > 0 {
			kv("Base paths", strings.Join(localDirBase, ", "))
		}
		if hasDetected && len(ldMap) > 0 {
			lines = append(lines, keyStyle.Render("  Detected:"))
			for _, repo := range configSortedKeys(ldMap) {
				path := fmt.Sprintf("%v", ldMap[repo])
				lines = append(lines, fmt.Sprintf("    %s  %s",
					valStyle.Render("• "+repo),
					keyStyle.Render("→ "+path)))
			}
		}
		blank()
	}

	// ── Activity Log ──
	if _, ok := d.config["activity_log_enabled"]; ok {
		section("Activity Log")
		kv("Enabled", boolStr("activity_log_enabled"))
		if n := toInt(d.config["activity_log_retention_days"]); n != 0 {
			kv("Retention", fmt.Sprintf("%d days", n))
		}
		blank()
	}

	// ── Retention ──
	if n := toInt(d.config["retention_days"]); n != 0 {
		section("Retention")
		kv("Max days", fmt.Sprintf("%d", n))
		blank()
	}

	// ── Autonomous Mode ──
	if autRaw, ok := d.config["autonomous"]; ok {
		if aut, ok := autRaw.(map[string]any); ok {
			section("Autonomous Mode")
			if v, ok := aut["enabled"].(bool); ok {
				kv("Enabled", fmt.Sprintf("%v", v))
			}
			if v, ok := aut["auto_merge"].(bool); ok {
				kv("Auto merge", fmt.Sprintf("%v", v))
			}
			if v, ok := aut["merge_method"].(string); ok && v != "" {
				kv("Merge method", v)
			}
			if f, ok := aut["dev_max_turns"].(float64); ok {
				if int(f) == 0 {
					kv("Dev max turns", "unlimited")
				} else {
					kv("Dev max turns", fmt.Sprintf("%d", int(f)))
				}
			}
			if v, ok := aut["dev_effort"].(string); ok && v != "" {
				kv("Dev effort", v)
			}
			if v, ok := aut["dev_timeout"].(string); ok && v != "" {
				kv("Dev timeout", v)
			}
			if v, ok := aut["claim_lease"].(string); ok && v != "" {
				kv("Claim lease", v)
			}
			if v, ok := aut["take_others_tasks"].(bool); ok {
				kv("Take others tasks", fmt.Sprintf("%v", v))
			}
			if v, ok := aut["reassign_on_take"].(bool); ok {
				kv("Reassign on take", fmt.Sprintf("%v", v))
			}
			blank()
		}
	}

	// ── Circuit Breaker ──
	if cbRaw, ok := d.config["circuit_breaker"]; ok {
		if cb, ok := cbRaw.(map[string]any); ok {
			section("Circuit Breaker")
			if f, ok := cb["per_pr_24h"].(float64); ok {
				kv("Per PR / 24h", fmt.Sprintf("%d", int(f)))
			}
			if f, ok := cb["per_repo_hr"].(float64); ok {
				kv("Per repo / hr", fmt.Sprintf("%d", int(f)))
			}
			if f, ok := cb["per_issue_24h"].(float64); ok {
				kv("Per issue / 24h", fmt.Sprintf("%d", int(f)))
			}
			if f, ok := cb["per_issue_repo_hr"].(float64); ok {
				kv("Per issue-repo / hr", fmt.Sprintf("%d", int(f)))
			}
			if f, ok := cb["per_impl_repo_hr"].(float64); ok {
				kv("Per impl-repo / hr", fmt.Sprintf("%d", int(f)))
			}
			blank()
		}
	}

	// ── Polling / Rate Limit ──
	if pRaw, ok := d.config["polling"]; ok {
		if p, ok := pRaw.(map[string]any); ok {
			section("Polling / Rate Limit")
			if v, ok := p["adaptive"].(bool); ok {
				kv("Adaptive", fmt.Sprintf("%v", v))
			}
			if v, ok := p["poll_interval"].(string); ok && v != "" {
				kv("Poll interval", v)
			}
			if v, ok := p["min_interval"].(string); ok && v != "" {
				kv("Min interval", v)
			}
			if v, ok := p["max_interval"].(string); ok && v != "" {
				kv("Max interval", v)
			}
			if v, ok := p["discovery_interval"].(string); ok && v != "" {
				kv("Discovery interval", v)
			}
			if v, ok := p["tier3_interval"].(string); ok && v != "" {
				kv("Tier3 interval", v)
			}
			if v, ok := p["rate_limit_safety_threshold"].(float64); ok {
				kv("Rate-limit safety threshold", fmt.Sprintf("%d", int(v)))
			}
			if v, ok := p["use_etag"].(bool); ok {
				kv("ETag/304 caching", fmt.Sprintf("%v", v))
			}
			if v, ok := p["use_graphql"].(bool); ok {
				kv("GraphQL batching", fmt.Sprintf("%v", v))
			}
			blank()
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

func configStringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			result = append(result, s)
		} else {
			result = append(result, fmt.Sprintf("%v", item))
		}
	}
	return result
}

func configSortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func configFlatValue(v any) string {
	switch val := v.(type) {
	case []any:
		parts := make([]string, len(val))
		for i, item := range val {
			parts[i] = fmt.Sprintf("%v", item)
		}
		return strings.Join(parts, ", ")
	case map[string]any:
		b, _ := json.Marshal(val)
		return string(b)
	case float64:
		if val == float64(int(val)) {
			return fmt.Sprintf("%d", int(val))
		}
		return fmt.Sprintf("%g", val)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", v)
	}
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

func (d *Dashboard) visiblePRs() []api.PR {
	if d.prRepoFilter == "" {
		return d.prs
	}
	var filtered []api.PR
	for _, pr := range d.prs {
		if pr.Repo == d.prRepoFilter {
			filtered = append(filtered, pr)
		}
	}
	return filtered
}

func (d *Dashboard) cycleRepoFilter() {
	repos := make(map[string]bool)
	for _, pr := range d.prs {
		repos[pr.Repo] = true
	}
	if len(repos) == 0 {
		d.prRepoFilter = ""
		return
	}
	sorted := make([]string, 0, len(repos))
	for r := range repos {
		sorted = append(sorted, r)
	}
	sort.Strings(sorted)

	if d.prRepoFilter == "" {
		d.prRepoFilter = sorted[0]
		d.cursor = 0
		return
	}
	for i, r := range sorted {
		if r == d.prRepoFilter {
			if i+1 < len(sorted) {
				d.prRepoFilter = sorted[i+1]
			} else {
				d.prRepoFilter = ""
			}
			d.cursor = 0
			return
		}
	}
	d.prRepoFilter = ""
	d.cursor = 0
}

func openURLCmd(url string) tea.Cmd {
	return func() tea.Msg {
		var cmd *exec.Cmd
		switch runtime.GOOS {
		case "darwin":
			cmd = exec.Command("open", url)
		case "windows":
			cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
		default:
			cmd = exec.Command("xdg-open", url)
		}
		return openURLMsg{err: cmd.Start()}
	}
}

func humanizeAction(r *api.IssueReview) string {
	if r == nil {
		return "---"
	}
	switch r.ActionTaken {
	case "review_only":
		return "Triaged"
	case "auto_implement":
		if r.PRCreated > 0 {
			return fmt.Sprintf("→ PR #%d", r.PRCreated)
		}
		return "Implemented"
	case "refinement":
		return "Refined"
	default:
		return r.ActionTaken
	}
}

func timeAgo(t time.Time) string {
	if t.IsZero() {
		return "---"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("Jan 02")
	}
}

func parseLabels(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var names []string
	if json.Unmarshal(raw, &names) == nil {
		return names
	}
	var objects []struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &objects) == nil {
		// Fresh slice — the prior Unmarshal into `names` may have
		// partially populated it with zero-value strings before the
		// type error surfaced, which would leak empty entries here.
		out := make([]string, 0, len(objects))
		for _, o := range objects {
			out = append(out, o.Name)
		}
		return out
	}
	return nil
}

func (d *Dashboard) visibleIssues() []api.Issue {
	if d.issueRepoFilter == "" && d.issueActionFilter == "" {
		return d.issues
	}
	var result []api.Issue
	for _, iss := range d.issues {
		if d.issueRepoFilter != "" && iss.Repo != d.issueRepoFilter {
			continue
		}
		if d.issueActionFilter != "" {
			action := ""
			if iss.LatestReview != nil {
				action = iss.LatestReview.ActionTaken
			}
			if action != d.issueActionFilter {
				continue
			}
		}
		result = append(result, iss)
	}
	return result
}

func (d *Dashboard) cycleIssueRepoFilter() {
	repos := d.uniqueIssueRepos()
	if len(repos) == 0 {
		d.issueRepoFilter = ""
		return
	}
	if d.issueRepoFilter == "" {
		d.issueRepoFilter = repos[0]
		return
	}
	for i, r := range repos {
		if r == d.issueRepoFilter {
			if i+1 < len(repos) {
				d.issueRepoFilter = repos[i+1]
			} else {
				d.issueRepoFilter = ""
			}
			return
		}
	}
	d.issueRepoFilter = ""
}

func (d *Dashboard) cycleIssueActionFilter() {
	actions := d.uniqueIssueActions()
	if len(actions) == 0 {
		d.issueActionFilter = ""
		return
	}
	if d.issueActionFilter == "" {
		d.issueActionFilter = actions[0]
		return
	}
	for i, a := range actions {
		if a == d.issueActionFilter {
			if i+1 < len(actions) {
				d.issueActionFilter = actions[i+1]
			} else {
				d.issueActionFilter = ""
			}
			return
		}
	}
	d.issueActionFilter = ""
}

func (d *Dashboard) uniqueIssueRepos() []string {
	seen := map[string]bool{}
	var repos []string
	for _, iss := range d.issues {
		if !seen[iss.Repo] {
			seen[iss.Repo] = true
			repos = append(repos, iss.Repo)
		}
	}
	sort.Strings(repos)
	return repos
}

func (d *Dashboard) uniqueIssueActions() []string {
	seen := map[string]bool{}
	var actions []string
	for _, iss := range d.issues {
		if iss.LatestReview != nil && !seen[iss.LatestReview.ActionTaken] {
			seen[iss.LatestReview.ActionTaken] = true
			actions = append(actions, iss.LatestReview.ActionTaken)
		}
	}
	sort.Strings(actions)
	return actions
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

package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/theburrowhub/heimdallm/cli/internal/api"
)

type logStatus int

const (
	logNeutral  logStatus = iota
	logProgress           // in-progress / started
	logSuccess            // completed successfully
	logError              // failed
)

type logLine struct {
	Time    string
	Badge   string // "PR", "ISSUE", "REPO"
	Action  string // "Detected", "Review ▶", "Implement ✓", etc.
	Target  string // "org/repo #123"
	Details string // "severity=low", "error: …", "→ PR #157"
	Status  logStatus
}

func sseToLogLine(evt api.SSEEvent) logLine {
	var m map[string]any
	_ = json.Unmarshal([]byte(evt.Data), &m)
	if m == nil {
		m = make(map[string]any)
	}

	repo, _ := m["repo"].(string)
	errMsg, _ := m["error"].(string)
	title, _ := m["pr_title"].(string)
	if title == "" {
		title, _ = m["issue_title"].(string)
	}
	author, _ := m["author"].(string)

	line := logLine{
		Time: time.Now().Format("15:04:05"),
	}

	var num int
	switch evt.Type {
	case "pr_detected":
		num = jsonInt(m, "pr_number")
		line.Badge = "PR"
		line.Action = "Detected"
		line.Status = logNeutral

	case "review_started":
		num = jsonInt(m, "pr_number")
		line.Badge = "PR"
		line.Action = "Review ▶"
		line.Status = logProgress

	case "review_completed":
		num = jsonInt(m, "pr_number")
		line.Badge = "PR"
		line.Action = "Review ✓"
		line.Status = logSuccess
		if sev, ok := m["severity"].(string); ok {
			line.Details = "severity=" + sev
		}

	case "review_error":
		num = jsonInt(m, "pr_number")
		line.Badge = "PR"
		line.Action = "Review ✗"
		line.Status = logError
		if errMsg != "" {
			line.Details = "error: " + errMsg
		}

	case "issue_detected":
		num = jsonInt(m, "issue_number")
		line.Badge = "ISSUE"
		line.Action = "Detected"
		line.Status = logNeutral

	case "issue_review_started":
		num = jsonInt(m, "issue_number")
		line.Badge = "ISSUE"
		action := "Review"
		if a, ok := m["action"].(string); ok && a == "implement" {
			action = "Implement"
		}
		line.Action = action + " ▶"
		line.Status = logProgress

	case "issue_review_completed":
		num = jsonInt(m, "issue_number")
		line.Badge = "ISSUE"
		line.Action = "Review ✓"
		line.Status = logSuccess
		if sev, ok := m["severity"].(string); ok {
			line.Details = "severity=" + sev
		}

	case "issue_implemented":
		num = jsonInt(m, "issue_number")
		line.Badge = "ISSUE"
		line.Action = "Implement ✓"
		line.Status = logSuccess
		if pr := jsonInt(m, "pr_created"); pr > 0 {
			line.Details = fmt.Sprintf("→ PR #%d", pr)
		}

	case "issue_review_error":
		num = jsonInt(m, "issue_number")
		line.Badge = "ISSUE"
		action := "Review"
		if a, ok := m["action"].(string); ok && a == "implement" {
			action = "Implement"
		}
		line.Action = action + " ✗"
		line.Status = logError
		if errMsg != "" {
			line.Details = "error: " + errMsg
		}

	case "issue_promoted":
		num = jsonInt(m, "issue_number")
		line.Badge = "ISSUE"
		line.Action = "Promoted"
		line.Status = logSuccess
		from, _ := m["from_stage"].(string)
		to, _ := m["to_stage"].(string)
		if from == "" && to == "" {
			from, _ = m["from_label"].(string)
			to, _ = m["to_label"].(string)
		}
		if from != "" || to != "" {
			line.Details = strings.TrimSpace(from + " -> " + to)
		}

	case "repo_discovered":
		line.Badge = "REPO"
		line.Action = "Discovered"
		line.Status = logNeutral
		line.Target = repo
		return line

	default:
		line.Badge = "EVENT"
		line.Action = humanize(evt.Type)
		line.Status = logNeutral
		_, line.Target = formatSSEData(evt.Type, evt.Data)
		return line
	}

	if repo != "" && num > 0 {
		line.Target = fmt.Sprintf("%s #%d", repo, num)
		if title != "" {
			line.Target += " " + truncateRunes(title, 40)
		}
	} else if repo != "" {
		line.Target = repo
	}

	if author != "" {
		if line.Details != "" {
			line.Details = "by @" + author + "  " + line.Details
		} else {
			line.Details = "by @" + author
		}
	}

	return line
}

func activityToLogLine(e api.ActivityEntry) logLine {
	line := logLine{
		Time: formatLogTime(e.TS),
	}

	switch e.ItemType {
	case "pr":
		line.Badge = "PR"
	case "issue":
		line.Badge = "ISSUE"
	default:
		if e.ItemType != "" {
			line.Badge = strings.ToUpper(e.ItemType)
		} else {
			line.Badge = "EVENT"
		}
	}

	if e.ItemNumber > 0 {
		line.Target = fmt.Sprintf("%s #%d", e.Repo, e.ItemNumber)
		if e.ItemTitle != "" {
			line.Target += " " + truncateRunes(e.ItemTitle, 40)
		}
	} else if e.Repo != "" {
		line.Target = e.Repo
	}

	action := humanize(e.Action)
	switch e.Outcome {
	case "error", "failed":
		line.Action = action + " ✗"
		line.Status = logError
	case "success", "completed":
		line.Action = action + " ✓"
		line.Status = logSuccess
	case "started", "in_progress":
		line.Action = action + " ▶"
		line.Status = logProgress
	default:
		line.Action = action
		line.Status = logNeutral
	}

	if a, ok := e.Details["author"]; ok {
		if s, ok := a.(string); ok && s != "" {
			line.Details = "by @" + s
		}
	}
	if sev, ok := e.Details["severity"]; ok {
		line.Details = appendDetail(line.Details, fmt.Sprintf("severity=%v", sev))
	}
	if em, ok := e.Details["error"]; ok {
		line.Details = appendDetail(line.Details, fmt.Sprintf("error: %v", em))
	}
	if pr, ok := e.Details["pr_created"]; ok {
		line.Details = appendDetail(line.Details, fmt.Sprintf("→ PR #%v", pr))
	}

	return line
}

func formatLogTime(ts string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	return t.Format("15:04:05")
}

func jsonInt(m map[string]any, key string) int {
	v, ok := m[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return 0
	}
}

func humanize(s string) string {
	s = strings.ReplaceAll(strings.ToLower(s), "_", " ")
	if len(s) > 0 {
		return strings.ToUpper(s[:1]) + s[1:]
	}
	return s
}

func appendDetail(existing, extra string) string {
	if existing == "" {
		return extra
	}
	return existing + "  " + extra
}

var (
	logActionStyleFn = func(status logStatus) lipgloss.Style {
		switch status {
		case logSuccess:
			return lipgloss.NewStyle().Foreground(colorSuccess)
		case logError:
			return lipgloss.NewStyle().Foreground(colorDanger)
		case logProgress:
			return lipgloss.NewStyle().Foreground(colorWarning)
		default:
			return lipgloss.NewStyle()
		}
	}

	logBadgeStyleFn = func(badge string) lipgloss.Style {
		switch badge {
		case "PR":
			return lipgloss.NewStyle().Foreground(colorSecondary)
		case "ISSUE":
			return lipgloss.NewStyle().Foreground(colorPrimary)
		default:
			return lipgloss.NewStyle().Foreground(colorMuted)
		}
	}
)

func (d *Dashboard) renderLogs(height int) string {
	if len(d.logLines) == 0 {
		return lipgloss.NewStyle().Foreground(colorMuted).Render("  No activity yet. Events will appear here in real-time.")
	}

	var b strings.Builder

	maxVisible := height
	total := len(d.logLines)

	var start int
	if d.logFollow {
		start = total - maxVisible
	} else {
		start = d.logOffset
	}
	if start < 0 {
		start = 0
	}

	end := start + maxVisible
	if end > total {
		end = total
	}

	for _, l := range d.logLines[start:end] {
		badge := logBadgeStyleFn(l.Badge).Render(fmt.Sprintf("[%-5s]", l.Badge))
		action := logActionStyleFn(l.Status).Render(fmt.Sprintf("%-13s", l.Action))
		line := fmt.Sprintf("  %s %s %s %s", l.Time, badge, action, l.Target)
		if l.Details != "" {
			line += "  " + lipgloss.NewStyle().Foreground(colorMuted).Render(l.Details)
		}
		b.WriteString(line)
		b.WriteString("\n")
	}

	if d.logFollow && total > maxVisible {
		b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render("  ── following ──"))
	} else if !d.logFollow {
		remaining := total - end
		if remaining > 0 {
			b.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(
				fmt.Sprintf("  ── %d more below (G to follow) ──", remaining)))
		}
	}

	return b.String()
}

func (d *Dashboard) scrollLogsDown() {
	contentHeight := d.height - 10
	if contentHeight < 5 {
		contentHeight = 5
	}
	if d.logFollow {
		return
	}
	d.logOffset++
	maxOffset := len(d.logLines) - contentHeight
	if maxOffset < 0 {
		maxOffset = 0
	}
	if d.logOffset >= maxOffset {
		d.logOffset = maxOffset
		d.logFollow = true
	}
}

func (d *Dashboard) scrollLogsUp() {
	contentHeight := d.height - 10
	if contentHeight < 5 {
		contentHeight = 5
	}
	if d.logFollow {
		maxOffset := len(d.logLines) - contentHeight
		if maxOffset < 0 {
			maxOffset = 0
		}
		d.logOffset = maxOffset
		d.logFollow = false
	}
	if d.logOffset > 0 {
		d.logOffset--
	}
}

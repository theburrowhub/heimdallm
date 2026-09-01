package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/theburrowhub/heimdallm/cli/internal/api"
)

// buildInstanceLines renders the fleet as scrollable rows.
//
// The TUI talks to one daemon at a time (chosen with --instance), so this tab
// answers "what else is out there and is it healthy", not "show me everyone's
// PRs at once". Fanning every list out across instances would duplicate the
// GUI's aggregation for far less benefit in a terminal.
func (d *Dashboard) buildInstanceLines() []string {
	if d.registry == nil {
		return []string{
			"  This daemon is not a cluster hub, so it manages no instances.",
			"",
			"  Set role = \"hub\" under [cluster] in its config.toml to enable one,",
			"  then register the other daemons with `heimdallm-cli instances`.",
		}
	}
	if len(d.registry.Instances) == 0 {
		return []string{"  No instances registered."}
	}

	lines := []string{
		fmt.Sprintf("  Hub: %s (%s)",
			api.DisplayText(d.registry.SelfName, 40), api.DisplayText(d.registry.SelfID, 40)),
		"",
	}
	for _, inst := range d.registry.Instances {
		markers := make([]string, 0, 3)
		if inst.Self {
			markers = append(markers, "hub")
		}
		if inst.IsFallback {
			markers = append(markers, "default")
		}
		if inst.InPool {
			markers = append(markers, "pool")
		}
		suffix := ""
		if len(markers) > 0 {
			suffix = "  [" + strings.Join(markers, ",") + "]"
		}

		lines = append(lines, fmt.Sprintf("  %-16s %-12s %s%s",
			api.DisplayText(inst.ID, 30),
			instanceStatusLabel(inst),
			api.DisplayText(inst.DisplayName(), 34),
			suffix,
		))

		details := []string{api.DisplayText(inst.BaseURL, 60)}
		if inst.State != nil && inst.State.Version != "" {
			details = append(details, api.DisplayText(inst.State.Version, 24))
		}
		details = append(details, fmt.Sprintf("%d repos", inst.AssignedRepos))
		lines = append(lines, "    "+strings.Join(details, " · "))

		if problem := instanceProblem(inst); problem != "" {
			lines = append(lines, "    "+problem)
		}
		lines = append(lines, "")
	}
	return lines
}

func instanceStatusLabel(inst api.Instance) string {
	switch {
	case inst.TokenError != "":
		return "token error"
	case !inst.Enabled:
		return "disabled"
	case inst.State == nil:
		return "not probed"
	case inst.State.Reachable:
		return "reachable"
	default:
		return "UNREACHABLE"
	}
}

// instanceProblem returns the one line worth showing about a broken instance,
// or empty when it is healthy.
func instanceProblem(inst api.Instance) string {
	if inst.TokenError != "" {
		return "token: " + api.DisplayText(inst.TokenError, 100)
	}
	if inst.State == nil || inst.State.Reachable || inst.State.LastError == "" {
		return ""
	}
	msg := "error: " + api.DisplayText(inst.State.LastError, 100)
	if inst.State.ConsecutiveFailures > 1 {
		msg += fmt.Sprintf(" (%d failed probes)", inst.State.ConsecutiveFailures)
	}
	return msg
}

func (d *Dashboard) renderInstances(height int) string {
	lines := d.buildInstanceLines()
	if len(lines) == 0 {
		return lipgloss.NewStyle().Foreground(colorMuted).Render("  No instances.")
	}

	var b strings.Builder
	b.WriteString(headerStyle.Render("  Instances"))
	b.WriteString("\n")
	b.WriteString("  " + strings.Repeat("─", 64))
	b.WriteString("\n")

	maxVisible := height - 2
	if maxVisible < 1 {
		maxVisible = 1
	}
	start := 0
	if d.cursor >= maxVisible {
		start = d.cursor - maxVisible + 1
	}
	for i := start; i < len(lines) && i < start+maxVisible; i++ {
		b.WriteString(lines[i])
		b.WriteString("\n")
	}
	return b.String()
}

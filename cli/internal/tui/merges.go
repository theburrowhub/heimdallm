package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/theburrowhub/heimdallm/cli/internal/api"
)

// Local aliases for the shared palette, so this file reads the same as the
// severity styling elsewhere in the TUI.
var (
	dangerStyle  = lipgloss.NewStyle().Bold(true).Foreground(colorDanger)
	warningStyle = lipgloss.NewStyle().Foreground(colorWarning)
	successStyle = lipgloss.NewStyle().Foreground(colorSuccess)
	mutedStyle   = lipgloss.NewStyle().Foreground(colorMuted)
)

// renderMerges draws the Merges tab: the PRs the operator authored or is
// assigned to, and what is blocking each merge.
//
// The daemon already sorts rows blocked by CI first, so the order is preserved
// rather than re-derived here.
func (d *Dashboard) renderMerges(height int) string {
	if len(d.merges) == 0 {
		return lipgloss.NewStyle().Foreground(colorMuted).Render(
			"  No pull requests tracked. Enable [merge_tracking] in the daemon config.")
	}

	var b strings.Builder
	start, end := d.viewportRange(len(d.merges), height)
	for i := start; i < end; i++ {
		b.WriteString(d.renderMergeRow(d.merges[i], i == d.cursor))
		b.WriteString("\n")
	}
	return b.String()
}

// viewportRange returns the slice of rows to draw for the current cursor.
func (d *Dashboard) viewportRange(total, height int) (int, int) {
	if height <= 0 || total == 0 {
		return 0, 0
	}
	// The list shrinks under the cursor when a PR merges and drops out between
	// refreshes. An out-of-range cursor would put start past total, and the tab
	// would render blank until the operator moved it.
	cursor := d.cursor
	if cursor >= total {
		cursor = total - 1
	}
	start := 0
	if cursor >= height {
		start = cursor - height + 1
	}
	end := start + height
	if end > total {
		end = total
	}
	return start, end
}

func (d *Dashboard) renderMergeRow(e api.MergeTrackingEntry, selected bool) string {
	cursor := "  "
	if selected {
		cursor = "> "
	}

	// The marker is what makes a CI problem visible while scanning: a row that
	// needs a human should not look like every other row.
	marker := "  "
	markerStyle := lipgloss.NewStyle().Foreground(colorMuted)
	switch {
	case e.ChecksRequiredFailing > 0:
		marker = "! "
		markerStyle = dangerStyle
	case e.ChecksRequiredPending > 0:
		marker = "~ "
		markerStyle = warningStyle
	}

	title := e.Title
	if title == "" {
		title = fmt.Sprintf("%s#%d", e.Repo, e.Number)
	}

	line := fmt.Sprintf("%s%s%-30s %-14s %s",
		cursor,
		markerStyle.Render(marker),
		truncateRunes(fmt.Sprintf("%s#%d", e.Repo, e.Number), 30),
		mergePhaseLabel(e.Phase),
		truncateRunes(title, maxInt(d.width-56, 20)),
	)
	if selected {
		return lipgloss.NewStyle().Bold(true).Render(line)
	}
	return line
}

func mergePhaseLabel(phase string) string {
	switch phase {
	case "auto_merge_armed":
		return "auto-merge on"
	case "merged":
		return "merged"
	case "blocked":
		return "blocked"
	case "updating":
		return "updating"
	case "update_pending":
		return "syncing"
	case "resolving":
		return "resolving"
	case "merging":
		return "merging"
	case "abandoned":
		return "not tracked"
	default:
		return "tracking"
	}
}

// buildMergeDetailLines renders the per-check breakdown for one PR, mirroring
// what the GUI shows so an operator gets the same explanation on either
// surface.
func buildMergeDetailLines(e api.MergeTrackingEntry, width int) []string {
	keyStyle := lipgloss.NewStyle().Foreground(colorMuted)
	lines := []string{
		lipgloss.NewStyle().Bold(true).Render(fmt.Sprintf("%s#%d  %s", e.Repo, e.Number, e.Title)),
		"",
		keyStyle.Render("State:   ") + mergePhaseLabel(e.Phase),
	}
	if e.HeadRef != "" {
		lines = append(lines, keyStyle.Render("Branch:  ")+e.HeadRef+" -> "+e.BaseRef)
	}
	if e.BlockReason != "" && !e.Terminal() {
		detail := e.BlockDetail
		if detail == "" {
			detail = e.BlockReason
		}
		lines = append(lines, keyStyle.Render("Blocked: ")+detail)
	}
	if e.PreRebaseSHA != "" {
		lines = append(lines,
			keyStyle.Render("Before:  ")+e.PreRebaseSHA+" (the commit this branch was at before Heimdallm rewrote it)")
	}
	if e.LastError != "" {
		lines = append(lines, keyStyle.Render("Error:   ")+dangerStyle.Render(e.LastError))
	}
	if e.URL != "" {
		lines = append(lines, keyStyle.Render("URL:     ")+e.URL)
	}

	if e.Decision == nil {
		return append(lines, "", "Heimdallm has not evaluated this PR yet.")
	}

	lines = append(lines, "", mergeChecksHeadlineTUI(e.Decision.ChecksSummary))
	if len(e.Decision.Checks) == 0 {
		return lines
	}
	lines = append(lines, "")
	for _, c := range e.Decision.Checks {
		required := " "
		if c.Required {
			required = "*"
		}
		label := c.Name
		if c.App != "" {
			label += " (" + c.App + ")"
		}
		lines = append(lines, fmt.Sprintf("  %s %s %s",
			styleCheckGlyph(c.State), required, truncateRunes(label, maxInt(width-12, 20))))
	}
	lines = append(lines, "", keyStyle.Render("  * required   ✓ passed  ✕ failed  … running  – skipped"))
	return lines
}

// mergeChecksHeadlineTUI is the same sentence the CLI and the GUI show.
func mergeChecksHeadlineTUI(s api.MergeChecksSummary) string {
	switch {
	case s.Truncated:
		return dangerStyle.Render("This PR has more checks than Heimdallm can read in one pass, so its merge state cannot be confirmed.")
	case len(s.MissingRequired) > 0:
		return warningStyle.Render("Waiting for required checks that have not run: " +
			strings.Join(s.MissingRequired, ", "))
	case s.RequiredFailing > 0:
		return dangerStyle.Render(fmt.Sprintf("This PR cannot be merged: %d of the %d required checks %s failing.",
			s.RequiredFailing, s.RequiredTotal, pluralTUI(s.RequiredFailing, "is", "are")))
	case s.RequiredPending > 0:
		return warningStyle.Render(fmt.Sprintf("Waiting on %d required %s. The PR merges on its own once they pass.",
			s.RequiredPending, pluralTUI(s.RequiredPending, "check", "checks")))
	case s.OptionalFailing > 0 && s.RequiredTotal > 0:
		return successStyle.Render(fmt.Sprintf("All %d required checks passed. %d optional %s failing, which does not block the merge.",
			s.RequiredTotal, s.OptionalFailing, pluralTUI(s.OptionalFailing, "check is", "checks are")))
	case s.Total == 0:
		return mutedStyle.Render("This PR has no checks configured.")
	default:
		return successStyle.Render(fmt.Sprintf("All %d checks passed.", s.Total))
	}
}

func styleCheckGlyph(state string) string {
	switch state {
	case "success":
		return successStyle.Render("✓")
	case "failure":
		return dangerStyle.Render("✕")
	case "pending":
		return warningStyle.Render("…")
	default:
		return mutedStyle.Render("–")
	}
}

func pluralTUI(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

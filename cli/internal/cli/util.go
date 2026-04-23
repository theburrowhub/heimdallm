package cli

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
)

// truncate returns s truncated to at most maxLen runes, appending "..." if
// truncated. This avoids corrupting multi-byte UTF-8 characters that
// byte-indexed slicing would cause.
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-1]) + "…"
}

var (
	sevHigh   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#EF4444"))
	sevMedium = lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B"))
	sevLow    = lipgloss.NewStyle().Foreground(lipgloss.Color("#10B981"))
	sevInfo   = lipgloss.NewStyle().Foreground(lipgloss.Color("#06B6D4"))
	sevMuted  = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
)

func colorSeverity(sev string, width int) string {
	padded := fmt.Sprintf("%-*s", width, sev)
	switch sev {
	case "high":
		return sevHigh.Render(padded)
	case "medium":
		return sevMedium.Render(padded)
	case "low":
		return sevLow.Render(padded)
	case "info":
		return sevInfo.Render(padded)
	default:
		return sevMuted.Render(padded)
	}
}

package tui

import "github.com/charmbracelet/lipgloss"

var (
	colorPrimary   = lipgloss.Color("#7C3AED")
	colorSecondary = lipgloss.Color("#06B6D4")
	colorSuccess   = lipgloss.Color("#10B981")
	colorWarning   = lipgloss.Color("#F59E0B")
	colorDanger    = lipgloss.Color("#EF4444")
	colorMuted     = lipgloss.Color("#6B7280")
	colorBorder    = lipgloss.Color("#4B5563")

	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorPrimary).
			Padding(0, 1)

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorSecondary)

	activeTabStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(colorPrimary).
			Padding(0, 2)

	inactiveTabStyle = lipgloss.NewStyle().
				Foreground(colorMuted).
				Padding(0, 2)

	boxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorBorder).
			Padding(0, 1)

	selectedRowStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#FFFFFF")).
				Background(lipgloss.Color("#374151"))

	normalRowStyle = lipgloss.NewStyle()

	severityStyle = func(sev string) lipgloss.Style {
		switch sev {
		case "high":
			return lipgloss.NewStyle().Bold(true).Foreground(colorDanger)
		case "medium":
			return lipgloss.NewStyle().Foreground(colorWarning)
		case "low":
			return lipgloss.NewStyle().Foreground(colorSuccess)
		case "info":
			return lipgloss.NewStyle().Foreground(colorSecondary)
		default:
			return lipgloss.NewStyle().Foreground(colorMuted)
		}
	}

	helpStyle = lipgloss.NewStyle().
			Foreground(colorMuted).
			Padding(0, 1)

	statusOnline = lipgloss.NewStyle().
			Foreground(colorSuccess).
			Bold(true)

	statusError = lipgloss.NewStyle().
			Foreground(colorDanger).
			Bold(true)
)

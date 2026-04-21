package cli

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/heimdallm/cli/internal/tui"
	"github.com/spf13/cobra"
)

func newDashboardCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "dashboard",
		Short: "Launch interactive TUI dashboard",
		RunE: func(cmd *cobra.Command, args []string) error {
			m := tui.NewDashboard(client.Host, client.Token)
			p := tea.NewProgram(m, tea.WithAltScreen())
			if _, err := p.Run(); err != nil {
				return fmt.Errorf("dashboard error: %w", err)
			}
			return nil
		},
	}
}

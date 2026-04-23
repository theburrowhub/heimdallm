package cli

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/theburrowhub/heimdallm/cli/internal/tui"
	"github.com/spf13/cobra"
)

func newDashboardCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "dashboard",
		Short: "Launch interactive TUI dashboard",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := clientFromContext(cmd.Context())
			m := tui.NewDashboard(c.Host, c.Token, cmd.Root().Version)
			p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
			if _, err := p.Run(); err != nil {
				return fmt.Errorf("dashboard error: %w", err)
			}
			return nil
		},
	}
}

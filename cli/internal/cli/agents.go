package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/theburrowhub/heimdallm/cli/internal/api"
)

func newAgentsCmd() *cobra.Command {
	var rescan bool
	cmd := &cobra.Command{
		Use:   "agents",
		Short: "List the AI agents the daemon can review with and which are installed",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := clientFromContext(cmd.Context())
			var (
				cat *api.CLIAgentCatalog
				err error
			)
			if rescan {
				cat, err = c.RescanCLIAgents()
			} else {
				cat, err = c.ListCLIAgents()
			}
			if err != nil {
				return fmt.Errorf("fetching agents: %w", err)
			}
			printAgents(os.Stdout, cat)
			return nil
		},
	}
	cmd.Flags().BoolVar(&rescan, "rescan", false, "scan the daemon's machine again before listing (needs a token)")
	return cmd
}

// printAgents renders the catalog as a table. Every daemon-supplied string
// goes through DisplayText.
func printAgents(w io.Writer, cat *api.CLIAgentCatalog) {
	fmt.Fprintln(w, "AI Agents")
	fmt.Fprintln(w, "═════════")
	for _, a := range cat.Agents {
		state := "not installed"
		if a.Installed {
			state = "installed"
			if a.Version != "" {
				state += " " + api.DisplayText(a.Version, 40)
			}
		}
		notes := ""
		if a.Configured {
			notes = "configured"
		}
		if a.Kind == "ide" {
			if notes != "" {
				notes += ", "
			}
			notes += "reviews via " + api.DisplayText(a.ConfigAgent, 40)
		}
		if len(a.Models) > 0 {
			if notes != "" {
				notes += ", "
			}
			notes += fmt.Sprintf("%d models", len(a.Models))
		}
		fmt.Fprintf(w, "  %-22s %-12s %-26s %s\n",
			api.DisplayText(a.Name, 22), api.DisplayText(a.ID, 12), state, notes)
		if !a.Installed && a.InstallHint != "" {
			fmt.Fprintf(w, "  %-22s %-12s install: %s\n", "", "", api.DisplayText(a.InstallHint, 120))
		}
	}
	if !cat.ScannedAt.IsZero() {
		fmt.Fprintf(w, "\n  Scanned %s\n", cat.ScannedAt.Local().Format("2006-01-02 15:04:05"))
	}
}

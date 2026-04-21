package cli

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show daemon state, uptime, and monitored repos",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := clientFromContext(cmd.Context())
			if err := c.Health(); err != nil {
				return fmt.Errorf("daemon unreachable: %w", err)
			}

			cfg, err := c.GetConfig()
			if err != nil {
				return fmt.Errorf("fetching config: %w", err)
			}

			stats, err := c.GetStats()
			if err != nil {
				return fmt.Errorf("fetching stats: %w", err)
			}

			fmt.Println("Heimdallm Daemon Status")
			fmt.Println("═══════════════════════")
			fmt.Printf("  Status:       online\n")

			if repos, ok := cfg["repositories"]; ok {
				if arr, ok := repos.([]any); ok {
					fmt.Printf("  Repositories: %d monitored\n", len(arr))
					for _, r := range arr {
						fmt.Printf("                • %v\n", r)
					}
				}
			}

			if interval, ok := cfg["poll_interval"]; ok {
				fmt.Printf("  Poll interval: %v\n", interval)
			}
			if primary, ok := cfg["ai_primary"]; ok {
				fmt.Printf("  AI primary:    %v\n", primary)
			}
			if mode, ok := cfg["review_mode"]; ok {
				fmt.Printf("  Review mode:   %v\n", mode)
			}

			fmt.Printf("\n  Total reviews: %d\n", stats.TotalReviews)
			fmt.Printf("  Activity (24h): %d events\n", stats.ActivityCount24h)

			if len(stats.BySeverity) > 0 {
				sevKeys := make([]string, 0, len(stats.BySeverity))
				for sev := range stats.BySeverity {
					sevKeys = append(sevKeys, sev)
				}
				sort.Strings(sevKeys)
				fmt.Printf("  By severity:   ")
				first := true
				for _, sev := range sevKeys {
					if !first {
						fmt.Printf(", ")
					}
					fmt.Printf("%s=%d", sev, stats.BySeverity[sev])
					first = false
				}
				fmt.Println()
			}

			return nil
		},
	}
}

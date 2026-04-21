package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show daemon state, uptime, and monitored repos",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := client.Health(); err != nil {
				return fmt.Errorf("daemon unreachable: %w", err)
			}

			cfg, err := client.GetConfig()
			if err != nil {
				return fmt.Errorf("fetching config: %w", err)
			}

			stats, err := client.GetStats()
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
				fmt.Printf("  By severity:   ")
				first := true
				for sev, count := range stats.BySeverity {
					if !first {
						fmt.Printf(", ")
					}
					fmt.Printf("%s=%d", sev, count)
					first = false
				}
				fmt.Println()
			}

			return nil
		},
	}
}

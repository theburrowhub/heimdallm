package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func newPRsCmd() *cobra.Command {
	var severity string

	cmd := &cobra.Command{
		Use:   "prs",
		Short: "List reviewed PRs",
		RunE: func(cmd *cobra.Command, args []string) error {
			prs, err := client.ListPRs()
			if err != nil {
				return fmt.Errorf("fetching PRs: %w", err)
			}

			if len(prs) == 0 {
				fmt.Println("No PRs found.")
				return nil
			}

			filtered := prs
			if severity != "" {
				filtered = nil
				for _, pr := range prs {
					if pr.LatestReview != nil && strings.EqualFold(pr.LatestReview.Severity, severity) {
						filtered = append(filtered, pr)
					}
				}
			}

			if len(filtered) == 0 {
				fmt.Printf("No PRs matching severity %q.\n", severity)
				return nil
			}

			fmt.Printf("%-6s %-30s %-40s %-8s %-8s\n", "ID", "REPO", "TITLE", "SEVERITY", "STATE")
			fmt.Println(strings.Repeat("─", 96))

			for _, pr := range filtered {
				sev := "---"
				if pr.LatestReview != nil {
					sev = pr.LatestReview.Severity
				}
				title := pr.Title
				if len(title) > 38 {
					title = title[:35] + "..."
				}
				repo := pr.Repo
				if len(repo) > 28 {
					repo = repo[:25] + "..."
				}
				fmt.Printf("%-6d %-30s %-40s %-8s %-8s\n", pr.ID, repo, title, sev, pr.State)
			}

			fmt.Printf("\n%d PRs listed.\n", len(filtered))
			return nil
		},
	}

	cmd.Flags().StringVar(&severity, "severity", "", "filter by severity (info, low, medium, high)")

	return cmd
}

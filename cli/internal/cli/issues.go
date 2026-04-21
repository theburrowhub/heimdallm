package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func newIssuesCmd() *cobra.Command {
	var severity string

	cmd := &cobra.Command{
		Use:   "issues",
		Short: "List triaged issues",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := clientFromContext(cmd.Context())
			issues, err := c.ListIssues()
			if err != nil {
				return fmt.Errorf("fetching issues: %w", err)
			}

			if len(issues) == 0 {
				fmt.Println("No issues found.")
				return nil
			}

			filtered := issues
			if severity != "" {
				filtered = nil
				for _, iss := range issues {
					if iss.LatestReview != nil {
						sev := extractTriageSeverity(iss.LatestReview.Triage)
						if strings.EqualFold(sev, severity) {
							filtered = append(filtered, iss)
						}
					}
				}
			}

			if len(filtered) == 0 {
				fmt.Printf("No issues matching severity %q.\n", severity)
				return nil
			}

			fmt.Printf("%-6s %-30s %-40s %-8s %-12s\n", "ID", "REPO", "TITLE", "SEVERITY", "ACTION")
			fmt.Println(strings.Repeat("─", 100))

			for _, iss := range filtered {
				sev := "---"
				action := "---"
				if iss.LatestReview != nil {
					sev = extractTriageSeverity(iss.LatestReview.Triage)
					action = iss.LatestReview.ActionTaken
				}
				title := truncate(iss.Title, 38)
				repo := truncate(iss.Repo, 28)
				fmt.Printf("%-6d %-30s %-40s %-8s %-12s\n", iss.ID, repo, title, sev, action)
			}

			fmt.Printf("\n%d issues listed.\n", len(filtered))
			return nil
		},
	}

	cmd.Flags().StringVar(&severity, "severity", "", "filter by severity (info, low, medium, high)")

	return cmd
}

func extractTriageSeverity(triage json.RawMessage) string {
	if len(triage) == 0 {
		return "---"
	}
	var t map[string]any
	if err := json.Unmarshal(triage, &t); err != nil {
		return "---"
	}
	if sev, ok := t["severity"]; ok {
		return fmt.Sprintf("%v", sev)
	}
	return "---"
}

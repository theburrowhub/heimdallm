package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/theburrowhub/heimdallm/cli/internal/api"
)

func newIssuesCmd() *cobra.Command {
	var severity string
	var repo string
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "issues",
		Short: "List triaged issues",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := clientFromContext(cmd.Context())
			issues, err := c.ListIssues()
			if err != nil {
				return fmt.Errorf("fetching issues: %w", err)
			}

			var reviewed []api.Issue
			for _, iss := range issues {
				if iss.LatestReview != nil {
					reviewed = append(reviewed, iss)
				}
			}

			if len(reviewed) == 0 {
				fmt.Println("No issues found.")
				return nil
			}

			filtered := reviewed
			if severity != "" {
				filtered = nil
				for _, iss := range reviewed {
					sev := extractTriageSeverity(iss.LatestReview.Triage)
					if strings.EqualFold(sev, severity) {
						filtered = append(filtered, iss)
					}
				}
			}

			if repo != "" {
				n := 0
				for _, iss := range filtered {
					if strings.EqualFold(iss.Repo, repo) {
						filtered[n] = iss
						n++
					}
				}
				filtered = filtered[:n]
			}

			if len(filtered) == 0 {
				fmt.Println("No issues matching the given filters.")
				return nil
			}

			sort.Slice(filtered, func(i, j int) bool {
				ri, rj := filtered[i].LatestReview, filtered[j].LatestReview
				if ri == nil {
					return false
				}
				if rj == nil {
					return true
				}
				return ri.CreatedAt.After(rj.CreatedAt)
			})

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(filtered)
			}

			fmt.Printf("%-6s %-28s %-34s %-14s %-8s %-12s %-10s\n",
				"ID", "REPO", "TITLE", "AUTHOR", "SEVERITY", "ACTION", "DATE")
			fmt.Println(strings.Repeat("─", 118))

			for _, iss := range filtered {
				sev := "---"
				action := "---"
				date := "---"
				if iss.LatestReview != nil {
					sev = extractTriageSeverity(iss.LatestReview.Triage)
					action = iss.LatestReview.ActionTaken
					date = iss.LatestReview.CreatedAt.Format("2006-01-02")
				}
				title := truncate(iss.Title, 32)
				repoStr := truncate(iss.Repo, 26)
				author := truncate(iss.Author, 12)
				sevCol := colorSeverity(sev, 8)
				fmt.Printf("%-6d %-28s %-34s %-14s %s %-12s %-10s\n",
					iss.ID, repoStr, title, author, sevCol, action, date)
			}

			fmt.Printf("\n%d issues listed.\n", len(filtered))
			return nil
		},
	}

	cmd.Flags().StringVar(&severity, "severity", "", "filter by severity (info, low, medium, high)")
	cmd.Flags().StringVar(&repo, "repo", "", "filter by repository (org/repo)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")

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

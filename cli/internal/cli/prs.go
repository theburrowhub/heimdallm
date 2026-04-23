package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

func newPRsCmd() *cobra.Command {
	var severity string
	var repo string
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "prs",
		Short: "List reviewed PRs",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := clientFromContext(cmd.Context())
			prs, err := c.ListPRs()
			if err != nil {
				return fmt.Errorf("fetching PRs: %w", err)
			}

			n := 0
			for _, pr := range prs {
				if pr.LatestReview != nil {
					prs[n] = pr
					n++
				}
			}
			prs = prs[:n]

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

			if repo != "" {
				n := 0
				for _, pr := range filtered {
					if strings.EqualFold(pr.Repo, repo) {
						filtered[n] = pr
						n++
					}
				}
				filtered = filtered[:n]
			}

			if len(filtered) == 0 {
				fmt.Println("No PRs matching the given filters.")
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

			fmt.Printf("%-6s %-28s %-34s %-14s %-8s %-8s %-10s\n",
				"ID", "REPO", "TITLE", "AUTHOR", "SEVERITY", "STATE", "DATE")
			fmt.Println(strings.Repeat("─", 114))

			for _, pr := range filtered {
				sev := "---"
				date := "---"
				if pr.LatestReview != nil {
					sev = pr.LatestReview.Severity
					date = pr.LatestReview.CreatedAt.Format("2006-01-02")
				}
				title := truncate(pr.Title, 32)
				repoStr := truncate(pr.Repo, 26)
				author := truncate(pr.Author, 12)
				sevCol := colorSeverity(sev, 8)
				fmt.Printf("%-6d %-28s %-34s %-14s %s %-8s %-10s\n",
					pr.ID, repoStr, title, author, sevCol, pr.State, date)
			}

			fmt.Printf("\n%d PRs listed.\n", len(filtered))
			return nil
		},
	}

	cmd.Flags().StringVar(&severity, "severity", "", "filter by severity (info, low, medium, high)")
	cmd.Flags().StringVar(&repo, "repo", "", "filter by repository (org/repo)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")

	return cmd
}

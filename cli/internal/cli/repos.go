package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func newReposCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "repos",
		Short: "List monitored repos with status",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := clientFromContext(cmd.Context())

			cfg, err := c.GetConfig()
			if err != nil {
				return fmt.Errorf("fetching config: %w", err)
			}

			repos, _ := cfg["repositories"].([]any)
			if len(repos) == 0 {
				fmt.Println("No monitored repositories.")
				return nil
			}

			localDirsDetected, _ := cfg["local_dirs_detected"].(map[string]any)
			repoOverrides, _ := cfg["repo_overrides"].(map[string]any)

			prs, err := c.ListPRs()
			if err != nil {
				return fmt.Errorf("fetching PRs: %w", err)
			}
			issues, err := c.ListIssues()
			if err != nil {
				return fmt.Errorf("fetching issues: %w", err)
			}

			prCount := make(map[string]int)
			for _, pr := range prs {
				if pr.State == "open" {
					prCount[pr.Repo]++
				}
			}
			issueCount := make(map[string]int)
			for _, iss := range issues {
				if iss.State == "open" {
					issueCount[iss.Repo]++
				}
			}

			fmt.Printf("%-35s %-10s %-8s %-10s\n", "REPO", "LOCAL_DIR", "ISSUES", "PRS")
			fmt.Println(strings.Repeat("─", 67))

			for _, r := range repos {
				repo := fmt.Sprintf("%v", r)
				localDir := "no"
				if override, ok := repoOverrides[repo].(map[string]any); ok {
					if ld, ok := override["local_dir"].(string); ok && ld != "" {
						localDir = "yes"
					}
				}
				if localDir == "no" {
					if ld, ok := localDirsDetected[repo].(string); ok && ld != "" {
						localDir = "auto"
					}
				}
				fmt.Printf("%-35s %-10s %-8d %-10d\n",
					truncate(repo, 33), localDir, issueCount[repo], prCount[repo])
			}

			fmt.Printf("\n%d repositories monitored.\n", len(repos))
			return nil
		},
	}
}

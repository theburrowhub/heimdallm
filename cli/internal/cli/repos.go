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

			prCount := make(map[string]int)
			for _, pr := range prs {
				if pr.State == "open" {
					prCount[pr.Repo]++
				}
			}

			fmt.Printf("%-35s %-10s %-10s\n", "REPO", "LOCAL_DIR", "PRS")
			fmt.Println(strings.Repeat("─", 58))

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
				fmt.Printf("%-35s %-10s %-10d\n",
					truncate(repo, 33), localDir, prCount[repo])
			}

			fmt.Printf("\n%d repositories monitored.\n", len(repos))
			return nil
		},
	}
}

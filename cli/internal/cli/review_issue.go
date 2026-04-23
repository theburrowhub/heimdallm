package cli

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
)

func newReviewIssueCmd() *cobra.Command {
	var repo string

	cmd := &cobra.Command{
		Use:   "review-issue <number>",
		Short: "Trigger a manual triage for an issue by number",
		Long: "Queue a triage for the issue identified by its GitHub number.\n" +
			"Use --repo to specify the repository (required when the daemon monitors multiple repos).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := clientFromContext(cmd.Context())
			number, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid issue number: %w", err)
			}

			issues, err := c.ListIssues()
			if err != nil {
				return fmt.Errorf("fetching issues: %w", err)
			}

			var matched []int64
			for _, iss := range issues {
				if iss.Number == number && (repo == "" || iss.Repo == repo) {
					matched = append(matched, iss.ID)
				}
			}

			switch len(matched) {
			case 0:
				if repo != "" {
					return fmt.Errorf("no issue #%d found in %s", number, repo)
				}
				return fmt.Errorf("no issue #%d found (try --repo org/repo)", number)
			case 1:
				if err := c.TriggerIssueReview(matched[0]); err != nil {
					return fmt.Errorf("triggering issue review: %w", err)
				}
				fmt.Printf("Review queued for issue #%d.\n", number)
				return nil
			default:
				return fmt.Errorf("issue #%d exists in multiple repos — use --repo to disambiguate", number)
			}
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "", "repository slug (org/repo)")

	return cmd
}

package cli

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
)

func newReviewPRCmd() *cobra.Command {
	var repo string

	cmd := &cobra.Command{
		Use:   "review-pr <number>",
		Short: "Trigger a manual review for a PR by number",
		Long: "Queue a review for the PR identified by its GitHub number.\n" +
			"Use --repo to specify the repository (required when the daemon monitors multiple repos).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := clientFromContext(cmd.Context())
			number, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid PR number: %w", err)
			}

			prs, err := c.ListPRs()
			if err != nil {
				return fmt.Errorf("fetching PRs: %w", err)
			}

			var matched []int64
			for _, pr := range prs {
				if pr.Number == number && (repo == "" || pr.Repo == repo) {
					matched = append(matched, pr.ID)
				}
			}

			switch len(matched) {
			case 0:
				if repo != "" {
					return fmt.Errorf("no PR #%d found in %s", number, repo)
				}
				return fmt.Errorf("no PR #%d found (try --repo org/repo)", number)
			case 1:
				if err := c.TriggerPRReview(matched[0]); err != nil {
					return fmt.Errorf("triggering PR review: %w", err)
				}
				fmt.Printf("Review queued for PR #%d.\n", number)
				return nil
			default:
				return fmt.Errorf("PR #%d exists in multiple repos — use --repo to disambiguate", number)
			}
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "", "repository slug (org/repo)")

	return cmd
}

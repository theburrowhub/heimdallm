package cli

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
)

func newReviewPRCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "review-pr <id>",
		Short: "Trigger a manual review for a PR",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := clientFromContext(cmd.Context())
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid PR ID: %w", err)
			}
			if err := c.TriggerPRReview(id); err != nil {
				return fmt.Errorf("triggering PR review: %w", err)
			}
			fmt.Printf("Review queued for PR %d.\n", id)
			return nil
		},
	}
}

func newReviewIssueCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "review-issue <id>",
		Short: "Trigger a manual review for an issue",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := clientFromContext(cmd.Context())
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid issue ID: %w", err)
			}
			if err := c.TriggerIssueReview(id); err != nil {
				return fmt.Errorf("triggering issue review: %w", err)
			}
			fmt.Printf("Review queued for issue %d.\n", id)
			return nil
		},
	}
}

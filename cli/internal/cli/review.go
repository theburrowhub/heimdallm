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

func newDismissIssueCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "dismiss-issue <id>",
		Short: "Dismiss an issue, stopping the daemon from retrying it",
		Long: "Marks an issue as dismissed so the pipeline skips it on future polls.\n" +
			"Use undismiss-issue to restore it.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := clientFromContext(cmd.Context())
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid issue ID: %w", err)
			}
			if err := c.DismissIssue(id); err != nil {
				return fmt.Errorf("dismissing issue: %w", err)
			}
			fmt.Printf("Issue %d dismissed.\n", id)
			return nil
		},
	}
}

func newUndismissIssueCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "undismiss-issue <id>",
		Short: "Restore a dismissed issue so the pipeline can retry it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := clientFromContext(cmd.Context())
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid issue ID: %w", err)
			}
			if err := c.UndismissIssue(id); err != nil {
				return fmt.Errorf("undismissing issue: %w", err)
			}
			fmt.Printf("Issue %d undismissed.\n", id)
			return nil
		},
	}
}

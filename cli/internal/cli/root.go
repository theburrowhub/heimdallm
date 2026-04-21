package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/theburrowhub/heimdallm/cli/internal/api"
	"github.com/spf13/cobra"
)

// contextKey is an unexported type for context keys in this package.
type contextKey struct{}

// clientKey is the context key for the API client.
var clientKey = contextKey{}

// clientFromContext retrieves the *api.Client stored in the context.
func clientFromContext(ctx context.Context) *api.Client {
	return ctx.Value(clientKey).(*api.Client)
}

func NewRootCmd() *cobra.Command {
	var (
		flagHost  string
		flagToken string
	)

	root := &cobra.Command{
		Use:   "heimdallm-cli",
		Short: "CLI client for the Heimdallm daemon",
		Long:  "Monitor and interact with the Heimdallm daemon from the terminal.",
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			if flagHost == "" {
				flagHost = os.Getenv("HEIMDALLM_HOST")
			}
			if flagToken == "" {
				flagToken = os.Getenv("HEIMDALLM_TOKEN")
			}
			c := api.New(flagHost, flagToken)
			cmd.SetContext(context.WithValue(cmd.Context(), clientKey, c))
		},
		SilenceUsage: true,
	}

	root.PersistentFlags().StringVar(&flagHost, "host", "", fmt.Sprintf("daemon URL (env: HEIMDALLM_HOST, default: %s)", api.DefaultHost))
	root.PersistentFlags().StringVar(&flagToken, "token", "", "API token for mutating commands (env: HEIMDALLM_TOKEN; note: flag value may be visible in process listings)")

	root.AddCommand(
		newStatusCmd(),
		newPRsCmd(),
		newIssuesCmd(),
		newFollowCmd(),
		newReviewPRCmd(),
		newReviewIssueCmd(),
		newConfigCmd(),
		newStatsCmd(),
		newDashboardCmd(),
	)

	return root
}

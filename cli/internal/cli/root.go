package cli

import (
	"fmt"
	"os"

	"github.com/heimdallm/cli/internal/api"
	"github.com/spf13/cobra"
)

var (
	flagHost  string
	flagToken string
	client    *api.Client
)

func NewRootCmd() *cobra.Command {
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
			client = api.New(flagHost, flagToken)
		},
		SilenceUsage: true,
	}

	root.PersistentFlags().StringVar(&flagHost, "host", "", fmt.Sprintf("daemon URL (env: HEIMDALLM_HOST, default: %s)", api.DefaultHost))
	root.PersistentFlags().StringVar(&flagToken, "token", "", "API token for mutating commands (env: HEIMDALLM_TOKEN)")

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

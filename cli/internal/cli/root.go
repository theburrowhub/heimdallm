package cli

import (
	"context"
	"fmt"
	"net/url"
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
			// Resolution priority:
			// 1. --token / --host flags (already in flagToken/flagHost)
			// 2. Environment variables
			if flagHost == "" {
				flagHost = os.Getenv("HEIMDALLM_HOST")
			}
			if flagToken == "" {
				flagToken = os.Getenv("HEIMDALLM_TOKEN")
			}

			// 3. Config file (~/.config/heimdallm/cli.toml)
			if flagHost == "" || flagToken == "" {
				if cfg, err := loadCLIConfig(); err == nil {
					if flagHost == "" && cfg.Host != "" {
						flagHost = cfg.Host
					}
					if flagToken == "" && cfg.Token != "" {
						flagToken = cfg.Token
					}
				}
			}

			// 4. Auto-discover from Docker (if localhost and no token yet).
			//    Skipped for the configure command to avoid unnecessary latency.
			if flagToken == "" && cmd.Name() != "configure" {
				host := flagHost
				if host == "" {
					host = api.DefaultHost
				}
				if isLocalhost(host) {
					if token, err := discoverDockerToken(); err == nil {
						flagToken = token
					}
				}
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
		newConfigureCmd(),
	)

	return root
}

func isLocalhost(host string) bool {
	u, err := url.Parse(host)
	if err != nil {
		return false
	}
	h := u.Hostname()
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

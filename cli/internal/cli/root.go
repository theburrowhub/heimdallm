package cli

import (
	"context"
	"fmt"
	"net/url"
	"os"

	"github.com/spf13/cobra"
	"github.com/theburrowhub/heimdallm/cli/internal/api"
)

// contextKey is an unexported type for context keys in this package.
type contextKey struct{}

// clientKey is the context key for the API client.
var clientKey = contextKey{}

// instanceKey is the context key for the resolved instance id ("" when the CLI
// is talking to a single unnamed daemon).
type instanceKeyType struct{}

var instanceKey = instanceKeyType{}

// clientFromContext retrieves the *api.Client stored in the context.
func clientFromContext(ctx context.Context) *api.Client {
	return ctx.Value(clientKey).(*api.Client)
}

// instanceFromContext returns the instance the command is scoped to, or "" for
// a plain single-daemon setup.
func instanceFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(instanceKey).(string); ok {
		return id
	}
	return ""
}

func NewRootCmd(version string) *cobra.Command {
	var (
		flagHost     string
		flagToken    string
		flagInstance string
	)

	root := &cobra.Command{
		Use:     "heimdallm-cli",
		Version: version,
		Short:   "CLI client for the Heimdallm daemon",
		Long:    "Monitor and interact with the Heimdallm daemon from the terminal.",
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
			//
			// --instance selects one of the [instances.*] entries; without it
			// the resolver falls back to default_instance, then to the sole
			// instance, then to the legacy flat host/token pair. That keeps
			// every existing single-daemon config working untouched.
			resolvedInstance := flagInstance
			if flagHost == "" || flagToken == "" {
				if cfg, err := loadCLIConfig(); err == nil {
					host, token, resolveErr := cfg.resolve(flagInstance)
					if resolveErr != nil {
						fmt.Fprintln(os.Stderr, "heimdallm-cli:", resolveErr)
						os.Exit(1)
					}
					if flagHost == "" && host != "" {
						flagHost = host
					}
					if flagToken == "" && token != "" {
						flagToken = token
					}
					if resolvedInstance == "" {
						resolvedInstance = cfg.DefaultInstance
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
			ctx := context.WithValue(cmd.Context(), clientKey, c)
			ctx = context.WithValue(ctx, instanceKey, resolvedInstance)
			cmd.SetContext(ctx)
		},
		SilenceUsage: true,
	}

	root.PersistentFlags().StringVar(&flagHost, "host", "", fmt.Sprintf("daemon URL (env: HEIMDALLM_HOST, default: %s)", api.DefaultHost))
	root.PersistentFlags().StringVar(&flagToken, "token", "", "API token for mutating commands (env: HEIMDALLM_TOKEN; note: flag value may be visible in process listings)")
	root.PersistentFlags().StringVarP(&flagInstance, "instance", "I", "", "instance to talk to, from [instances.*] in cli.toml")

	root.AddCommand(
		newStatusCmd(),
		newReposCmd(),
		newPRsCmd(),
		newPRDetailCmd(),
		newMergesCmd(),
		newMergeDetailCmd(),
		newIssuesCmd(),
		newIssueDetailCmd(),
		newFollowCmd(),
		newReviewPRCmd(),
		newReviewIssueCmd(),
		newPromoteIssueCmd(),
		newDismissIssueCmd(),
		newUndismissIssueCmd(),
		newConfigCmd(),
		newStatsCmd(),
		newDashboardCmd(),
		newConfigureCmd(),
		newInstancesCmd(),
		newRoutingCmd(),
		newPropagateConfigCmd(),
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

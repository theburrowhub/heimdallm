package cli

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/theburrowhub/heimdallm/cli/internal/api"
)

// notAHubMessage explains the empty case without making it look like a failure:
// a plain single-daemon install legitimately has no control plane.
const notAHubMessage = "This daemon is not a cluster hub, so it manages no instances.\n" +
	"Set role = \"hub\" under [cluster] in its config.toml to enable one."

func newInstancesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "instances",
		Short: "List the Heimdallm instances this hub manages",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := clientFromContext(cmd.Context())
			registry, err := c.ListInstances()
			if err != nil {
				if errors.Is(err, api.ErrNotAHub) {
					fmt.Println(notAHubMessage)
					return nil
				}
				return fmt.Errorf("fetching instances: %w", err)
			}
			printInstances(registry)
			return nil
		},
	}
	cmd.AddCommand(newInstancesUseCmd())
	return cmd
}

func printInstances(registry *api.ClusterRegistry) {
	if len(registry.Instances) == 0 {
		fmt.Println("No instances registered.")
		return
	}
	fmt.Printf("Hub: %s (%s)\n\n",
		api.DisplayText(registry.SelfName, 60), api.DisplayText(registry.SelfID, 60))

	for _, inst := range registry.Instances {
		status := "not probed"
		switch {
		case inst.TokenError != "":
			status = "token error"
		case !inst.Enabled:
			status = "disabled"
		case inst.State != nil && inst.State.Reachable:
			status = "reachable"
		case inst.State != nil:
			status = "UNREACHABLE"
		}

		markers := make([]string, 0, 3)
		if inst.Self {
			markers = append(markers, "hub")
		}
		if inst.IsFallback {
			markers = append(markers, "default")
		}
		if inst.InPool {
			markers = append(markers, "pool")
		}
		suffix := ""
		if len(markers) > 0 {
			suffix = "  [" + strings.Join(markers, ",") + "]"
		}

		fmt.Printf("%-16s %-11s %s%s\n",
			api.DisplayText(inst.ID, 30), status,
			api.DisplayText(inst.DisplayName(), 40), suffix)
		fmt.Printf("  %s\n", api.DisplayText(inst.BaseURL, 120))

		details := make([]string, 0, 3)
		if inst.State != nil && inst.State.Version != "" {
			details = append(details, "version "+api.DisplayText(inst.State.Version, 30))
		}
		if inst.State != nil && inst.State.UptimeSeconds > 0 {
			details = append(details, "up "+formatUptime(inst.State.UptimeSeconds))
		}
		details = append(details, fmt.Sprintf("%d repos routed", inst.AssignedRepos))
		if len(inst.Labels) > 0 {
			details = append(details, strings.Join(inst.Labels, ","))
		}
		fmt.Printf("  %s\n", strings.Join(details, " · "))

		if inst.TokenError != "" {
			fmt.Printf("  token: %s\n", api.DisplayText(inst.TokenError, 200))
		}
		if inst.State != nil && !inst.State.Reachable && inst.State.LastError != "" {
			fmt.Printf("  error: %s", api.DisplayText(inst.State.LastError, 200))
			if inst.State.ConsecutiveFailures > 1 {
				fmt.Printf(" (%d failed probes)", inst.State.ConsecutiveFailures)
			}
			fmt.Println()
		}
		fmt.Println()
	}
}

func formatUptime(seconds float64) string {
	d := time.Duration(seconds) * time.Second
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
	case d >= time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}

// newInstancesUseCmd records which configured instance subsequent commands
// target, so an operator working on one machine does not have to repeat
// --instance on every invocation.
func newInstancesUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use <instance-id>",
		Short: "Set the default instance for future commands",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			cfg, err := loadCLIConfig()
			if err != nil {
				return fmt.Errorf("reading %s: %w", configPath(), err)
			}
			if _, ok := cfg.resolvedInstances()[id]; !ok {
				return fmt.Errorf("unknown instance %q; configured: %s",
					id, strings.Join(cfg.instanceIDs(), ", "))
			}
			cfg.DefaultInstance = id
			if err := saveCLIConfig(cfg); err != nil {
				return err
			}
			fmt.Printf("Default instance set to %s\n", id)
			return nil
		},
	}
}

func newRoutingCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "routing",
		Short: "Show which instance owns which organizations and repositories",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := clientFromContext(cmd.Context())
			rules, err := c.GetRouting()
			if err != nil {
				if errors.Is(err, api.ErrNotAHub) {
					fmt.Println(notAHubMessage)
					return nil
				}
				return fmt.Errorf("fetching routing: %w", err)
			}
			printRouting(rules)
			return nil
		},
	}
	cmd.AddCommand(newRoutingSetCmd("set-repo", "repository", "repos"))
	cmd.AddCommand(newRoutingSetCmd("set-org", "organization", "orgs"))
	return cmd
}

func printRouting(rules *api.RoutingRules) {
	if !rules.Enabled {
		fmt.Println("Routing is not configured: every repository is handled by this daemon.")
		return
	}
	fmt.Printf("Mode:    %s\n", api.DisplayText(rules.Mode, 30))
	fmt.Printf("Default: %s\n", api.DisplayText(rules.DefaultInstance, 60))
	if len(rules.ResolvedPool) > 0 {
		fmt.Printf("Pool:    %s\n", strings.Join(rules.ResolvedPool, ", "))
	}
	if len(rules.RoundRobinOps) > 0 {
		fmt.Printf("Rotates: %s\n", strings.Join(rules.RoundRobinOps, ", "))
	}
	printScope("Organizations", rules.Orgs)
	printScope("Repositories", rules.Repos)
}

func printScope(title string, assignments map[string]string) {
	if len(assignments) == 0 {
		return
	}
	keys := make([]string, 0, len(assignments))
	for key := range assignments {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	fmt.Printf("\n%s\n", title)
	for _, key := range keys {
		fmt.Printf("  %-40s -> %s\n",
			api.DisplayText(key, 60), api.DisplayText(assignments[key], 60))
	}
}

// newRoutingSetCmd builds `routing set-repo` / `routing set-org`. An empty
// instance argument clears the rule so the scope falls back to the default.
func newRoutingSetCmd(use, noun, field string) *cobra.Command {
	return &cobra.Command{
		Use:   fmt.Sprintf("%s <%s> [instance-id]", use, noun),
		Short: fmt.Sprintf("Route a %s to an instance (omit the id to clear the rule)", noun),
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := clientFromContext(cmd.Context())
			rules, err := c.GetRouting()
			if err != nil {
				if errors.Is(err, api.ErrNotAHub) {
					fmt.Println(notAHubMessage)
					return nil
				}
				return fmt.Errorf("fetching routing: %w", err)
			}

			current := rules.Repos
			if field == "orgs" {
				current = rules.Orgs
			}
			// The whole map is sent back: PUT replaces it wholesale, which is
			// what makes clearing a rule possible at all.
			updated := make(map[string]string, len(current)+1)
			for key, value := range current {
				updated[key] = value
			}
			if len(args) == 2 && args[1] != "" {
				updated[args[0]] = args[1]
			} else {
				delete(updated, args[0])
			}

			if err := c.PutRouting(map[string]any{field: updated}); err != nil {
				return fmt.Errorf("updating routing: %w", err)
			}
			if len(args) == 2 && args[1] != "" {
				fmt.Printf("%s %s now routed to %s\n", capitalize(noun), args[0], args[1])
			} else {
				fmt.Printf("%s %s now inherits the default instance\n", capitalize(noun), args[0])
			}
			return nil
		},
	}
}

// capitalize upper-cases the first letter. strings.Title is deprecated and
// title-cases every word, which is wrong for a single noun.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func newPropagateConfigCmd() *cobra.Command {
	var targets []string
	cmd := &cobra.Command{
		Use:   "propagate-config",
		Short: "Push shared configuration from this hub to the other instances",
		Long: "Pushes the settings every instance should agree on — prompts, review and\n" +
			"merge policy, polling, per-repo and per-org overrides.\n\n" +
			"Machine-specific settings are never sent: the port, the bind address,\n" +
			"GitHub and API tokens, local directories, and each instance's own\n" +
			"repository lists.",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := clientFromContext(cmd.Context())
			report, err := c.PropagateConfig(targets)
			if err != nil {
				if errors.Is(err, api.ErrNotAHub) {
					fmt.Println(notAHubMessage)
					return nil
				}
				return fmt.Errorf("propagating config: %w", err)
			}

			for _, res := range report.Results {
				name := res.Name
				if name == "" {
					name = res.InstanceID
				}
				switch {
				case res.Skipped:
					fmt.Printf("  -  %-20s skipped", api.DisplayText(name, 30))
					if res.Error != "" {
						fmt.Printf(" (%s)", api.DisplayText(res.Error, 120))
					}
					fmt.Println()
				case res.OK:
					fmt.Printf("  ok %-20s applied %d settings\n",
						api.DisplayText(name, 30), len(res.AppliedKeys))
				default:
					fmt.Printf("  !! %-20s %s\n",
						api.DisplayText(name, 30), api.DisplayText(res.Error, 200))
				}
			}
			if len(report.SkippedLocal) > 0 {
				fmt.Printf("\nKept local: %s\n", strings.Join(report.SkippedLocal, ", "))
			}
			if report.Failures > 0 {
				// A partial push is not a crash, but the exit code has to say
				// something went wrong so a script notices.
				return fmt.Errorf("%d instance(s) could not be updated", report.Failures)
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&targets, "instance", nil, "only push to these instances (repeatable)")
	return cmd
}

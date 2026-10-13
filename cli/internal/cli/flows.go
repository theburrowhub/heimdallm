package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/theburrowhub/heimdallm/cli/internal/api"
)

func newFlowsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "flows",
		Short: "List the review flows that decide which agent reviews a PR",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			listing, err := clientFromContext(cmd.Context()).ListFlows()
			if err != nil {
				return fmt.Errorf("fetching flows: %w", err)
			}
			printFlows(os.Stdout, listing)
			return nil
		},
	}
	cmd.AddCommand(newFlowsSimulateCmd(), newFlowsQuotasCmd(), newFlowsAssistCmd())
	return cmd
}

func newFlowsSimulateCmd() *cobra.Command {
	var flow, repo, at string
	cmd := &cobra.Command{
		Use:   "simulate",
		Short: "Show which agent a flow would pick now (or at --at) and why",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var when time.Time
			if at != "" {
				t, err := time.Parse(time.RFC3339, at)
				if err != nil {
					return fmt.Errorf("--at must be RFC 3339 (2026-10-08T09:00:00+02:00): %w", err)
				}
				when = t
			}
			d, err := clientFromContext(cmd.Context()).SimulateFlow(flow, repo, when)
			if err != nil {
				return fmt.Errorf("simulating flow: %w", err)
			}
			printFlowDecision(os.Stdout, d, at == "")
			return nil
		},
	}
	cmd.Flags().StringVar(&flow, "flow", "", "flow id to evaluate (default: the global flow)")
	cmd.Flags().StringVar(&repo, "repo", "", "evaluate the flow this owner/repo resolves to")
	cmd.Flags().StringVar(&at, "at", "", "evaluate at this time (RFC 3339) instead of now")
	return cmd
}

func newFlowsAssistCmd() *cobra.Command {
	var flowID, agent string
	var apply bool
	cmd := &cobra.Command{
		Use:   "assist <instruction>",
		Short: "Draft a flow (or a change to --flow) from plain language with an installed agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if apply && flowID == "" {
				return fmt.Errorf("--apply needs --flow: the id the draft is saved as")
			}
			client := clientFromContext(cmd.Context())
			var current *api.Flow
			if flowID != "" {
				listing, err := client.ListFlows()
				if err != nil {
					return fmt.Errorf("fetching flows: %w", err)
				}
				if f, ok := listing.Flows[flowID]; ok {
					current = &f
				}
			}
			if agent == "" {
				agent = "claude"
			}
			res, err := client.AssistFlow(agent, args[0], current)
			if err != nil {
				return fmt.Errorf("drafting flow: %w", err)
			}
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "Plan (%s, %.1fs):\n  %s\n\n", api.DisplayText(res.Agent, 24), float64(res.DurationMS)/1000, api.DisplayText(res.Plan, 2000))
			for _, line := range res.Flow.Describe() {
				fmt.Fprintf(w, "  %s\n", line)
			}
			for _, warn := range res.Warnings {
				fmt.Fprintf(w, "  ⚠ %s\n", api.DisplayText(warn, 200))
			}
			if !apply {
				fmt.Fprintln(w, "\nNot saved. Re-run with --flow <id> --apply to save it.")
				return nil
			}
			if err := client.PutFlow(flowID, res.Flow); err != nil {
				return fmt.Errorf("saving flow %s: %w", flowID, err)
			}
			fmt.Fprintf(w, "\nSaved as %s.\n", api.DisplayText(flowID, 64))
			return nil
		},
	}
	cmd.Flags().StringVar(&flowID, "flow", "", "flow id to change (and, with --apply, to save as)")
	cmd.Flags().StringVar(&agent, "agent", "", "installed CLI agent that drafts the flow (default claude)")
	cmd.Flags().BoolVar(&apply, "apply", false, "save the draft with PUT /flows/<id>")
	return cmd
}

func newFlowsQuotasCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "quotas",
		Short: "Show each agent's remaining quota, as flows see it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			quotas, err := clientFromContext(cmd.Context()).GetQuotas()
			if err != nil {
				return fmt.Errorf("fetching quotas: %w", err)
			}
			printQuotas(os.Stdout, quotas)
			return nil
		},
	}
}

// printFlows renders every flow as its steps in walk order. Every
// daemon-supplied string goes through DisplayText.
func printFlows(w io.Writer, l *api.FlowListing) {
	fmt.Fprintln(w, "Review Flows")
	fmt.Fprintln(w, "════════════")
	ids := make([]string, 0, len(l.Flows))
	for id := range l.Flows {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		f := l.Flows[id]
		title := api.DisplayText(id, 64)
		if f.Name != "" {
			title += " — " + api.DisplayText(f.Name, 120)
		}
		if id == l.Selected {
			title += "  (global)"
		}
		fmt.Fprintf(w, "\n  %s\n", title)
		lines := f.Describe()
		if len(lines) == 0 {
			fmt.Fprintln(w, "    (no steps)")
		}
		for _, line := range lines {
			fmt.Fprintf(w, "    %s\n", line)
		}
	}
	var writers []string
	for _, a := range l.Agents {
		if a.WriteCapable {
			writers = append(writers, a.ID)
		}
	}
	if len(writers) > 0 {
		fmt.Fprintf(w, "\n  Conflict resolution uses only: %s\n", api.DisplayText(strings.Join(writers, ", "), 200))
	}
}

// printFlowDecision explains the path a simulated flow walked. now is false when
// the flow was evaluated at another time (--at), so the outcome is not
// described as the present.
func printFlowDecision(w io.Writer, d *api.FlowDecision, now bool) {
	name := api.DisplayText(d.FlowID, 64)
	if d.FlowName != "" {
		name += " — " + api.DisplayText(d.FlowName, 120)
	}
	fmt.Fprintf(w, "Flow %s", name)
	if !d.At.IsZero() {
		// The daemon's clock and zone, which schedules without a tz use.
		fmt.Fprintf(w, " at %s", d.At.Format("Mon 2006-01-02 15:04 MST"))
	}
	fmt.Fprintln(w)
	switch len(d.Candidates) {
	case 0:
		when := "now"
		if !now {
			when = "at that time"
		}
		fmt.Fprintf(w, "  No agent would review %s (no path reaches an available agent): the review would wait.\n", when)
	default:
		names := make([]string, len(d.Candidates))
		for i, c := range d.Candidates {
			names[i] = api.DisplayText(c, 24)
		}
		fmt.Fprintf(w, "  Reviews with %s", names[0])
		if len(names) > 1 {
			fmt.Fprintf(w, ", then %s if it runs out of quota", strings.Join(names[1:], ", "))
		}
		fmt.Fprintln(w)
	}
	for _, s := range d.Path {
		switch s.Type {
		case "condition":
			branch := "no"
			if s.Holds != nil && *s.Holds {
				branch = "yes"
			}
			fmt.Fprintf(w, "  %s condition → %s\n", api.DisplayText(s.Node, 64), branch)
		case "agent":
			mark := "✓ added"
			switch {
			case s.Available != nil && !*s.Available:
				mark = "⚠"
			case !s.Added:
				mark = "· already listed"
			}
			fmt.Fprintf(w, "  %s %s %s\n", api.DisplayText(s.Node, 64), api.DisplayText(s.Agent, 24), mark)
		default:
			fmt.Fprintf(w, "  %s\n", api.DisplayText(s.Node, 64))
		}
		for _, why := range s.Reasons {
			fmt.Fprintf(w, "      %s\n", api.DisplayText(why, 200))
		}
	}
}

// printQuotas lists each agent's quota windows.
func printQuotas(w io.Writer, quotas []api.AgentQuota) {
	fmt.Fprintln(w, "Agent Quotas")
	fmt.Fprintln(w, "════════════")
	for _, q := range quotas {
		fmt.Fprintf(w, "  %-12s %s\n", api.DisplayText(q.Agent, 12), q.Summary())
	}
}

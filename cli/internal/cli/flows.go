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
	cmd.AddCommand(newFlowsSimulateCmd(), newFlowsQuotasCmd())
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
			printFlowDecision(os.Stdout, d)
			return nil
		},
	}
	cmd.Flags().StringVar(&flow, "flow", "", "flow id to evaluate (default: the global flow)")
	cmd.Flags().StringVar(&repo, "repo", "", "evaluate the flow this owner/repo resolves to")
	cmd.Flags().StringVar(&at, "at", "", "evaluate at this time (RFC 3339) instead of now")
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

// printFlows renders every flow with its rules in evaluation order. Every
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
		keys := f.OrderedRuleKeys()
		if len(keys) == 0 {
			fmt.Fprintln(w, "    (no rules)")
		}
		for i, k := range keys {
			fmt.Fprintf(w, "    %d. %s\n", i+1, f.Rules[k].Describe())
		}
	}
	if len(l.WriteCapable) > 0 {
		fmt.Fprintf(w, "\n  Conflict resolution uses only: %s\n", api.DisplayText(strings.Join(l.WriteCapable, ", "), 200))
	}
}

// printFlowDecision explains a simulated flow rule by rule.
func printFlowDecision(w io.Writer, d *api.FlowDecision) {
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
		fmt.Fprintln(w, "  No agent would review now (no rule matches with an available agent): the review would wait.")
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
	for _, r := range d.Rules {
		mark := "✗" // conditions do not hold
		switch {
		case r.Matched && r.Available:
			mark = "✓"
		case r.Matched:
			mark = "⚠" // conditions hold, but the agent is not available
		}
		fmt.Fprintf(w, "  %s %s\n", mark, api.DisplayText(r.Agent, 24))
		for _, why := range r.Reasons {
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

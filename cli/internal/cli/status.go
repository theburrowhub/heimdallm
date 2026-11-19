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

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show daemon state, uptime, and monitored repos",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := clientFromContext(cmd.Context())
			// GetHealth rather than Health: the latter is only a reachability
			// gate, and it treats a 503 "degraded" daemon as reachable so the
			// dashboard can still read its version. Reporting that as a flat
			// "online" would hide the fault in the state this command exists to
			// diagnose, so the payload drives the Status line below.
			health, err := c.GetHealth()
			if err != nil {
				return fmt.Errorf("daemon unreachable: %w", err)
			}

			cfg, err := c.GetConfig()
			if err != nil {
				return fmt.Errorf("fetching config: %w", err)
			}

			stats, err := c.GetStats()
			if err != nil {
				return fmt.Errorf("fetching stats: %w", err)
			}

			fmt.Println("Heimdallm Daemon Status")
			fmt.Println("═══════════════════════")
			fmt.Printf("  Status:       %s\n", statusLine(health))
			if v := health.DisplayVersion(); v != "" {
				fmt.Printf("  Version:      %s\n", v)
			}
			if line := pollingLine(health); line != "" {
				fmt.Printf("  GitHub polls: %s\n", line)
			}

			if repos, ok := cfg["repositories"]; ok {
				if arr, ok := repos.([]any); ok {
					fmt.Printf("  Repositories: %d monitored\n", len(arr))
					for _, r := range arr {
						fmt.Printf("                • %v\n", r)
					}
				}
			}

			if interval, ok := cfg["poll_interval"]; ok {
				fmt.Printf("  Poll interval: %v\n", interval)
			}
			if primary, ok := cfg["ai_primary"]; ok {
				fmt.Printf("  AI primary:    %v\n", primary)
			}
			if mode, ok := cfg["review_mode"]; ok {
				fmt.Printf("  Review mode:   %v\n", mode)
			}

			fmt.Printf("\n  Total reviews: %d\n", stats.TotalReviews)
			fmt.Printf("  Activity (24h): %d events\n", stats.ActivityCount24h)

			if len(stats.BySeverity) > 0 {
				sevKeys := make([]string, 0, len(stats.BySeverity))
				for sev := range stats.BySeverity {
					sevKeys = append(sevKeys, sev)
				}
				sort.Strings(sevKeys)
				fmt.Printf("  By severity:   ")
				first := true
				for _, sev := range sevKeys {
					if !first {
						fmt.Printf(", ")
					}
					fmt.Printf("%s=%d", sev, stats.BySeverity[sev])
					first = false
				}
				fmt.Println()
			}

			// Older daemons have no /review-limits; the section is optional.
			if limits, err := c.GetReviewLimits(); err == nil {
				printReviewLimits(os.Stdout, limits, time.Now())
			}

			return nil
		},
	}
}

// printReviewLimits renders each configured review budget as
// "used/limit per window", flagging a full window with when it frees up.
// Prints nothing when no budget is configured.
func printReviewLimits(w io.Writer, limits []api.ReviewLimitStatus, now time.Time) {
	if len(limits) == 0 {
		return
	}
	fmt.Fprintln(w, "\n  Review limits:")
	for _, s := range limits {
		if len(s.Windows) == 0 {
			continue
		}
		parts := make([]string, 0, len(s.Windows))
		for _, win := range s.Windows {
			part := fmt.Sprintf("%d/%d per %s", win.Used, win.Limit, api.DisplayText(win.Window, 16))
			if win.Exhausted() && win.ResetAt.After(now) {
				part += fmt.Sprintf(" (full, next slot in %s)", win.ResetAt.Sub(now).Round(time.Second))
			} else if win.Exhausted() {
				part += " (full)"
			}
			parts = append(parts, part)
		}
		fmt.Fprintf(w, "    • %s: %s\n", s.Label(), strings.Join(parts, ", "))
	}
}

// pollingLine describes failing GitHub polls for the status command, or ""
// while they succeed or when the daemon predates the report. The daemon's own
// status stays "online" through such failures, because its poll loop is alive,
// so without this line a revoked token looks healthy.
func pollingLine(h *api.Health) string {
	if h == nil || h.Checks.LastPoll.ConsecutiveFailures <= 0 {
		return ""
	}
	p := h.Checks.LastPoll
	line := fmt.Sprintf("failing (%d in a row", p.ConsecutiveFailures)
	if p.LastSuccessAt != nil {
		line += ", last success " + p.LastSuccessAt.Local().Format("2006-01-02 15:04")
	} else {
		line += ", none since the daemon started"
	}
	line += ")"
	switch p.LastErrorKind {
	case "auth":
		line += ": GitHub rejected the token, so renew it"
	case "rate_limited":
		line += ": GitHub rate limit, so polling resumes when it resets"
	}
	return line
}

// statusLine renders the daemon's state for the status command. "ok" is spelled
// "online" for continuity with the previous output; any other state the daemon
// reports (notably "degraded", which /health returns with 503) is printed as-is
// rather than flattened, and a payload without a status says so instead of
// asserting health it cannot confirm.
// Every branch reads the sanitised value, including the "unreported" guard: a
// status of only non-printable bytes is non-empty, so testing the raw field let
// it past the guard and then printed the sanitised "" as a blank Status line. The
// dashboard avoids the same asymmetry by storing daemonStatus already sanitised.
// DisplayStatus is nil-safe, so it also covers a nil payload.
func statusLine(h *api.Health) string {
	status := h.DisplayStatus()
	if status == "" {
		return "online (status unreported)"
	}
	if status == "ok" {
		return "online"
	}
	return status
}

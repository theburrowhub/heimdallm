package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/theburrowhub/heimdallm/cli/internal/api"
)

func newStatsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stats",
		Short: "Show review statistics",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := clientFromContext(cmd.Context())
			stats, err := c.GetStats()
			if err != nil {
				return fmt.Errorf("fetching stats: %w", err)
			}

			fmt.Println("Review Statistics")
			fmt.Println("═════════════════")
			fmt.Printf("  Total reviews:    %d\n", stats.TotalReviews)
			fmt.Printf("  Activity (24h):   %d\n", stats.ActivityCount24h)
			fmt.Printf("  Avg issues/review: %.1f\n", stats.AvgIssuesPerReview)

			if len(stats.BySeverity) > 0 {
				fmt.Println("\n  By Severity:")
				sevKeys := make([]string, 0, len(stats.BySeverity))
				for sev := range stats.BySeverity {
					sevKeys = append(sevKeys, sev)
				}
				sort.Strings(sevKeys)
				for _, sev := range sevKeys {
					fmt.Printf("    %-8s %d\n", sev, stats.BySeverity[sev])
				}
			}

			if len(stats.ByCLI) > 0 {
				fmt.Println("\n  By CLI:")
				cliKeys := make([]string, 0, len(stats.ByCLI))
				for k := range stats.ByCLI {
					cliKeys = append(cliKeys, k)
				}
				sort.Strings(cliKeys)
				for _, k := range cliKeys {
					fmt.Printf("    %-10s %d\n", k, stats.ByCLI[k])
				}
			}

			if len(stats.TopRepos) > 0 {
				fmt.Println("\n  Top Repos:")
				for _, rc := range stats.TopRepos {
					fmt.Printf("    %-30s %d reviews\n", rc.Repo, rc.Count)
				}
			}

			if len(stats.ReviewsLast7Days) > 0 {
				fmt.Println("\n  Reviews (last 7 days):")
				const maxBar = 40
				for _, dc := range stats.ReviewsLast7Days {
					barLen := dc.Count
					if barLen > maxBar {
						barLen = maxBar
					}
					bar := strings.Repeat("\u2588", barLen)
					fmt.Printf("    %s  %s (%d)\n", dc.Day, bar, dc.Count)
				}
			}

			if stats.ReviewTiming.SampleCount > 0 {
				t := stats.ReviewTiming
				fmt.Println("\n  Review Timing:")
				fmt.Printf("    Samples: %d\n", t.SampleCount)
				fmt.Printf("    Avg:     %.1fs\n", t.AvgSeconds)
				fmt.Printf("    Median:  %.1fs\n", t.MedianSeconds)
				fmt.Printf("    Range:   %.1fs – %.1fs\n", t.MinSeconds, t.MaxSeconds)
				fmt.Printf("    Fast (<30s):    %d\n", t.BucketFast)
				fmt.Printf("    Medium (30-120s): %d\n", t.BucketMedium)
				fmt.Printf("    Slow (120-300s):  %d\n", t.BucketSlow)
				fmt.Printf("    Very slow (>300s): %d\n", t.BucketVerySlow)
			}

			printTokenStats(os.Stdout, stats.TokensLast7Days)
			return nil
		},
	}
}

// printTokenStats renders the 7-day token usage section; nothing when no
// review in the window recorded usage.
func printTokenStats(w io.Writer, t api.TokenStats) {
	if t.Reviews == 0 {
		return
	}
	fmt.Fprintln(w, "\n  Tokens (last 7 days):")
	fmt.Fprintf(w, "    Reviews:      %d", t.Reviews)
	if t.EstimatedReviews > 0 {
		fmt.Fprintf(w, " (%d estimated)", t.EstimatedReviews)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "    Input:        %d\n", t.InputTokens)
	fmt.Fprintf(w, "    Output:       %d\n", t.OutputTokens)
	if t.CacheReadTokens > 0 {
		fmt.Fprintf(w, "    Cache reads:  %d\n", t.CacheReadTokens)
	}
	// Estimated reviews count only prompt and answer text (no agent
	// exploration) and report no cost, so say when they are mixed in.
	perReview := fmt.Sprintf("%d", (t.InputTokens+t.OutputTokens)/int64(t.Reviews))
	if t.EstimatedReviews > 0 {
		perReview += " (includes estimates)"
	}
	fmt.Fprintf(w, "    Per review:   %s\n", perReview)
	fmt.Fprintf(w, "    Avg prompt:   %.1f KB\n", t.AvgPromptBytes/1024)
	if t.CostUSD > 0 {
		cost := fmt.Sprintf("$%.2f", t.CostUSD)
		if t.EstimatedReviews > 0 {
			cost += " (reported reviews only)"
		}
		fmt.Fprintf(w, "    Cost:         %s\n", cost)
	}
}

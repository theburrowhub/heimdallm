package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
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

			return nil
		},
	}
}

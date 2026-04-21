package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func newStatsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stats",
		Short: "Show review statistics",
		RunE: func(cmd *cobra.Command, args []string) error {
			stats, err := client.GetStats()
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
				for sev, count := range stats.BySeverity {
					fmt.Printf("    %-8s %d\n", sev, count)
				}
			}

			if len(stats.ByCLI) > 0 {
				fmt.Println("\n  By CLI:")
				for cli, count := range stats.ByCLI {
					fmt.Printf("    %-10s %d\n", cli, count)
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
				for _, dc := range stats.ReviewsLast7Days {
					bar := strings.Repeat("█", dc.Count)
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

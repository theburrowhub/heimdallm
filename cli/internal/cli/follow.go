package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/theburrowhub/heimdallm/cli/internal/api"
	"github.com/spf13/cobra"
)

func newFollowCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "follow",
		Short: "Stream real-time SSE events (like tail -f)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()

			events := make(chan api.SSEEvent, 32)
			errCh := make(chan error, 1)

			c := clientFromContext(cmd.Context())
			go func() {
				errCh <- c.StreamEvents(ctx, events)
			}()

			fmt.Println("Listening for events... (Ctrl+C to stop)")
			fmt.Println()

			for {
				select {
				case event := <-events:
					if jsonOutput {
						out := map[string]string{
							"type": event.Type,
							"data": event.Data,
							"time": time.Now().Format(time.RFC3339),
						}
						b, _ := json.Marshal(out)
						fmt.Println(string(b))
					} else {
						ts := time.Now().Format("15:04:05")
						fmt.Printf("[%s] %-25s %s\n", ts, event.Type, formatEventData(event.Data))
					}
				case err := <-errCh:
					if err != nil {
						return fmt.Errorf("SSE stream: %w", err)
					}
					fmt.Println("Stream ended.")
					return nil
				case <-cmd.Context().Done():
					cancel()
					fmt.Println("\nStopped.")
					return nil
				}
			}
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output raw JSON events")

	return cmd
}

func formatEventData(data string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		return data
	}

	parts := make([]string, 0)
	if repo, ok := m["repo"]; ok {
		parts = append(parts, fmt.Sprintf("%v", repo))
	}
	if num, ok := m["pr_number"]; ok {
		if n := toInt(num); n != 0 {
			parts = append(parts, fmt.Sprintf("PR #%d", n))
		}
	}
	if num, ok := m["issue_number"]; ok {
		if n := toInt(num); n != 0 {
			parts = append(parts, fmt.Sprintf("Issue #%d", n))
		}
	}
	if sev, ok := m["severity"]; ok {
		parts = append(parts, fmt.Sprintf("[%v]", sev))
	}
	if errMsg, ok := m["error"]; ok {
		parts = append(parts, fmt.Sprintf("error: %v", errMsg))
	}

	if len(parts) > 0 {
		result := parts[0]
		for _, p := range parts[1:] {
			result += " " + p
		}
		return result
	}
	return data
}

func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	default:
		return 0
	}
}

package cli

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/heimdallm/cli/internal/api"
	"github.com/spf13/cobra"
)

func newFollowCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "follow",
		Short: "Stream real-time SSE events (like tail -f)",
		RunE: func(cmd *cobra.Command, args []string) error {
			events := make(chan api.SSEEvent, 32)
			done := make(chan struct{})
			errCh := make(chan error, 1)

			go func() {
				errCh <- client.StreamEvents(events, done)
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
		parts = append(parts, fmt.Sprintf("#%v", num))
	}
	if num, ok := m["issue_number"]; ok {
		parts = append(parts, fmt.Sprintf("#%v", num))
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

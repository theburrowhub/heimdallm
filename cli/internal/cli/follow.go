package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"github.com/theburrowhub/heimdallm/cli/internal/api"
)

var (
	badgePR     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#3B82F6"))
	badgeIssue  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#F59E0B"))
	badgeError  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#EF4444"))
	badgeSystem = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#06B6D4"))
	styleError  = lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444"))
	styleMuted  = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
)

func eventCategory(eventType string) string {
	switch {
	case strings.HasPrefix(eventType, "pr_"),
		strings.HasPrefix(eventType, "review_"),
		strings.HasPrefix(eventType, "circuit_breaker_"):
		return "pr"
	case strings.HasPrefix(eventType, "issue_"):
		return "issue"
	default:
		return "system"
	}
}

func eventBadge(eventType string) string {
	if strings.HasSuffix(eventType, "_error") || strings.HasSuffix(eventType, "_tripped") {
		return badgeError.Render("[ERROR]")
	}
	switch eventCategory(eventType) {
	case "pr":
		return badgePR.Render("[PR]")
	case "issue":
		return badgeIssue.Render("[ISSUE]")
	default:
		return badgeSystem.Render("[SYSTEM]")
	}
}

func newFollowCmd() *cobra.Command {
	var (
		jsonOutput bool
		repoFilter string
		typeFilter string
	)

	cmd := &cobra.Command{
		Use:   "follow",
		Short: "Stream real-time SSE events (like tail -f)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()

			allowedTypes := make(map[string]bool)
			if typeFilter != "" {
				for _, t := range strings.Split(typeFilter, ",") {
					t = strings.TrimSpace(strings.ToLower(t))
					if t != "" {
						allowedTypes[t] = true
					}
				}
			}

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
					cat := eventCategory(event.Type)

					if len(allowedTypes) > 0 && !allowedTypes[cat] {
						continue
					}

					if repoFilter != "" {
						if repo := extractRepo(event.Data); repo != "" && !strings.EqualFold(repo, repoFilter) {
							continue
						}
					}

					if jsonOutput {
						out := map[string]string{
							"type": event.Type,
							"data": event.Data,
							"time": time.Now().Format(time.RFC3339),
						}
						b, _ := json.Marshal(out)
						fmt.Println(string(b))
					} else {
						ts := styleMuted.Render(time.Now().Format("15:04:05"))
						fmt.Printf("%s %-10s %-28s %s\n", ts, eventBadge(event.Type), event.Type, formatEventData(event.Data))
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
	cmd.Flags().StringVar(&repoFilter, "repo", "", "filter by repository (e.g. org/repo)")
	cmd.Flags().StringVar(&typeFilter, "type", "", "filter by event category: pr, issue, system (comma-separated)")

	return cmd
}

func extractRepo(data string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		return ""
	}
	if repo, ok := m["repo"].(string); ok {
		return repo
	}
	return ""
}

func formatEventData(data string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		return data
	}

	parts := make([]string, 0, 6)

	if repo, ok := m["repo"].(string); ok {
		parts = append(parts, repo)
	}

	if num := toInt(m["pr_number"]); num != 0 {
		s := fmt.Sprintf("#%d", num)
		if title, ok := m["pr_title"].(string); ok && title != "" {
			s += " " + truncate(title, 40)
		}
		parts = append(parts, s)
	}

	if num := toInt(m["issue_number"]); num != 0 {
		s := fmt.Sprintf("#%d", num)
		if title, ok := m["issue_title"].(string); ok && title != "" {
			s += " " + truncate(title, 40)
		}
		parts = append(parts, s)
	}

	if sev, ok := m["severity"].(string); ok && sev != "" {
		parts = append(parts, fmt.Sprintf("[%s]", sev))
	}

	if action, ok := m["chosen_action"].(string); ok && action != "" {
		parts = append(parts, action)
	}

	if from, ok := m["from_label"].(string); ok {
		if to, ok := m["to_label"].(string); ok {
			parts = append(parts, from+" → "+to)
		}
	}

	if reason, ok := m["reason"].(string); ok && reason != "" {
		parts = append(parts, styleMuted.Render(reason))
	}

	if errMsg, ok := m["error"].(string); ok && errMsg != "" {
		parts = append(parts, styleError.Render("error: "+errMsg))
	}

	if len(parts) > 0 {
		return strings.Join(parts, " ")
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

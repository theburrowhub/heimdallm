package cli

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

var (
	detailBoxPR = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#3B82F6")).
			Padding(0, 1).
			Bold(true)

	detailBoxIssue = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#F59E0B")).
			Padding(0, 1).
			Bold(true)

	detailLabel = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6B7280"))

	detailSection = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#06B6D4"))

	detailSeverity = func(sev string) lipgloss.Style {
		switch sev {
		case "high":
			return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#EF4444"))
		case "medium":
			return lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B"))
		case "low":
			return lipgloss.NewStyle().Foreground(lipgloss.Color("#10B981"))
		default:
			return lipgloss.NewStyle().Foreground(lipgloss.Color("#06B6D4"))
		}
	}
)

func newPRDetailCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "pr <id>",
		Short: "Show full PR detail with review history",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := clientFromContext(cmd.Context())
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid PR ID: %w", err)
			}

			detail, err := c.GetPR(id)
			if err != nil {
				return fmt.Errorf("fetching PR: %w", err)
			}

			if jsonOutput {
				b, err := json.MarshalIndent(detail, "", "  ")
				if err != nil {
					return fmt.Errorf("marshalling JSON: %w", err)
				}
				fmt.Println(string(b))
				return nil
			}

			pr := detail.PR

			fmt.Println(detailBoxPR.Render(
				fmt.Sprintf("PR #%d — %s", pr.Number, pr.Title)))
			fmt.Println()
			fmt.Printf("  %s  %s\n", detailLabel.Render("Repo:"), pr.Repo)
			fmt.Printf("  %s  %s\n", detailLabel.Render("Author:"), pr.Author)
			fmt.Printf("  %s  %s\n", detailLabel.Render("State:"), pr.State)
			if pr.URL != "" {
				fmt.Printf("  %s  %s\n", detailLabel.Render("URL:"), pr.URL)
			}
			fmt.Printf("  %s  %s\n", detailLabel.Render("Updated:"),
				pr.UpdatedAt.Format("2006-01-02 15:04"))
			if pr.Dismissed {
				fmt.Printf("  %s  yes\n", detailLabel.Render("Dismissed:"))
			}

			if len(detail.Reviews) == 0 {
				fmt.Println("\n  No reviews yet.")
				return nil
			}

			for i, rev := range detail.Reviews {
				fmt.Println()
				fmt.Println(detailSection.Render(
					fmt.Sprintf("══ Review %d ══", i+1)))
				fmt.Println()
				fmt.Printf("  %s  %s\n", detailLabel.Render("Severity:"),
					detailSeverity(rev.Severity).Render(rev.Severity))
				fmt.Printf("  %s  %s\n", detailLabel.Render("Date:"),
					rev.CreatedAt.Format("2006-01-02 15:04"))
				if rev.CLIUsed != "" {
					fmt.Printf("  %s  %s\n", detailLabel.Render("CLI:"), rev.CLIUsed)
				}
				if rev.GitHubReviewState != "" {
					fmt.Printf("  %s  %s\n", detailLabel.Render("GH State:"),
						rev.GitHubReviewState)
				}

				if rev.Summary != "" {
					fmt.Println()
					fmt.Println(detailSection.Render("  Summary"))
					fmt.Println("  " + strings.Repeat("─", 40))
					printIndented(rev.Summary, "  ")
				}

				printJSONArray("Issues", rev.Issues)
				printJSONArray("Suggestions", rev.Suggestions)
			}

			fmt.Println()
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output raw JSON")
	return cmd
}

func printIndented(text, indent string) {
	for _, line := range strings.Split(text, "\n") {
		fmt.Println(indent + line)
	}
}

func printJSONArray(title, raw string) {
	if raw == "" || raw == "[]" || raw == "null" {
		return
	}
	var items []any
	if err := json.Unmarshal([]byte(raw), &items); err != nil || len(items) == 0 {
		return
	}
	fmt.Println()
	fmt.Println(detailSection.Render("  " + title))
	fmt.Println("  " + strings.Repeat("─", 40))
	for i, item := range items {
		switch v := item.(type) {
		case string:
			fmt.Printf("  %d. %s\n", i+1, v)
		default:
			b, _ := json.MarshalIndent(v, "     ", "  ")
			fmt.Printf("  %d. %s\n", i+1, string(b))
		}
	}
}

func printTriageMap(raw string) {
	if raw == "" || raw == "{}" || raw == "null" {
		return
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil || len(m) == 0 {
		return
	}
	fmt.Println()
	fmt.Println(detailSection.Render("  Classification"))
	fmt.Println("  " + strings.Repeat("─", 40))
	if sev, ok := m["severity"]; ok {
		sevStr := fmt.Sprintf("%v", sev)
		fmt.Printf("  %-14s %s\n", "severity:",
			detailSeverity(sevStr).Render(sevStr))
	}
	for k, v := range m {
		if k == "severity" {
			continue
		}
		fmt.Printf("  %-14s %v\n", k+":", v)
	}
}

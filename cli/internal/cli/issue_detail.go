package cli

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

func newIssueDetailCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "issue <id>",
		Short: "Show full issue detail with triage history",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := clientFromContext(cmd.Context())
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid issue ID: %w", err)
			}

			detail, err := c.GetIssue(id)
			if err != nil {
				return fmt.Errorf("fetching issue: %w", err)
			}

			if jsonOutput {
				b, err := json.MarshalIndent(detail, "", "  ")
				if err != nil {
					return fmt.Errorf("marshalling JSON: %w", err)
				}
				fmt.Println(string(b))
				return nil
			}

			iss := detail.Issue

			fmt.Println(detailBoxIssue.Render(
				fmt.Sprintf("Issue #%d — %s", iss.Number, iss.Title)))
			fmt.Println()
			fmt.Printf("  %s  %s\n", detailLabel.Render("Repo:"), iss.Repo)
			fmt.Printf("  %s  %s\n", detailLabel.Render("Author:"), iss.Author)
			fmt.Printf("  %s  %s\n", detailLabel.Render("State:"), iss.State)
			fmt.Printf("  %s  %s\n", detailLabel.Render("Created:"),
				iss.CreatedAt.Format("2006-01-02 15:04"))

			if labels := formatJSONStringArray(iss.Labels); labels != "" {
				fmt.Printf("  %s  %s\n", detailLabel.Render("Labels:"), labels)
			}
			if assignees := formatJSONStringArray(iss.Assignees); assignees != "" {
				fmt.Printf("  %s  %s\n", detailLabel.Render("Assignees:"), assignees)
			}
			if iss.Dismissed {
				fmt.Printf("  %s  yes\n", detailLabel.Render("Dismissed:"))
			}

			if iss.Body != "" {
				fmt.Println()
				fmt.Println(detailSection.Render("  Body"))
				fmt.Println("  " + strings.Repeat("─", 40))
				printIndented(iss.Body, "  ")
			}

			if len(detail.Reviews) == 0 {
				fmt.Println("\n  No triages yet.")
				return nil
			}

			for i, rev := range detail.Reviews {
				fmt.Println()
				fmt.Println(detailSection.Render(
					fmt.Sprintf("══ Triage %d ══", i+1)))
				fmt.Println()
				fmt.Printf("  %s  %s\n", detailLabel.Render("Action:"),
					rev.ActionTaken)
				if rev.PRCreated != 0 {
					fmt.Printf("  %s  #%d\n", detailLabel.Render("PR Created:"),
						rev.PRCreated)
				}
				fmt.Printf("  %s  %s\n", detailLabel.Render("Date:"),
					rev.CreatedAt.Format("2006-01-02 15:04"))
				if rev.CLIUsed != "" {
					fmt.Printf("  %s  %s\n", detailLabel.Render("CLI:"), rev.CLIUsed)
				}

				if rev.Summary != "" {
					fmt.Println()
					fmt.Println(detailSection.Render("  Summary"))
					fmt.Println("  " + strings.Repeat("─", 40))
					printIndented(rev.Summary, "  ")
				}

				printTriageMap(string(rev.Triage))
				printJSONArray("Suggestions", string(rev.Suggestions))
			}

			fmt.Println()
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output raw JSON")
	return cmd
}

func formatJSONStringArray(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var items []string
	if err := json.Unmarshal(raw, &items); err != nil || len(items) == 0 {
		return ""
	}
	return strings.Join(items, ", ")
}

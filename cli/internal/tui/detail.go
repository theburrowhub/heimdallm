package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/theburrowhub/heimdallm/cli/internal/api"
)

func buildPRDetailLines(pr api.PR, width int) []string {
	w := width - 6
	if w < 40 {
		w = 40
	}

	sep := strings.Repeat("─", min(w, 60))

	var lines []string
	lines = append(lines, headerStyle.Render(fmt.Sprintf("  PR #%d — %s", pr.Number, pr.Repo)))
	lines = append(lines, "  "+sep)
	lines = append(lines, "")
	lines = append(lines, fmt.Sprintf("  %-12s %s", "Title:", pr.Title))
	lines = append(lines, fmt.Sprintf("  %-12s %s", "Author:", pr.Author))
	lines = append(lines, fmt.Sprintf("  %-12s %s", "State:", pr.State))
	lines = append(lines, fmt.Sprintf("  %-12s %s", "URL:", pr.URL))
	lines = append(lines, fmt.Sprintf("  %-12s %s", "Updated:", pr.UpdatedAt.Format("2006-01-02 15:04")))

	if pr.LatestReview != nil {
		r := pr.LatestReview
		lines = append(lines, "")
		lines = append(lines, headerStyle.Render("  Latest Review"))
		lines = append(lines, "  "+sep)
		lines = append(lines, fmt.Sprintf("  %-12s %s", "Severity:", severityStyle(r.Severity).Render(r.Severity)))
		lines = append(lines, fmt.Sprintf("  %-12s %s", "Reviewed:", r.CreatedAt.Format("2006-01-02 15:04")))
		lines = append(lines, fmt.Sprintf("  %-12s %s", "CLI:", r.CLIUsed))
		if r.HeadSHA != "" {
			sha := r.HeadSHA
			if len(sha) > 8 {
				sha = sha[:8]
			}
			lines = append(lines, fmt.Sprintf("  %-12s %s", "SHA:", sha))
		}

		if r.Summary != "" {
			lines = append(lines, "")
			lines = append(lines, headerStyle.Render("  Summary"))
			for _, l := range wrapText(r.Summary, w-4) {
				lines = append(lines, "    "+l)
			}
		}
		if r.Issues != "" {
			lines = append(lines, "")
			lines = append(lines, headerStyle.Render("  Issues"))
			for _, l := range wrapText(r.Issues, w-4) {
				lines = append(lines, "    "+l)
			}
		}
		if r.Suggestions != "" {
			lines = append(lines, "")
			lines = append(lines, headerStyle.Render("  Suggestions"))
			for _, l := range wrapText(r.Suggestions, w-4) {
				lines = append(lines, "    "+l)
			}
		}
	}

	return lines
}

func buildIssueDetailLines(issue api.Issue, width int) []string {
	w := width - 6
	if w < 40 {
		w = 40
	}

	sep := strings.Repeat("─", min(w, 60))

	var lines []string
	lines = append(lines, headerStyle.Render(fmt.Sprintf("  Issue #%d — %s", issue.Number, issue.Repo)))
	lines = append(lines, "  "+sep)
	lines = append(lines, "")
	lines = append(lines, fmt.Sprintf("  %-12s %s", "Title:", issue.Title))
	lines = append(lines, fmt.Sprintf("  %-12s %s", "Author:", issue.Author))
	lines = append(lines, fmt.Sprintf("  %-12s %s", "State:", issue.State))
	lines = append(lines, fmt.Sprintf("  %-12s %s", "Created:", issue.CreatedAt.Format("2006-01-02 15:04")))

	if issue.Body != "" {
		lines = append(lines, "")
		lines = append(lines, headerStyle.Render("  Description"))
		body := issue.Body
		if len([]rune(body)) > 500 {
			body = string([]rune(body)[:500]) + "…"
		}
		for _, l := range wrapText(body, w-4) {
			lines = append(lines, "    "+l)
		}
	}

	if issue.LatestReview != nil {
		r := issue.LatestReview
		lines = append(lines, "")
		lines = append(lines, headerStyle.Render("  Latest Review"))
		lines = append(lines, "  "+sep)
		lines = append(lines, fmt.Sprintf("  %-12s %s", "Action:", r.ActionTaken))
		lines = append(lines, fmt.Sprintf("  %-12s %s", "Reviewed:", r.CreatedAt.Format("2006-01-02 15:04")))
		lines = append(lines, fmt.Sprintf("  %-12s %s", "CLI:", r.CLIUsed))

		if r.PRCreated > 0 {
			lines = append(lines, fmt.Sprintf("  %-12s #%d", "PR Created:", r.PRCreated))
		}

		if len(r.Triage) > 0 {
			var triage map[string]any
			if json.Unmarshal(r.Triage, &triage) == nil && len(triage) > 0 {
				lines = append(lines, "")
				lines = append(lines, headerStyle.Render("  Triage"))
				if sev, ok := triage["severity"]; ok {
					sevStr := fmt.Sprintf("%v", sev)
					lines = append(lines, fmt.Sprintf("    %-16s %s", "severity:", severityStyle(sevStr).Render(sevStr)))
				}
				for k, v := range triage {
					if k == "severity" {
						continue
					}
					lines = append(lines, fmt.Sprintf("    %-16s %v", k+":", v))
				}
			}
		}

		if r.Summary != "" {
			lines = append(lines, "")
			lines = append(lines, headerStyle.Render("  Summary"))
			for _, l := range wrapText(r.Summary, w-4) {
				lines = append(lines, "    "+l)
			}
		}
	}

	return lines
}

func wrapText(text string, width int) []string {
	if width <= 0 {
		return []string{text}
	}
	var lines []string
	for _, paragraph := range strings.Split(text, "\n") {
		if paragraph == "" {
			lines = append(lines, "")
			continue
		}
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		current := words[0]
		for _, w := range words[1:] {
			if len(current)+1+len(w) > width {
				lines = append(lines, current)
				current = w
			} else {
				current += " " + w
			}
		}
		lines = append(lines, current)
	}
	return lines
}

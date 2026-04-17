package issues

import (
	"fmt"
	"strings"

	"github.com/heimdallm/daemon/internal/github"
)

// maxBodyBytes bounds the issue body we send to the LLM. Long issue bodies
// mostly contain copy-pasted stack traces or log dumps that waste tokens; the
// first few KB carry the signal the triage actually needs.
const maxBodyBytes = 8 * 1024

// maxCommentsBytes caps the formatted comment thread so a chatty issue cannot
// push the prompt past the CLI's context window.
const maxCommentsBytes = 8 * 1024

// PromptContext is the data the triage template substitutes into the prompt.
type PromptContext struct {
	Repo        string
	Number      int
	Title       string
	Author      string
	Labels      []string
	Body        string
	Comments    []github.Comment
	HasLocalDir bool // when true, the LLM can read the repo for deeper context
}

// BuildPrompt formats the LLM prompt for a review_only triage run. The output
// schema is fixed — the daemon parses it back into IssueReviewResult — so the
// prompt ends with a strict JSON-only instruction.
func BuildPrompt(ctx PromptContext) string {
	var sb strings.Builder

	sb.WriteString("You are Heimdallm, an engineering assistant triaging a GitHub issue.\n")
	sb.WriteString("Read the issue below and produce a short, actionable triage report.\n\n")

	sb.WriteString(fmt.Sprintf("Repository: %s\n", ctx.Repo))
	sb.WriteString(fmt.Sprintf("Issue: #%d — %s\n", ctx.Number, ctx.Title))
	sb.WriteString(fmt.Sprintf("Author: @%s\n", ctx.Author))
	if len(ctx.Labels) > 0 {
		sb.WriteString("Labels: " + strings.Join(ctx.Labels, ", ") + "\n")
	}
	if ctx.HasLocalDir {
		sb.WriteString("You have read access to the repository checked out at the working directory — consult the code when it helps the triage.\n")
	}
	sb.WriteString("\n")

	body := strings.TrimSpace(ctx.Body)
	if body == "" {
		body = "(empty issue body)"
	}
	if len(body) > maxBodyBytes {
		body = body[:maxBodyBytes] + "\n... (truncated)"
	}
	sb.WriteString("<issue_body>\n")
	sb.WriteString(body)
	sb.WriteString("\n</issue_body>\n\n")

	if comments := formatComments(ctx.Comments); comments != "" {
		sb.WriteString(comments)
		sb.WriteString("\n")
	}

	sb.WriteString("Return a single JSON object, and nothing else, with this exact shape:\n")
	sb.WriteString("{\n")
	sb.WriteString(`  "summary": "2–4 sentence recap of what the issue is actually asking for",` + "\n")
	sb.WriteString(`  "triage": {` + "\n")
	sb.WriteString(`    "severity": "low|medium|high|critical",` + "\n")
	sb.WriteString(`    "category": "one of: bug, feature, question, docs, infra, other",` + "\n")
	sb.WriteString(`    "suggested_assignee": "github-login or empty string"` + "\n")
	sb.WriteString("  },\n")
	sb.WriteString(`  "suggestions": ["concrete next step", "another one"],` + "\n")
	sb.WriteString(`  "severity": "low|medium|high|critical"` + "\n")
	sb.WriteString("}\n")
	sb.WriteString("If unsure about a field, use a conservative default. Do not wrap the JSON in prose or code fences.\n")

	return sb.String()
}

// formatComments renders the comment thread as a prompt section, trimming to
// the configured byte cap. Empty input returns empty string so the prompt
// does not show an empty "Existing discussion:" header.
func formatComments(comments []github.Comment) string {
	if len(comments) == 0 {
		return ""
	}
	lines := make([]string, 0, len(comments))
	for _, c := range comments {
		lines = append(lines, fmt.Sprintf("@%s: %s", c.Author, strings.TrimSpace(c.Body)))
	}
	joined := strings.Join(lines, "\n---\n")
	if len(joined) > maxCommentsBytes {
		joined = joined[:maxCommentsBytes] + "\n... (truncated)"
	}
	return "Existing discussion:\n<issue_comments>\n" + joined + "\n</issue_comments>"
}

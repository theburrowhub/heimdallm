package issues

import (
	"fmt"
	"strings"

	"github.com/heimdallm/daemon/internal/github"
)

// maxBodyBytes bounds the issue body we send to the LLM. Long issue bodies
// mostly contain copy-pasted stack traces or log dumps that waste tokens; the
// first few KB carry the signal the triage actually needs.
//
// NOTE: this is deliberately distinct from github.maxBodyBytes (1 MB) — the
// GitHub one bounds API response reads, this one bounds prompt size. They
// happen to share a name because of their shape, not their purpose.
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

// BuildImplementPrompt formats the LLM prompt for an auto_implement run.
// In this mode the agent is expected to modify files in the working tree
// directly; the outer pipeline picks up whatever it leaves behind with
// `git add -A && git commit`. There is no JSON schema for the output — the
// filesystem state is the output.
func BuildImplementPrompt(ctx PromptContext) string {
	var sb strings.Builder

	sb.WriteString("You are Heimdallm, an engineering agent implementing a GitHub issue.\n")
	sb.WriteString("You have WRITE access to the working directory, which is a checkout of the repository.\n\n")

	sb.WriteString(fmt.Sprintf("Repository: %s\n", ctx.Repo))
	sb.WriteString(fmt.Sprintf("Issue: #%d — %s\n", ctx.Number, ctx.Title))
	sb.WriteString(fmt.Sprintf("Author: @%s\n", ctx.Author))
	if len(ctx.Labels) > 0 {
		sb.WriteString("Labels: " + strings.Join(ctx.Labels, ", ") + "\n")
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

	sb.WriteString("Implement what the issue asks for. Write real, working code.\n")
	sb.WriteString("Rules:\n")
	sb.WriteString("- Keep the change minimal and focused on the issue.\n")
	sb.WriteString("- Follow the existing code style; do not reformat unrelated files.\n")
	sb.WriteString("- If tests exist for the area you are changing, extend them.\n")
	sb.WriteString("- Do not commit secrets, credentials, or files outside the repository.\n")
	sb.WriteString("- Leave the working tree in the final state you want committed — the outer pipeline will run `git add -A && git commit` over whatever you change.\n")
	sb.WriteString("- If you cannot implement the issue (insufficient information, risky change, requires a human decision), leave the tree untouched. A review-only comment will be posted instead.\n")

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

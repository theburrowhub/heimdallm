package issues

import (
	"fmt"
	"log/slog"
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
	Repo          string
	Number        int
	Title         string
	Author        string
	Labels        []string
	Assignees     []string
	Body          string
	Comments      []github.Comment
	HasLocalDir   bool   // when true, the LLM can read the repo for deeper context
	TriageContext string // structured re-triage context; empty on first triage
	TriageOwner   string // fallback GitHub login when ownership is unclear
}

// BuildPromptWithProfile formats the LLM prompt for a review_only triage run,
// applying customizations from Agent profiles when set:
//   - customTemplate non-empty: replaces the entire default template with
//     placeholder substitution ({repo}, {number}, {title}, {author}, {labels},
//     {body}, {comments}, {assignees}, {triage_owner}). NOTE: the custom
//     template is responsible for including the JSON output schema — the
//     pipeline parses the LLM response as IssueReviewResult. Same contract as
//     PR review custom prompts.
//   - customInstructions non-empty: injects the text into the default template
//     between the issue context and the JSON schema (safer — schema is preserved).
//
// When both are empty, falls back to the built-in default template.
func BuildPromptWithProfile(ctx PromptContext, customTemplate, customInstructions string) string {
	if customTemplate != "" {
		return applyPlaceholders(customTemplate, ctx)
	}
	return buildDefaultPrompt(ctx, customInstructions)
}

// BuildPrompt is the zero-config entry point — no agent profile, no custom
// instructions. Equivalent to BuildPromptWithProfile(ctx, "", "").
func BuildPrompt(ctx PromptContext) string {
	return buildDefaultPrompt(ctx, "")
}

func applyPlaceholders(tmpl string, ctx PromptContext) string {
	labels := ""
	if len(ctx.Labels) > 0 {
		labels = strings.Join(ctx.Labels, ", ")
	}
	comments := ""
	if formatted := formatComments(ctx.Comments); formatted != "" {
		comments = formatted
	}
	assignees := ""
	if len(ctx.Assignees) > 0 {
		assignees = strings.Join(ctx.Assignees, ", ")
	}

	body := strings.TrimSpace(ctx.Body)
	if len(body) > maxBodyBytes {
		body = body[:maxBodyBytes] + "\n... (truncated)"
	}

	hasTriageCtx := strings.Contains(tmpl, "{triage_context}")

	r := strings.NewReplacer(
		"{repo}", ctx.Repo,
		"{number}", fmt.Sprintf("%d", ctx.Number),
		"{title}", ctx.Title,
		"{author}", ctx.Author,
		"{labels}", labels,
		"{body}", body,
		"{comments}", comments,
		"{assignees}", assignees,
		"{triage_context}", ctx.TriageContext,
		"{triage_owner}", ctx.TriageOwner,
	)
	result := r.Replace(tmpl)

	// Collapse triple+ newlines caused by an empty placeholder.
	for strings.Contains(result, "\n\n\n") {
		result = strings.ReplaceAll(result, "\n\n\n", "\n\n")
	}

	// Prepend triage context if template had no {triage_context} placeholder.
	if !hasTriageCtx && ctx.TriageContext != "" {
		result = ctx.TriageContext + "\n" + result
	}

	// Warn about unreplaced placeholders — helps debug typos in custom templates.
	if idx := strings.Index(result, "{"); idx != -1 {
		if end := strings.Index(result[idx:], "}"); end != -1 {
			slog.Warn("issue prompt: unreplaced placeholder in custom template",
				"placeholder", result[idx:idx+end+1])
		}
	}

	return result
}

func buildDefaultPrompt(ctx PromptContext, customInstructions string) string {
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
		sb.WriteString("You have read access to the repository checked out at the working directory. Keep triage lightweight, but inspect likely files and git history when it helps identify ownership.\n")
		sb.WriteString("For bugs, prefer the person who recently touched or introduced the likely affected area. For feature requests, prefer the person with the most relevant changes in that area. Use git log/blame/shortlog evidence when available.\n")
	}
	if owner := strings.TrimSpace(ctx.TriageOwner); owner != "" {
		sb.WriteString(fmt.Sprintf("Fallback triage owner: @%s. Use this only when the affected owner cannot be determined from repository evidence.\n", strings.TrimLeft(owner, "@")))
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

	if ctx.TriageContext != "" {
		sb.WriteString(ctx.TriageContext)
		sb.WriteString("\n")
	}

	if customInstructions != "" {
		sb.WriteString("Additional triage instructions from the repository maintainer:\n")
		sb.WriteString(strings.TrimSpace(customInstructions))
		sb.WriteString("\n\n")
	}

	sb.WriteString("Return a single JSON object, and nothing else, with this exact shape:\n")
	sb.WriteString("{\n")
	sb.WriteString(`  "summary": "2–4 sentence recap of what the issue is actually asking for",` + "\n")
	sb.WriteString(`  "triage": {` + "\n")
	sb.WriteString(`    "severity": "low|medium|high|critical",` + "\n")
	sb.WriteString(`    "category": "one of: bug, feature, question, docs, infra, other",` + "\n")
	sb.WriteString(`    "affected_area": "short likely root-cause area, or empty string",` + "\n")
	sb.WriteString(`    "affected_paths": ["relative/path.go"],` + "\n")
	sb.WriteString(`    "priority_label": "priority: low|priority: medium|priority: high|priority: critical",` + "\n")
	sb.WriteString(`    "suggested_assignee": "github-login or empty string",` + "\n")
	sb.WriteString(`    "assignee_reason": "why this owner has the most relevant context, or why fallback was used",` + "\n")
	sb.WriteString(`    "assignee_confidence": "low|medium|high",` + "\n")
	sb.WriteString(`    "assignee_evidence": ["git log/blame/shortlog evidence or fallback reason"]` + "\n")
	sb.WriteString("  },\n")
	sb.WriteString(`  "suggestions": ["concrete next step", "another one"],` + "\n")
	sb.WriteString(`  "severity": "low|medium|high|critical"` + "\n")
	sb.WriteString("}\n")
	sb.WriteString("If unsure about a field, use a conservative default. Do not wrap the JSON in prose or code fences.\n")

	return sb.String()
}

// BuildImplementPromptWithProfile formats the LLM prompt for an auto_implement
// run, applying customisations from Agent profiles when set:
//   - customTemplate non-empty: replaces the entire default template with
//     placeholder substitution ({repo}, {number}, {title}, {author}, {labels},
//     {body}, {comments}, {assignees}). The custom template is responsible
//     for preserving whatever safety rules + escape-hatch the caller cares
//     about — the pipeline relies on `git status` to detect a no-op run,
//     so the agent MUST still be able to leave the tree untouched when it
//     cannot implement the issue.
//   - customInstructions non-empty: injects the text into the default
//     template between the safety rules and the escape hatch so the rules
//     still apply when the maintainer only wants to nudge style / tooling.
//
// When both are empty, falls back to the built-in default template.
func BuildImplementPromptWithProfile(ctx PromptContext, customTemplate, customInstructions string) string {
	if customTemplate != "" {
		if customInstructions != "" {
			slog.Debug("implement prompt: custom template set, discarding customInstructions",
				"repo", ctx.Repo, "issue", ctx.Number)
		}
		return applyPlaceholders(customTemplate, ctx)
	}
	return buildDefaultImplementPrompt(ctx, customInstructions)
}

// BuildImplementPrompt is the zero-config entry point — no agent profile, no
// custom instructions. Equivalent to BuildImplementPromptWithProfile(ctx, "", "").
func BuildImplementPrompt(ctx PromptContext) string {
	return buildDefaultImplementPrompt(ctx, "")
}

// BuildRefinementPrompt formats the prompt for the deep unattended
// investigation stage. It deliberately has no custom-template variant yet:
// the output schema is a contract consumed by downstream auto_implement.
func BuildRefinementPrompt(ctx PromptContext) string {
	var sb strings.Builder

	sb.WriteString("You are Heimdallm, a senior engineering agent performing deep unattended refinement for a GitHub issue.\n")
	sb.WriteString("You have READ access to the repository in the current working directory. Investigate the codebase before answering.\n")
	sb.WriteString("Your job is to turn the issue into a concrete, auditable implementation plan for a later developer or agent.\n")
	sb.WriteString("Do not modify files. Do not ask the user questions. Resolve ambiguity from code, docs, tests, and git history when possible.\n")
	sb.WriteString("Only list open_questions for information you actively looked for and could not determine from the repository.\n\n")

	sb.WriteString(fmt.Sprintf("Repository: %s\n", ctx.Repo))
	sb.WriteString(fmt.Sprintf("Issue: #%d — %s\n", ctx.Number, ctx.Title))
	sb.WriteString(fmt.Sprintf("Author: @%s\n", ctx.Author))
	if len(ctx.Labels) > 0 {
		sb.WriteString("Labels: " + strings.Join(ctx.Labels, ", ") + "\n")
	}
	if len(ctx.Assignees) > 0 {
		sb.WriteString("Assignees: " + strings.Join(ctx.Assignees, ", ") + "\n")
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

	if ctx.TriageContext != "" {
		sb.WriteString(ctx.TriageContext)
		sb.WriteString("\n")
	}

	sb.WriteString("Investigate these sources before producing the plan:\n")
	sb.WriteString("- Relevant source files, tests, package/module boundaries, and local architecture.\n")
	sb.WriteString("- README/docs/configuration files when they explain expected behavior.\n")
	sb.WriteString("- Git history for likely affected paths when ownership or intent is unclear.\n")
	sb.WriteString("- Existing patterns in nearby code; prefer extending them over inventing new abstractions.\n\n")

	sb.WriteString("Return a single JSON object, and nothing else, with this exact shape:\n")
	sb.WriteString("{\n")
	sb.WriteString(`  "analysis_summary": "3-6 sentence technical summary of what must change and why",` + "\n")
	sb.WriteString(`  "affected_areas": [` + "\n")
	sb.WriteString(`    {"path": "relative/path.go", "symbols": ["FunctionName"], "reason": "why this area matters"}` + "\n")
	sb.WriteString("  ],\n")
	sb.WriteString(`  "subtasks": [` + "\n")
	sb.WriteString(`    {"id": "task-1", "description": "concrete task", "affected_files": ["relative/path.go"], "symbols": ["FunctionName"], "expected_change": "specific expected change", "complexity": "low|medium|high", "dependencies": []}` + "\n")
	sb.WriteString("  ],\n")
	sb.WriteString(`  "implementation_order": ["task-1"],` + "\n")
	sb.WriteString(`  "assumptions": ["assumption grounded in code evidence"],` + "\n")
	sb.WriteString(`  "open_questions": ["question only if repository evidence was insufficient"],` + "\n")
	sb.WriteString(`  "risks": ["risk area and why"],` + "\n")
	sb.WriteString(`  "test_plan": ["specific test or verification step"]` + "\n")
	sb.WriteString("}\n")
	sb.WriteString("Every subtask id must be stable, unique, and referenced by dependencies/implementation_order when needed. Do not include files outside this repository as subtasks; mention cross-repo needs as open_questions.\n")
	sb.WriteString("Do not wrap the JSON in prose or code fences.\n")

	return sb.String()
}

func buildDefaultImplementPrompt(ctx PromptContext, customInstructions string) string {
	var sb strings.Builder

	sb.WriteString("You are Heimdallm, an engineering agent implementing a GitHub issue.\n")
	sb.WriteString("You have WRITE access to the working directory, which is a checkout of the repository.\n\n")

	sb.WriteString(fmt.Sprintf("Repository: %s\n", ctx.Repo))
	sb.WriteString(fmt.Sprintf("Issue: #%d — %s\n", ctx.Number, ctx.Title))
	sb.WriteString(fmt.Sprintf("Author: @%s\n", ctx.Author))
	if len(ctx.Labels) > 0 {
		sb.WriteString("Labels: " + strings.Join(ctx.Labels, ", ") + "\n")
	}
	if len(ctx.Assignees) > 0 {
		sb.WriteString("Assignees: " + strings.Join(ctx.Assignees, ", ") + "\n")
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

	if ctx.TriageContext != "" {
		sb.WriteString(ctx.TriageContext)
		sb.WriteString("\n")
	}

	sb.WriteString("Implement what the issue asks for. Write real, working code.\n")
	sb.WriteString("Rules:\n")
	sb.WriteString("- Keep the change minimal and focused on the issue.\n")
	sb.WriteString("- Follow the existing code style; do not reformat unrelated files.\n")
	sb.WriteString("- If tests exist for the area you are changing, extend them.\n")
	sb.WriteString("- Do not commit secrets, credentials, or files outside the repository.\n")

	if customInstructions != "" {
		sb.WriteString("\nAdditional implementation instructions from the repository maintainer:\n")
		sb.WriteString(strings.TrimSpace(customInstructions))
		sb.WriteString("\n")
	}

	sb.WriteString("\n")
	sb.WriteString("- Leave the working tree in the final state you want committed — the outer pipeline will run `git add -A && git commit` over whatever you change.\n")
	sb.WriteString("- If you cannot implement the issue (insufficient information, risky change, requires a human decision), leave the tree untouched. A review-only comment will be posted instead.\n")

	return sb.String()
}

// maxDiffBytesForDescription caps the diff sent to the LLM for PR description
// generation. Matches the PR review prompt limit (32 KB).
const maxDiffBytesForDescription = 32 * 1024

// BuildPRDescriptionPrompt builds the prompt for the second LLM call that
// generates a rich PR title and description from the implementation diff.
// The LLM must respond with a JSON object: {"title": "...", "body": "..."}.
func BuildPRDescriptionPrompt(issueNumber int, issueTitle, diff string) string {
	var sb strings.Builder

	sb.WriteString("You just implemented changes for a GitHub issue.\n\n")
	sb.WriteString(fmt.Sprintf("Issue #%d: %s\n\n", issueNumber, issueTitle))

	if len(diff) > maxDiffBytesForDescription {
		diff = diff[:maxDiffBytesForDescription] + "\n... (diff truncated)"
	}

	sb.WriteString("Here is the diff of what you implemented:\n")
	sb.WriteString("<diff>\n")
	sb.WriteString(diff)
	sb.WriteString("\n</diff>\n\n")

	sb.WriteString("Write a pull request title and description.\n\n")
	sb.WriteString("Guidelines:\n")
	sb.WriteString("- Title: concise, conventional commit style (e.g. 'feat: add CLI client with Bubbletea TUI dashboard')\n")
	sb.WriteString("- Summary: 2-3 sentences explaining what was implemented and why\n")
	sb.WriteString("- Changes: bullet list of key files and their purpose\n")
	sb.WriteString("- Test plan: how to verify the changes work\n")
	sb.WriteString("- Notes: caveats or follow-ups, if any\n\n")
	sb.WriteString("Return a single JSON object, and nothing else:\n")
	sb.WriteString("{\n")
	sb.WriteString(`  "title": "concise conventional commit title",` + "\n")
	sb.WriteString(`  "body": "full PR description in Markdown"` + "\n")
	sb.WriteString("}\n")
	sb.WriteString("Do not wrap the JSON in prose or code fences.\n")

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

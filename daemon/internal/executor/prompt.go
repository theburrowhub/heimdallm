package executor

import (
	"fmt"
	"strings"
)

const maxDiffBytes = 32 * 1024 // 32KB ~ 8k tokens

// PRContext holds all substitutable data for a prompt template.
type PRContext struct {
	Title                string
	Number               int
	Repo                 string
	Author               string
	Link                 string
	Diff                 string
	Comments             string // pre-formatted discussion section; empty string if no comments
	ReviewContext        string // structured re-review context; empty on first review
	StandingInstructions string // persistent per-repo instructions; empty when none
}

// defaultTemplate is used when no custom agent template is configured.
//
// Security note — prompt injection risk:
// The title, author, and diff come from untrusted GitHub PR data. A malicious PR
// author could craft a title or diff body containing LLM instructions intended to
// override the system prompt. The <user_content>…</user_content> delimiters below
// signal to the model that the enclosed content is untrusted user data and should
// be treated as data, not instructions. This mitigation reduces (but cannot fully
// eliminate) the risk of prompt injection in open-ended LLM interactions.
const defaultTemplate = `You are a senior software engineer performing a pull request code review.

PR: {title} (#{number})
Repo: {repo}
Author: {author}
Link: {link}

{standing_instructions}
<user_content>
Diff:
{diff}
</user_content>

{review_context}
{comments}

Review the above diff and respond with ONLY valid JSON in this exact format (no markdown, no explanation):
{
  "summary": "brief overall assessment",
  "issues": [
    {"file": "filename", "line": 0, "description": "issue description", "severity": "low|medium|high"}
  ],
  "severity": "low|medium|high"
}

Rules for severity determination:
- The top-level "severity" MUST be at least the highest severity of any individual issue.
- If the PR discussion contains unresolved concerns from reviewers (especially about correctness, security, or design), factor them into your severity assessment. An unresolved reviewer concern about a real defect warrants at least "medium".
- If reviewers have explicitly flagged something as a blocker or requested changes that were not addressed in the current diff, this is "high" severity.
- If no issues exist and no unresolved concerns remain, return empty arrays and severity "low".`

// DefaultTemplate returns the built-in prompt template.
func DefaultTemplate() string { return defaultTemplate }

// DefaultTemplateWithInstructions injects custom review instructions into the
// default template. The instructions define what to focus on (e.g. security,
// performance) while the output format stays consistent.
func DefaultTemplateWithInstructions(instructions string) string {
	return `You are a senior software engineer performing a pull request code review.

PR: {title} (#{number})
Repo: {repo}
Author: {author}
Link: {link}

{standing_instructions}
REVIEW FOCUS:
` + instructions + `

<user_content>
Diff:
{diff}
</user_content>

{review_context}
{comments}

Review the diff according to the focus above and respond with ONLY valid JSON (no markdown, no explanation):
{
  "summary": "brief overall assessment",
  "issues": [
    {"file": "filename", "line": 0, "description": "issue description", "severity": "low|medium|high"}
  ],
  "severity": "low|medium|high"
}

Rules for severity determination:
- The top-level "severity" MUST be at least the highest severity of any individual issue.
- If the PR discussion contains unresolved concerns from reviewers (especially about correctness, security, or design), factor them into your severity assessment. An unresolved reviewer concern about a real defect warrants at least "medium".
- If reviewers have explicitly flagged something as a blocker or requested changes that were not addressed in the current diff, this is "high" severity.
- If no issues exist and no unresolved concerns remain, return empty arrays and severity "low".`
}

// compactRules is the answer contract shared by the compact templates: the
// same JSON shape and severity rules as the default template, worded tersely.
const compactRules = `Answer with ONLY this JSON (no markdown):
{"summary":"...","issues":[{"file":"path","line":0,"description":"...","severity":"low|medium|high"}],"severity":"low|medium|high"}
Top-level severity >= the highest issue severity. An unresolved reviewer concern about a real defect is at least "medium"; an unaddressed blocker or requested change is "high". No issues and no open concerns: empty arrays, "low". Report real defects only, not style.`

// compactTemplate is the token-saving variant of defaultTemplate (the
// compact_prompt measure). It keeps the untrusted-content delimiters and the
// answer contract and drops everything the model does not need: the PR link
// and the restated instructions.
const compactTemplate = `Review this pull request as a senior engineer.
PR: {title} (#{number}) in {repo} by {author}
{standing_instructions}
<user_content>
Diff:
{diff}
</user_content>
{review_context}
{comments}
` + compactRules

// CompactTemplate returns the token-saving built-in template.
func CompactTemplate() string { return compactTemplate }

// CompactTemplateWithInstructions is the compact counterpart of
// DefaultTemplateWithInstructions.
func CompactTemplateWithInstructions(instructions string) string {
	return `Review this pull request as a senior engineer.
PR: {title} (#{number}) in {repo} by {author}
{standing_instructions}
FOCUS:
` + instructions + `
<user_content>
Diff:
{diff}
</user_content>
{review_context}
{comments}
` + compactRules
}

// BuildPrompt builds a prompt from the default template.
// Kept for backwards compatibility.
func BuildPrompt(title, author, diff string) string {
	return BuildPromptFromTemplate(defaultTemplate, PRContext{
		Title:  title,
		Author: author,
		Diff:   diff,
	})
}

// BuildPromptFromTemplate substitutes placeholders in a template.
// Supported placeholders: {title} {number} {repo} {author} {link} {diff} {comments} {review_context} {standing_instructions}
//
// Behavior for {comments}:
//
//	A) If the template contains {comments}: substituted directly (empty string if no comments).
//	B) If the template does NOT contain {comments} and Comments is non-empty: the comments
//	   block is appended at the very end of the rendered prompt.
func BuildPromptFromTemplate(tmpl string, ctx PRContext) string {
	if len(ctx.Diff) > maxDiffBytes {
		ctx.Diff = ctx.Diff[:maxDiffBytes] + "\n... (diff truncated)"
	}

	hasPlaceholder := strings.Contains(tmpl, "{comments}")
	hasReviewCtx := strings.Contains(tmpl, "{review_context}")
	hasStanding := strings.Contains(tmpl, "{standing_instructions}")

	r := strings.NewReplacer(
		"{title}", ctx.Title,
		"{number}", fmt.Sprintf("%d", ctx.Number),
		"{repo}", ctx.Repo,
		"{author}", ctx.Author,
		"{link}", ctx.Link,
		"{diff}", ctx.Diff,
		"{comments}", ctx.Comments,
		"{review_context}", ctx.ReviewContext,
		"{standing_instructions}", ctx.StandingInstructions,
	)
	result := r.Replace(tmpl)

	// Collapse triple+ newlines caused by an empty {comments} placeholder
	for strings.Contains(result, "\n\n\n") {
		result = strings.ReplaceAll(result, "\n\n\n", "\n\n")
	}

	// B: append comments if the template had no {comments} placeholder
	if !hasPlaceholder && ctx.Comments != "" {
		result += "\n\n" + ctx.Comments
	}

	// Prepend review context if template had no {review_context} placeholder
	if !hasReviewCtx && ctx.ReviewContext != "" {
		result = ctx.ReviewContext + "\n" + result
	}

	// Prepend standing instructions if the template had no placeholder.
	if !hasStanding && ctx.StandingInstructions != "" {
		result = ctx.StandingInstructions + "\n" + result
	}

	return result
}

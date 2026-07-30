package issues

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/executor"
	"github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/heimdallm/daemon/internal/store"
)

const maxRefinementCommentBytes = 60 * 1000

func (p *Pipeline) runRefinement(ctx context.Context, issue *github.Issue, issueID int64, workDir string, opts RunOptions) (*store.IssueReview, error) {
	_ = ctx // reserved for executor/gh cancellation when those deps accept one

	latestRefinement, latestErr := p.store.LatestIssueReviewByAction(issueID, string(config.IssueModeRefinement))
	if latestErr != nil && !errorsIsNoRows(latestErr) {
		slog.Warn("issues pipeline: latest refinement lookup failed, proceeding",
			"repo", issue.Repo, "number", issue.Number, "err", latestErr)
	}
	if latestRefinement != nil && !opts.Force && refinementIsFresh(issue, latestRefinement) {
		slog.Info("issues pipeline: refinement already fresh, skipping",
			"repo", issue.Repo, "number", issue.Number)
		return nil, nil
	}

	p.publish(sse.EventIssueReviewStarted, map[string]any{
		"issue_id": issueID, "number": issue.Number, "repo": issue.Repo, "mode": "refinement",
	})
	if p.notify != nil {
		p.notify.Notify("Issue Refinement Started", fmt.Sprintf("%s #%d", issue.Repo, issue.Number))
	}

	comments, err := p.gh.FetchIssueCommentsOnly(issue.Repo, issue.Number)
	if err != nil {
		slog.Warn("issues pipeline: failed to fetch comments for refinement, proceeding without",
			"repo", issue.Repo, "number", issue.Number, "err", err)
		comments = nil
	}

	var humanComments []github.Comment
	for _, c := range comments {
		if p.botLogin != "" && strings.EqualFold(c.Author, p.botLogin) {
			continue
		}
		humanComments = append(humanComments, c)
	}

	prevContext := ""
	prevReview, _ := p.store.LatestIssueReview(issueID)
	if prevReview != nil {
		prevContext = buildIssueRunContext(prevReview, comments, p.botLogin)
	}

	prompt := BuildRefinementPrompt(PromptContext{
		Repo:          issue.Repo,
		Number:        issue.Number,
		Title:         issue.Title,
		Author:        issue.User.Login,
		Labels:        issue.LabelNames(),
		Assignees:     issue.AssigneeLogins(),
		Body:          issue.Body,
		Comments:      humanComments,
		HasLocalDir:   workDir != "",
		TriageContext: prevContext,
	})

	cli, err := p.executor.Detect(opts.Primary, opts.Fallback)
	if err != nil {
		p.publishError(issueID, issue, fmt.Errorf("detect CLI: %w", err))
		return nil, fmt.Errorf("issues pipeline: detect CLI: %w", err)
	}
	execOpts := executor.OptionsForSelectedCLI(opts.Primary, cli, opts.ExecOpts)
	raw, err := p.executor.ExecuteRaw(cli, prompt, execOpts)
	if err != nil {
		p.publishError(issueID, issue, fmt.Errorf("execute %s: %w", cli, err))
		return nil, fmt.Errorf("issues pipeline: execute %s: %w", cli, err)
	}
	result, err := parseRefinementResult(raw)
	if err != nil {
		p.publishError(issueID, issue, err)
		return nil, fmt.Errorf("issues pipeline: parse refinement result: %w", err)
	}

	refinementJSON, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("issues pipeline: marshal refinement: %w", err)
	}
	body, truncated := BuildRefinementMarkdownComment(result)
	if truncated {
		slog.Warn("issues pipeline: refinement comment truncated",
			"repo", issue.Repo, "number", issue.Number, "limit", maxRefinementCommentBytes)
	}
	commentedAt, postErr := p.gh.PostComment(issue.Repo, issue.Number, body)
	if postErr != nil {
		slog.Warn("issues pipeline: refinement PostComment failed, review will be stored locally only",
			"repo", issue.Repo, "number", issue.Number, "err", postErr)
	}

	rev := &store.IssueReview{
		IssueID:        issueID,
		CLIUsed:        cli,
		Summary:        result.AnalysisSummary,
		Triage:         "{}",
		RefinementData: string(refinementJSON),
		NextSteps:      "[]",
		ActionTaken:    string(config.IssueModeRefinement),
		CreatedAt:      time.Now().UTC(),
		CommentedAt:    commentedAt,
	}
	revID, err := p.store.InsertIssueReview(rev)
	if err != nil {
		return nil, fmt.Errorf("issues pipeline: store refinement: %w", err)
	}
	rev.ID = revID

	// issue_number is the canonical activity-log key; number remains as a
	// legacy SSE alias for older UI/session consumers during the transition.
	p.publish(sse.EventIssueRefinementDone, map[string]any{
		"issue_id": issueID, "number": issue.Number, "issue_number": issue.Number, "repo": issue.Repo,
		"issue_title": issue.Title, "cli_used": cli, "review_id": revID,
		"post_ok": postErr == nil, "truncated": truncated,
	})
	if p.notify != nil {
		p.notify.Notify("Issue Refinement Complete", fmt.Sprintf("%s #%d", issue.Repo, issue.Number))
	}
	slog.Info("issues pipeline: refinement complete",
		"repo", issue.Repo, "number", issue.Number, "posted", postErr == nil)
	return rev, nil
}

func errorsIsNoRows(err error) bool {
	return err == nil || errors.Is(err, sql.ErrNoRows)
}

func refinementIsFresh(issue *github.Issue, latest *store.IssueReview) bool {
	if latest == nil {
		return false
	}
	ref := latest.CommentedAt
	if ref.IsZero() {
		ref = latest.CreatedAt
	}
	if issue.UpdatedAt.IsZero() {
		return true
	}
	return !issue.UpdatedAt.After(ref.Add(RecomputeGrace))
}

func parseRefinementResult(data []byte) (*RefinementResult, error) {
	clean := executor.StripToJSON(data)
	var r RefinementResult
	if err := json.Unmarshal(clean, &r); err != nil {
		return nil, fmt.Errorf("issues pipeline: parse refinement JSON: %w (raw: %.200s)", err, clean)
	}
	if err := sanitizeAndValidateRefinement(&r); err != nil {
		return nil, err
	}
	return &r, nil
}

func sanitizeAndValidateRefinement(r *RefinementResult) error {
	r.AnalysisSummary = strings.TrimSpace(r.AnalysisSummary)
	if r.AnalysisSummary == "" {
		return fmt.Errorf("refinement result missing analysis_summary")
	}
	if len(r.Subtasks) == 0 {
		return fmt.Errorf("refinement result missing subtasks")
	}

	areas := make([]RefinementArea, 0, len(r.AffectedAreas))
	for _, area := range r.AffectedAreas {
		paths := sanitizeAffectedPaths([]string{area.Path})
		if len(paths) == 0 {
			continue
		}
		area.Path = paths[0]
		area.Symbols = cleanStringList(area.Symbols, 20)
		area.Reason = strings.TrimSpace(area.Reason)
		areas = append(areas, area)
	}
	r.AffectedAreas = areas

	ids := make(map[string]struct{}, len(r.Subtasks))
	for i := range r.Subtasks {
		st := &r.Subtasks[i]
		st.ID = strings.TrimSpace(st.ID)
		if st.ID == "" {
			return fmt.Errorf("refinement subtask at index %d missing id", i)
		}
		if _, exists := ids[st.ID]; exists {
			return fmt.Errorf("refinement subtask id %q is duplicated", st.ID)
		}
		ids[st.ID] = struct{}{}
		st.Description = strings.TrimSpace(st.Description)
		if st.Description == "" {
			return fmt.Errorf("refinement subtask %q missing description", st.ID)
		}
		st.AffectedFiles = sanitizeAffectedPaths(st.AffectedFiles)
		st.Symbols = cleanStringList(st.Symbols, 30)
		st.ExpectedChange = strings.TrimSpace(st.ExpectedChange)
		st.Complexity = normalizeComplexity(st.Complexity)
		st.Dependencies = cleanStringList(st.Dependencies, 30)
	}
	for _, st := range r.Subtasks {
		for _, dep := range st.Dependencies {
			if dep == st.ID {
				return fmt.Errorf("refinement subtask %q depends on itself", st.ID)
			}
			if _, ok := ids[dep]; !ok {
				return fmt.Errorf("refinement subtask %q depends on unknown id %q", st.ID, dep)
			}
		}
	}

	r.ImplementationOrder = cleanStringList(r.ImplementationOrder, len(r.Subtasks))
	if len(r.ImplementationOrder) == 0 {
		r.ImplementationOrder = make([]string, 0, len(r.Subtasks))
		for _, st := range r.Subtasks {
			r.ImplementationOrder = append(r.ImplementationOrder, st.ID)
		}
	} else {
		ordered := make(map[string]struct{}, len(r.ImplementationOrder))
		for _, id := range r.ImplementationOrder {
			ordered[id] = struct{}{}
		}
		for _, st := range r.Subtasks {
			if _, ok := ordered[st.ID]; !ok {
				r.ImplementationOrder = append(r.ImplementationOrder, st.ID)
			}
		}
	}
	if err := validateImplementationOrder(r); err != nil {
		return err
	}
	r.Assumptions = cleanStringList(r.Assumptions, 50)
	r.OpenQuestions = cleanStringList(r.OpenQuestions, 50)
	r.Risks = cleanStringList(r.Risks, 50)
	r.TestPlan = cleanStringList(r.TestPlan, 50)
	return nil
}

func validateImplementationOrder(r *RefinementResult) error {
	pos := make(map[string]int, len(r.ImplementationOrder))
	for i, id := range r.ImplementationOrder {
		pos[id] = i
	}
	for _, id := range r.ImplementationOrder {
		found := false
		for _, st := range r.Subtasks {
			if st.ID == id {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("implementation_order references unknown id %q", id)
		}
	}
	for _, st := range r.Subtasks {
		stPos, ok := pos[st.ID]
		if !ok {
			continue
		}
		for _, dep := range st.Dependencies {
			depPos, ok := pos[dep]
			if ok && depPos > stPos {
				return fmt.Errorf("implementation_order places %q before dependency %q", st.ID, dep)
			}
		}
	}
	return nil
}

func normalizeComplexity(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "low", "medium", "high":
		return strings.ToLower(strings.TrimSpace(s))
	default:
		return "medium"
	}
}

func cleanStringList(in []string, max int) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
		if max > 0 && len(out) >= max {
			break
		}
	}
	return out
}

func BuildRefinementMarkdownComment(r *RefinementResult) (string, bool) {
	body := renderRefinementMarkdown(r, false)
	if len(body) <= maxRefinementCommentBytes {
		return body, false
	}
	body = renderRefinementMarkdown(r, true)
	if len(body) <= maxRefinementCommentBytes {
		return body, true
	}
	note := "\n\n> Truncated to fit GitHub's comment size limit. The complete JSON refinement is stored locally in `refinement_data`.\n"
	limit := maxRefinementCommentBytes - len(note)
	if limit < 0 {
		limit = maxRefinementCommentBytes
		note = ""
	}
	return body[:limit] + note, true
}

func renderRefinementMarkdown(r *RefinementResult, compact bool) string {
	var sb strings.Builder
	sb.WriteString("## Heimdallm refinement\n\n")
	if r.AnalysisSummary != "" {
		sb.WriteString(r.AnalysisSummary)
		sb.WriteString("\n\n")
	}
	writeAreas(&sb, r.AffectedAreas, compact)
	writeSubtasks(&sb, r.Subtasks, compact)
	writeStringSection(&sb, "Implementation order", r.ImplementationOrder, compact, 40)
	writeStringSection(&sb, "Assumptions", r.Assumptions, compact, 10)
	writeStringSection(&sb, "Open questions", r.OpenQuestions, compact, 10)
	writeStringSection(&sb, "Risks", r.Risks, compact, 10)
	writeStringSection(&sb, "Test plan", r.TestPlan, compact, 10)
	if compact {
		sb.WriteString("> Some sections were shortened to fit GitHub's comment size limit. The complete JSON refinement is stored locally in `refinement_data`.\n\n")
	}
	sb.WriteString("---\n*refinement mode · reviewed by Heimdallm*")
	return sb.String()
}

func writeAreas(sb *strings.Builder, areas []RefinementArea, compact bool) {
	if len(areas) == 0 {
		return
	}
	sb.WriteString("### Affected areas\n\n")
	limit := len(areas)
	if compact && limit > 20 {
		limit = 20
	}
	for _, area := range areas[:limit] {
		sb.WriteString(fmt.Sprintf("- `%s`", area.Path))
		if len(area.Symbols) > 0 {
			sb.WriteString(fmt.Sprintf(" (%s)", strings.Join(area.Symbols, ", ")))
		}
		if area.Reason != "" {
			sb.WriteString(": " + maybeShorten(area.Reason, compact, 400))
		}
		sb.WriteString("\n")
	}
	writeTruncatedCount(sb, len(areas), limit)
	sb.WriteString("\n")
}

func writeSubtasks(sb *strings.Builder, tasks []RefinementSubtask, compact bool) {
	if len(tasks) == 0 {
		return
	}
	sb.WriteString("### Subtasks\n\n")
	limit := len(tasks)
	if compact && limit > 40 {
		limit = 40
	}
	for _, st := range tasks[:limit] {
		sb.WriteString(fmt.Sprintf("- **%s** (%s): %s\n", st.ID, st.Complexity, maybeShorten(st.Description, compact, 500)))
		if len(st.AffectedFiles) > 0 {
			sb.WriteString(fmt.Sprintf("  - Files: `%s`\n", strings.Join(st.AffectedFiles, "`, `")))
		}
		if len(st.Symbols) > 0 {
			sb.WriteString(fmt.Sprintf("  - Symbols: %s\n", strings.Join(st.Symbols, ", ")))
		}
		if st.ExpectedChange != "" {
			sb.WriteString(fmt.Sprintf("  - Expected change: %s\n", maybeShorten(st.ExpectedChange, compact, 500)))
		}
		if len(st.Dependencies) > 0 {
			sb.WriteString(fmt.Sprintf("  - Depends on: %s\n", strings.Join(st.Dependencies, ", ")))
		}
	}
	writeTruncatedCount(sb, len(tasks), limit)
	sb.WriteString("\n")
}

func writeStringSection(sb *strings.Builder, title string, items []string, compact bool, compactLimit int) {
	if len(items) == 0 {
		return
	}
	sb.WriteString("### " + title + "\n\n")
	limit := len(items)
	if compact && limit > compactLimit {
		limit = compactLimit
	}
	for _, item := range items[:limit] {
		sb.WriteString("- " + maybeShorten(item, compact, 500) + "\n")
	}
	writeTruncatedCount(sb, len(items), limit)
	sb.WriteString("\n")
}

func writeTruncatedCount(sb *strings.Builder, total, shown int) {
	if shown < total {
		sb.WriteString(fmt.Sprintf("- ... %d more omitted from the GitHub comment\n", total-shown))
	}
}

func maybeShorten(s string, compact bool, limit int) string {
	s = strings.TrimSpace(s)
	if !compact || len(s) <= limit {
		return s
	}
	if limit < 20 {
		return s[:limit]
	}
	return s[:limit-15] + "... (truncated)"
}

func buildIssueRunContext(prev *store.IssueReview, comments []github.Comment, botLogin string) string {
	if prev == nil {
		return ""
	}
	if prev.ActionTaken == string(config.IssueModeRefinement) && prev.RefinementData != "" {
		return buildRefinementContext(prev.RefinementData, prev.Summary, prev.CreatedAt)
	}
	return buildTriageContext(
		prev.Triage,
		prev.NextSteps,
		prev.Summary,
		extractSeverity(prev.Triage),
		prev.CreatedAt,
		comments,
		botLogin,
	)
}

func buildRefinementContext(refinementJSON, summary string, createdAt time.Time) string {
	if refinementJSON == "" {
		return ""
	}
	var r RefinementResult
	if err := json.Unmarshal([]byte(refinementJSON), &r); err != nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Previous refinement plan\n\n")
	if !createdAt.IsZero() {
		b.WriteString(fmt.Sprintf("Created at: %s\n\n", createdAt.UTC().Format(time.RFC3339)))
	}
	if r.AnalysisSummary != "" {
		b.WriteString("Summary: " + r.AnalysisSummary + "\n\n")
	} else if summary != "" {
		b.WriteString("Summary: " + summary + "\n\n")
	}
	if len(r.Subtasks) > 0 {
		b.WriteString("Subtasks:\n")
		for _, st := range r.Subtasks {
			b.WriteString(fmt.Sprintf("- %s: %s\n", st.ID, st.Description))
			if len(st.AffectedFiles) > 0 {
				b.WriteString("  - Affected files: " + strings.Join(st.AffectedFiles, ", ") + "\n")
			}
			if len(st.Symbols) > 0 {
				b.WriteString("  - Symbols: " + strings.Join(st.Symbols, ", ") + "\n")
			}
			if st.ExpectedChange != "" {
				b.WriteString("  - Expected change: " + st.ExpectedChange + "\n")
			}
			if st.Complexity != "" {
				b.WriteString("  - Complexity: " + st.Complexity + "\n")
			}
			if len(st.Dependencies) > 0 {
				b.WriteString("  - Depends on: " + strings.Join(st.Dependencies, ", ") + "\n")
			}
		}
		b.WriteString("\n")
	}
	if len(r.ImplementationOrder) > 0 {
		b.WriteString("Implementation order: " + strings.Join(r.ImplementationOrder, ", ") + "\n\n")
	}
	if len(r.Assumptions) > 0 {
		b.WriteString("Assumptions:\n")
		for _, assumption := range r.Assumptions {
			b.WriteString("- " + assumption + "\n")
		}
		b.WriteString("\n")
	}
	if len(r.OpenQuestions) > 0 {
		b.WriteString("Open questions:\n")
		for _, question := range r.OpenQuestions {
			b.WriteString("- " + question + "\n")
		}
		b.WriteString("\n")
	}
	if len(r.Risks) > 0 {
		b.WriteString("Risks:\n")
		for _, risk := range r.Risks {
			b.WriteString("- " + risk + "\n")
		}
		b.WriteString("\n")
	}
	if len(r.TestPlan) > 0 {
		b.WriteString("Test plan:\n")
		for _, step := range r.TestPlan {
			b.WriteString("- " + step + "\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

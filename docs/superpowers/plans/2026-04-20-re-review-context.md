# Re-review Cycle Context Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When re-reviewing a PR, provide the LLM with structured context about previous review findings and author responses so it does not repeat false positives that were already discussed and resolved.

**Architecture:** The pipeline already fetches comments and the store has the previous review content. We add a comment partitioning function that splits comments into "before last review" and "after last review", format previous findings as structured context, and inject a re-review instruction into the prompt. No changes to the GitHub client or store — only pipeline and prompt logic.

**Tech Stack:** Go 1.21, existing pipeline/executor/store packages

---

## File Map

### Files to modify
- `daemon/internal/pipeline/pipeline.go` — query previous review, partition comments, build review context
- `daemon/internal/executor/prompt.go` — add `ReviewContext` field to `PRContext`, inject re-review instruction

### Files to create
- `daemon/internal/pipeline/review_context.go` — comment partitioning + review context formatting (keep pipeline.go focused)
- `daemon/internal/pipeline/review_context_test.go` — tests for partitioning and formatting

---

## Task 1: Comment partitioning function

**Files:**
- Create: `daemon/internal/pipeline/review_context.go`
- Test: `daemon/internal/pipeline/review_context_test.go`

- [ ] **Step 1: Write the test for partitionComments**

```go
// review_context_test.go
package pipeline

import (
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/github"
)

func TestPartitionComments_SplitsByTimestamp(t *testing.T) {
	cutoff := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	comments := []github.Comment{
		{Author: "reviewer", Body: "old comment", CreatedAt: cutoff.Add(-1 * time.Hour)},
		{Author: "author", Body: "old response", CreatedAt: cutoff.Add(-30 * time.Minute)},
		{Author: "author", Body: "new push", CreatedAt: cutoff.Add(1 * time.Hour)},
		{Author: "other", Body: "new feedback", CreatedAt: cutoff.Add(2 * time.Hour)},
	}

	before, after := partitionComments(comments, cutoff)
	if len(before) != 2 {
		t.Errorf("before = %d, want 2", len(before))
	}
	if len(after) != 2 {
		t.Errorf("after = %d, want 2", len(after))
	}
}

func TestPartitionComments_AllBefore(t *testing.T) {
	cutoff := time.Now()
	comments := []github.Comment{
		{Author: "a", Body: "old", CreatedAt: cutoff.Add(-1 * time.Hour)},
	}
	before, after := partitionComments(comments, cutoff)
	if len(before) != 1 || len(after) != 0 {
		t.Errorf("before=%d after=%d, want 1/0", len(before), len(after))
	}
}

func TestPartitionComments_AllAfter(t *testing.T) {
	cutoff := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	comments := []github.Comment{
		{Author: "a", Body: "new", CreatedAt: time.Now()},
	}
	before, after := partitionComments(comments, cutoff)
	if len(before) != 0 || len(after) != 1 {
		t.Errorf("before=%d after=%d, want 0/1", len(before), len(after))
	}
}

func TestPartitionComments_Empty(t *testing.T) {
	before, after := partitionComments(nil, time.Now())
	if len(before) != 0 || len(after) != 0 {
		t.Error("expected empty slices for nil input")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/stejon/develop/heimdallr/daemon && go test ./internal/pipeline/ -run TestPartitionComments -v`
Expected: FAIL — `partitionComments` not defined

- [ ] **Step 3: Implement partitionComments**

```go
// review_context.go
package pipeline

import (
	"time"

	"github.com/heimdallm/daemon/internal/github"
)

// partitionComments splits comments into those created before and after the cutoff timestamp.
func partitionComments(comments []github.Comment, cutoff time.Time) (before, after []github.Comment) {
	for _, c := range comments {
		if c.CreatedAt.After(cutoff) {
			after = append(after, c)
		} else {
			before = append(before, c)
		}
	}
	return
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/stejon/develop/heimdallr/daemon && go test ./internal/pipeline/ -run TestPartitionComments -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /Users/stejon/develop/heimdallr
git add daemon/internal/pipeline/review_context.go daemon/internal/pipeline/review_context_test.go
git commit -m "feat(pipeline): add partitionComments to split comments by review cycle"
```

---

## Task 2: Format review context for re-reviews

**Files:**
- Modify: `daemon/internal/pipeline/review_context.go`
- Test: `daemon/internal/pipeline/review_context_test.go`

- [ ] **Step 1: Write the test for buildReviewContext**

```go
func TestBuildReviewContext_WithPreviousReview(t *testing.T) {
	prevIssues := `[{"file":"handler.go","line":42,"description":"Missing error handling","severity":"high"}]`
	lastReviewAt := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)

	comments := []github.Comment{
		{Author: "heimdallm-bot", Body: "Review comment", CreatedAt: lastReviewAt.Add(-1 * time.Minute)},
		{Author: "author", Body: "Fixed the error handling in latest commit", CreatedAt: lastReviewAt.Add(1 * time.Hour)},
		{Author: "other-reviewer", Body: "Looks good now", CreatedAt: lastReviewAt.Add(2 * time.Hour)},
	}

	ctx := buildReviewContext(prevIssues, "low", lastReviewAt, comments, "heimdallm-bot")

	if ctx == "" {
		t.Fatal("expected non-empty review context")
	}
	// Should contain re-review instruction
	if !strings.Contains(ctx, "RE-REVIEW") {
		t.Error("missing RE-REVIEW instruction")
	}
	// Should contain previous findings
	if !strings.Contains(ctx, "Missing error handling") {
		t.Error("missing previous finding")
	}
	// Should contain author response
	if !strings.Contains(ctx, "Fixed the error handling") {
		t.Error("missing author response")
	}
	// Should NOT contain bot's own review comment in the "since last review" section
	if strings.Contains(ctx, "Review comment\n") && strings.Contains(ctx, "Discussion since") {
		// This is checking that the bot comment is in "previous review" not "new discussion"
	}
}

func TestBuildReviewContext_NoPreviousReview(t *testing.T) {
	ctx := buildReviewContext("", "", time.Time{}, nil, "bot")
	if ctx != "" {
		t.Errorf("expected empty context for first review, got: %q", ctx)
	}
}

func TestBuildReviewContext_PreviousReviewNoNewComments(t *testing.T) {
	prevIssues := `[{"file":"main.go","line":10,"description":"Unused import","severity":"low"}]`
	lastReviewAt := time.Now()

	ctx := buildReviewContext(prevIssues, "low", lastReviewAt, nil, "bot")

	if !strings.Contains(ctx, "RE-REVIEW") {
		t.Error("missing RE-REVIEW instruction")
	}
	if !strings.Contains(ctx, "Unused import") {
		t.Error("missing previous finding")
	}
}
```

Add `"strings"` to imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/stejon/develop/heimdallr/daemon && go test ./internal/pipeline/ -run TestBuildReviewContext -v`
Expected: FAIL — `buildReviewContext` not defined

- [ ] **Step 3: Implement buildReviewContext**

Add to `review_context.go`:

```go
import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/heimdallm/daemon/internal/github"
)

// reviewIssue is the minimal struct for deserializing previous review issues from JSON.
type reviewIssue struct {
	File        string `json:"file"`
	Line        int    `json:"line"`
	Description string `json:"description"`
	Severity    string `json:"severity"`
}

// buildReviewContext creates a structured prompt section for re-reviews.
// Returns empty string for first-time reviews (no previous review exists).
func buildReviewContext(prevIssuesJSON, prevSeverity string, lastReviewAt time.Time, comments []github.Comment, botLogin string) string {
	if prevIssuesJSON == "" && lastReviewAt.IsZero() {
		return "" // first review — no context needed
	}

	var b strings.Builder

	// Re-review instruction
	b.WriteString("IMPORTANT: This is a RE-REVIEW. You previously reviewed this PR.\n")
	b.WriteString("Your previous findings and the discussion since then are shown below.\n")
	b.WriteString("- Do NOT repeat findings that the author has addressed (check the diff for changes)\n")
	b.WriteString("- Only re-flag a finding if the code is STILL unchanged despite the feedback\n")
	b.WriteString("- Focus on NEW changes since the last review\n\n")

	// Previous findings
	var issues []reviewIssue
	if prevIssuesJSON != "" {
		json.Unmarshal([]byte(prevIssuesJSON), &issues)
	}

	if len(issues) > 0 {
		b.WriteString(fmt.Sprintf("## Your previous review (severity: %s)\n\n", prevSeverity))
		b.WriteString("Previous findings:\n")
		for _, iss := range issues {
			b.WriteString(fmt.Sprintf("- [%s] %s:%d — %s\n", strings.ToUpper(iss.Severity), iss.File, iss.Line, iss.Description))
		}
		b.WriteString("\n")
	}

	// Discussion since last review
	_, afterComments := partitionComments(comments, lastReviewAt)

	// Filter out bot's own comments from "new discussion" — they're already in "previous findings"
	var newDiscussion []github.Comment
	for _, c := range afterComments {
		if !strings.EqualFold(c.Author, botLogin) {
			newDiscussion = append(newDiscussion, c)
		}
	}

	if len(newDiscussion) > 0 {
		b.WriteString("## Discussion since last review\n\n")
		for _, c := range newDiscussion {
			if c.File != "" {
				b.WriteString(fmt.Sprintf("@%s (%s:%d): %s\n", c.Author, c.File, c.Line, c.Body))
			} else {
				b.WriteString(fmt.Sprintf("@%s: %s\n", c.Author, c.Body))
			}
		}
		b.WriteString("\n")
	}

	return b.String()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/stejon/develop/heimdallr/daemon && go test ./internal/pipeline/ -run TestBuildReviewContext -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /Users/stejon/develop/heimdallr
git add daemon/internal/pipeline/review_context.go daemon/internal/pipeline/review_context_test.go
git commit -m "feat(pipeline): add buildReviewContext for structured re-review prompts"
```

---

## Task 3: Add ReviewContext to PRContext and update prompt template

**Files:**
- Modify: `daemon/internal/executor/prompt.go`

- [ ] **Step 1: Add ReviewContext field to PRContext**

In `prompt.go`, add to the `PRContext` struct:

```go
type PRContext struct {
	Title         string
	Number        int
	Repo          string
	Author        string
	Link          string
	Diff          string
	Comments      string // pre-formatted discussion section
	ReviewContext string // structured re-review context (empty on first review)
}
```

- [ ] **Step 2: Add {review_context} placeholder to BuildPromptFromTemplate**

In `BuildPromptFromTemplate()`, add `{review_context}` to the replacer:

```go
r := strings.NewReplacer(
	"{title}", ctx.Title,
	"{number}", fmt.Sprintf("%d", ctx.Number),
	"{repo}", ctx.Repo,
	"{author}", ctx.Author,
	"{link}", ctx.Link,
	"{diff}", ctx.Diff,
	"{comments}", ctx.Comments,
	"{review_context}", ctx.ReviewContext,
)
```

Also handle the case where `ReviewContext` is non-empty but the template has no `{review_context}` placeholder — prepend it before the diff section:

```go
hasReviewContext := strings.Contains(tmpl, "{review_context}")
// ... after replacing ...
if !hasReviewContext && ctx.ReviewContext != "" {
	// Insert review context before the diff for maximum visibility
	result = ctx.ReviewContext + "\n" + result
}
```

- [ ] **Step 3: Update default templates to include {review_context}**

In the `defaultTemplate` constant, add `{review_context}` before `{comments}`:

```go
{review_context}
{comments}
```

Same for `DefaultTemplateWithInstructions()`.

- [ ] **Step 4: Run tests**

Run: `cd /Users/stejon/develop/heimdallr/daemon && go test ./internal/executor/ -v -timeout 60s`
Expected: PASS (existing tests should pass — ReviewContext is empty string by default)

- [ ] **Step 5: Commit**

```bash
cd /Users/stejon/develop/heimdallr
git add daemon/internal/executor/prompt.go
git commit -m "feat(prompt): add ReviewContext field and {review_context} placeholder"
```

---

## Task 4: Wire review context into pipeline.Run()

**Files:**
- Modify: `daemon/internal/pipeline/pipeline.go`

- [ ] **Step 1: Add botLogin field to Pipeline struct**

The pipeline needs to know the bot's login to filter its own comments. Add a field and pass it from the constructor or set it via a setter. Read the current Pipeline struct and constructor first.

Add `botLogin string` field. Add a `SetBotLogin(login string)` method.

- [ ] **Step 2: Query previous review in Run()**

In `Run()`, after upserting the PR record (which gives us the store PR ID), query the latest review:

```go
// Check for previous review to build re-review context.
var reviewCtx string
storedPR, _ := p.store.GetPRByGithubID(pr.ID)
if storedPR != nil {
	prevReview, _ := p.store.LatestReviewForPR(storedPR.ID)
	if prevReview != nil {
		reviewCtx = buildReviewContext(
			prevReview.Issues,
			prevReview.Severity,
			prevReview.CreatedAt,
			prComments,
			p.botLogin,
		)
	}
}
```

This must happen AFTER `FetchComments()` (so we have `prComments`) and BEFORE building the prompt.

- [ ] **Step 3: Pass ReviewContext to PRContext**

Update the `executor.PRContext` construction to include the new field:

```go
executor.PRContext{
	Title:         pr.Title,
	Number:        pr.Number,
	Repo:          pr.Repo,
	Author:        pr.User.Login,
	Link:          pr.HTMLURL,
	Diff:          diff,
	Comments:      commentsSection,
	ReviewContext:  reviewCtx,
}
```

- [ ] **Step 4: Wire botLogin in main.go**

In `daemon/cmd/heimdallm/main.go`, after creating the pipeline and resolving the authenticated user, set the bot login:

```go
p.SetBotLogin(cachedLogin)
```

Or pass it via the constructor if that's cleaner. Read main.go to find the right place.

- [ ] **Step 5: Run full test suite**

Run: `cd /Users/stejon/develop/heimdallr/daemon && go test ./... -timeout 60s`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
cd /Users/stejon/develop/heimdallr
git add daemon/internal/pipeline/pipeline.go daemon/cmd/heimdallm/main.go
git commit -m "feat(pipeline): wire review context into PR review flow

When re-reviewing a PR, the pipeline now:
- Queries the previous review from the store
- Partitions comments into before/after last review
- Builds structured context with previous findings and new discussion
- Injects RE-REVIEW instruction telling the LLM not to repeat addressed findings

First-time reviews are unaffected (ReviewContext is empty).

Closes #90"
```

---

## Verification

1. `cd daemon && go test ./... -timeout 60s` — all tests pass
2. First-time reviews: `ReviewContext` is empty, prompt is identical to current behavior
3. Re-reviews: `ReviewContext` contains previous findings + new discussion + RE-REVIEW instruction
4. Bot's own comments are excluded from "Discussion since last review"
5. Previous findings are formatted as structured list, not raw comments

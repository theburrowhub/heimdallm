# PR Comments Context Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fetch existing PR comments (inline review comments + general issue comments) from GitHub and include them in the AI prompt so the reviewer has full discussion context.

**Architecture:** Three independent layers — (1) GitHub client fetches and merges both comment types, (2) executor prompt layer adds `{comments}` placeholder with append fallback for templates that lack it, (3) pipeline wires them together with a non-fatal fetch + size-cap formatting.

**Tech Stack:** Go, `net/http`, `encoding/json`, `sort`, `httptest` for tests.

---

## File Map

| File | Change |
|------|--------|
| `daemon/internal/github/models.go` | Add `Comment` struct |
| `daemon/internal/github/client.go` | Add `FetchComments`, `fetchReviewComments`, `fetchIssueComments`; add `"sort"` import |
| `daemon/internal/github/poller_test.go` | Add `TestFetchComments_*` tests |
| `daemon/internal/executor/prompt.go` | Add `Comments string` to `PRContext`; update `BuildPromptFromTemplate`; update both templates |
| `daemon/internal/executor/prompt_test.go` | Create — tests for Comments substitution and append fallback |
| `daemon/internal/pipeline/pipeline.go` | Add `CommentFetcher` interface; update `Pipeline` struct + `New`; add `formatComments`; update `Run` |
| `daemon/internal/pipeline/pipeline_test.go` | Add `FetchComments` to `fakeGH`; add comments-related tests |

---

## Task 1: `Comment` type and `FetchComments` in GitHub client

**Files:**
- Modify: `daemon/internal/github/models.go`
- Modify: `daemon/internal/github/client.go`
- Modify: `daemon/internal/github/poller_test.go`

- [ ] **Step 1.1 — Write failing tests**

Add to `daemon/internal/github/poller_test.go`:

```go
func TestFetchComments_MergesAndSorts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/org/repo/pulls/1/comments":
			json.NewEncoder(w).Encode([]map[string]any{
				{
					"user":          map[string]string{"login": "bob"},
					"body":          "inline comment",
					"created_at":    "2024-01-02T00:00:00Z",
					"path":          "main.go",
					"original_line": 10,
				},
			})
		case "/repos/org/repo/issues/1/comments":
			json.NewEncoder(w).Encode([]map[string]any{
				{
					"user":       map[string]string{"login": "alice"},
					"body":       "general comment",
					"created_at": "2024-01-01T00:00:00Z",
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	comments, err := client.FetchComments("org/repo", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(comments) != 2 {
		t.Fatalf("expected 2 comments, got %d", len(comments))
	}
	// Should be sorted by CreatedAt: alice first (2024-01-01), bob second (2024-01-02)
	if comments[0].Author != "alice" {
		t.Errorf("expected alice first, got %s", comments[0].Author)
	}
	if comments[1].Author != "bob" {
		t.Errorf("expected bob second, got %s", comments[1].Author)
	}
	if comments[1].File != "main.go" {
		t.Errorf("expected File=main.go for review comment, got %q", comments[1].File)
	}
	if comments[1].Line != 10 {
		t.Errorf("expected Line=10 for review comment, got %d", comments[1].Line)
	}
}

func TestFetchComments_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]any{})
	}))
	defer srv.Close()

	client := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	comments, err := client.FetchComments("org/repo", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(comments) != 0 {
		t.Errorf("expected 0 comments, got %d", len(comments))
	}
}

func TestFetchComments_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	}))
	defer srv.Close()

	client := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	_, err := client.FetchComments("org/repo", 1)
	if err == nil {
		t.Fatal("expected error for 404 response, got nil")
	}
}
```

- [ ] **Step 1.2 — Run tests to verify they fail**

```bash
cd daemon && go test ./internal/github/... -run TestFetchComments -v
```

Expected: `FAIL — undefined: gh.Comment` or `undefined: client.FetchComments`

- [ ] **Step 1.3 — Add `Comment` struct to `daemon/internal/github/models.go`**

Add after the existing types:

```go
// Comment represents a single comment on a PR — either an inline review comment
// (File and Line are set) or a general issue comment (File and Line are zero values).
type Comment struct {
	Author    string
	Body      string
	CreatedAt time.Time
	File      string // non-empty for inline review comments
	Line      int    // non-zero for inline review comments
}
```

- [ ] **Step 1.4 — Add `FetchComments` and helpers to `daemon/internal/github/client.go`**

Add `"sort"` to the import block.

Add the following functions at the end of the file:

```go
// FetchComments retrieves both inline review comments and general issue comments
// for a PR, merged and sorted chronologically.
// Both endpoint calls run concurrently. An error from either is returned immediately.
func (c *Client) FetchComments(repo string, number int) ([]Comment, error) {
	type result struct {
		comments []Comment
		err      error
	}
	reviewCh := make(chan result, 1)
	issueCh := make(chan result, 1)

	go func() {
		comments, err := c.fetchReviewComments(repo, number)
		reviewCh <- result{comments, err}
	}()
	go func() {
		comments, err := c.fetchIssueComments(repo, number)
		issueCh <- result{comments, err}
	}()

	r1 := <-reviewCh
	r2 := <-issueCh
	if r1.err != nil {
		return nil, r1.err
	}
	if r2.err != nil {
		return nil, r2.err
	}

	all := append(r1.comments, r2.comments...)
	sort.Slice(all, func(i, j int) bool {
		return all[i].CreatedAt.Before(all[j].CreatedAt)
	})
	return all, nil
}

func (c *Client) fetchReviewComments(repo string, number int) ([]Comment, error) {
	path := fmt.Sprintf("/repos/%s/pulls/%d/comments", repo, number)
	resp, err := c.do("GET", path, "application/vnd.github+json")
	if err != nil {
		return nil, fmt.Errorf("github: fetch review comments: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if resp.StatusCode != http.StatusOK {
		errBody := string(body)
		if len(errBody) > maxErrBodyLen {
			errBody = errBody[:maxErrBodyLen]
		}
		return nil, fmt.Errorf("github: fetch review comments: status %d: %s", resp.StatusCode, errBody)
	}
	var raw []struct {
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		Body         string    `json:"body"`
		CreatedAt    time.Time `json:"created_at"`
		Path         string    `json:"path"`
		Line         *int      `json:"line"`          // null for outdated comments
		OriginalLine int       `json:"original_line"` // always set
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("github: decode review comments: %w", err)
	}
	comments := make([]Comment, len(raw))
	for i, r := range raw {
		line := r.OriginalLine
		if r.Line != nil {
			line = *r.Line
		}
		comments[i] = Comment{
			Author:    r.User.Login,
			Body:      r.Body,
			CreatedAt: r.CreatedAt,
			File:      r.Path,
			Line:      line,
		}
	}
	return comments, nil
}

func (c *Client) fetchIssueComments(repo string, number int) ([]Comment, error) {
	path := fmt.Sprintf("/repos/%s/issues/%d/comments", repo, number)
	resp, err := c.do("GET", path, "application/vnd.github+json")
	if err != nil {
		return nil, fmt.Errorf("github: fetch issue comments: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if resp.StatusCode != http.StatusOK {
		errBody := string(body)
		if len(errBody) > maxErrBodyLen {
			errBody = errBody[:maxErrBodyLen]
		}
		return nil, fmt.Errorf("github: fetch issue comments: status %d: %s", resp.StatusCode, errBody)
	}
	var raw []struct {
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		Body      string    `json:"body"`
		CreatedAt time.Time `json:"created_at"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("github: decode issue comments: %w", err)
	}
	comments := make([]Comment, len(raw))
	for i, r := range raw {
		comments[i] = Comment{
			Author:    r.User.Login,
			Body:      r.Body,
			CreatedAt: r.CreatedAt,
		}
	}
	return comments, nil
}
```

- [ ] **Step 1.5 — Run tests**

```bash
cd daemon && go test ./internal/github/... -v
```

Expected: all tests pass including `TestFetchComments_*`.

- [ ] **Step 1.6 — Commit**

```bash
git add daemon/internal/github/models.go daemon/internal/github/client.go daemon/internal/github/poller_test.go
git commit -m "feat(github): add Comment type and FetchComments for PR discussion context"
```

---

## Task 2: `PRContext.Comments` and prompt template updates

**Files:**
- Modify: `daemon/internal/executor/prompt.go`
- Create: `daemon/internal/executor/prompt_test.go`

- [ ] **Step 2.1 — Create `daemon/internal/executor/prompt_test.go` with failing tests**

```go
package executor_test

import (
	"strings"
	"testing"

	"github.com/heimdallm/daemon/internal/executor"
)

func TestBuildPromptFromTemplate_CommentsSubstituted(t *testing.T) {
	tmpl := "Diff: {diff}\n{comments}\nReview."
	ctx := executor.PRContext{
		Diff:     "some diff",
		Comments: "Existing PR discussion:\n<user_content>\n@alice: LGTM\n</user_content>",
	}
	result := executor.BuildPromptFromTemplate(tmpl, ctx)
	if !strings.Contains(result, "@alice: LGTM") {
		t.Errorf("expected comments in result, got: %s", result)
	}
	if strings.Contains(result, "{comments}") {
		t.Errorf("placeholder {comments} not substituted in result")
	}
}

func TestBuildPromptFromTemplate_CommentsAppendedWhenNoPlaceholder(t *testing.T) {
	// Template without {comments}: comments should be appended at the end
	tmpl := "Diff: {diff}\nReview now."
	ctx := executor.PRContext{
		Diff:     "some diff",
		Comments: "Existing PR discussion:\n<user_content>\n@bob: fix the typo\n</user_content>",
	}
	result := executor.BuildPromptFromTemplate(tmpl, ctx)
	if !strings.Contains(result, "@bob: fix the typo") {
		t.Errorf("expected appended comments in result, got: %s", result)
	}
	// The original template content must still be there
	if !strings.Contains(result, "Review now.") {
		t.Errorf("original template content missing from result")
	}
	// Comments must come AFTER the original content
	reviewIdx := strings.Index(result, "Review now.")
	commentsIdx := strings.Index(result, "@bob:")
	if commentsIdx < reviewIdx {
		t.Errorf("expected comments after original content: reviewIdx=%d, commentsIdx=%d", reviewIdx, commentsIdx)
	}
}

func TestBuildPromptFromTemplate_EmptyCommentsNoAppend(t *testing.T) {
	tmpl := "Diff: {diff}\nReview now."
	ctx := executor.PRContext{
		Diff:     "some diff",
		Comments: "", // empty
	}
	result := executor.BuildPromptFromTemplate(tmpl, ctx)
	if strings.Contains(result, "Existing PR discussion") {
		t.Errorf("expected no comments section when Comments is empty, got: %s", result)
	}
}

func TestBuildPromptFromTemplate_EmptyCommentsPlaceholderRemoved(t *testing.T) {
	tmpl := "Diff: {diff}\n{comments}\nReview."
	ctx := executor.PRContext{
		Diff:     "some diff",
		Comments: "",
	}
	result := executor.BuildPromptFromTemplate(tmpl, ctx)
	if strings.Contains(result, "{comments}") {
		t.Errorf("empty Comments should remove the placeholder, got: %s", result)
	}
}

func TestDefaultTemplate_ContainsCommentsPlaceholder(t *testing.T) {
	tmpl := executor.DefaultTemplate()
	if !strings.Contains(tmpl, "{comments}") {
		t.Error("defaultTemplate must contain {comments} placeholder")
	}
}

func TestDefaultTemplateWithInstructions_ContainsCommentsPlaceholder(t *testing.T) {
	tmpl := executor.DefaultTemplateWithInstructions("focus on security")
	if !strings.Contains(tmpl, "{comments}") {
		t.Error("DefaultTemplateWithInstructions must contain {comments} placeholder")
	}
}
```

- [ ] **Step 2.2 — Run tests to verify they fail**

```bash
cd daemon && go test ./internal/executor/... -run TestBuild -v
cd daemon && go test ./internal/executor/... -run TestDefault -v
```

Expected: compile error or FAIL because `PRContext` has no `Comments` field.

- [ ] **Step 2.3 — Add `Comments` field to `PRContext` in `daemon/internal/executor/prompt.go`**

Replace the `PRContext` struct:

```go
// PRContext holds all substitutable data for a prompt template.
type PRContext struct {
	Title    string
	Number   int
	Repo     string
	Author   string
	Link     string
	Diff     string
	Comments string // pre-formatted discussion section; empty string if no comments
}
```

- [ ] **Step 2.4 — Update `BuildPromptFromTemplate` in `daemon/internal/executor/prompt.go`**

Replace the existing `BuildPromptFromTemplate` function:

```go
// BuildPromptFromTemplate substitutes placeholders in a template.
// Supported placeholders: {title} {number} {repo} {author} {link} {diff} {comments}
//
// Behavior for {comments}:
//   A) If the template contains {comments}: substituted directly (empty string if no comments).
//   B) If the template does NOT contain {comments} and Comments is non-empty: the comments
//      block is appended at the very end of the rendered prompt.
func BuildPromptFromTemplate(tmpl string, ctx PRContext) string {
	if len(ctx.Diff) > maxDiffBytes {
		ctx.Diff = ctx.Diff[:maxDiffBytes] + "\n... (diff truncated)"
	}

	hasPlaceholder := strings.Contains(tmpl, "{comments}")

	r := strings.NewReplacer(
		"{title}", ctx.Title,
		"{number}", fmt.Sprintf("%d", ctx.Number),
		"{repo}", ctx.Repo,
		"{author}", ctx.Author,
		"{link}", ctx.Link,
		"{diff}", ctx.Diff,
		"{comments}", ctx.Comments,
	)
	result := r.Replace(tmpl)

	// B: append comments if the template had no {comments} placeholder
	if !hasPlaceholder && ctx.Comments != "" {
		result += "\n\n" + ctx.Comments
	}

	return result
}
```

- [ ] **Step 2.5 — Update `defaultTemplate` to include `{comments}` in `daemon/internal/executor/prompt.go`**

Replace the `defaultTemplate` constant:

```go
// defaultTemplate is used when no custom agent template is configured.
//
// Security note — prompt injection risk:
// The title, author, diff, and comments come from untrusted GitHub PR data. A malicious PR
// author could craft content containing LLM instructions intended to override the system
// prompt. The <user_content>…</user_content> delimiters signal to the model that the
// enclosed content is untrusted user data and should be treated as data, not instructions.
// This mitigation reduces (but cannot fully eliminate) the risk of prompt injection.
const defaultTemplate = `You are a senior software engineer performing a pull request code review.

PR: {title} (#{number})
Repo: {repo}
Author: {author}
Link: {link}

<user_content>
Diff:
{diff}
</user_content>

{comments}

Review the above diff and respond with ONLY valid JSON in this exact format (no markdown, no explanation):
{
  "summary": "brief overall assessment",
  "issues": [
    {"file": "filename", "line": 0, "description": "issue description", "severity": "low|medium|high"}
  ],
  "suggestions": ["suggestion 1", "suggestion 2"],
  "severity": "low|medium|high"
}

The top-level "severity" is the highest severity found. If no issues, return empty arrays and severity "low".`
```

- [ ] **Step 2.6 — Update `DefaultTemplateWithInstructions` to include `{comments}`**

Replace the `DefaultTemplateWithInstructions` function:

```go
// DefaultTemplateWithInstructions injects custom review instructions into the
// default template. The instructions define what to focus on (e.g. security,
// performance) while the output format stays consistent.
func DefaultTemplateWithInstructions(instructions string) string {
	return `You are a senior software engineer performing a pull request code review.

PR: {title} (#{number})
Repo: {repo}
Author: {author}
Link: {link}

REVIEW FOCUS:
` + instructions + `

<user_content>
Diff:
{diff}
</user_content>

{comments}

Review the diff according to the focus above and respond with ONLY valid JSON (no markdown, no explanation):
{
  "summary": "brief overall assessment",
  "issues": [
    {"file": "filename", "line": 0, "description": "issue description", "severity": "low|medium|high"}
  ],
  "suggestions": ["suggestion 1"],
  "severity": "low|medium|high"
}

The top-level "severity" is the highest severity found. If no issues, return empty arrays and severity "low".`
}
```

- [ ] **Step 2.7 — Run all executor tests**

```bash
cd daemon && go test ./internal/executor/... -v
```

Expected: all tests pass including new `TestBuild*` and `TestDefault*`.

- [ ] **Step 2.8 — Commit**

```bash
git add daemon/internal/executor/prompt.go daemon/internal/executor/prompt_test.go
git commit -m "feat(executor): add Comments to PRContext with placeholder and append fallback"
```

---

## Task 3: Pipeline integration

**Files:**
- Modify: `daemon/internal/pipeline/pipeline.go`
- Modify: `daemon/internal/pipeline/pipeline_test.go`

- [ ] **Step 3.1 — Add `FetchComments` to `fakeGH` in `daemon/internal/pipeline/pipeline_test.go`**

The existing `fakeGH` struct needs `FetchComments`. Replace the `fakeGH` definition:

```go
type fakeGH struct {
	diff     string
	comments []github.Comment
}

func (f *fakeGH) FetchDiff(repo string, number int) (string, error) {
	return f.diff, nil
}

func (f *fakeGH) SubmitReview(repo string, number int, body, event string) (int64, error) {
	return 12345, nil
}

func (f *fakeGH) PostComment(repo string, number int, body string) error {
	return nil
}

func (f *fakeGH) FetchComments(repo string, number int) ([]github.Comment, error) {
	return f.comments, nil
}
```

Also add tests for comment injection. Add these test functions after `TestPipeline_Run`:

```go
func TestPipeline_Run_CommentsInjectedIntoPrompt(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	var capturedPrompt string
	exec := &fakeExecCapture{capturePrompt: &capturedPrompt}

	comments := []github.Comment{
		{Author: "reviewer1", Body: "Please add error handling here"},
	}
	p := pipeline.New(s, &fakeGH{diff: "+new line", comments: comments}, exec, &fakeNotify{})

	pr := &github.PullRequest{
		ID: 2, Number: 2, Title: "Add feature", Repo: "org/repo",
		User: github.User{Login: "alice"}, State: "open",
		UpdatedAt: time.Now(), HTMLURL: "https://github.com/org/repo/pull/2",
	}
	_, err = p.Run(pr, pipeline.RunOptions{Primary: "claude", Fallback: "gemini"})
	if err != nil {
		t.Fatalf("pipeline run: %v", err)
	}
	if !strings.Contains(capturedPrompt, "reviewer1") {
		t.Errorf("expected comments in prompt, got: %s", capturedPrompt)
	}
	if !strings.Contains(capturedPrompt, "Please add error handling here") {
		t.Errorf("expected comment body in prompt, got: %s", capturedPrompt)
	}
}

func TestPipeline_Run_CommentsFetchErrorIsNonFatal(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	p := pipeline.New(s, &fakeGHCommentsError{diff: "+line"}, &fakeExec{}, &fakeNotify{})

	pr := &github.PullRequest{
		ID: 3, Number: 3, Title: "Fix", Repo: "org/repo",
		User: github.User{Login: "alice"}, State: "open",
		UpdatedAt: time.Now(), HTMLURL: "https://github.com/org/repo/pull/3",
	}
	// Should succeed even when FetchComments returns an error
	_, err = p.Run(pr, pipeline.RunOptions{Primary: "claude", Fallback: "gemini"})
	if err != nil {
		t.Fatalf("expected pipeline to succeed despite comments fetch error, got: %v", err)
	}
}
```

Also add the supporting test types at the bottom of the file:

```go
// fakeExecCapture captures the prompt passed to Execute for assertion in tests.
type fakeExecCapture struct {
	capturePrompt *string
}

func (f *fakeExecCapture) Detect(primary, fallback string) (string, error) {
	return "fake_claude", nil
}

func (f *fakeExecCapture) Execute(cli, prompt string, _ executor.ExecOptions) (*executor.ReviewResult, error) {
	if f.capturePrompt != nil {
		*f.capturePrompt = prompt
	}
	return &executor.ReviewResult{Summary: "ok", Severity: "low"}, nil
}

// fakeGHCommentsError simulates a GitHub client where FetchComments fails.
type fakeGHCommentsError struct {
	diff string
}

func (f *fakeGHCommentsError) FetchDiff(repo string, number int) (string, error) {
	return f.diff, nil
}

func (f *fakeGHCommentsError) SubmitReview(repo string, number int, body, event string) (int64, error) {
	return 1, nil
}

func (f *fakeGHCommentsError) PostComment(repo string, number int, body string) error {
	return nil
}

func (f *fakeGHCommentsError) FetchComments(repo string, number int) ([]github.Comment, error) {
	return nil, fmt.Errorf("network error")
}
```

Add `"fmt"` and `"strings"` to the test file imports if not already present.

- [ ] **Step 3.2 — Run tests to verify they fail**

```bash
cd daemon && go test ./internal/pipeline/... -v
```

Expected: compile error because `fakeGH` doesn't satisfy the updated interface yet (after we add `CommentFetcher` in the next step).

- [ ] **Step 3.3 — Add `CommentFetcher` interface to `daemon/internal/pipeline/pipeline.go`**

Add after the `GitHubReviewer` interface definition:

```go
// CommentFetcher retrieves PR comments for context injection into the AI prompt.
type CommentFetcher interface {
	FetchComments(repo string, number int) ([]github.Comment, error)
}
```

Update the `Pipeline` struct and `New` to include `CommentFetcher` in the combined `gh` interface:

```go
// Pipeline orchestrates the full PR review flow.
type Pipeline struct {
	store *store.Store
	gh    interface {
		DiffFetcher
		GitHubReviewer
		CommentFetcher
	}
	executor CLIExecutor
	notify   Notifier
}

// New creates a new Pipeline with the provided dependencies.
func New(s *store.Store, gh interface {
	DiffFetcher
	GitHubReviewer
	CommentFetcher
}, exec CLIExecutor, n Notifier) *Pipeline {
	return &Pipeline{store: s, gh: gh, executor: exec, notify: n}
}
```

- [ ] **Step 3.4 — Add `formatComments` helper and `maxCommentsBytes` constant to `daemon/internal/pipeline/pipeline.go`**

Add the constant near the top of the file (after the imports):

```go
// maxCommentsBytes limits the total size of PR comments included in the prompt.
// If the formatted comments exceed this limit, older comments are trimmed starting
// from before the PR author's last message.
const maxCommentsBytes = 16 * 1024 // 16KB
```

Add the helper function at the bottom of the file:

```go
// formatComments formats a slice of GitHub comments into a prompt section.
// Returns empty string if comments is nil or empty.
// If the formatted text exceeds maxCommentsBytes, trims comments before the
// PR author's last message. If still too large, hard-truncates with a note.
func formatComments(comments []github.Comment, prAuthor string) string {
	if len(comments) == 0 {
		return ""
	}

	lines := make([]string, len(comments))
	for i, c := range comments {
		if c.File != "" {
			lines[i] = fmt.Sprintf("@%s (%s:%d): %s", c.Author, c.File, c.Line, c.Body)
		} else {
			lines[i] = fmt.Sprintf("@%s: %s", c.Author, c.Body)
		}
	}

	formatted := strings.Join(lines, "\n---\n")
	if len(formatted) <= maxCommentsBytes {
		return wrapCommentsSection(formatted)
	}

	// Find the last comment by the PR author and trim everything before it
	lastAuthorIdx := -1
	for i := len(comments) - 1; i >= 0; i-- {
		if comments[i].Author == prAuthor {
			lastAuthorIdx = i
			break
		}
	}

	start := 0
	if lastAuthorIdx > 0 {
		start = lastAuthorIdx
	}

	trimmed := strings.Join(lines[start:], "\n---\n")
	if len(trimmed) <= maxCommentsBytes {
		return wrapCommentsSection(trimmed)
	}

	// Hard-truncate if still too large
	return wrapCommentsSection(trimmed[:maxCommentsBytes] + "\n... (truncated)")
}

// wrapCommentsSection wraps formatted comment text in the prompt section header and
// <user_content> tags to signal untrusted input to the LLM.
func wrapCommentsSection(text string) string {
	return "Existing PR discussion:\n<user_content>\n" + text + "\n</user_content>"
}
```

- [ ] **Step 3.5 — Update `Run` in `daemon/internal/pipeline/pipeline.go` to fetch and inject comments**

In the `Run` function, after step 2 (FetchDiff) and before step 3 (Build prompt), add step 2b:

```go
	// 2b. Fetch PR comments for context (non-fatal: proceed without if unavailable)
	prComments, err := p.gh.FetchComments(pr.Repo, pr.Number)
	if err != nil {
		slog.Warn("pipeline: failed to fetch PR comments, proceeding without", "err", err)
		prComments = nil
	}
	commentsSection := formatComments(prComments, pr.User.Login)
```

Then update the `PRContext` construction in step 3 to include `Comments`:

```go
	prompt := executor.BuildPromptFromTemplate(promptTemplate, executor.PRContext{
		Title:    pr.Title,
		Number:   pr.Number,
		Repo:     pr.Repo,
		Author:   pr.User.Login,
		Link:     pr.HTMLURL,
		Diff:     diff,
		Comments: commentsSection,
	})
```

- [ ] **Step 3.6 — Run all tests**

```bash
cd daemon && go test ./... -v
```

Expected: all tests pass. Verify specifically:
- `TestPipeline_Run` still passes
- `TestPipeline_Run_CommentsInjectedIntoPrompt` passes
- `TestPipeline_Run_CommentsFetchErrorIsNonFatal` passes

- [ ] **Step 3.7 — Verify integration test still compiles**

```bash
cd daemon && go test -tags integration -run ^$ .
```

Expected: `ok ... [no tests to run]`

- [ ] **Step 3.8 — Commit**

```bash
git add daemon/internal/pipeline/pipeline.go daemon/internal/pipeline/pipeline_test.go
git commit -m "feat(pipeline): inject PR comments into AI prompt for full discussion context"
```

---

## Self-Review Checklist

- [x] **Spec coverage:** Comment struct ✓, FetchComments both endpoints ✓, chronological sort ✓, size cap + author trim ✓, `{comments}` placeholder ✓, append fallback ✓, empty comments renders nothing ✓, non-fatal fetch error ✓, both default templates updated ✓
- [x] **No placeholders:** All steps have complete code blocks
- [x] **Type consistency:** `github.Comment` used consistently across tasks; `CommentFetcher` interface matches `FetchComments` signature; `PRContext.Comments string` matches usage in `BuildPromptFromTemplate`
- [x] **Import check:** `"sort"` added to `client.go`; `"fmt"` and `"strings"` already imported in `pipeline.go`; test imports explicit

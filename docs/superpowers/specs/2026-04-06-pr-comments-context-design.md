# PR Comments Context — Design Spec

**Date:** 2026-04-06
**Status:** Approved

## Problem

Heimdallm reviews PRs using only the diff, title, and author. It ignores existing comments from other reviewers and the PR author, which often contain critical context: questions, clarifications, decisions already made, and prior feedback. This leads to redundant or uninformed reviews.

## Goal

Fetch both types of GitHub comments (review comments and issue comments) for each PR and include them in the prompt sent to the AI CLI.

---

## Architecture

Three layers change:

1. **`github/client.go`** — new `FetchComments` method
2. **`executor/prompt.go`** — new `{comments}` placeholder + append fallback
3. **`daemon/internal/pipeline/pipeline.go`** — fetch comments and inject into `PRContext`

---

## 1. GitHub Client — `FetchComments`

### New type

```go
type Comment struct {
    Author    string
    Body      string
    CreatedAt time.Time
    File      string // non-empty for inline review comments
    Line      int    // non-zero for inline review comments
}
```

### New method signature

```go
func (c *Client) FetchComments(repo string, number int) ([]Comment, error)
```

### Behavior

- Calls two endpoints concurrently:
  - `GET /repos/{repo}/pulls/{number}/comments` — inline review comments (have `path` and `original_line`)
  - `GET /repos/{repo}/issues/{number}/comments` — general PR conversation comments
- Merges and sorts by `created_at` ascending (chronological order)
- Both calls use `io.LimitReader(resp.Body, maxBodyBytes)` (1MB each)
- Errors from either endpoint are returned; partial results are not used

### Size cap and truncation

- A constant `maxCommentsBytes = 16 * 1024` (16KB) limits the total formatted text
- If the formatted comments exceed the cap:
  1. Find the last comment where `Comment.Author == pr.Author` (PR author's last message)
  2. Discard all comments before that point
  3. If still over cap after trimming, hard-truncate with `... (truncated)`
- If no author comment exists and still over cap, keep the most recent comments that fit

---

## 2. Prompt Layer — `executor/prompt.go`

### PRContext extension

```go
type PRContext struct {
    Title    string
    Number   int
    Repo     string
    Author   string
    Link     string
    Diff     string
    Comments string // pre-formatted; empty string if no comments
}
```

### Comment formatting

Each comment is formatted as:
- Review comment (inline): `@author (file.go:42): body text`
- Issue comment (general): `@author: body text`

Comments are joined with `\n---\n` as separator.

### Placeholder behavior (A+B)

`BuildPromptFromTemplate` handles `{comments}` in two ways:

**A — Template has `{comments}`:** substituted normally. If `Comments` is empty, the placeholder is replaced with an empty string.

**B — Template does NOT have `{comments}`:** if `Comments` is non-empty, append a block at the very end of the prompt:

```
---
Existing PR discussion:
<user_content>
{formatted comments}
</user_content>
```

If `Comments` is empty, nothing is appended.

### Default template changes

Both `defaultTemplate` and `DefaultTemplateWithInstructions` include `{comments}` explicitly between the diff and the output instructions:

```
<user_content>
Diff:
{diff}
</user_content>

{comments}

Review the above...
```

When `Comments` is empty, the `{comments}` line renders as empty — no visible gap because the surrounding newlines collapse naturally.

---

## 3. Pipeline — `pipeline.go`

### New interface

```go
// CommentFetcher retrieves PR comments for context injection.
type CommentFetcher interface {
    FetchComments(repo string, number int) ([]Comment, error)
}
```

The `Pipeline` struct gains a `commentFetcher CommentFetcher` field. `github.Client` implements this interface automatically.

### Pipeline.New signature change

```go
func New(s *store.Store, gh interface {
    DiffFetcher
    GitHubReviewer
    CommentFetcher
}, exec CLIExecutor, n Notifier) *Pipeline
```

(Single combined interface — `gh` already satisfies all three.)

### Run() change

After step 2 (FetchDiff), add step 2b:

```go
// 2b. Fetch PR comments for context
comments, err := p.gh.FetchComments(pr.Repo, pr.Number)
if err != nil {
    slog.Warn("pipeline: failed to fetch comments, proceeding without", "err", err)
    comments = nil // non-fatal
}
formattedComments := formatComments(comments, pr.User.Login)
```

`formattedComments` is passed into `PRContext.Comments`. Comment fetch failure is non-fatal — the review proceeds without comments rather than aborting.

### Helper

```go
func formatComments(comments []github.Comment, prAuthor string) string
```

Applies the size cap logic, formats each comment, joins with separators.

---

## Error Handling

- `FetchComments` errors → warning log, empty comments, review continues
- Individual comment decode errors → skip that comment, continue
- Size cap exceeded → truncation as described above

---

## Testing

- `github/client_test.go`: mock server returning both comment types; verify merge+sort; verify size cap truncation; verify author-based trimming
- `executor/prompt_test.go`: verify `{comments}` substitution; verify append fallback when no placeholder; verify empty comments renders nothing
- `pipeline/pipeline_test.go`: mock `CommentFetcher`; verify comments flow into prompt; verify review proceeds when `FetchComments` returns error

---

## Out of Scope

- Storing comments in SQLite
- Displaying comments in the Flutter UI
- Filtering comments by date or author beyond the size-cap strategy
- Re-fetching comments on retry (`PublishPending` does not re-execute the review)

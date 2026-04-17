# Issue Tracking UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add issue tracking HTTP endpoints to the daemon and issue tracking screens to the Flutter desktop app, with a unified activity dashboard.

**Architecture:** The daemon's store and pipeline layers for issues already exist (PRs #42–#44). This plan adds the HTTP handler layer on top (5 endpoints mirroring PRs), wires the issue pipeline for manual triggers, and builds Flutter screens/providers following the exact patterns of the existing PR views. The dashboard gets a 6th "Issues" tab and the "Reviews" tab becomes a unified "Activity" feed.

**Tech Stack:** Go 1.21 (chi router, net/http/httptest), Flutter 3.8+ (Riverpod, GoRouter, json_annotation + build_runner)

---

## File Structure

### Daemon (Go) — files to modify

| File | Responsibility |
|------|---------------|
| `daemon/internal/server/handlers.go` | Add 5 issue handlers, register routes, extend `sensitiveGETPaths` |
| `daemon/internal/server/handlers_test.go` | Tests for all new handlers |
| `daemon/cmd/heimdallm/main.go` | Create issue pipeline, wire `SetTriggerIssueReviewFn` |

### Flutter (Dart) — files to create/modify

| File | Responsibility |
|------|---------------|
| `flutter_app/lib/core/models/tracked_issue.dart` | **Create** — `TrackedIssue` + `TrackedIssueReview` models |
| `flutter_app/lib/core/models/tracked_issue.g.dart` | **Generated** — build_runner output |
| `flutter_app/lib/core/api/api_client.dart` | **Modify** — add 5 issue methods |
| `flutter_app/lib/features/issues/issues_providers.dart` | **Create** — Riverpod providers for issues |
| `flutter_app/lib/features/issues/issues_screen.dart` | **Create** — issue list with filters |
| `flutter_app/lib/features/issues/issue_detail_screen.dart` | **Create** — two-panel issue detail |
| `flutter_app/lib/features/dashboard/dashboard_providers.dart` | **Modify** — handle issue SSE events |
| `flutter_app/lib/features/dashboard/dashboard_screen.dart` | **Modify** — add Issues tab, unify Activity tab |
| `flutter_app/lib/shared/router.dart` | **Modify** — add `/issues/:id` route |

---

### Task 1: Daemon — List and Get issue handlers

**Files:**
- Modify: `daemon/internal/server/handlers.go`
- Modify: `daemon/internal/server/handlers_test.go`

- [ ] **Step 1: Write failing tests for handleListIssues and handleGetIssue**

Add to `daemon/internal/server/handlers_test.go`:

```go
func TestHandlerListIssues(t *testing.T) {
	srv, s := setupServer(t)
	now := time.Now()
	id, err := s.UpsertIssue(&store.Issue{
		GithubID: 100, Repo: "org/repo", Number: 7, Title: "bug: crash",
		Body: "details", Author: "alice", Assignees: `["bob"]`, Labels: `["bug"]`,
		State: "open", CreatedAt: now, FetchedAt: now,
	})
	if err != nil {
		t.Fatalf("upsert issue: %v", err)
	}
	s.InsertIssueReview(&store.IssueReview{
		IssueID: id, CLIUsed: "claude", Summary: "triage summary",
		Triage: `{"severity":"high","category":"bug"}`, Suggestions: `["fix it"]`,
		ActionTaken: "review_only", CreatedAt: now,
	})

	req := httptest.NewRequest("GET", "/issues", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list issues: status %d, body: %s", w.Code, w.Body.String())
	}
	var issues []map[string]any
	json.NewDecoder(w.Body).Decode(&issues)
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}
	iss := issues[0]
	if iss["title"] != "bug: crash" {
		t.Errorf("title = %v", iss["title"])
	}
	// Verify assignees/labels are arrays, not strings
	if assignees, ok := iss["assignees"].([]any); !ok || len(assignees) != 1 {
		t.Errorf("assignees should be parsed array, got %T: %v", iss["assignees"], iss["assignees"])
	}
	if labels, ok := iss["labels"].([]any); !ok || len(labels) != 1 {
		t.Errorf("labels should be parsed array, got %T: %v", iss["labels"], iss["labels"])
	}
	// Verify latest_review is attached
	rev, ok := iss["latest_review"].(map[string]any)
	if !ok || rev == nil {
		t.Fatalf("expected latest_review, got %v", iss["latest_review"])
	}
	if rev["summary"] != "triage summary" {
		t.Errorf("review summary = %v", rev["summary"])
	}
	// Verify triage is parsed object, not string
	if _, ok := rev["triage"].(map[string]any); !ok {
		t.Errorf("triage should be parsed object, got %T: %v", rev["triage"], rev["triage"])
	}
}

func TestHandlerGetIssue(t *testing.T) {
	srv, s := setupServer(t)
	now := time.Now()
	id, _ := s.UpsertIssue(&store.Issue{
		GithubID: 200, Repo: "org/repo", Number: 8, Title: "feat request",
		Body: "details", Author: "bob", Assignees: `[]`, Labels: `["enhancement"]`,
		State: "open", CreatedAt: now, FetchedAt: now,
	})
	s.InsertIssueReview(&store.IssueReview{
		IssueID: id, CLIUsed: "gemini", Summary: "looks good",
		Triage: `{"severity":"low","category":"feature"}`, Suggestions: `[]`,
		ActionTaken: "review_only", CreatedAt: now,
	})

	req := httptest.NewRequest("GET", "/issues/"+itoa(id), nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("get issue: status %d, body: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	json.NewDecoder(w.Body).Decode(&body)
	iss, ok := body["issue"].(map[string]any)
	if !ok {
		t.Fatalf("expected issue key")
	}
	if iss["title"] != "feat request" {
		t.Errorf("title = %v", iss["title"])
	}
	reviews, ok := body["reviews"].([]any)
	if !ok || len(reviews) != 1 {
		t.Fatalf("expected 1 review, got %v", body["reviews"])
	}
}

func TestHandlerGetIssue_NotFound(t *testing.T) {
	srv, _ := setupServer(t)
	req := httptest.NewRequest("GET", "/issues/9999", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd daemon && go test ./internal/server/ -run "TestHandlerListIssues|TestHandlerGetIssue" -v -timeout 30s`
Expected: FAIL — routes not registered, handlers not defined.

- [ ] **Step 3: Implement handlers and register routes**

Add to `daemon/internal/server/handlers.go`:

1. Add `"encoding/json"` to imports (already present).

2. Add `/issues` to `sensitiveGETPaths`:
```go
var sensitiveGETPaths = []string{
	"/config",
	"/agents",
	"/events",
	"/logs/stream",
	"/me",
	"/prs",
	"/stats",
	"/issues", // covers /issues and /issues/{id}
}
```

3. Register routes in `buildRouter()`, after the `/prs` block:
```go
r.Get("/issues", srv.handleListIssues)
r.Get("/issues/{id}", srv.handleGetIssue)
r.Post("/issues/{id}/review", srv.handleTriggerIssueReview)
r.Post("/issues/{id}/dismiss", srv.handleDismissIssue)
r.Post("/issues/{id}/undismiss", srv.handleUndismissIssue)
```

4. Add `issueResponse` and `issueReviewResponse` types (after `writeJSON`):
```go
// issueResponse wraps a store.Issue for JSON serialization, parsing the
// Assignees/Labels JSON strings into proper arrays so the API consumer
// receives `[]string` instead of a JSON-encoded string.
type issueResponse struct {
	ID        int64               `json:"id"`
	GithubID  int64               `json:"github_id"`
	Repo      string              `json:"repo"`
	Number    int                 `json:"number"`
	Title     string              `json:"title"`
	Body      string              `json:"body"`
	Author    string              `json:"author"`
	Assignees json.RawMessage     `json:"assignees"`
	Labels    json.RawMessage     `json:"labels"`
	State     string              `json:"state"`
	CreatedAt time.Time           `json:"created_at"`
	FetchedAt time.Time           `json:"fetched_at"`
	Dismissed bool                `json:"dismissed"`
	LatestReview *issueReviewResponse `json:"latest_review,omitempty"`
}

// issueReviewResponse wraps a store.IssueReview, parsing Triage/Suggestions
// JSON strings into structured objects.
type issueReviewResponse struct {
	ID          int64           `json:"id"`
	IssueID     int64           `json:"issue_id"`
	CLIUsed     string          `json:"cli_used"`
	Summary     string          `json:"summary"`
	Triage      json.RawMessage `json:"triage"`
	Suggestions json.RawMessage `json:"suggestions"`
	ActionTaken string          `json:"action_taken"`
	PRCreated   int             `json:"pr_created"`
	CreatedAt   time.Time       `json:"created_at"`
}

func toIssueResponse(iss *store.Issue, rev *store.IssueReview) issueResponse {
	resp := issueResponse{
		ID: iss.ID, GithubID: iss.GithubID, Repo: iss.Repo,
		Number: iss.Number, Title: iss.Title, Body: iss.Body,
		Author: iss.Author, State: iss.State,
		Assignees: json.RawMessage(iss.Assignees),
		Labels:    json.RawMessage(iss.Labels),
		CreatedAt: iss.CreatedAt, FetchedAt: iss.FetchedAt,
		Dismissed: iss.Dismissed,
	}
	if rev != nil {
		resp.LatestReview = toIssueReviewResponse(rev)
	}
	return resp
}

func toIssueReviewResponse(r *store.IssueReview) *issueReviewResponse {
	return &issueReviewResponse{
		ID: r.ID, IssueID: r.IssueID, CLIUsed: r.CLIUsed,
		Summary: r.Summary,
		Triage:      json.RawMessage(r.Triage),
		Suggestions: json.RawMessage(r.Suggestions),
		ActionTaken: r.ActionTaken, PRCreated: r.PRCreated,
		CreatedAt: r.CreatedAt,
	}
}
```

5. Add handler functions:
```go
func (srv *Server) handleListIssues(w http.ResponseWriter, r *http.Request) {
	issues, err := srv.store.ListIssues()
	if err != nil {
		slog.Error("handleListIssues: store error", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	result := make([]issueResponse, 0, len(issues))
	for _, iss := range issues {
		rev, _ := srv.store.LatestIssueReview(iss.ID)
		result = append(result, toIssueResponse(iss, rev))
	}
	writeJSON(w, http.StatusOK, result)
}

func (srv *Server) handleGetIssue(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	iss, err := srv.store.GetIssue(id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	reviews, _ := srv.store.ListIssueReviews(id)
	reviewResps := make([]*issueReviewResponse, 0, len(reviews))
	for _, rev := range reviews {
		reviewResps = append(reviewResps, toIssueReviewResponse(rev))
	}
	issResp := toIssueResponse(iss, nil)
	writeJSON(w, http.StatusOK, map[string]any{"issue": issResp, "reviews": reviewResps})
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd daemon && go test ./internal/server/ -run "TestHandlerListIssues|TestHandlerGetIssue" -v -timeout 30s`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /Users/jamuriano/personal-workspace/auto-pr
git add daemon/internal/server/handlers.go daemon/internal/server/handlers_test.go
git commit -m "feat(issues): add GET /issues and GET /issues/{id} handlers

Wire issue list and detail endpoints following the existing PR handler
pattern. Assignees/labels/triage/suggestions are parsed from JSON
strings into structured objects in the response."
```

---

### Task 2: Daemon — Dismiss, undismiss, and trigger issue review handlers

**Files:**
- Modify: `daemon/internal/server/handlers.go`
- Modify: `daemon/internal/server/handlers_test.go`

- [ ] **Step 1: Write failing tests**

Add to `daemon/internal/server/handlers_test.go`:

```go
func TestHandlerDismissIssue(t *testing.T) {
	srv, s := setupServer(t)
	now := time.Now()
	id, _ := s.UpsertIssue(&store.Issue{
		GithubID: 300, Repo: "org/r", Number: 10, Title: "t",
		Body: "b", Author: "a", Assignees: `[]`, Labels: `[]`,
		State: "open", CreatedAt: now, FetchedAt: now,
	})

	req := httptest.NewRequest("POST", "/issues/"+itoa(id)+"/dismiss", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("dismiss issue: status %d, body: %s", w.Code, w.Body.String())
	}

	// Verify issue is now dismissed (not in list)
	issues, _ := s.ListIssues()
	if len(issues) != 0 {
		t.Errorf("expected 0 issues after dismiss, got %d", len(issues))
	}
}

func TestHandlerUndismissIssue(t *testing.T) {
	srv, s := setupServer(t)
	now := time.Now()
	id, _ := s.UpsertIssue(&store.Issue{
		GithubID: 400, Repo: "org/r", Number: 11, Title: "t",
		Body: "b", Author: "a", Assignees: `[]`, Labels: `[]`,
		State: "open", CreatedAt: now, FetchedAt: now,
	})
	s.DismissIssue(id)

	req := httptest.NewRequest("POST", "/issues/"+itoa(id)+"/undismiss", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("undismiss issue: status %d, body: %s", w.Code, w.Body.String())
	}

	issues, _ := s.ListIssues()
	if len(issues) != 1 {
		t.Errorf("expected 1 issue after undismiss, got %d", len(issues))
	}
}

func TestHandlerTriggerIssueReview(t *testing.T) {
	srv, s := setupServer(t)
	now := time.Now()
	id, _ := s.UpsertIssue(&store.Issue{
		GithubID: 500, Repo: "org/r", Number: 12, Title: "t",
		Body: "b", Author: "a", Assignees: `[]`, Labels: `[]`,
		State: "open", CreatedAt: now, FetchedAt: now,
	})

	triggered := make(chan int64, 1)
	srv.SetTriggerIssueReviewFn(func(issueID int64) error {
		triggered <- issueID
		return nil
	})

	req := httptest.NewRequest("POST", "/issues/"+itoa(id)+"/review", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("trigger issue review: status %d, body: %s", w.Code, w.Body.String())
	}

	// Wait for the goroutine to fire
	select {
	case got := <-triggered:
		if got != id {
			t.Errorf("triggered with issue_id %d, expected %d", got, id)
		}
	case <-time.After(2 * time.Second):
		t.Error("trigger callback not called within 2s")
	}
}

func TestHandlerTriggerIssueReview_NotConfigured(t *testing.T) {
	srv, s := setupServer(t)
	now := time.Now()
	id, _ := s.UpsertIssue(&store.Issue{
		GithubID: 600, Repo: "org/r", Number: 13, Title: "t",
		Body: "b", Author: "a", Assignees: `[]`, Labels: `[]`,
		State: "open", CreatedAt: now, FetchedAt: now,
	})

	// Don't set trigger fn → should return 503
	req := httptest.NewRequest("POST", "/issues/"+itoa(id)+"/review", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when trigger not configured, got %d", w.Code)
	}
}

func TestIssueEndpointsRequireAuthWhenTokenSet(t *testing.T) {
	srv := setupServerWithToken(t, "secret-token")

	paths := []string{"/issues"}
	for _, path := range paths {
		req := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		srv.Router().ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without token: expected 401, got %d", path, w.Code)
		}

		req2 := httptest.NewRequest("GET", path, nil)
		req2.Header.Set("X-Heimdallm-Token", "secret-token")
		w2 := httptest.NewRecorder()
		srv.Router().ServeHTTP(w2, req2)
		if w2.Code == http.StatusUnauthorized {
			t.Errorf("GET %s with valid token: unexpected 401", path)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd daemon && go test ./internal/server/ -run "TestHandlerDismissIssue|TestHandlerUndismissIssue|TestHandlerTriggerIssueReview|TestIssueEndpointsRequireAuth" -v -timeout 30s`
Expected: FAIL — handlers not defined yet.

- [ ] **Step 3: Implement handlers and server wiring**

Add to `daemon/internal/server/handlers.go`:

1. Add field and setter to `Server` struct:
```go
// In Server struct, after triggerReviewFn:
triggerIssueReviewFn func(issueID int64) error
```

```go
// After SetTriggerReviewFn:
// SetTriggerIssueReviewFn wires the issue-review-trigger callback called by POST /issues/{id}/review.
func (srv *Server) SetTriggerIssueReviewFn(fn func(issueID int64) error) {
	srv.triggerIssueReviewFn = fn
}
```

2. Add handler functions:
```go
func (srv *Server) handleDismissIssue(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := srv.store.DismissIssue(id); err != nil {
		slog.Error("handleDismissIssue: store error", "id", id, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "dismissed"})
}

func (srv *Server) handleUndismissIssue(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := srv.store.UndismissIssue(id); err != nil {
		slog.Error("handleUndismissIssue: store error", "id", id, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "undismissed"})
}

func (srv *Server) handleTriggerIssueReview(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if srv.triggerIssueReviewFn == nil {
		http.Error(w, "issue review trigger not configured", http.StatusServiceUnavailable)
		return
	}
	select {
	case srv.reviewSem <- struct{}{}:
	default:
		http.Error(w, `{"error":"too many concurrent reviews — try again later"}`, http.StatusTooManyRequests)
		return
	}
	go func() {
		defer func() { <-srv.reviewSem }()
		if err := srv.triggerIssueReviewFn(id); err != nil {
			slog.Error("trigger issue review failed", "issue_id", id, "err", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "review queued"})
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd daemon && go test ./internal/server/ -run "TestHandlerDismissIssue|TestHandlerUndismissIssue|TestHandlerTriggerIssueReview|TestIssueEndpointsRequireAuth" -v -timeout 30s`
Expected: PASS

- [ ] **Step 5: Run full daemon test suite**

Run: `cd daemon && make test`
Expected: All tests pass.

- [ ] **Step 6: Commit**

```bash
cd /Users/jamuriano/personal-workspace/auto-pr
git add daemon/internal/server/handlers.go daemon/internal/server/handlers_test.go
git commit -m "feat(issues): add dismiss, undismiss, and trigger review handlers

Complete the 5 issue HTTP endpoints. Trigger uses the same review
semaphore as PR reviews for shared concurrency limiting."
```

---

### Task 3: Daemon — Wire issue pipeline trigger in main.go

**Files:**
- Modify: `daemon/cmd/heimdallm/main.go`

- [ ] **Step 1: Add issue pipeline import and instantiation**

In `daemon/cmd/heimdallm/main.go`, add import:
```go
issuepipeline "github.com/heimdallm/daemon/internal/issues"
```

After `p := pipeline.New(...)` (line 99), add:
```go
issuePipe := issuepipeline.New(s, ghClient, exec, broker, &notifyWithSSE{notifier: notifier})
```

- [ ] **Step 2: Wire SetTriggerIssueReviewFn**

After the `srv.SetTriggerReviewFn(...)` block (around line 461), add:

```go
// Wire the issue-review trigger callback: re-run issue pipeline on a stored issue.
srv.SetTriggerIssueReviewFn(func(issueID int64) error {
	publishIssueErr := func(msg string) {
		broker.Publish(sse.Event{
			Type: sse.EventIssueReviewError,
			Data: sseData(map[string]any{"issue_id": issueID, "error": msg}),
		})
	}

	iss, err := s.GetIssue(issueID)
	if err != nil {
		publishIssueErr(fmt.Sprintf("Issue not found: %v", err))
		return fmt.Errorf("trigger issue review: get issue %d: %w", issueID, err)
	}

	cfgMu.Lock()
	aiCfg := cfg.AIForRepo(iss.Repo)
	agentCfg := cfg.AgentConfigFor(aiCfg.Primary)
	if aiCfg.Primary == "" {
		aiCfg.Primary = cfg.AI.Primary
		agentCfg = cfg.AgentConfigFor(cfg.AI.Primary)
	}
	cfgMu.Unlock()

	// Reconstruct github.Issue from store data for the pipeline
	ghIssue := &gh.Issue{
		ID:      iss.GithubID,
		Number:  iss.Number,
		Title:   iss.Title,
		Body:    iss.Body,
		State:   iss.State,
		Repo:    iss.Repo,
		HTMLURL: fmt.Sprintf("https://github.com/%s/issues/%d", iss.Repo, iss.Number),
	}
	ghIssue.User.Login = iss.Author
	// Mode is always review_only for manual triggers
	ghIssue.Mode = config.IssueModeReviewOnly

	extraFlags := agentCfg.ExtraFlags
	if extraFlags != "" {
		if err := executor.ValidateExtraFlags(extraFlags); err != nil {
			slog.Warn("triggerIssueReview: extra_flags rejected", "err", err)
			extraFlags = ""
		}
	}

	opts := issuepipeline.RunOptions{
		Primary:  aiCfg.Primary,
		Fallback: aiCfg.Fallback,
		ExecOpts: executor.ExecOptions{
			Model:                agentCfg.Model,
			MaxTurns:             agentCfg.MaxTurns,
			ApprovalMode:         agentCfg.ApprovalMode,
			ExtraFlags:           extraFlags,
			WorkDir:              aiCfg.LocalDir,
			Effort:               agentCfg.Effort,
			PermissionMode:       agentCfg.PermissionMode,
			Bare:                 agentCfg.Bare,
			DangerouslySkipPerms: agentCfg.DangerouslySkipPerms,
			NoSessionPersistence: agentCfg.NoSessionPersistence,
		},
	}

	slog.Info("trigger issue review: running pipeline",
		"store_issue_id", issueID, "repo", iss.Repo, "number", iss.Number)

	_, err = issuePipe.Run(ghIssue, opts)
	if err != nil {
		broker.Publish(sse.Event{Type: sse.EventIssueReviewError, Data: sseData(map[string]any{
			"issue_id": issueID, "repo": iss.Repo, "error": err.Error(),
		})})
		return err
	}
	return nil
})
```

- [ ] **Step 3: Verify daemon builds**

Run: `cd daemon && go build -o bin/heimdallm ./cmd/heimdallm`
Expected: Build succeeds with no errors.

- [ ] **Step 4: Run full test suite**

Run: `cd daemon && make test`
Expected: All tests pass.

- [ ] **Step 5: Commit**

```bash
cd /Users/jamuriano/personal-workspace/auto-pr
git add daemon/cmd/heimdallm/main.go
git commit -m "feat(issues): wire issue pipeline trigger in main.go

Create issuePipeline instance and connect SetTriggerIssueReviewFn
so POST /issues/{id}/review can run the triage pipeline on demand."
```

---

### Task 4: Flutter — TrackedIssue and TrackedIssueReview models

**Files:**
- Create: `flutter_app/lib/core/models/tracked_issue.dart`
- Generate: `flutter_app/lib/core/models/tracked_issue.g.dart`

- [ ] **Step 1: Create the model file**

Create `flutter_app/lib/core/models/tracked_issue.dart`:

```dart
import 'package:json_annotation/json_annotation.dart';
part 'tracked_issue.g.dart';

@JsonSerializable()
class TrackedIssueReview {
  final int id;
  @JsonKey(name: 'issue_id')
  final int issueId;
  @JsonKey(name: 'cli_used')
  final String cliUsed;
  final String summary;
  final Map<String, dynamic> triage;
  final List<dynamic> suggestions;
  @JsonKey(name: 'action_taken')
  final String actionTaken;
  @JsonKey(name: 'pr_created')
  final int prCreated;
  @JsonKey(name: 'created_at')
  final DateTime createdAt;

  const TrackedIssueReview({
    required this.id,
    required this.issueId,
    required this.cliUsed,
    required this.summary,
    required this.triage,
    required this.suggestions,
    required this.actionTaken,
    required this.prCreated,
    required this.createdAt,
  });

  factory TrackedIssueReview.fromJson(Map<String, dynamic> json) =>
      _$TrackedIssueReviewFromJson(json);
  Map<String, dynamic> toJson() => _$TrackedIssueReviewToJson(this);

  String get severity => (triage['severity'] as String?) ?? 'low';
  String get category => (triage['category'] as String?) ?? '';
}

@JsonSerializable()
class TrackedIssue {
  final int id;
  @JsonKey(name: 'github_id')
  final int githubId;
  final String repo;
  final int number;
  final String title;
  final String body;
  final String author;
  final List<dynamic> assignees;
  final List<dynamic> labels;
  final String state;
  @JsonKey(name: 'created_at')
  final DateTime createdAt;
  @JsonKey(name: 'fetched_at')
  final DateTime fetchedAt;
  @JsonKey(defaultValue: false)
  final bool dismissed;
  @JsonKey(name: 'latest_review', includeIfNull: false)
  final TrackedIssueReview? latestReview;

  const TrackedIssue({
    required this.id,
    required this.githubId,
    required this.repo,
    required this.number,
    required this.title,
    required this.body,
    required this.author,
    required this.assignees,
    required this.labels,
    required this.state,
    required this.createdAt,
    required this.fetchedAt,
    this.dismissed = false,
    this.latestReview,
  });

  factory TrackedIssue.fromJson(Map<String, dynamic> json) =>
      _$TrackedIssueFromJson(json);
  Map<String, dynamic> toJson() => _$TrackedIssueToJson(this);
}
```

- [ ] **Step 2: Run build_runner**

Run: `cd flutter_app && dart run build_runner build --delete-conflicting-outputs`
Expected: Generates `tracked_issue.g.dart` with no errors.

- [ ] **Step 3: Verify no analysis errors**

Run: `cd flutter_app && flutter analyze`
Expected: No issues found.

- [ ] **Step 4: Commit**

```bash
cd /Users/jamuriano/personal-workspace/auto-pr
git add flutter_app/lib/core/models/tracked_issue.dart flutter_app/lib/core/models/tracked_issue.g.dart
git commit -m "feat(flutter): add TrackedIssue and TrackedIssueReview models

New models for GitHub issues monitored by the issue tracking pipeline.
Named TrackedIssue to avoid collision with the existing Issue model
(which represents code findings within PR reviews)."
```

---

### Task 5: Flutter — API client methods for issues

**Files:**
- Modify: `flutter_app/lib/core/api/api_client.dart`

- [ ] **Step 1: Add import and methods**

Add import at top of `api_client.dart`:
```dart
import '../models/tracked_issue.dart';
```

Add methods after `updateConfig`:

```dart
// ── Issues ────────────────────────────────────────────────────────────

Future<List<TrackedIssue>> fetchIssues() async {
  final resp = await _client.get(_uri('/issues'), headers: await _authHeaders());
  if (resp.statusCode != 200) {
    throw ApiException('GET /issues failed: ${resp.statusCode}');
  }
  final list = jsonDecode(resp.body) as List<dynamic>;
  return list
      .map((e) => TrackedIssue.fromJson(_parseIssueMap(e as Map<String, dynamic>)))
      .toList();
}

Future<Map<String, dynamic>> fetchIssue(int id) async {
  final resp = await _client.get(_uri('/issues/$id'), headers: await _authHeaders());
  if (resp.statusCode != 200) {
    throw ApiException('GET /issues/$id failed: ${resp.statusCode}');
  }
  final body = jsonDecode(resp.body) as Map<String, dynamic>;
  final issue = TrackedIssue.fromJson(
      _parseIssueMap(body['issue'] as Map<String, dynamic>));
  final reviewsRaw = body['reviews'] as List<dynamic>? ?? [];
  final reviews = reviewsRaw
      .map((r) => TrackedIssueReview.fromJson(
          _parseIssueReviewMap(r as Map<String, dynamic>)))
      .toList();
  return {'issue': issue, 'reviews': reviews};
}

Future<void> triggerIssueReview(int issueId) async {
  final resp = await _client.post(_uri('/issues/$issueId/review'),
      headers: await _authHeaders());
  if (resp.statusCode != 202) {
    throw ApiException('POST /issues/$issueId/review failed: ${resp.statusCode}');
  }
}

Future<void> dismissIssue(int issueId) async {
  final resp = await _client.post(_uri('/issues/$issueId/dismiss'),
      headers: await _authHeaders());
  if (resp.statusCode != 200) {
    throw ApiException('POST /issues/$issueId/dismiss failed: ${resp.statusCode}');
  }
}

Future<void> undismissIssue(int issueId) async {
  final resp = await _client.post(_uri('/issues/$issueId/undismiss'),
      headers: await _authHeaders());
  if (resp.statusCode != 200) {
    throw ApiException('POST /issues/$issueId/undismiss failed: ${resp.statusCode}');
  }
}

/// Parses issue JSON, ensuring nested latest_review is handled.
Map<String, dynamic> _parseIssueMap(Map<String, dynamic> json) {
  final result = Map<String, dynamic>.from(json);
  if (result['latest_review'] != null) {
    result['latest_review'] = _parseIssueReviewMap(
        result['latest_review'] as Map<String, dynamic>);
  }
  return result;
}

/// Parses issue review JSON, decoding triage/suggestions from strings if needed.
Map<String, dynamic> _parseIssueReviewMap(Map<String, dynamic> json) {
  final result = Map<String, dynamic>.from(json);
  if (result['triage'] is String) {
    result['triage'] = jsonDecode(result['triage'] as String);
  }
  if (result['suggestions'] is String) {
    result['suggestions'] = jsonDecode(result['suggestions'] as String);
  }
  result['triage'] ??= <String, dynamic>{};
  result['suggestions'] ??= <dynamic>[];
  return result;
}
```

- [ ] **Step 2: Verify no analysis errors**

Run: `cd flutter_app && flutter analyze`
Expected: No issues found.

- [ ] **Step 3: Commit**

```bash
cd /Users/jamuriano/personal-workspace/auto-pr
git add flutter_app/lib/core/api/api_client.dart
git commit -m "feat(flutter): add issue tracking API client methods

Five new methods mirroring the PR endpoints: fetchIssues, fetchIssue,
triggerIssueReview, dismissIssue, undismissIssue. Includes JSON parsing
helpers for triage/suggestions fields."
```

---

### Task 6: Flutter — Issue providers and SSE event handling

**Files:**
- Create: `flutter_app/lib/features/issues/issues_providers.dart`
- Modify: `flutter_app/lib/features/dashboard/dashboard_providers.dart`

- [ ] **Step 1: Create issues_providers.dart**

Create `flutter_app/lib/features/issues/issues_providers.dart`:

```dart
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../../core/models/tracked_issue.dart';
import '../dashboard/dashboard_providers.dart';

/// Counter incremented by SSE events to trigger issue list refresh.
final issueListRefreshProvider = StateProvider<int>((ref) => 0);

/// Tracks issues currently being reviewed, keyed by "repo:issueNumber".
final reviewingIssuesProvider = StateProvider<Set<String>>((ref) => const {});

final issuesProvider = FutureProvider<List<TrackedIssue>>((ref) async {
  ref.watch(issueListRefreshProvider);
  final api = ref.watch(apiClientProvider);
  return api.fetchIssues();
});

final issueDetailProvider =
    FutureProvider.family<Map<String, dynamic>, int>((ref, issueId) async {
  ref.watch(sseStreamProvider);
  final api = ref.watch(apiClientProvider);
  return api.fetchIssue(issueId);
});
```

- [ ] **Step 2: Add issue SSE event handling to dashboard_providers.dart**

In `flutter_app/lib/features/dashboard/dashboard_providers.dart`, add import at the top:

```dart
import '../issues/issues_providers.dart';
```

In the `_handleSseEvent` function, add new cases inside the `switch (event.type)` block, after the `'review_error'` case:

```dart
    case 'issue_detected':
      ref.read(issueListRefreshProvider.notifier).update((s) => s + 1);

    case 'issue_review_started':
      final issueNumber = (data['number'] as num?)?.toInt();
      final issueKey = (repo.isNotEmpty && issueNumber != null)
          ? '$repo:$issueNumber'
          : null;
      if (issueKey != null) {
        ref.read(reviewingIssuesProvider.notifier).update((s) => {...s, issueKey});
      }

    case 'issue_review_completed':
      final issueNumber = (data['number'] as num?)?.toInt();
      final issueKey = (repo.isNotEmpty && issueNumber != null)
          ? '$repo:$issueNumber'
          : null;
      if (issueKey != null) {
        ref.read(reviewingIssuesProvider.notifier).update((s) => s.difference({issueKey}));
      }
      ref.read(issueListRefreshProvider.notifier).update((s) => s + 1);

    case 'issue_review_error':
      final issueNumber = (data['number'] as num?)?.toInt();
      final issueKey = (repo.isNotEmpty && issueNumber != null)
          ? '$repo:$issueNumber'
          : null;
      if (issueKey != null) {
        ref.read(reviewingIssuesProvider.notifier).update((s) => s.difference({issueKey}));
      }
```

- [ ] **Step 3: Verify no analysis errors**

Run: `cd flutter_app && flutter analyze`
Expected: No issues found.

- [ ] **Step 4: Commit**

```bash
cd /Users/jamuriano/personal-workspace/auto-pr
git add flutter_app/lib/features/issues/issues_providers.dart flutter_app/lib/features/dashboard/dashboard_providers.dart
git commit -m "feat(flutter): add issue providers and SSE event handling

New Riverpod providers for issue list/detail with SSE-driven refresh.
Dashboard SSE handler extended with issue_detected, issue_review_started,
issue_review_completed, and issue_review_error events."
```

---

### Task 7: Flutter — IssuesScreen (dedicated list)

**Files:**
- Create: `flutter_app/lib/features/issues/issues_screen.dart`

- [ ] **Step 1: Create issues_screen.dart**

Create `flutter_app/lib/features/issues/issues_screen.dart`:

```dart
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import '../../core/models/tracked_issue.dart';
import '../../shared/widgets/severity_badge.dart';
import '../../shared/widgets/toast.dart';
import '../dashboard/dashboard_providers.dart';
import 'issues_providers.dart';

/// Filter state for the issues list.
final _repoFilterProvider = StateProvider<String?>((ref) => null);

class IssuesScreen extends ConsumerWidget {
  const IssuesScreen({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final issuesAsync = ref.watch(issuesProvider);
    final repoFilter = ref.watch(_repoFilterProvider);

    return issuesAsync.when(
      loading: () => const Center(child: CircularProgressIndicator()),
      error: (e, _) => Center(
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            const Icon(Icons.error_outline, size: 48, color: Colors.grey),
            const SizedBox(height: 12),
            Text('Error: $e'),
            const SizedBox(height: 16),
            FilledButton(
              onPressed: () => ref.invalidate(issuesProvider),
              child: const Text('Retry'),
            ),
          ],
        ),
      ),
      data: (issues) {
        if (issues.isEmpty) {
          return const Center(child: Text('No tracked issues'));
        }

        final repos = issues.map((i) => i.repo).toSet().toList()..sort();
        final filtered = repoFilter == null
            ? issues
            : issues.where((i) => i.repo == repoFilter).toList();

        return Column(
          children: [
            // Filter bar
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 12, 16, 4),
              child: Row(
                children: [
                  Text('Repo:', style: TextStyle(fontSize: 12, color: Colors.grey.shade500)),
                  const SizedBox(width: 8),
                  DropdownButton<String?>(
                    value: repoFilter,
                    hint: const Text('All', style: TextStyle(fontSize: 13)),
                    isDense: true,
                    underline: const SizedBox.shrink(),
                    items: [
                      const DropdownMenuItem(value: null, child: Text('All')),
                      ...repos.map((r) =>
                          DropdownMenuItem(value: r, child: Text(r, style: const TextStyle(fontSize: 13)))),
                    ],
                    onChanged: (v) => ref.read(_repoFilterProvider.notifier).state = v,
                  ),
                  const Spacer(),
                  Text('${filtered.length} issue${filtered.length == 1 ? '' : 's'}',
                      style: TextStyle(fontSize: 12, color: Colors.grey.shade500)),
                ],
              ),
            ),
            // Issue list
            Expanded(
              child: ListView.builder(
                padding: const EdgeInsets.symmetric(vertical: 4),
                itemCount: filtered.length,
                itemBuilder: (_, i) => _IssueTile(issue: filtered[i]),
              ),
            ),
          ],
        );
      },
    );
  }
}

class _IssueTile extends ConsumerStatefulWidget {
  final TrackedIssue issue;
  const _IssueTile({required this.issue});

  @override
  ConsumerState<_IssueTile> createState() => _IssueTileState();
}

class _IssueTileState extends ConsumerState<_IssueTile> {
  String get _reviewKey => '${widget.issue.repo}:${widget.issue.number}';

  Future<void> _triggerReview() async {
    ref.read(reviewingIssuesProvider.notifier).update((s) => {...s, _reviewKey});
    try {
      await ref.read(apiClientProvider).triggerIssueReview(widget.issue.id);
    } catch (e) {
      ref.read(reviewingIssuesProvider.notifier).update((s) => s.difference({_reviewKey}));
      if (mounted) showToast(context, 'Error: $e', isError: true);
    }
  }

  Future<void> _dismiss() async {
    final api = ref.read(apiClientProvider);
    try {
      await api.dismissIssue(widget.issue.id);
      ref.invalidate(issuesProvider);
      if (mounted) {
        ScaffoldMessenger.of(context).showSnackBar(SnackBar(
          duration: const Duration(seconds: 5),
          showCloseIcon: true,
          content: Text('Issue #${widget.issue.number} dismissed'),
          action: SnackBarAction(
            label: 'Undo',
            onPressed: () async {
              await api.undismissIssue(widget.issue.id);
              ref.invalidate(issuesProvider);
            },
          ),
        ));
      }
    } catch (e) {
      if (mounted) showToast(context, 'Error: $e', isError: true);
    }
  }

  @override
  Widget build(BuildContext context) {
    final issue = widget.issue;
    final reviewed = issue.latestReview != null;
    final isReviewing = ref.watch(reviewingIssuesProvider).contains(_reviewKey);
    final severity = issue.latestReview?.severity ?? '';

    return Card(
      margin: const EdgeInsets.symmetric(horizontal: 16, vertical: 3),
      child: InkWell(
        borderRadius: BorderRadius.circular(12),
        onTap: () => context.push('/issues/${issue.id}'),
        child: Padding(
          padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 12),
          child: Row(
            children: [
              // Severity bar
              Container(
                width: 4, height: 48,
                margin: const EdgeInsets.only(right: 12),
                decoration: BoxDecoration(
                  color: isReviewing
                      ? Theme.of(context).colorScheme.primary
                      : reviewed
                          ? _severityColor(severity)
                          : Colors.grey.shade600,
                  borderRadius: BorderRadius.circular(2),
                ),
              ),
              // Title + subtitle
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(issue.title,
                        style: const TextStyle(fontWeight: FontWeight.w600),
                        maxLines: 1, overflow: TextOverflow.ellipsis),
                    const SizedBox(height: 4),
                    Row(
                      children: [
                        Text('${issue.repo} · #${issue.number} · ${issue.author}',
                            style: Theme.of(context).textTheme.bodySmall),
                        const SizedBox(width: 8),
                        ...issue.labels.take(3).map((l) => Padding(
                              padding: const EdgeInsets.only(right: 4),
                              child: Container(
                                padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 1),
                                decoration: BoxDecoration(
                                  color: Theme.of(context).colorScheme.surfaceContainerHighest,
                                  borderRadius: BorderRadius.circular(4),
                                ),
                                child: Text(l.toString(),
                                    style: const TextStyle(fontSize: 10)),
                              ),
                            )),
                      ],
                    ),
                  ],
                ),
              ),
              const SizedBox(width: 12),
              // Trailing actions
              Row(
                mainAxisSize: MainAxisSize.min,
                children: [
                  if (isReviewing)
                    SizedBox(
                      width: 18, height: 18,
                      child: CircularProgressIndicator(
                        strokeWidth: 2,
                        color: Theme.of(context).colorScheme.primary,
                      ),
                    )
                  else if (reviewed)
                    SeverityBadge(severity: severity)
                  else
                    _chip('PENDING', Colors.grey.shade700),
                  const SizedBox(width: 8),
                  if (!isReviewing)
                    SizedBox(
                      height: 28,
                      child: ElevatedButton(
                        style: ElevatedButton.styleFrom(
                            padding: const EdgeInsets.symmetric(horizontal: 10),
                            textStyle: const TextStyle(fontSize: 12)),
                        onPressed: _triggerReview,
                        child: const Text('Review'),
                      ),
                    ),
                  IconButton(
                    icon: const Icon(Icons.close, size: 14),
                    tooltip: 'Dismiss issue',
                    color: Colors.grey.shade600,
                    visualDensity: VisualDensity.compact,
                    onPressed: _dismiss,
                  ),
                ],
              ),
            ],
          ),
        ),
      ),
    );
  }

  Widget _chip(String label, Color color) => Container(
        padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 3),
        decoration: BoxDecoration(color: color, borderRadius: BorderRadius.circular(4)),
        child: Text(label,
            style: const TextStyle(
                color: Colors.white, fontSize: 11, fontWeight: FontWeight.w600)),
      );

  Color _severityColor(String s) {
    switch (s.toLowerCase()) {
      case 'critical': return Colors.red.shade900;
      case 'high':     return Colors.red.shade700;
      case 'medium':   return Colors.orange.shade700;
      default:         return Colors.green.shade700;
    }
  }
}
```

- [ ] **Step 2: Verify no analysis errors**

Run: `cd flutter_app && flutter analyze`
Expected: No issues found.

- [ ] **Step 3: Commit**

```bash
cd /Users/jamuriano/personal-workspace/auto-pr
git add flutter_app/lib/features/issues/issues_screen.dart
git commit -m "feat(flutter): add IssuesScreen with repo filter and review/dismiss

Dedicated issue list view with severity bars, label chips, repo filter
dropdown, and the same review/dismiss UX pattern as PR tiles."
```

---

### Task 8: Flutter — IssueDetailScreen

**Files:**
- Create: `flutter_app/lib/features/issues/issue_detail_screen.dart`

- [ ] **Step 1: Create issue_detail_screen.dart**

Create `flutter_app/lib/features/issues/issue_detail_screen.dart`:

```dart
import 'dart:async';
import 'dart:convert';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:url_launcher/url_launcher.dart';
import '../../core/models/tracked_issue.dart';
import '../../shared/widgets/severity_badge.dart';
import '../../shared/widgets/toast.dart';
import '../dashboard/dashboard_providers.dart';
import 'issues_providers.dart';

class IssueDetailScreen extends ConsumerStatefulWidget {
  final int issueId;
  const IssueDetailScreen({super.key, required this.issueId});

  @override
  ConsumerState<IssueDetailScreen> createState() => _IssueDetailScreenState();
}

class _IssueDetailScreenState extends ConsumerState<IssueDetailScreen> {
  bool _reviewing = false;
  Timer? _reviewTimeout;

  @override
  void dispose() {
    _reviewTimeout?.cancel();
    super.dispose();
  }

  void _startReviewing() {
    setState(() => _reviewing = true);
    _reviewTimeout?.cancel();
    _reviewTimeout = Timer(const Duration(seconds: 90), () {
      if (mounted) setState(() => _reviewing = false);
    });
  }

  void _stopReviewing() {
    _reviewTimeout?.cancel();
    if (mounted) setState(() => _reviewing = false);
  }

  Future<void> _dismiss(BuildContext context) async {
    final api = ref.read(apiClientProvider);
    try {
      await api.dismissIssue(widget.issueId);
      ref.invalidate(issuesProvider);
      if (context.mounted) {
        final messenger = ScaffoldMessenger.of(context);
        context.canPop() ? context.pop() : context.go('/');
        messenger.showSnackBar(
          SnackBar(
            duration: const Duration(seconds: 5),
            showCloseIcon: true,
            content: const Text('Issue dismissed'),
            action: SnackBarAction(
              label: 'Undo',
              onPressed: () async {
                await api.undismissIssue(widget.issueId);
                ref.invalidate(issuesProvider);
              },
            ),
          ),
        );
      }
    } catch (e) {
      if (!context.mounted) return;
      showToast(context, 'Error: $e', isError: true);
    }
  }

  Future<void> _trigger() async {
    _startReviewing();
    final api = ref.read(apiClientProvider);
    try {
      await api.triggerIssueReview(widget.issueId);
      ref.invalidate(issueDetailProvider(widget.issueId));
    } catch (e) {
      _stopReviewing();
      if (mounted) showToast(context, 'Error: $e', isError: true);
    }
  }

  @override
  Widget build(BuildContext context) {
    final detailAsync = ref.watch(issueDetailProvider(widget.issueId));

    // SSE listener for real-time review updates
    ref.listen(sseStreamProvider, (_, next) {
      next.whenData((event) {
        try {
          final data = jsonDecode(event.data) as Map<String, dynamic>;
          final issueId = (data['issue_id'] as num?)?.toInt();

          switch (event.type) {
            case 'issue_review_started':
              if (issueId == widget.issueId) {
                _startReviewing();
              }
            case 'issue_review_completed':
              if (issueId == widget.issueId) {
                _stopReviewing();
                ref.invalidate(issueDetailProvider(widget.issueId));
              }
            case 'issue_review_error':
              if (issueId == widget.issueId) {
                _stopReviewing();
                final error = data['error'] as String? ?? 'Unknown error';
                if (mounted) showToast(context, 'Review failed: $error', isError: true);
              }
          }
        } catch (_) {}
      });
    });

    final detailData = detailAsync.valueOrNull;
    final reviews = detailData?['reviews'] as List<TrackedIssueReview>? ?? [];
    final hasReviews = reviews.isNotEmpty;
    final issue = detailData?['issue'] as TrackedIssue?;

    final reviewKey = issue != null ? '${issue.repo}:${issue.number}' : null;
    final isReviewingShared =
        reviewKey != null && ref.watch(reviewingIssuesProvider).contains(reviewKey);
    final reviewing = _reviewing || isReviewingShared;

    return Scaffold(
      appBar: AppBar(
        title: const Text('Issue Review'),
        leading: IconButton(
          icon: const Icon(Icons.arrow_back),
          onPressed: () => context.canPop() ? context.pop() : context.go('/'),
        ),
        actions: [
          if (reviewing)
            const Padding(
              padding: EdgeInsets.symmetric(horizontal: 16),
              child: SizedBox(
                  width: 20, height: 20,
                  child: CircularProgressIndicator(strokeWidth: 2)),
            )
          else ...[
            ElevatedButton.icon(
              icon: const Icon(Icons.refresh, size: 16),
              label: Text(hasReviews ? 'Re-review' : 'Review'),
              onPressed: _trigger,
            ),
            const SizedBox(width: 8),
            OutlinedButton.icon(
              icon: const Icon(Icons.visibility_off_outlined, size: 16),
              label: const Text('Dismiss'),
              onPressed: () => _dismiss(context),
            ),
          ],
          const SizedBox(width: 12),
        ],
      ),
      body: Column(
        children: [
          if (reviewing)
            LinearProgressIndicator(
              minHeight: 3,
              backgroundColor: Theme.of(context).colorScheme.surfaceContainerHighest,
            ),
          Expanded(
            child: detailAsync.when(
              loading: () => const Center(child: CircularProgressIndicator()),
              error: (e, _) => Center(child: Text('Error: $e')),
              data: (data) {
                final issue = data['issue'] as TrackedIssue;
                final reviews = data['reviews'] as List<TrackedIssueReview>;
                return Row(
                  children: [
                    Expanded(flex: 2, child: _ReviewPanel(issue: issue, reviews: reviews)),
                    const VerticalDivider(width: 1),
                    Expanded(flex: 1, child: _IssueMetaPanel(issue: issue)),
                  ],
                );
              },
            ),
          ),
        ],
      ),
    );
  }
}

class _ReviewPanel extends StatelessWidget {
  final TrackedIssue issue;
  final List<TrackedIssueReview> reviews;
  const _ReviewPanel({required this.issue, required this.reviews});

  @override
  Widget build(BuildContext context) {
    return SingleChildScrollView(
      padding: const EdgeInsets.all(16),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(issue.title, style: Theme.of(context).textTheme.headlineSmall),
          Text('${issue.repo} #${issue.number} by ${issue.author}',
              style: Theme.of(context).textTheme.bodySmall),
          const SizedBox(height: 16),
          if (reviews.isEmpty)
            const Text('No reviews yet.')
          else
            ...reviews.map((rev) => _IssueReviewCard(review: rev)),
        ],
      ),
    );
  }
}

class _IssueReviewCard extends StatelessWidget {
  final TrackedIssueReview review;
  const _IssueReviewCard({required this.review});

  @override
  Widget build(BuildContext context) {
    return Card(
      margin: const EdgeInsets.only(bottom: 12),
      child: Padding(
        padding: const EdgeInsets.all(12),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Text('Reviewed by ${review.cliUsed}',
                    style: Theme.of(context).textTheme.labelSmall),
                const SizedBox(width: 8),
                Container(
                  padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 2),
                  decoration: BoxDecoration(
                    color: Theme.of(context).colorScheme.surfaceContainerHighest,
                    borderRadius: BorderRadius.circular(4),
                  ),
                  child: Text(review.actionTaken,
                      style: const TextStyle(fontSize: 10)),
                ),
                const Spacer(),
                SeverityBadge(severity: review.severity),
              ],
            ),
            const SizedBox(height: 8),
            Text(review.summary),
            // Triage block
            if (review.category.isNotEmpty) ...[
              const SizedBox(height: 8),
              Text('Classification', style: Theme.of(context).textTheme.labelMedium),
              Padding(
                padding: const EdgeInsets.only(top: 4, left: 8),
                child: Text('Category: ${review.category}'),
              ),
            ],
            // Suggestions
            if (review.suggestions.isNotEmpty) ...[
              const SizedBox(height: 8),
              Text('Suggestions', style: Theme.of(context).textTheme.labelMedium),
              ...review.suggestions.map((s) => Padding(
                    padding: const EdgeInsets.only(top: 4, left: 8),
                    child: Row(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        const Icon(Icons.lightbulb_outline, size: 14),
                        const SizedBox(width: 4),
                        Expanded(child: Text(s.toString())),
                      ],
                    ),
                  )),
            ],
          ],
        ),
      ),
    );
  }
}

class _IssueMetaPanel extends StatelessWidget {
  final TrackedIssue issue;
  const _IssueMetaPanel({required this.issue});

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.all(16),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text('Details', style: Theme.of(context).textTheme.titleMedium),
          const SizedBox(height: 12),
          _row(context, 'Repo', issue.repo),
          _row(context, 'Number', '#${issue.number}'),
          _row(context, 'Author', issue.author),
          _row(context, 'State', issue.state),
          _row(context, 'Created',
              issue.createdAt.toLocal().toString().substring(0, 16)),
          if (issue.assignees.isNotEmpty)
            _row(context, 'Assignees', issue.assignees.join(', ')),
          if (issue.labels.isNotEmpty) ...[
            const SizedBox(height: 8),
            Wrap(
              spacing: 4,
              runSpacing: 4,
              children: issue.labels
                  .map((l) => Chip(
                        label: Text(l.toString(), style: const TextStyle(fontSize: 11)),
                        materialTapTargetSize: MaterialTapTargetSize.shrinkWrap,
                        visualDensity: VisualDensity.compact,
                      ))
                  .toList(),
            ),
          ],
          const SizedBox(height: 12),
          OutlinedButton.icon(
            icon: const Icon(Icons.open_in_browser),
            label: const Text('Open on GitHub'),
            onPressed: () {
              final uri = Uri.tryParse(
                  'https://github.com/${issue.repo}/issues/${issue.number}');
              if (uri != null) launchUrl(uri);
            },
          ),
        ],
      ),
    );
  }

  Widget _row(BuildContext context, String label, String value) {
    return Padding(
      padding: const EdgeInsets.only(bottom: 8),
      child: Row(
        children: [
          SizedBox(
              width: 72,
              child: Text('$label:',
                  style: const TextStyle(fontWeight: FontWeight.w600))),
          Expanded(child: Text(value)),
        ],
      ),
    );
  }
}
```

- [ ] **Step 2: Verify no analysis errors**

Run: `cd flutter_app && flutter analyze`
Expected: No issues found.

- [ ] **Step 3: Commit**

```bash
cd /Users/jamuriano/personal-workspace/auto-pr
git add flutter_app/lib/features/issues/issue_detail_screen.dart
git commit -m "feat(flutter): add IssueDetailScreen with two-panel layout

Two-panel view mirroring PRDetailScreen: review history on the left
with triage classification and suggestions, issue metadata on the right
with labels, assignees, and GitHub link. Real-time SSE updates."
```

---

### Task 9: Flutter — Router update and dashboard unification

**Files:**
- Modify: `flutter_app/lib/shared/router.dart`
- Modify: `flutter_app/lib/features/dashboard/dashboard_screen.dart`

- [ ] **Step 1: Add /issues/:id route**

In `flutter_app/lib/shared/router.dart`, add import:

```dart
import '../features/issues/issue_detail_screen.dart';
```

Add route after the `/prs/:id` route:

```dart
GoRoute(
  path: '/issues/:id',
  builder: (context, state) {
    final id = int.parse(state.pathParameters['id']!);
    return IssueDetailScreen(issueId: id);
  },
),
```

- [ ] **Step 2: Update dashboard — add Issues tab and Activity feed**

In `flutter_app/lib/features/dashboard/dashboard_screen.dart`:

1. Add imports at the top:
```dart
import '../../core/models/tracked_issue.dart';
import '../issues/issues_screen.dart';
import '../issues/issues_providers.dart';
```

2. Change `DefaultTabController` length from 5 to 6.

3. Update the `TabBar` tabs list — replace the first tab and add the new Issues tab:
```dart
bottom: const TabBar(
  tabs: [
    Tab(icon: Icon(Icons.dashboard),       text: 'Activity'),
    Tab(icon: Icon(Icons.bug_report),      text: 'Issues'),
    Tab(icon: Icon(Icons.folder_outlined), text: 'Repositories'),
    Tab(icon: Icon(Icons.auto_awesome),    text: 'Prompts'),
    Tab(icon: Icon(Icons.smart_toy),       text: 'Agents'),
    Tab(icon: Icon(Icons.bar_chart),       text: 'Stats'),
  ],
),
```

4. Update `TabBarView` children:
```dart
body: const TabBarView(
  children: [
    _ActivityTab(),
    IssuesScreen(),
    ReposScreen(),
    AgentsScreen(),
    CLIAgentsScreen(),
    StatsScreen(),
  ],
),
```

5. Update the refresh button to also invalidate issues:
```dart
IconButton(
  icon: const Icon(Icons.refresh),
  onPressed: () {
    ref.invalidate(prsProvider);
    ref.invalidate(issuesProvider);
    ref.invalidate(statsProvider);
  },
),
```

6. Rename `_ReviewsTab` to `_ActivityTab` and add the Issues section. Replace the entire `_ReviewsTab` / `_ReviewsTabState` classes with:

```dart
class _ActivityTab extends ConsumerStatefulWidget {
  const _ActivityTab();
  @override
  ConsumerState<_ActivityTab> createState() => _ActivityTabState();
}

class _ActivityTabState extends ConsumerState<_ActivityTab> {
  bool _reviewsExpanded = true;
  bool _prsExpanded     = true;
  bool _issuesExpanded  = true;

  @override
  Widget build(BuildContext context) {
    final prsAsync    = ref.watch(prsProvider);
    final issuesAsync = ref.watch(issuesProvider);
    final meAsync     = ref.watch(meProvider);
    final sort        = ref.watch(_reviewsSortProvider);

    // Combine loading states
    if (prsAsync.isLoading && issuesAsync.isLoading) {
      return const Center(child: CircularProgressIndicator());
    }
    if (prsAsync.hasError && issuesAsync.hasError) {
      return _errorView(context, prsAsync.error!);
    }

    final prs    = prsAsync.valueOrNull ?? [];
    final issues = issuesAsync.valueOrNull ?? [];
    final me     = meAsync.valueOrNull ?? '';

    final myReviews = _sortedPRs(prs.where((p) =>
        p.repo.isNotEmpty && p.author.toLowerCase() != me.toLowerCase()).toList(), sort);
    final myPRs = _sortedPRs(prs.where((p) =>
        p.repo.isNotEmpty && p.author.toLowerCase() == me.toLowerCase()).toList(), sort);

    if (prs.isEmpty && issues.isEmpty) {
      return const Center(child: Text('No activity yet'));
    }

    return ListView(
      padding: const EdgeInsets.symmetric(vertical: 8),
      children: [
        // Sort selector
        Padding(
          padding: const EdgeInsets.fromLTRB(16, 8, 16, 4),
          child: Row(
            children: [
              Text('Sort:',
                  style: TextStyle(fontSize: 12, color: Colors.grey.shade500)),
              const SizedBox(width: 8),
              _SortButton(
                label: 'Priority',
                icon: Icons.sort,
                selected: sort == _SortMode.priority,
                onTap: () => ref.read(_reviewsSortProvider.notifier).state = _SortMode.priority,
              ),
              const SizedBox(width: 6),
              _SortButton(
                label: 'Newest',
                icon: Icons.schedule,
                selected: sort == _SortMode.newest,
                onTap: () => ref.read(_reviewsSortProvider.notifier).state = _SortMode.newest,
              ),
            ],
          ),
        ),
        if (myReviews.isNotEmpty) ...[
          _CollapseHeader(
            title: 'My Reviews',
            count: myReviews.length,
            expanded: _reviewsExpanded,
            onToggle: () => setState(() => _reviewsExpanded = !_reviewsExpanded),
          ),
          if (_reviewsExpanded)
            ...myReviews.map((pr) => _PRTile(pr: pr)),
        ],
        if (myPRs.isNotEmpty) ...[
          _CollapseHeader(
            title: 'My PRs',
            count: myPRs.length,
            expanded: _prsExpanded,
            onToggle: () => setState(() => _prsExpanded = !_prsExpanded),
          ),
          if (_prsExpanded)
            ...myPRs.map((pr) => _PRTile(pr: pr)),
        ],
        if (issues.isNotEmpty) ...[
          _CollapseHeader(
            title: 'Tracked Issues',
            count: issues.length,
            expanded: _issuesExpanded,
            onToggle: () => setState(() => _issuesExpanded = !_issuesExpanded),
          ),
          if (_issuesExpanded)
            ...issues.map((issue) => _IssueActivityTile(issue: issue)),
        ],
      ],
    );
  }

  Widget _errorView(BuildContext context, Object e) {
    return Center(
      child: Column(
        mainAxisSize: MainAxisSize.min,
        children: [
          const Icon(Icons.wifi_off, size: 48, color: Colors.grey),
          const SizedBox(height: 12),
          const Text('Could not reach the Heimdallm daemon.',
              style: TextStyle(fontWeight: FontWeight.w600)),
          const SizedBox(height: 4),
          const Text('Go to Settings to configure and start it.',
              style: TextStyle(color: Colors.grey)),
          const SizedBox(height: 16),
          Row(
            mainAxisSize: MainAxisSize.min,
            children: [
              TextButton(
                  onPressed: () {
                    ref.invalidate(prsProvider);
                    ref.invalidate(issuesProvider);
                  },
                  child: const Text('Retry')),
              const SizedBox(width: 8),
              FilledButton.icon(
                icon: const Icon(Icons.settings, size: 16),
                label: const Text('Settings'),
                onPressed: () => context.push('/config'),
              ),
            ],
          ),
        ],
      ),
    );
  }
}
```

7. Add the `_IssueActivityTile` widget (after `_PRTile`):

```dart
class _IssueActivityTile extends StatelessWidget {
  final TrackedIssue issue;
  const _IssueActivityTile({required this.issue});

  @override
  Widget build(BuildContext context) {
    final reviewed = issue.latestReview != null;
    final severity = issue.latestReview?.severity ?? '';

    return Card(
      margin: const EdgeInsets.symmetric(horizontal: 16, vertical: 3),
      child: InkWell(
        borderRadius: BorderRadius.circular(12),
        onTap: () => context.push('/issues/${issue.id}'),
        child: Padding(
          padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 12),
          child: Row(
            children: [
              Container(
                width: 4, height: 48,
                margin: const EdgeInsets.only(right: 12),
                decoration: BoxDecoration(
                  color: reviewed ? _severityColor(severity) : Colors.grey.shade600,
                  borderRadius: BorderRadius.circular(2),
                ),
              ),
              // Issue icon to distinguish from PRs
              Icon(Icons.bug_report, size: 16, color: Colors.grey.shade500),
              const SizedBox(width: 8),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(issue.title,
                        style: const TextStyle(fontWeight: FontWeight.w600),
                        maxLines: 1, overflow: TextOverflow.ellipsis),
                    const SizedBox(height: 4),
                    Text('${issue.repo} · #${issue.number} · ${issue.author}',
                        style: Theme.of(context).textTheme.bodySmall,
                        maxLines: 1, overflow: TextOverflow.ellipsis),
                  ],
                ),
              ),
              const SizedBox(width: 12),
              if (reviewed)
                SeverityBadge(severity: severity)
              else
                Container(
                  padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 3),
                  decoration: BoxDecoration(
                      color: Colors.grey.shade700,
                      borderRadius: BorderRadius.circular(4)),
                  child: const Text('PENDING',
                      style: TextStyle(
                          color: Colors.white, fontSize: 11, fontWeight: FontWeight.w600)),
                ),
            ],
          ),
        ),
      ),
    );
  }

  Color _severityColor(String s) {
    switch (s.toLowerCase()) {
      case 'critical': return Colors.red.shade900;
      case 'high':     return Colors.red.shade700;
      case 'medium':   return Colors.orange.shade700;
      default:         return Colors.green.shade700;
    }
  }
}
```

- [ ] **Step 3: Verify no analysis errors**

Run: `cd flutter_app && flutter analyze`
Expected: No issues found.

- [ ] **Step 4: Commit**

```bash
cd /Users/jamuriano/personal-workspace/auto-pr
git add flutter_app/lib/shared/router.dart flutter_app/lib/features/dashboard/dashboard_screen.dart
git commit -m "feat(flutter): unified Activity tab + Issues tab in dashboard

Dashboard grows from 5 to 6 tabs. Reviews tab becomes Activity
(PRs + tracked issues in a unified feed). New Issues tab shows
the dedicated IssuesScreen with filters. Router adds /issues/:id."
```

---

### Task 10: Full verification

**Files:** None — verification only.

- [ ] **Step 1: Run daemon tests**

Run: `cd daemon && make test`
Expected: All tests pass.

- [ ] **Step 2: Build daemon binary**

Run: `cd daemon && go build -o bin/heimdallm ./cmd/heimdallm`
Expected: Build succeeds.

- [ ] **Step 3: Run flutter analyze**

Run: `cd flutter_app && flutter analyze`
Expected: No issues found.

- [ ] **Step 4: Run flutter tests**

Run: `cd flutter_app && flutter test`
Expected: All tests pass (existing tests should not break).

- [ ] **Step 5: Verify build_runner output is up to date**

Run: `cd flutter_app && dart run build_runner build --delete-conflicting-outputs`
Expected: No files changed (already generated in Task 4).

- [ ] **Step 6: Final commit if any generated files changed**

Only if Step 5 produced changes:
```bash
cd /Users/jamuriano/personal-workspace/auto-pr
git add flutter_app/lib/core/models/tracked_issue.g.dart
git commit -m "chore: regenerate build_runner output"
```

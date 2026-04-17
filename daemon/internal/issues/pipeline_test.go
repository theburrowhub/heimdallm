package issues_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/executor"
	"github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/issues"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/heimdallm/daemon/internal/store"
)

// ── fakes ────────────────────────────────────────────────────────────────────

type fakeStore struct {
	upserts      []*store.Issue
	reviews      []*store.IssueReview
	nextIssueID  int64
	nextReviewID int64
	upsertErr    error
	insertErr    error
}

func (f *fakeStore) UpsertIssue(i *store.Issue) (int64, error) {
	if f.upsertErr != nil {
		return 0, f.upsertErr
	}
	f.nextIssueID++
	copy := *i
	copy.ID = f.nextIssueID
	f.upserts = append(f.upserts, &copy)
	return f.nextIssueID, nil
}

func (f *fakeStore) InsertIssueReview(r *store.IssueReview) (int64, error) {
	if f.insertErr != nil {
		return 0, f.insertErr
	}
	f.nextReviewID++
	copy := *r
	copy.ID = f.nextReviewID
	f.reviews = append(f.reviews, &copy)
	return f.nextReviewID, nil
}

type fakeGH struct {
	postCalls     []postCall
	commentsByKey map[string][]github.Comment
	postErr       error
	commentsErr   error
}

type postCall struct {
	Repo   string
	Number int
	Body   string
}

func (f *fakeGH) PostComment(repo string, number int, body string) error {
	f.postCalls = append(f.postCalls, postCall{Repo: repo, Number: number, Body: body})
	return f.postErr
}

func (f *fakeGH) FetchComments(repo string, number int) ([]github.Comment, error) {
	if f.commentsErr != nil {
		return nil, f.commentsErr
	}
	return f.commentsByKey[fmt.Sprintf("%s#%d", repo, number)], nil
}

type fakeExec struct {
	detectCLI  string
	detectErr  error
	rawOutput  []byte
	rawErr     error
	lastPrompt string
	lastOpts   executor.ExecOptions
	lastCLI    string
}

func (f *fakeExec) Detect(primary, fallback string) (string, error) {
	if f.detectErr != nil {
		return "", f.detectErr
	}
	if f.detectCLI == "" {
		return primary, nil
	}
	return f.detectCLI, nil
}

func (f *fakeExec) ExecuteRaw(cli, prompt string, opts executor.ExecOptions) ([]byte, error) {
	f.lastCLI = cli
	f.lastPrompt = prompt
	f.lastOpts = opts
	if f.rawErr != nil {
		return nil, f.rawErr
	}
	return f.rawOutput, nil
}

type fakeBroker struct {
	mu     sync.Mutex
	events []sse.Event
}

func (b *fakeBroker) Publish(e sse.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, e)
}

func (b *fakeBroker) types() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.events))
	for i, e := range b.events {
		out[i] = e.Type
	}
	return out
}

type fakeNotifier struct {
	calls []string
}

func (n *fakeNotifier) Notify(title, message string) {
	n.calls = append(n.calls, title+": "+message)
}

// validResult is a sample JSON triage payload returned by the fake executor.
const validResult = `
{
  "summary": "User cannot log in after upgrade.",
  "triage": {
    "severity": "high",
    "category": "bug",
    "suggested_assignee": "alice"
  },
  "suggestions": ["reproduce locally", "check auth migration"],
  "severity": "high"
}
`

func newIssue(mode config.IssueMode) *github.Issue {
	return &github.Issue{
		ID:        12345,
		Number:    7,
		Title:     "Login broken",
		Body:      "After upgrade, login fails with 500.",
		Repo:      "org/repo",
		State:     "open",
		User:      github.User{Login: "reporter"},
		Labels:    []github.Label{{Name: "bug"}},
		Assignees: []github.User{{Login: "alice"}},
		CreatedAt: time.Now().Add(-1 * time.Hour),
		UpdatedAt: time.Now(),
		Mode:      mode,
	}
}

// ── happy path ───────────────────────────────────────────────────────────────

func TestPipeline_RunHappyPath(t *testing.T) {
	s := &fakeStore{}
	gh := &fakeGH{}
	exec := &fakeExec{detectCLI: "claude", rawOutput: []byte(validResult)}
	broker := &fakeBroker{}
	notify := &fakeNotifier{}
	p := issues.New(s, gh, exec, broker, notify)

	issue := newIssue(config.IssueModeReviewOnly)
	rev, err := p.Run(issue, issues.RunOptions{Primary: "claude"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rev == nil {
		t.Fatal("nil review returned")
	}

	// Issue upserted once with JSON-encoded labels/assignees.
	if len(s.upserts) != 1 {
		t.Fatalf("expected 1 upsert, got %d", len(s.upserts))
	}
	up := s.upserts[0]
	var labels []string
	_ = json.Unmarshal([]byte(up.Labels), &labels)
	if len(labels) != 1 || labels[0] != "bug" {
		t.Errorf("labels JSON wrong: %q", up.Labels)
	}
	var assignees []string
	_ = json.Unmarshal([]byte(up.Assignees), &assignees)
	if len(assignees) != 1 || assignees[0] != "alice" {
		t.Errorf("assignees JSON wrong: %q", up.Assignees)
	}

	// Comment posted to GitHub.
	if len(gh.postCalls) != 1 || gh.postCalls[0].Number != 7 {
		t.Fatalf("expected 1 PostComment on #7, got %+v", gh.postCalls)
	}
	if !strings.Contains(gh.postCalls[0].Body, "Heimdallm triage") {
		t.Errorf("body missing heading: %q", gh.postCalls[0].Body)
	}
	if !strings.Contains(gh.postCalls[0].Body, "reproduce locally") {
		t.Errorf("body missing suggestion: %q", gh.postCalls[0].Body)
	}

	// Review persisted with action_taken=review_only.
	if len(s.reviews) != 1 {
		t.Fatalf("expected 1 review stored, got %d", len(s.reviews))
	}
	storedRev := s.reviews[0]
	if storedRev.ActionTaken != string(config.IssueModeReviewOnly) {
		t.Errorf("action_taken=%q, want review_only", storedRev.ActionTaken)
	}
	if !strings.Contains(storedRev.Triage, `"category":"bug"`) {
		t.Errorf("triage JSON not persisted correctly: %q", storedRev.Triage)
	}
	if !strings.Contains(storedRev.Suggestions, "reproduce locally") {
		t.Errorf("suggestions JSON missing: %q", storedRev.Suggestions)
	}

	// SSE sequence: detected → started → completed.
	types := broker.types()
	want := []string{sse.EventIssueDetected, sse.EventIssueReviewStarted, sse.EventIssueReviewCompleted}
	if !stringsEqual(types, want) {
		t.Errorf("SSE sequence = %v, want %v", types, want)
	}

	if len(notify.calls) != 2 {
		t.Errorf("expected 2 notifications (start + complete), got %d", len(notify.calls))
	}
}

// ── fallback: develop without local_dir → review_only ────────────────────────

func TestPipeline_DevelopFallsBackToReviewOnlyWithoutLocalDir(t *testing.T) {
	s := &fakeStore{}
	gh := &fakeGH{}
	exec := &fakeExec{detectCLI: "claude", rawOutput: []byte(validResult)}
	p := issues.New(s, gh, exec, &fakeBroker{}, nil)

	issue := newIssue(config.IssueModeDevelop)
	// WorkDir zero → no working tree → downgrade to review_only.
	rev, err := p.Run(issue, issues.RunOptions{Primary: "claude"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rev.ActionTaken != string(config.IssueModeReviewOnly) {
		t.Errorf("fallback should store action_taken=review_only, got %q", rev.ActionTaken)
	}
	if len(gh.postCalls) != 1 {
		t.Errorf("fallback should still post a review_only comment, got %d calls", len(gh.postCalls))
	}
	// Prompt context must match the mode-selection logic: no working
	// directory means the LLM should not be told to read a repo.
	if strings.Contains(exec.lastPrompt, "read access to the repository") {
		t.Errorf("prompt leaks local-dir instruction when there is no WorkDir")
	}
}

func TestPipeline_DevelopWithLocalDirIsNotYetSupportedHere(t *testing.T) {
	// #27 owns the auto_implement path. Until that lands, the review_only
	// pipeline must refuse rather than silently behaving like review_only when
	// a working tree IS available.
	s := &fakeStore{}
	gh := &fakeGH{}
	exec := &fakeExec{}
	p := issues.New(s, gh, exec, nil, nil)

	issue := newIssue(config.IssueModeDevelop)
	_, err := p.Run(issue, issues.RunOptions{
		Primary:  "claude",
		ExecOpts: executor.ExecOptions{WorkDir: "/tmp/repo"},
	})
	if err == nil {
		t.Fatal("expected error for develop+local_dir until #27 lands")
	}
	if !strings.Contains(err.Error(), "auto_implement") {
		t.Errorf("error should point at auto_implement/#27, got: %v", err)
	}
}

func TestPipeline_IgnoreModeRejectedWithItsOwnError(t *testing.T) {
	// The fetcher normally filters ignore-classified issues out, but the
	// pipeline must surface a specific error (not the auto_implement one)
	// if one ever sneaks in. Regression guard for Muriano's review feedback.
	s := &fakeStore{}
	p := issues.New(s, &fakeGH{}, &fakeExec{}, nil, nil)

	issue := newIssue(config.IssueModeIgnore)
	_, err := p.Run(issue, issues.RunOptions{Primary: "claude"})
	if err == nil {
		t.Fatal("expected error for ignore-classified issue")
	}
	if strings.Contains(err.Error(), "auto_implement") {
		t.Errorf("ignore-classified issue should not mention auto_implement, got: %v", err)
	}
	if !strings.Contains(err.Error(), "ignore") {
		t.Errorf("error should mention the ignore mode, got: %v", err)
	}
}

func TestPipeline_StripsLeadingAtInSuggestedAssignee(t *testing.T) {
	// If the LLM happens to include '@' in SuggestedAssignee the Markdown
	// template must still render a single '@alice', not '@@alice'.
	raw := `
	{
	  "summary": "bug",
	  "triage": {"severity":"low","category":"bug","suggested_assignee":"@alice"},
	  "suggestions": [],
	  "severity": "low"
	}`
	s := &fakeStore{}
	gh := &fakeGH{}
	exec := &fakeExec{detectCLI: "claude", rawOutput: []byte(raw)}
	p := issues.New(s, gh, exec, nil, nil)

	if _, err := p.Run(newIssue(config.IssueModeReviewOnly), issues.RunOptions{Primary: "claude"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(gh.postCalls) != 1 {
		t.Fatalf("expected 1 PostComment, got %d", len(gh.postCalls))
	}
	body := gh.postCalls[0].Body
	if strings.Contains(body, "@@alice") {
		t.Errorf("body has double @@alice: %q", body)
	}
	if !strings.Contains(body, "@alice") {
		t.Errorf("body missing single @alice mention: %q", body)
	}
}

// ── robustness: LLM output, comments, postcomment ────────────────────────────

func TestPipeline_HandlesMarkdownWrappedJSON(t *testing.T) {
	wrapped := "```json\n" + validResult + "\n```\nextra trailing text"
	s := &fakeStore{}
	gh := &fakeGH{}
	exec := &fakeExec{detectCLI: "claude", rawOutput: []byte(wrapped)}
	p := issues.New(s, gh, exec, nil, nil)

	rev, err := p.Run(newIssue(config.IssueModeReviewOnly), issues.RunOptions{Primary: "claude"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rev.Summary == "" {
		t.Errorf("summary not parsed out of wrapped JSON")
	}
}

func TestPipeline_MissingTopSeverityFallsBackToTriage(t *testing.T) {
	noTopSeverity := `
	{ "summary": "x",
	  "triage": {"severity":"medium","category":"bug","suggested_assignee":""},
	  "suggestions": []
	}`
	s := &fakeStore{}
	gh := &fakeGH{}
	exec := &fakeExec{detectCLI: "claude", rawOutput: []byte(noTopSeverity)}
	broker := &fakeBroker{}
	p := issues.New(s, gh, exec, broker, nil)

	_, err := p.Run(newIssue(config.IssueModeReviewOnly), issues.RunOptions{Primary: "claude"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// completion event carries severity; check it is not empty.
	evts := broker.events
	if len(evts) == 0 {
		t.Fatal("no SSE events emitted")
	}
	last := evts[len(evts)-1]
	if !strings.Contains(last.Data, `"severity":"medium"`) {
		t.Errorf("completion event should carry inherited severity, got %q", last.Data)
	}
}

func TestPipeline_PostCommentErrorDoesNotAbort(t *testing.T) {
	s := &fakeStore{}
	gh := &fakeGH{postErr: errors.New("rate limited")}
	exec := &fakeExec{detectCLI: "claude", rawOutput: []byte(validResult)}
	broker := &fakeBroker{}
	p := issues.New(s, gh, exec, broker, nil)

	_, err := p.Run(newIssue(config.IssueModeReviewOnly), issues.RunOptions{Primary: "claude"})
	if err != nil {
		t.Fatalf("PostComment errors must not abort the pipeline, got: %v", err)
	}
	if len(s.reviews) != 1 {
		t.Errorf("review should still be persisted locally, got %d", len(s.reviews))
	}
	// completion event should carry post_ok=false.
	last := broker.events[len(broker.events)-1]
	if !strings.Contains(last.Data, `"post_ok":false`) {
		t.Errorf("completion event should flag post_ok=false, got %q", last.Data)
	}
}

func TestPipeline_FetchCommentsErrorIsNonFatal(t *testing.T) {
	s := &fakeStore{}
	gh := &fakeGH{commentsErr: errors.New("comments API broken")}
	exec := &fakeExec{detectCLI: "claude", rawOutput: []byte(validResult)}
	p := issues.New(s, gh, exec, nil, nil)

	if _, err := p.Run(newIssue(config.IssueModeReviewOnly), issues.RunOptions{Primary: "claude"}); err != nil {
		t.Fatalf("comment fetch failure must not abort, got: %v", err)
	}
}

// ── error paths → SSE issue_review_error ────────────────────────────────────

func TestPipeline_CLIDetectFailureEmitsErrorEvent(t *testing.T) {
	s := &fakeStore{}
	gh := &fakeGH{}
	exec := &fakeExec{detectErr: errors.New("no CLI available")}
	broker := &fakeBroker{}
	p := issues.New(s, gh, exec, broker, nil)

	_, err := p.Run(newIssue(config.IssueModeReviewOnly), issues.RunOptions{Primary: "claude"})
	if err == nil {
		t.Fatal("expected error for CLI detect failure")
	}
	types := broker.types()
	if types[len(types)-1] != sse.EventIssueReviewError {
		t.Errorf("expected error SSE as last event, got %v", types)
	}
}

func TestPipeline_BadJSONEmitsErrorEvent(t *testing.T) {
	s := &fakeStore{}
	gh := &fakeGH{}
	exec := &fakeExec{detectCLI: "claude", rawOutput: []byte("not json at all")}
	broker := &fakeBroker{}
	p := issues.New(s, gh, exec, broker, nil)

	_, err := p.Run(newIssue(config.IssueModeReviewOnly), issues.RunOptions{Primary: "claude"})
	if err == nil {
		t.Fatal("expected parse error")
	}
	types := broker.types()
	if types[len(types)-1] != sse.EventIssueReviewError {
		t.Errorf("expected issue_review_error, got %v", types)
	}
}

func TestPipeline_NilIssueIsRejected(t *testing.T) {
	p := issues.New(&fakeStore{}, &fakeGH{}, &fakeExec{}, nil, nil)
	if _, err := p.Run(nil, issues.RunOptions{}); err == nil {
		t.Fatal("expected error for nil issue")
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func stringsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

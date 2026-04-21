package issues_test

import (
	"context"
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
	prs          []*store.PR
	nextIssueID  int64
	nextReviewID int64
	nextPRID     int64
	upsertErr    error
	insertErr    error
	upsertPRErr  error
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

func (f *fakeStore) UpsertPR(pr *store.PR) (int64, error) {
	if f.upsertPRErr != nil {
		return 0, f.upsertPRErr
	}
	f.nextPRID++
	copy := *pr
	copy.ID = f.nextPRID
	f.prs = append(f.prs, &copy)
	return f.nextPRID, nil
}

type fakeGH struct {
	postCalls     []postCall
	commentsByKey map[string][]github.Comment
	postErr       error
	commentsErr   error

	// auto_implement-specific knobs; review_only tests leave them zero.
	defaultBranch    string
	defaultBranchErr error
	createPRCalls    []prCall
	createPRNumber   int
	createPRID       int64
	createPRHTMLURL  string
	createPRErr      error

	// PR metadata tracking
	reviewersCalls [][]string
	labelsCalls    [][]string
	assigneesCalls [][]string
}

type postCall struct {
	Repo   string
	Number int
	Body   string
}

type prCall struct {
	Repo, Title, Body, Head, Base string
	Draft                         bool
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

func (f *fakeGH) GetDefaultBranch(repo string) (string, error) {
	if f.defaultBranchErr != nil {
		return "", f.defaultBranchErr
	}
	if f.defaultBranch == "" {
		return "main", nil
	}
	return f.defaultBranch, nil
}

func (f *fakeGH) CreatePR(repo, title, body, head, base string, draft bool) (*github.CreatedPR, error) {
	f.createPRCalls = append(f.createPRCalls, prCall{
		Repo: repo, Title: title, Body: body, Head: head, Base: base, Draft: draft,
	})
	if f.createPRErr != nil {
		return nil, f.createPRErr
	}
	num := f.createPRNumber
	if num == 0 {
		num = 999
	}
	id := f.createPRID
	if id == 0 {
		id = int64(num) * 100 // deterministic fake github_id
	}
	htmlURL := f.createPRHTMLURL
	if htmlURL == "" {
		htmlURL = fmt.Sprintf("https://github.com/%s/pull/%d", repo, num)
	}
	return &github.CreatedPR{Number: num, ID: id, HTMLURL: htmlURL}, nil
}

func (f *fakeGH) SetPRReviewers(repo string, prNumber int, reviewers []string) error {
	f.reviewersCalls = append(f.reviewersCalls, reviewers)
	return nil
}
func (f *fakeGH) AddLabels(repo string, number int, labels []string) error {
	f.labelsCalls = append(f.labelsCalls, labels)
	return nil
}
func (f *fakeGH) SetAssignees(repo string, number int, assignees []string) error {
	f.assigneesCalls = append(f.assigneesCalls, assignees)
	return nil
}

type fakeExec struct {
	detectCLI  string
	detectErr  error
	rawOutput  []byte
	rawErr     error
	lastPrompt string
	lastOpts   executor.ExecOptions
	lastCLI    string

	// Per-call outputs for multi-call tests (e.g. implement + PR description).
	// When set, ExecuteRaw returns rawOutputs[callCount] (clamped to last).
	rawOutputs [][]byte
	rawErrs    []error
	callCount  int
	prompts    []string
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
	idx := f.callCount
	f.callCount++
	f.lastCLI = cli
	f.lastPrompt = prompt
	f.lastOpts = opts
	f.prompts = append(f.prompts, prompt)

	if len(f.rawOutputs) > 0 {
		i := idx
		if i >= len(f.rawOutputs) {
			i = len(f.rawOutputs) - 1
		}
		var err error
		if len(f.rawErrs) > idx {
			err = f.rawErrs[idx]
		} else if len(f.rawErrs) > 0 {
			err = f.rawErrs[len(f.rawErrs)-1]
		}
		if err != nil {
			return nil, err
		}
		return f.rawOutputs[i], nil
	}

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

type fakeGit struct {
	checkoutCalls []string // branch names checked out
	commitCalls   []string // commit messages
	pushCalls     []string // branches pushed
	deleteCalls   []string // branches deleted from remote
	hasChanges    bool

	checkoutErr   error
	hasChangesErr error
	commitErr     error
	pushErr       error
	deleteErr     error

	diffOutput []byte
	diffErr    error
}

func (g *fakeGit) CheckoutNewBranch(ctx context.Context, dir, repo, branch, base, token string) error {
	g.checkoutCalls = append(g.checkoutCalls, branch)
	return g.checkoutErr
}
func (g *fakeGit) HasChanges(ctx context.Context, dir string) (bool, error) {
	return g.hasChanges, g.hasChangesErr
}
func (g *fakeGit) CommitAll(ctx context.Context, dir, msg string) error {
	g.commitCalls = append(g.commitCalls, msg)
	return g.commitErr
}
func (g *fakeGit) Push(ctx context.Context, dir, repo, branch, token string) error {
	g.pushCalls = append(g.pushCalls, branch)
	return g.pushErr
}
func (g *fakeGit) DeleteRemoteBranch(ctx context.Context, dir, repo, branch, token string) error {
	g.deleteCalls = append(g.deleteCalls, branch)
	return g.deleteErr
}
func (g *fakeGit) DiffHead(ctx context.Context, dir string) ([]byte, error) {
	return g.diffOutput, g.diffErr
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
	p := issues.New(s, gh, exec, nil, broker, notify)

	issue := newIssue(config.IssueModeReviewOnly)
	rev, err := p.Run(context.Background(), issue, issues.RunOptions{Primary: "claude"})
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
	p := issues.New(s, gh, exec, nil, &fakeBroker{}, nil)

	issue := newIssue(config.IssueModeDevelop)
	// WorkDir zero → no working tree → downgrade to review_only.
	rev, err := p.Run(context.Background(), issue, issues.RunOptions{Primary: "claude"})
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

// ── auto_implement ───────────────────────────────────────────────────────────

func autoImplementRunOptions() issues.RunOptions {
	return issues.RunOptions{
		Primary:     "claude",
		ExecOpts:    executor.ExecOptions{WorkDir: "/tmp/repo"},
		GitHubToken: "ghs_fake_token",
	}
}

func TestPipeline_AutoImplementHappyPath(t *testing.T) {
	s := &fakeStore{}
	gh := &fakeGH{defaultBranch: "main", createPRNumber: 123}
	// Agent output content is irrelevant in auto_implement — only HasChanges matters.
	exec := &fakeExec{detectCLI: "claude", rawOutput: []byte("done")}
	git := &fakeGit{hasChanges: true}
	broker := &fakeBroker{}
	notify := &fakeNotifier{}
	p := issues.New(s, gh, exec, git, broker, notify)

	issue := newIssue(config.IssueModeDevelop)
	rev, err := p.Run(context.Background(), issue, autoImplementRunOptions())
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// Git ops ran in the expected order.
	if len(git.checkoutCalls) != 1 || git.checkoutCalls[0] != "heimdallm/issue-7" {
		t.Errorf("branch expected heimdallm/issue-7, got %v", git.checkoutCalls)
	}
	if len(git.commitCalls) != 1 {
		t.Fatalf("expected 1 commit, got %d", len(git.commitCalls))
	}
	if !strings.Contains(git.commitCalls[0], "Closes #7") {
		t.Errorf("commit msg should mention Closes #7, got %q", git.commitCalls[0])
	}
	if len(git.pushCalls) != 1 || git.pushCalls[0] != "heimdallm/issue-7" {
		t.Errorf("expected push of heimdallm/issue-7, got %v", git.pushCalls)
	}

	// PR created against main with the work branch.
	if len(gh.createPRCalls) != 1 {
		t.Fatalf("expected 1 CreatePR, got %d", len(gh.createPRCalls))
	}
	call := gh.createPRCalls[0]
	if call.Head != "heimdallm/issue-7" || call.Base != "main" {
		t.Errorf("PR head/base wrong: %+v", call)
	}
	if !strings.Contains(call.Body, "Closes #7") {
		t.Errorf("PR body missing Closes #7: %q", call.Body)
	}

	// Review persisted with action_taken=develop and pr_created=123.
	if rev.ActionTaken != string(config.IssueModeDevelop) {
		t.Errorf("ActionTaken=%q, want develop", rev.ActionTaken)
	}
	if rev.PRCreated != 123 {
		t.Errorf("PRCreated=%d, want 123", rev.PRCreated)
	}

	// SSE: detected → started → implemented.
	types := broker.types()
	want := []string{sse.EventIssueDetected, sse.EventIssueReviewStarted, sse.EventIssueImplemented}
	if !stringsEqual(types, want) {
		t.Errorf("SSE sequence = %v, want %v", types, want)
	}

	// Exactly one PostComment: the link-back note pointing watchers of the
	// issue at the newly-opened PR. This is NOT the review_only fallback
	// comment — that one only fires on the no-changes path.
	if len(gh.postCalls) != 1 {
		t.Fatalf("auto_implement happy path should post the link-back comment, got %d", len(gh.postCalls))
	}
	if !strings.Contains(gh.postCalls[0].Body, "Implementation PR: #123") {
		t.Errorf("link-back comment body wrong: %q", gh.postCalls[0].Body)
	}

	// Prompt was the implement flavour, not the triage JSON instruction.
	if !strings.Contains(exec.lastPrompt, "Implement what the issue asks for") {
		t.Errorf("prompt should be the implement flavour")
	}
}

func TestPipeline_AutoImplementNoChangesFallsBackToReviewOnly(t *testing.T) {
	// Agent escape hatch fired; the working tree is untouched. The pipeline
	// must degrade to a review_only-style comment rather than open an empty PR.
	s := &fakeStore{}
	gh := &fakeGH{defaultBranch: "main"}
	exec := &fakeExec{detectCLI: "claude", rawOutput: []byte("nothing to do")}
	git := &fakeGit{hasChanges: false}
	broker := &fakeBroker{}
	p := issues.New(s, gh, exec, git, broker, nil)

	rev, err := p.Run(context.Background(), newIssue(config.IssueModeDevelop), autoImplementRunOptions())
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// No commit, no push, no PR.
	if len(git.commitCalls) != 0 || len(git.pushCalls) != 0 || len(gh.createPRCalls) != 0 {
		t.Errorf("no-changes path must not commit/push/createPR, got commits=%d pushes=%d prs=%d",
			len(git.commitCalls), len(git.pushCalls), len(gh.createPRCalls))
	}

	// Comment explaining the skip is posted.
	if len(gh.postCalls) != 1 {
		t.Fatalf("expected 1 fallback PostComment, got %d", len(gh.postCalls))
	}
	if !strings.Contains(gh.postCalls[0].Body, "auto-implement skipped") {
		t.Errorf("fallback body should explain the skip, got %q", gh.postCalls[0].Body)
	}

	// Review persisted reflects the actual mode that ran.
	if rev.ActionTaken != string(config.IssueModeReviewOnly) {
		t.Errorf("ActionTaken=%q, want review_only (audit-honest fallback)", rev.ActionTaken)
	}
	if rev.PRCreated != 0 {
		t.Errorf("PRCreated=%d, want 0", rev.PRCreated)
	}

	// SSE completes with review_completed, not implemented.
	types := broker.types()
	if types[len(types)-1] != sse.EventIssueReviewCompleted {
		t.Errorf("last event = %q, want issue_review_completed", types[len(types)-1])
	}
}

func TestPipeline_AutoImplementRequiresGitDep(t *testing.T) {
	p := issues.New(&fakeStore{}, &fakeGH{}, &fakeExec{}, nil, &fakeBroker{}, nil)
	_, err := p.Run(context.Background(), newIssue(config.IssueModeDevelop), autoImplementRunOptions())
	if err == nil {
		t.Fatal("expected error when GitOps is nil")
	}
}

func TestPipeline_AutoImplementRequiresToken(t *testing.T) {
	p := issues.New(&fakeStore{}, &fakeGH{}, &fakeExec{}, &fakeGit{}, &fakeBroker{}, nil)
	opts := autoImplementRunOptions()
	opts.GitHubToken = ""
	_, err := p.Run(context.Background(), newIssue(config.IssueModeDevelop), opts)
	if err == nil {
		t.Fatal("expected error when GitHubToken is empty")
	}
}

func TestPipeline_AutoImplementSurfacesDefaultBranchError(t *testing.T) {
	gh := &fakeGH{defaultBranchErr: errors.New("repo not found")}
	p := issues.New(&fakeStore{}, gh, &fakeExec{detectCLI: "claude", rawOutput: []byte("x")},
		&fakeGit{hasChanges: true}, &fakeBroker{}, nil)
	_, err := p.Run(context.Background(), newIssue(config.IssueModeDevelop), autoImplementRunOptions())
	if err == nil {
		t.Fatal("expected error on GetDefaultBranch failure")
	}
	if !strings.Contains(err.Error(), "default branch") {
		t.Errorf("error should mention default branch, got: %v", err)
	}
}

func TestPipeline_AutoImplementSurfacesCheckoutError(t *testing.T) {
	git := &fakeGit{checkoutErr: errors.New("fetch failed")}
	p := issues.New(&fakeStore{}, &fakeGH{defaultBranch: "main"}, &fakeExec{},
		git, &fakeBroker{}, nil)
	_, err := p.Run(context.Background(), newIssue(config.IssueModeDevelop), autoImplementRunOptions())
	if err == nil {
		t.Fatal("expected error on checkout failure")
	}
}

func TestPipeline_AutoImplementSurfacesPushError(t *testing.T) {
	git := &fakeGit{hasChanges: true, pushErr: errors.New("remote rejected")}
	broker := &fakeBroker{}
	p := issues.New(&fakeStore{}, &fakeGH{defaultBranch: "main"},
		&fakeExec{detectCLI: "claude", rawOutput: []byte("x")}, git, broker, nil)

	_, err := p.Run(context.Background(), newIssue(config.IssueModeDevelop), autoImplementRunOptions())
	if err == nil {
		t.Fatal("expected push error to surface")
	}
	// An issue_review_error event is published so operators see the failure.
	types := broker.types()
	if types[len(types)-1] != sse.EventIssueReviewError {
		t.Errorf("last event should be issue_review_error, got %v", types)
	}
}

func TestPipeline_AutoImplementSurfacesCreatePRError(t *testing.T) {
	gh := &fakeGH{defaultBranch: "main", createPRErr: errors.New("validation failed")}
	git := &fakeGit{hasChanges: true}
	p := issues.New(&fakeStore{}, gh,
		&fakeExec{detectCLI: "claude", rawOutput: []byte("x")}, git, &fakeBroker{}, nil)

	_, err := p.Run(context.Background(), newIssue(config.IssueModeDevelop), autoImplementRunOptions())
	if err == nil {
		t.Fatal("expected CreatePR error to surface")
	}
	// Commit+push ran before the PR step, so both should have been attempted.
	if len(git.pushCalls) != 1 {
		t.Errorf("push should have run before the failing CreatePR, got %d calls", len(git.pushCalls))
	}
}

func TestPipeline_AutoImplementTokenNeverLeaksIntoPushCall(t *testing.T) {
	// Regression guard: Push receives the token via an argument, not via
	// global state. Fake Push must receive the token verbatim and the
	// pipeline must not mutate RunOptions.GitHubToken along the way.
	git := &tokenSniffingGit{}
	opts := autoImplementRunOptions()
	p := issues.New(&fakeStore{}, &fakeGH{defaultBranch: "main"},
		&fakeExec{detectCLI: "claude", rawOutput: []byte("x")}, git, &fakeBroker{}, nil)

	_, err := p.Run(context.Background(), newIssue(config.IssueModeDevelop), opts)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if git.seenToken != opts.GitHubToken {
		t.Errorf("Push received a different token than RunOptions carried (%q vs %q)",
			git.seenToken, opts.GitHubToken)
	}
}

func TestPipeline_AutoImplementCleansOrphanBranchOnPRFailure(t *testing.T) {
	// Regression guard: when CreatePR fails AFTER Push succeeded, the
	// pipeline must DeleteRemoteBranch so a re-run does not collide with
	// the stale ref.
	gh := &fakeGH{defaultBranch: "main", createPRErr: errors.New("validation failed")}
	git := &fakeGit{hasChanges: true}
	p := issues.New(&fakeStore{}, gh,
		&fakeExec{detectCLI: "claude", rawOutput: []byte("x")}, git, &fakeBroker{}, nil)

	_, err := p.Run(context.Background(), newIssue(config.IssueModeDevelop), autoImplementRunOptions())
	if err == nil {
		t.Fatal("expected CreatePR error to surface")
	}
	if len(git.deleteCalls) != 1 || git.deleteCalls[0] != "heimdallm/issue-7" {
		t.Errorf("expected DeleteRemoteBranch for heimdallm/issue-7 on orphan cleanup, got %v",
			git.deleteCalls)
	}
}

func TestPipeline_AutoImplementSurfacesCommitError(t *testing.T) {
	// Gap between HasChanges=true and Push: a CommitAll failure must be
	// surfaced with its own error and emit issue_review_error. Otherwise a
	// partial-state bug could let a push fire over an empty tree.
	git := &fakeGit{hasChanges: true, commitErr: errors.New("hook rejected")}
	broker := &fakeBroker{}
	p := issues.New(&fakeStore{}, &fakeGH{defaultBranch: "main"},
		&fakeExec{detectCLI: "claude", rawOutput: []byte("x")}, git, broker, nil)

	_, err := p.Run(context.Background(), newIssue(config.IssueModeDevelop), autoImplementRunOptions())
	if err == nil {
		t.Fatal("expected CommitAll error to surface")
	}
	if len(git.pushCalls) != 0 {
		t.Errorf("push should not run after commit failure, got %d calls", len(git.pushCalls))
	}
	types := broker.types()
	if types[len(types)-1] != sse.EventIssueReviewError {
		t.Errorf("last event = %q, want issue_review_error", types[len(types)-1])
	}
}

func TestPipeline_AutoImplementSanitizesIssueTitleInCommitAndPR(t *testing.T) {
	// Newlines inside issue.Title are the trailer-injection vector: git only
	// parses `Co-Authored-By:` / similar as a trailer when it sits on its
	// OWN LINE at the end of the commit message. Collapsing CR/LF to spaces
	// keeps the attacker-controlled fragment inside the subject line, where
	// git ignores it. A very long title is an orthogonal readability issue
	// that sanitizeTitle caps independently.
	issue := newIssue(config.IssueModeDevelop)
	issue.Title = "Login broken\nCo-Authored-By: Attacker <evil@example.com>\n" +
		strings.Repeat("x", 200)

	gh := &fakeGH{defaultBranch: "main", createPRNumber: 55}
	git := &fakeGit{hasChanges: true}
	p := issues.New(&fakeStore{}, gh,
		&fakeExec{detectCLI: "claude", rawOutput: []byte("x")}, git, &fakeBroker{}, nil)

	if _, err := p.Run(context.Background(), issue, autoImplementRunOptions()); err != nil {
		t.Fatalf("run: %v", err)
	}

	if len(git.commitCalls) != 1 {
		t.Fatalf("expected 1 commit, got %d", len(git.commitCalls))
	}
	commit := git.commitCalls[0]
	// The subject line (everything up to the first blank line) must not
	// contain a raw CR/LF from the attacker-controlled title, because that
	// would let the attacker end the subject early and inject trailer-
	// looking lines before the daemon's own footer.
	subject := strings.SplitN(commit, "\n", 2)[0]
	if strings.ContainsAny(subject, "\r\n") {
		t.Errorf("commit subject contains raw CR/LF from title: %q", subject)
	}

	// No standalone trailer line with the attacker's "Co-Authored-By" may
	// exist — only the benign substring embedded in the sanitized subject
	// is permitted.
	for _, line := range strings.Split(commit, "\n")[1:] {
		if strings.HasPrefix(strings.TrimSpace(line), "Co-Authored-By: Attacker") {
			t.Errorf("trailer injection: attacker's Co-Authored-By reached a trailer line: %q", line)
		}
	}

	// sanitizeTitle caps at 120 bytes; the original hostile title was ~260.
	// Give the assertion a little slack for the template prefix.
	if len(subject) > 200 {
		t.Errorf("commit subject not length-capped: len=%d", len(subject))
	}

	if len(gh.createPRCalls) != 1 {
		t.Fatalf("expected 1 CreatePR call, got %d", len(gh.createPRCalls))
	}
	prTitle := gh.createPRCalls[0].Title
	if strings.ContainsAny(prTitle, "\r\n") {
		t.Errorf("PR title contains raw CR/LF from attacker title: %q", prTitle)
	}
}

func TestPipeline_AutoImplementStopsWhenContextAlreadyCancelled(t *testing.T) {
	// A cancelled context is surfaced by the fetcher today; this guards the
	// pipeline-level plumbing: we pass ctx straight through to the git
	// dependency so an already-cancelled context short-circuits the first
	// git command. We use a stub git that blows up on Context.Done to avoid
	// real git calls — the error bubbles up as the pipeline error.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ctxCheckingGit := &contextCheckingGit{expectCtx: ctx}
	p := issues.New(&fakeStore{}, &fakeGH{defaultBranch: "main"},
		&fakeExec{detectCLI: "claude", rawOutput: []byte("x")},
		ctxCheckingGit, &fakeBroker{}, nil)

	_, err := p.Run(ctx, newIssue(config.IssueModeDevelop), autoImplementRunOptions())
	if err == nil {
		t.Fatal("expected cancelled context to surface through git")
	}
	if !ctxCheckingGit.sawCancel {
		t.Error("GitOps never observed the cancellation — ctx did not propagate")
	}
}

// contextCheckingGit asserts the context reached the git layer and returns
// the context's cancellation error when already done.
type contextCheckingGit struct {
	expectCtx context.Context
	sawCancel bool
}

func (g *contextCheckingGit) CheckoutNewBranch(ctx context.Context, dir, repo, branch, base, token string) error {
	if ctx.Err() != nil {
		g.sawCancel = true
		return ctx.Err()
	}
	return nil
}
func (g *contextCheckingGit) HasChanges(ctx context.Context, dir string) (bool, error) {
	return false, nil
}
func (g *contextCheckingGit) CommitAll(ctx context.Context, dir, msg string) error { return nil }
func (g *contextCheckingGit) Push(ctx context.Context, dir, repo, branch, token string) error {
	return nil
}
func (g *contextCheckingGit) DeleteRemoteBranch(ctx context.Context, dir, repo, branch, token string) error {
	return nil
}
func (g *contextCheckingGit) DiffHead(ctx context.Context, dir string) ([]byte, error) {
	return nil, nil
}

func TestPipeline_AutoImplementUsesCustomPromptOverride(t *testing.T) {
	gh := &fakeGH{defaultBranch: "main", createPRNumber: 123}
	exec := &fakeExec{detectCLI: "claude", rawOutput: []byte("done")}
	git := &fakeGit{hasChanges: true}
	p := issues.New(&fakeStore{}, gh, exec, git, &fakeBroker{}, nil)

	opts := autoImplementRunOptions()
	opts.ImplementPromptOverride = "Custom impl template for {repo} issue {number}"

	if _, err := p.Run(context.Background(), newIssue(config.IssueModeDevelop), opts); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(exec.lastPrompt, "Custom impl template for org/repo issue 7") {
		t.Errorf("custom template not applied: %q", exec.lastPrompt)
	}
	// The default preamble must NOT appear when a full custom template is set.
	if strings.Contains(exec.lastPrompt, "You are Heimdallm, an engineering agent") {
		t.Errorf("default preamble leaked through override: %q", exec.lastPrompt)
	}
}

func TestPipeline_AutoImplementInjectsCustomInstructions(t *testing.T) {
	gh := &fakeGH{defaultBranch: "main", createPRNumber: 123}
	exec := &fakeExec{detectCLI: "claude", rawOutput: []byte("done")}
	git := &fakeGit{hasChanges: true}
	p := issues.New(&fakeStore{}, gh, exec, git, &fakeBroker{}, nil)

	opts := autoImplementRunOptions()
	opts.ImplementInstructions = "ALWAYS run go vet before finishing."

	if _, err := p.Run(context.Background(), newIssue(config.IssueModeDevelop), opts); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(exec.lastPrompt, "ALWAYS run go vet before finishing.") {
		t.Errorf("instructions not injected: %q", exec.lastPrompt)
	}
	// Default rules AND escape hatch must remain; instructions must not
	// displace the safety floor.
	for _, want := range []string{
		"Keep the change minimal",
		"leave the tree untouched",
	} {
		if !strings.Contains(exec.lastPrompt, want) {
			t.Errorf("instructions injection dropped default rule %q: %s", want, exec.lastPrompt)
		}
	}
}

// ── sanitizeTitle unit tests ─────────────────────────────────────────────────

// tokenSniffingGit is a GitOps that records the token it was pushed with.
type tokenSniffingGit struct {
	seenToken string
}

func (g *tokenSniffingGit) CheckoutNewBranch(ctx context.Context, dir, repo, branch, base, token string) error {
	return nil
}
func (g *tokenSniffingGit) HasChanges(ctx context.Context, dir string) (bool, error) {
	return true, nil
}
func (g *tokenSniffingGit) CommitAll(ctx context.Context, dir, msg string) error { return nil }
func (g *tokenSniffingGit) Push(ctx context.Context, dir, repo, branch, token string) error {
	g.seenToken = token
	return nil
}
func (g *tokenSniffingGit) DeleteRemoteBranch(ctx context.Context, dir, repo, branch, token string) error {
	return nil
}
func (g *tokenSniffingGit) DiffHead(ctx context.Context, dir string) ([]byte, error) {
	return nil, nil
}

func TestPipeline_IgnoreModeRejectedWithItsOwnError(t *testing.T) {
	// The fetcher normally filters ignore-classified issues out, but the
	// pipeline must surface a specific error (not the auto_implement one)
	// if one ever sneaks in. Regression guard for Muriano's review feedback.
	s := &fakeStore{}
	p := issues.New(s, &fakeGH{}, &fakeExec{}, nil, nil, nil)

	issue := newIssue(config.IssueModeIgnore)
	_, err := p.Run(context.Background(), issue, issues.RunOptions{Primary: "claude"})
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
	p := issues.New(s, gh, exec, nil, nil, nil)

	if _, err := p.Run(context.Background(), newIssue(config.IssueModeReviewOnly), issues.RunOptions{Primary: "claude"}); err != nil {
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
	p := issues.New(s, gh, exec, nil, nil, nil)

	rev, err := p.Run(context.Background(), newIssue(config.IssueModeReviewOnly), issues.RunOptions{Primary: "claude"})
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
	p := issues.New(s, gh, exec, nil, broker, nil)

	_, err := p.Run(context.Background(), newIssue(config.IssueModeReviewOnly), issues.RunOptions{Primary: "claude"})
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
	p := issues.New(s, gh, exec, nil, broker, nil)

	_, err := p.Run(context.Background(), newIssue(config.IssueModeReviewOnly), issues.RunOptions{Primary: "claude"})
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
	p := issues.New(s, gh, exec, nil, nil, nil)

	if _, err := p.Run(context.Background(), newIssue(config.IssueModeReviewOnly), issues.RunOptions{Primary: "claude"}); err != nil {
		t.Fatalf("comment fetch failure must not abort, got: %v", err)
	}
}

// ── error paths → SSE issue_review_error ────────────────────────────────────

func TestPipeline_CLIDetectFailureEmitsErrorEvent(t *testing.T) {
	s := &fakeStore{}
	gh := &fakeGH{}
	exec := &fakeExec{detectErr: errors.New("no CLI available")}
	broker := &fakeBroker{}
	p := issues.New(s, gh, exec, nil, broker, nil)

	_, err := p.Run(context.Background(), newIssue(config.IssueModeReviewOnly), issues.RunOptions{Primary: "claude"})
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
	p := issues.New(s, gh, exec, nil, broker, nil)

	_, err := p.Run(context.Background(), newIssue(config.IssueModeReviewOnly), issues.RunOptions{Primary: "claude"})
	if err == nil {
		t.Fatal("expected parse error")
	}
	types := broker.types()
	if types[len(types)-1] != sse.EventIssueReviewError {
		t.Errorf("expected issue_review_error, got %v", types)
	}
}

func TestPipeline_NilIssueIsRejected(t *testing.T) {
	p := issues.New(&fakeStore{}, &fakeGH{}, &fakeExec{}, nil, nil, nil)
	if _, err := p.Run(context.Background(), nil, issues.RunOptions{}); err == nil {
		t.Fatal("expected error for nil issue")
	}
}

func TestAutoImplement_AppliesPRMetadata(t *testing.T) {
	gh := &fakeGH{defaultBranch: "main", createPRNumber: 77}
	exec := &fakeExec{detectCLI: "claude", rawOutput: []byte("implement done")}
	git := &fakeGit{hasChanges: true}
	broker := &fakeBroker{}
	p := issues.New(&fakeStore{}, gh, exec, git, broker, nil)

	opts := issues.RunOptions{
		Primary:     "claude",
		ExecOpts:    executor.ExecOptions{WorkDir: "/tmp/repo"},
		GitHubToken: "tok",
		PRReviewers: []string{"alice", "bob"},
		PRAssignee:  "charlie",
		PRLabels:    []string{"auto-generated", "heimdallm"},
		PRDraft:     true,
	}

	rev, err := p.Run(context.Background(), newIssue(config.IssueModeDevelop), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rev.PRCreated != 77 {
		t.Errorf("pr_created = %d, want 77", rev.PRCreated)
	}

	// Verify draft was forwarded to CreatePR
	if len(gh.createPRCalls) != 1 {
		t.Fatalf("expected 1 CreatePR call, got %d", len(gh.createPRCalls))
	}
	if !gh.createPRCalls[0].Draft {
		t.Error("CreatePR draft should be true")
	}

	// Verify metadata was applied
	if len(gh.reviewersCalls) != 1 || !stringsEqual(gh.reviewersCalls[0], []string{"alice", "bob"}) {
		t.Errorf("reviewers = %v, want [alice bob]", gh.reviewersCalls)
	}
	if len(gh.labelsCalls) != 1 || !stringsEqual(gh.labelsCalls[0], []string{"auto-generated", "heimdallm"}) {
		t.Errorf("labels = %v, want [auto-generated heimdallm]", gh.labelsCalls)
	}
	if len(gh.assigneesCalls) != 1 || !stringsEqual(gh.assigneesCalls[0], []string{"charlie"}) {
		t.Errorf("assignees = %v, want [charlie]", gh.assigneesCalls)
	}
}

func TestAutoImplement_SkipsEmptyMetadata(t *testing.T) {
	gh := &fakeGH{defaultBranch: "main", createPRNumber: 88}
	exec := &fakeExec{detectCLI: "claude", rawOutput: []byte("implement done")}
	git := &fakeGit{hasChanges: true}
	p := issues.New(&fakeStore{}, gh, exec, git, &fakeBroker{}, nil)

	opts := issues.RunOptions{
		Primary:     "claude",
		ExecOpts:    executor.ExecOptions{WorkDir: "/tmp/repo"},
		GitHubToken: "tok",
		// No PR metadata set
	}

	_, err := p.Run(context.Background(), newIssue(config.IssueModeDevelop), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// No metadata calls should have been made
	if len(gh.reviewersCalls) != 0 {
		t.Errorf("expected 0 reviewers calls, got %d", len(gh.reviewersCalls))
	}
	if len(gh.labelsCalls) != 0 {
		t.Errorf("expected 0 labels calls, got %d", len(gh.labelsCalls))
	}
	if len(gh.assigneesCalls) != 0 {
		t.Errorf("expected 0 assignees calls, got %d", len(gh.assigneesCalls))
	}
}

// ── LLM-generated PR description tests ──────────────────────────────────────

func TestAutoImplement_GeneratePRDescription_HappyPath(t *testing.T) {
	descJSON := `{"title":"feat: add widget factory with validation","body":"## Summary\nAdded widget factory.\n\n## Changes\n- pkg/widget.go: new factory\n\n## Test plan\nRun go test ./..."}`

	gh := &fakeGH{defaultBranch: "main", createPRNumber: 42}
	exec := &fakeExec{
		detectCLI:  "claude",
		rawOutputs: [][]byte{[]byte("done"), []byte(descJSON)},
	}
	git := &fakeGit{
		hasChanges: true,
		diffOutput: []byte("diff --git a/pkg/widget.go b/pkg/widget.go\n+package pkg\n"),
	}
	broker := &fakeBroker{}
	p := issues.New(&fakeStore{}, gh, exec, git, broker, nil)

	opts := autoImplementRunOptions()
	opts.GeneratePRDescription = true

	rev, err := p.Run(context.Background(), newIssue(config.IssueModeDevelop), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rev.PRCreated != 42 {
		t.Errorf("PRCreated = %d, want 42", rev.PRCreated)
	}

	// The LLM was called twice: implement + description.
	if exec.callCount != 2 {
		t.Fatalf("expected 2 ExecuteRaw calls, got %d", exec.callCount)
	}

	// Second prompt should be the PR description prompt.
	if len(exec.prompts) < 2 {
		t.Fatalf("expected 2 prompts, got %d", len(exec.prompts))
	}
	if !strings.Contains(exec.prompts[1], "Write a pull request title") {
		t.Errorf("second prompt should be PR description prompt, got: %.100s", exec.prompts[1])
	}
	if !strings.Contains(exec.prompts[1], "diff --git") {
		t.Errorf("PR description prompt should contain the diff")
	}

	// PR was created with the LLM-generated title and body.
	if len(gh.createPRCalls) != 1 {
		t.Fatalf("expected 1 CreatePR, got %d", len(gh.createPRCalls))
	}
	call := gh.createPRCalls[0]
	if call.Title != "feat: add widget factory with validation" {
		t.Errorf("PR title = %q, want LLM-generated title", call.Title)
	}
	if !strings.Contains(call.Body, "## Summary") {
		t.Errorf("PR body should contain LLM-generated content, got: %q", call.Body)
	}
	// Closes #N must always be appended.
	if !strings.Contains(call.Body, "Closes #7") {
		t.Errorf("PR body should contain Closes #7, got: %q", call.Body)
	}
}

func TestAutoImplement_GeneratePRDescription_FallsBackOnLLMError(t *testing.T) {
	gh := &fakeGH{defaultBranch: "main", createPRNumber: 55}
	exec := &fakeExec{
		detectCLI:  "claude",
		rawOutputs: [][]byte{[]byte("done")},
		rawErrs:    []error{nil, errors.New("LLM timeout")},
	}
	git := &fakeGit{
		hasChanges: true,
		diffOutput: []byte("some diff"),
	}
	p := issues.New(&fakeStore{}, gh, exec, git, &fakeBroker{}, nil)

	opts := autoImplementRunOptions()
	opts.GeneratePRDescription = true

	rev, err := p.Run(context.Background(), newIssue(config.IssueModeDevelop), opts)
	if err != nil {
		t.Fatalf("Run should succeed with fallback: %v", err)
	}
	if rev.PRCreated != 55 {
		t.Errorf("PRCreated = %d, want 55", rev.PRCreated)
	}

	// Falls back to template title.
	if len(gh.createPRCalls) != 1 {
		t.Fatalf("expected 1 CreatePR, got %d", len(gh.createPRCalls))
	}
	call := gh.createPRCalls[0]
	if !strings.Contains(call.Title, "feat: implement #7") {
		t.Errorf("PR title should be template fallback, got: %q", call.Title)
	}
	if !strings.Contains(call.Body, "Auto-generated by Heimdallm") {
		t.Errorf("PR body should be template fallback, got: %q", call.Body)
	}
}

func TestAutoImplement_GeneratePRDescription_FallsBackOnDiffError(t *testing.T) {
	gh := &fakeGH{defaultBranch: "main", createPRNumber: 56}
	exec := &fakeExec{detectCLI: "claude", rawOutput: []byte("done")}
	git := &fakeGit{
		hasChanges: true,
		diffErr:    errors.New("git diff failed"),
	}
	p := issues.New(&fakeStore{}, gh, exec, git, &fakeBroker{}, nil)

	opts := autoImplementRunOptions()
	opts.GeneratePRDescription = true

	rev, err := p.Run(context.Background(), newIssue(config.IssueModeDevelop), opts)
	if err != nil {
		t.Fatalf("Run should succeed with fallback: %v", err)
	}
	if rev.PRCreated != 56 {
		t.Errorf("PRCreated = %d, want 56", rev.PRCreated)
	}

	// Only 1 ExecuteRaw call (implement); no second call because diff failed.
	if exec.callCount != 1 {
		t.Errorf("expected 1 ExecuteRaw call (no description call), got %d", exec.callCount)
	}

	call := gh.createPRCalls[0]
	if !strings.Contains(call.Title, "feat: implement #7") {
		t.Errorf("PR title should be template fallback, got: %q", call.Title)
	}
}

func TestAutoImplement_GeneratePRDescription_FallsBackOnBadJSON(t *testing.T) {
	gh := &fakeGH{defaultBranch: "main", createPRNumber: 57}
	exec := &fakeExec{
		detectCLI:  "claude",
		rawOutputs: [][]byte{[]byte("done"), []byte("not valid json at all")},
	}
	git := &fakeGit{
		hasChanges: true,
		diffOutput: []byte("some diff"),
	}
	p := issues.New(&fakeStore{}, gh, exec, git, &fakeBroker{}, nil)

	opts := autoImplementRunOptions()
	opts.GeneratePRDescription = true

	_, err := p.Run(context.Background(), newIssue(config.IssueModeDevelop), opts)
	if err != nil {
		t.Fatalf("Run should succeed with fallback: %v", err)
	}

	call := gh.createPRCalls[0]
	if !strings.Contains(call.Title, "feat: implement #7") {
		t.Errorf("PR title should be template fallback, got: %q", call.Title)
	}
}

func TestAutoImplement_GeneratePRDescription_SkippedWhenDisabled(t *testing.T) {
	// Default: GeneratePRDescription is false. Only 1 ExecuteRaw call.
	gh := &fakeGH{defaultBranch: "main", createPRNumber: 58}
	exec := &fakeExec{detectCLI: "claude", rawOutput: []byte("done")}
	git := &fakeGit{hasChanges: true, diffOutput: []byte("some diff")}
	p := issues.New(&fakeStore{}, gh, exec, git, &fakeBroker{}, nil)

	opts := autoImplementRunOptions()
	// GeneratePRDescription defaults to false — no explicit set.

	_, err := p.Run(context.Background(), newIssue(config.IssueModeDevelop), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if exec.callCount != 1 {
		t.Errorf("expected 1 ExecuteRaw call (no description), got %d", exec.callCount)
	}
	call := gh.createPRCalls[0]
	if !strings.Contains(call.Title, "feat: implement #7") {
		t.Errorf("PR title should be static template, got: %q", call.Title)
	}
}

func TestAutoImplement_GeneratePRDescription_SanitizesLLMTitle(t *testing.T) {
	// An LLM could return a title with newlines or excessive length.
	longTitle := "feat: " + strings.Repeat("x", 200)
	descJSON := fmt.Sprintf(`{"title":"malicious\nCo-Authored-By: evil@example.com\n%s","body":"body text"}`, longTitle)

	gh := &fakeGH{defaultBranch: "main", createPRNumber: 59}
	exec := &fakeExec{
		detectCLI:  "claude",
		rawOutputs: [][]byte{[]byte("done"), []byte(descJSON)},
	}
	git := &fakeGit{hasChanges: true, diffOutput: []byte("diff")}
	p := issues.New(&fakeStore{}, gh, exec, git, &fakeBroker{}, nil)

	opts := autoImplementRunOptions()
	opts.GeneratePRDescription = true

	_, err := p.Run(context.Background(), newIssue(config.IssueModeDevelop), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	call := gh.createPRCalls[0]
	if strings.ContainsAny(call.Title, "\r\n") {
		t.Errorf("LLM-generated PR title should be sanitized, got: %q", call.Title)
	}
	if len(call.Title) > 200 {
		t.Errorf("LLM-generated PR title should be length-capped, len=%d", len(call.Title))
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

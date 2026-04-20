package issues

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/github"
)

// fakePromoteClient is a minimal in-memory PromoteIssueClient. Records
// every mutating call so tests can assert the exact side effects without
// an HTTP server standing in.
type fakePromoteClient struct {
	open          map[string][]*github.Issue              // repo → open issues (listed)
	byRef         map[string]*github.Issue                // "repo#N" → issue (for GetIssue)
	added         []struct{ Repo string; N int; Labels []string }
	removed       []struct{ Repo string; N int; Labels []string }
	comments      []struct{ Repo string; N int; Body string }
	listErr       error
	getErr        error
	addErr        error
	removeErr     error
	commentErr    error
}

func (f *fakePromoteClient) ListOpenIssues(repo string) ([]*github.Issue, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.open[repo], nil
}
func (f *fakePromoteClient) GetIssue(repo string, number int) (*github.Issue, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	key := fmt.Sprintf("%s#%d", repo, number)
	if got, ok := f.byRef[key]; ok {
		return got, nil
	}
	return nil, fmt.Errorf("fake: no issue for %s", key)
}
func (f *fakePromoteClient) AddLabels(repo string, n int, labels []string) error {
	if f.addErr != nil {
		return f.addErr
	}
	f.added = append(f.added, struct{ Repo string; N int; Labels []string }{repo, n, labels})
	return nil
}
func (f *fakePromoteClient) RemoveLabels(repo string, n int, labels []string) error {
	if f.removeErr != nil {
		return f.removeErr
	}
	f.removed = append(f.removed, struct{ Repo string; N int; Labels []string }{repo, n, labels})
	return nil
}
func (f *fakePromoteClient) PostComment(repo string, n int, body string) error {
	if f.commentErr != nil {
		return f.commentErr
	}
	f.comments = append(f.comments, struct{ Repo string; N int; Body string }{repo, n, body})
	return nil
}

func mkIssue(repo string, number int, state, body string, labels ...string) *github.Issue {
	ls := make([]github.Label, len(labels))
	for i, l := range labels {
		ls[i] = github.Label{Name: l}
	}
	return &github.Issue{
		Number: number, Repo: repo, State: state, Body: body, Labels: ls,
	}
}

func baseCfg() config.IssueTrackingConfig {
	return config.IssueTrackingConfig{
		Enabled:       true,
		FilterMode:    config.FilterModeExclusive,
		DefaultAction: string(config.IssueModeIgnore),
		BlockedLabels: []string{"blocked"},
		DevelopLabels: []string{"ready"}, // acts as implicit promote-to
	}
}

func TestPromoteReady_Disabled_Noop(t *testing.T) {
	fake := &fakePromoteClient{}
	cfg := baseCfg()
	cfg.Enabled = false

	n, err := PromoteReady(context.Background(), fake, cfg, []string{"org/r"})
	if err != nil || n != 0 {
		t.Errorf("got n=%d err=%v, want 0, nil", n, err)
	}
	if fake.added != nil || fake.removed != nil {
		t.Errorf("expected no API calls")
	}
}

func TestPromoteReady_NoBlockedLabels_Noop(t *testing.T) {
	fake := &fakePromoteClient{}
	cfg := baseCfg()
	cfg.BlockedLabels = nil

	n, err := PromoteReady(context.Background(), fake, cfg, []string{"org/r"})
	if err != nil || n != 0 {
		t.Errorf("got n=%d err=%v, want 0, nil", n, err)
	}
}

func TestPromoteReady_BlockedIssueWithOpenDep_NotPromoted(t *testing.T) {
	blocked := mkIssue("org/r", 10, "open", "## Depends on\n- #5\n", "blocked")
	fake := &fakePromoteClient{
		open:  map[string][]*github.Issue{"org/r": {blocked}},
		byRef: map[string]*github.Issue{"org/r#5": mkIssue("org/r", 5, "open", "")},
	}
	n, err := PromoteReady(context.Background(), fake, baseCfg(), []string{"org/r"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if n != 0 {
		t.Errorf("n = %d, want 0 (dep still open)", n)
	}
	if len(fake.added) != 0 || len(fake.removed) != 0 {
		t.Errorf("no label mutations expected, got add=%v rm=%v", fake.added, fake.removed)
	}
}

func TestPromoteReady_AllDepsClosed_Promoted(t *testing.T) {
	blocked := mkIssue("org/r", 10, "open", "## Depends on\n- #5\n- #6\n", "blocked")
	fake := &fakePromoteClient{
		open: map[string][]*github.Issue{"org/r": {blocked}},
		byRef: map[string]*github.Issue{
			"org/r#5": mkIssue("org/r", 5, "closed", ""),
			"org/r#6": mkIssue("org/r", 6, "closed", ""),
		},
	}
	n, err := PromoteReady(context.Background(), fake, baseCfg(), []string{"org/r"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if n != 1 {
		t.Errorf("n = %d, want 1", n)
	}
	if len(fake.removed) != 1 || fake.removed[0].N != 10 || !equalSorted(fake.removed[0].Labels, []string{"blocked"}) {
		t.Errorf("removed = %v, want one call removing [blocked] on #10", fake.removed)
	}
	if len(fake.added) != 1 || fake.added[0].N != 10 || !equalSorted(fake.added[0].Labels, []string{"ready"}) {
		t.Errorf("added = %v, want one call adding [ready] on #10", fake.added)
	}
	if len(fake.comments) != 1 || fake.comments[0].N != 10 {
		t.Errorf("expected 1 audit comment on #10, got %+v", fake.comments)
	}
}

func TestPromoteReady_NoDepsDeclared_NotPromoted(t *testing.T) {
	// Issue carries a blocked label but has no `## Depends on` section.
	// We don't know what unblocks it → stay out, let the operator handle
	// manually.
	blocked := mkIssue("org/r", 10, "open", "just a body without deps", "blocked")
	fake := &fakePromoteClient{
		open: map[string][]*github.Issue{"org/r": {blocked}},
	}
	n, err := PromoteReady(context.Background(), fake, baseCfg(), []string{"org/r"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if n != 0 {
		t.Errorf("n = %d, want 0 (no deps declared)", n)
	}
}

func TestPromoteReady_CrossRepoDep_QueriedAndRespected(t *testing.T) {
	blocked := mkIssue("org/r", 10, "open", "## Depends on\n- other-org/shared#42\n", "blocked")
	fake := &fakePromoteClient{
		open: map[string][]*github.Issue{"org/r": {blocked}},
		byRef: map[string]*github.Issue{
			"other-org/shared#42": mkIssue("other-org/shared", 42, "closed", ""),
		},
	}
	n, err := PromoteReady(context.Background(), fake, baseCfg(), []string{"org/r"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if n != 1 {
		t.Errorf("n = %d, want 1", n)
	}
}

func TestPromoteReady_MultipleBlockedLabels_AllRemoved(t *testing.T) {
	cfg := baseCfg()
	cfg.BlockedLabels = []string{"blocked", "heimdallm-queued"}

	blocked := mkIssue("org/r", 10, "open", "## Depends on\n- #5\n", "blocked", "heimdallm-queued")
	fake := &fakePromoteClient{
		open: map[string][]*github.Issue{"org/r": {blocked}},
		byRef: map[string]*github.Issue{
			"org/r#5": mkIssue("org/r", 5, "closed", ""),
		},
	}
	n, err := PromoteReady(context.Background(), fake, cfg, []string{"org/r"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if n != 1 {
		t.Errorf("n = %d, want 1", n)
	}
	if len(fake.removed) != 1 {
		t.Fatalf("expected one remove call, got %d", len(fake.removed))
	}
	if !equalSorted(fake.removed[0].Labels, []string{"blocked", "heimdallm-queued"}) {
		t.Errorf("removed = %v, want both blocked labels", fake.removed[0].Labels)
	}
}

func TestPromoteReady_ListErrorPerRepo_ContinuesOtherRepos(t *testing.T) {
	// Simulate: one of the repos fails ListOpenIssues; the other works.
	// The failure must NOT abort the whole cycle.
	blocked := mkIssue("org/b", 20, "open", "## Depends on\n- #9\n", "blocked")
	fake := &fakePromoteClient{
		open: map[string][]*github.Issue{"org/b": {blocked}},
		byRef: map[string]*github.Issue{
			"org/b#9": mkIssue("org/b", 9, "closed", ""),
		},
		listErr: nil,
	}
	// Simulate the error only on the first repo. Simplest: use a list
	// function by swapping the map — here we'll simulate via a repo that
	// has no entry in `open` (fake returns nil, nil — that's success
	// with no issues). For a real list error, override listErr below
	// after processing.
	// To actually simulate a failure on repo "org/a" only, we use a
	// slightly different fake: return an error when repo == "org/a".
	failFake := &failingPromoteClient{
		inner: fake,
		failOnRepo: "org/a",
	}
	n, err := PromoteReady(context.Background(), failFake, baseCfg(), []string{"org/a", "org/b"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if n != 1 {
		t.Errorf("n = %d, want 1 (org/b succeeds even when org/a fails)", n)
	}
}

// failingPromoteClient is a thin decorator that fails ListOpenIssues on a
// specific repo while delegating everything else to the inner fake.
type failingPromoteClient struct {
	inner      *fakePromoteClient
	failOnRepo string
}

func (f *failingPromoteClient) ListOpenIssues(repo string) ([]*github.Issue, error) {
	if repo == f.failOnRepo {
		return nil, errors.New("simulated outage")
	}
	return f.inner.ListOpenIssues(repo)
}
func (f *failingPromoteClient) GetIssue(repo string, n int) (*github.Issue, error) {
	return f.inner.GetIssue(repo, n)
}
func (f *failingPromoteClient) AddLabels(repo string, n int, ls []string) error {
	return f.inner.AddLabels(repo, n, ls)
}
func (f *failingPromoteClient) RemoveLabels(repo string, n int, ls []string) error {
	return f.inner.RemoveLabels(repo, n, ls)
}
func (f *failingPromoteClient) PostComment(repo string, n int, body string) error {
	return f.inner.PostComment(repo, n, body)
}

func TestPromoteReady_CachesGetIssueAcrossBlockedIssues(t *testing.T) {
	// Two blocked issues share the same blocker #5. GetIssue on "org/r#5"
	// must fire ONCE, not twice — the in-pass cache reuses the first
	// result across later blocked issues that reference it.
	blockedA := mkIssue("org/r", 10, "open", "## Depends on\n- #5\n", "blocked")
	blockedB := mkIssue("org/r", 11, "open", "## Depends on\n- #5\n- #6\n", "blocked")
	counting := &countingPromoteClient{
		inner: &fakePromoteClient{
			open: map[string][]*github.Issue{"org/r": {blockedA, blockedB}},
			byRef: map[string]*github.Issue{
				"org/r#5": mkIssue("org/r", 5, "closed", ""),
				"org/r#6": mkIssue("org/r", 6, "closed", ""),
			},
		},
	}

	n, err := PromoteReady(context.Background(), counting, baseCfg(), []string{"org/r"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if n != 2 {
		t.Fatalf("n = %d, want 2", n)
	}
	// Three unique dep refs expected: #5 (shared, cached on second use),
	// #6. That's 2 distinct GetIssue calls — one per unique ref.
	if counting.getIssueCalls != 2 {
		t.Errorf("GetIssue calls = %d, want 2 (cache should dedup shared dep #5)", counting.getIssueCalls)
	}
}

func TestPromoteReady_AuditCommentIncludesDepStates(t *testing.T) {
	blocked := mkIssue("org/r", 10, "open", "## Depends on\n- #5\n- other/r#9\n", "blocked")
	fake := &fakePromoteClient{
		open: map[string][]*github.Issue{"org/r": {blocked}},
		byRef: map[string]*github.Issue{
			"org/r#5":   mkIssue("org/r", 5, "closed", ""),
			"other/r#9": mkIssue("other/r", 9, "closed", ""),
		},
	}
	if _, err := PromoteReady(context.Background(), fake, baseCfg(), []string{"org/r"}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(fake.comments) != 1 {
		t.Fatalf("expected 1 audit comment, got %d", len(fake.comments))
	}
	body := fake.comments[0].Body
	for _, needle := range []string{"org/r#5", "other/r#9", "closed", "ready"} {
		if !strings.Contains(body, needle) {
			t.Errorf("audit body missing %q: %s", needle, body)
		}
	}
}

// countingPromoteClient wraps a fake and counts GetIssue calls, so tests
// can assert on caching behaviour without reaching into internals.
type countingPromoteClient struct {
	inner         *fakePromoteClient
	getIssueCalls int
}

func (c *countingPromoteClient) ListOpenIssues(repo string) ([]*github.Issue, error) {
	return c.inner.ListOpenIssues(repo)
}
func (c *countingPromoteClient) GetIssue(repo string, n int) (*github.Issue, error) {
	c.getIssueCalls++
	return c.inner.GetIssue(repo, n)
}
func (c *countingPromoteClient) AddLabels(repo string, n int, ls []string) error {
	return c.inner.AddLabels(repo, n, ls)
}
func (c *countingPromoteClient) RemoveLabels(repo string, n int, ls []string) error {
	return c.inner.RemoveLabels(repo, n, ls)
}
func (c *countingPromoteClient) PostComment(repo string, n int, body string) error {
	return c.inner.PostComment(repo, n, body)
}

func TestPromoteReady_MissingPromoteTarget_ReturnsError(t *testing.T) {
	cfg := baseCfg()
	cfg.DevelopLabels = nil // no promote target anywhere
	cfg.PromoteToLabel = ""

	_, err := PromoteReady(context.Background(), &fakePromoteClient{}, cfg, []string{"org/r"})
	if err == nil {
		t.Fatal("expected error when promote target unresolved, got nil")
	}
}

func equalSorted(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ca := append([]string{}, a...)
	cb := append([]string{}, b...)
	sort.Strings(ca)
	sort.Strings(cb)
	for i := range ca {
		if ca[i] != cb[i] {
			return false
		}
	}
	return true
}

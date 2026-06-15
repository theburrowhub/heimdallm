package github_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	gh "github.com/heimdallm/daemon/internal/github"
)

// ── helpers ──────────────────────────────────────────────────────────────────

// searchIssueItem builds the JSON shape returned by /search/issues for a
// single issue item. The repository_url is used by SearchIssues to derive Repo.
func searchIssueItem(id int64, number int, repo, title string, labels, assignees []string, isPR bool) map[string]any {
	labelItems := make([]map[string]string, len(labels))
	for i, l := range labels {
		labelItems[i] = map[string]string{"name": l}
	}
	assigneeItems := make([]map[string]string, len(assignees))
	for i, a := range assignees {
		assigneeItems[i] = map[string]string{"login": a}
	}
	item := map[string]any{
		"id":             id,
		"number":         number,
		"title":          title,
		"state":          "open",
		"labels":         labelItems,
		"assignees":      assigneeItems,
		"user":           map[string]string{"login": "author"},
		"html_url":       fmt.Sprintf("https://github.com/%s/issues/%d", repo, number),
		"repository_url": fmt.Sprintf("https://api.github.com/repos/%s", repo),
		"created_at":     time.Now().UTC().Format(time.RFC3339),
		"updated_at":     time.Now().UTC().Format(time.RFC3339),
	}
	if isPR {
		item["pull_request"] = map[string]string{"url": "…"}
	}
	return item
}

// searchEnvelope builds the JSON envelope returned by GET /search/issues.
func searchEnvelope(totalCount int, items []map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{
		"total_count":        totalCount,
		"incomplete_results": false,
		"items":              items,
	})
	return b
}

// ── SearchIssues tests ───────────────────────────────────────────────────────

func TestSearchIssues_ParsesEnvelopeAndDeriveRepo(t *testing.T) {
	item := searchIssueItem(1, 42, "org/repo", "bug title", []string{"bug"}, []string{"alice"}, false)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/search/issues") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(searchEnvelope(1, []map[string]any{item}))
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	issues, err := client.SearchIssues("is:issue is:open assignee:alice repo:org/repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}
	got := issues[0]
	if got.Number != 42 {
		t.Errorf("Number: want 42, got %d", got.Number)
	}
	if got.Title != "bug title" {
		t.Errorf("Title: want %q, got %q", "bug title", got.Title)
	}
	if got.Repo != "org/repo" {
		t.Errorf("Repo: want %q, got %q", "org/repo", got.Repo)
	}
	if len(got.Labels) != 1 || got.Labels[0].Name != "bug" {
		t.Errorf("Labels: want [bug], got %v", got.Labels)
	}
	if len(got.Assignees) != 1 || got.Assignees[0].Login != "alice" {
		t.Errorf("Assignees: want [alice], got %v", got.Assignees)
	}
}

func TestSearchIssues_DropsEmbeddedPullRequests(t *testing.T) {
	items := []map[string]any{
		searchIssueItem(1, 10, "org/repo", "real issue", nil, nil, false),
		searchIssueItem(2, 11, "org/repo", "this is a PR", nil, nil, true),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(searchEnvelope(2, items))
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	issues, err := client.SearchIssues("is:issue is:open")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue (PR dropped), got %d", len(issues))
	}
	if issues[0].Number != 10 {
		t.Errorf("wrong issue kept: %d", issues[0].Number)
	}
}

func TestSearchIssues_PaginatesTwoPages(t *testing.T) {
	// Build page1 (100 items) and page2 (1 item).
	page1Items := make([]map[string]any, 100)
	for i := range page1Items {
		page1Items[i] = searchIssueItem(int64(i+1), i+1, "org/repo", fmt.Sprintf("issue %d", i+1), nil, nil, false)
	}
	page2Items := []map[string]any{
		searchIssueItem(101, 101, "org/repo", "issue 101", nil, nil, false),
	}

	var pageRequests []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page == 0 {
			page = 1
		}
		pageRequests = append(pageRequests, page)
		switch page {
		case 1:
			w.Write(searchEnvelope(101, page1Items))
		case 2:
			w.Write(searchEnvelope(101, page2Items))
		default:
			w.Write(searchEnvelope(101, nil))
		}
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	issues, err := client.SearchIssues("is:issue is:open")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issues) != 101 {
		t.Errorf("expected 101 issues, got %d", len(issues))
	}
	if len(pageRequests) != 2 {
		t.Errorf("expected 2 page requests, got %d: %v", len(pageRequests), pageRequests)
	}
}

func TestSearchIssues_StopsAtPageCap(t *testing.T) {
	// Always return a full page so the paginator would loop forever without the
	// cap.
	pageCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pageCount++
		items := make([]map[string]any, 100)
		for i := range items {
			id := int64(pageCount*100 + i)
			items[i] = searchIssueItem(id, int(id), "org/repo", "issue", nil, nil, false)
		}
		w.Write(searchEnvelope(10000, items))
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	issues, err := client.SearchIssues("is:issue is:open")
	if err != nil {
		t.Fatalf("cap should not surface an error, got: %v", err)
	}
	// Should have exactly 10 pages × 100 = 1000 issues.
	if len(issues) != 1000 {
		t.Errorf("expected 1000 issues at cap, got %d", len(issues))
	}
	if pageCount != 10 {
		t.Errorf("expected exactly 10 page requests (cap), got %d", pageCount)
	}
}

func TestSearchIssues_HTTPErrorReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"rate limited"}`, http.StatusForbidden)
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	_, err := client.SearchIssues("is:issue is:open")
	if err == nil {
		t.Fatal("expected error on 403, got nil")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error should include status code, got: %v", err)
	}
}

func TestSearchIssues_DerivesRepoFromHTMLURL(t *testing.T) {
	// Build an item without repository_url — Repo must be derived from html_url.
	item := map[string]any{
		"id":         int64(99),
		"number":     99,
		"title":      "html url fallback",
		"state":      "open",
		"labels":     []any{},
		"assignees":  []any{},
		"user":       map[string]string{"login": "bob"},
		"html_url":   "https://github.com/myorg/myrepo/issues/99",
		"created_at": time.Now().UTC().Format(time.RFC3339),
		"updated_at": time.Now().UTC().Format(time.RFC3339),
		// repository_url intentionally absent
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(searchEnvelope(1, []map[string]any{item}))
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	issues, err := client.SearchIssues("is:issue is:open")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}
	if issues[0].Repo != "myorg/myrepo" {
		t.Errorf("Repo derived from html_url: want %q, got %q", "myorg/myrepo", issues[0].Repo)
	}
}

// ── buildIssueSearchQuery / BuildIssueSearchQuery tests ──────────────────────

func TestBuildIssueSearchQuery_BaseAlways(t *testing.T) {
	q := gh.BuildIssueSearchQuery(config.IssueTrackingConfig{}, "", nil)
	if !strings.Contains(q, "is:issue") {
		t.Errorf("expected is:issue in query, got %q", q)
	}
	if !strings.Contains(q, "is:open") {
		t.Errorf("expected is:open in query, got %q", q)
	}
}

func TestBuildIssueSearchQuery_AssigneeFromConfig(t *testing.T) {
	it := config.IssueTrackingConfig{Assignees: []string{"alice", "bob"}}
	q := gh.BuildIssueSearchQuery(it, "ignored-user", nil)
	if !strings.Contains(q, "assignee:alice") {
		t.Errorf("expected assignee:alice, got %q", q)
	}
	if !strings.Contains(q, "assignee:bob") {
		t.Errorf("expected assignee:bob, got %q", q)
	}
}

func TestBuildIssueSearchQuery_AssigneeFallsBackToAuthUser(t *testing.T) {
	it := config.IssueTrackingConfig{} // no Assignees
	q := gh.BuildIssueSearchQuery(it, "carol", nil)
	if !strings.Contains(q, "assignee:carol") {
		t.Errorf("expected assignee:carol fallback, got %q", q)
	}
}

func TestBuildIssueSearchQuery_OrgScoping(t *testing.T) {
	it := config.IssueTrackingConfig{Organizations: []string{"myorg"}}
	q := gh.BuildIssueSearchQuery(it, "", nil) // no repos → uses orgs
	if !strings.Contains(q, "org:myorg") {
		t.Errorf("expected org:myorg, got %q", q)
	}
}

func TestBuildIssueSearchQuery_RepoScopingOverridesOrg(t *testing.T) {
	it := config.IssueTrackingConfig{Organizations: []string{"myorg"}}
	q := gh.BuildIssueSearchQuery(it, "", []string{"myorg/repo1", "myorg/repo2"})
	if strings.Contains(q, "org:myorg") {
		t.Errorf("org: qualifier should be absent when repos are provided, got %q", q)
	}
	if !strings.Contains(q, "repo:myorg/repo1") {
		t.Errorf("expected repo:myorg/repo1, got %q", q)
	}
	if !strings.Contains(q, "repo:myorg/repo2") {
		t.Errorf("expected repo:myorg/repo2, got %q", q)
	}
}

func TestBuildIssueSearchQuery_LabelFiltersAddedWhenDefaultIgnore(t *testing.T) {
	it := config.IssueTrackingConfig{
		DefaultAction:    string(config.IssueModeIgnore),
		DevelopLabels:    []string{"bug"},
		ReviewOnlyLabels: []string{"question"},
	}
	q := gh.BuildIssueSearchQuery(it, "", nil)
	if !strings.Contains(q, "label:bug") {
		t.Errorf("expected label:bug, got %q", q)
	}
	if !strings.Contains(q, "label:question") {
		t.Errorf("expected label:question, got %q", q)
	}
}

func TestBuildIssueSearchQuery_NoLabelFiltersWhenDefaultIsNotIgnore(t *testing.T) {
	it := config.IssueTrackingConfig{
		DefaultAction:    string(config.IssueModeReviewOnly),
		DevelopLabels:    []string{"bug"},
		ReviewOnlyLabels: []string{"question"},
	}
	q := gh.BuildIssueSearchQuery(it, "", nil)
	if strings.Contains(q, "label:") {
		t.Errorf("no label qualifier expected when default_action != ignore, got %q", q)
	}
}

func TestBuildIssueSearchQuery_NoLabelFiltersWhenDefaultEmptyAndNoLabels(t *testing.T) {
	it := config.IssueTrackingConfig{} // DefaultAction="" → treated as ignore but no labels
	q := gh.BuildIssueSearchQuery(it, "", nil)
	if strings.Contains(q, "label:") {
		t.Errorf("no label qualifier expected when no labels configured, got %q", q)
	}
}

func TestBuildIssueSearchQuery_FilterModeHasNoEffect(t *testing.T) {
	// filter_mode is applied client-side post-fetch; query should be the same
	// regardless of whether it's inclusive or exclusive.
	base := config.IssueTrackingConfig{
		DevelopLabels: []string{"bug"},
		DefaultAction: string(config.IssueModeIgnore),
	}
	incl := base
	incl.FilterMode = config.FilterModeInclusive
	excl := base
	excl.FilterMode = config.FilterModeExclusive

	qIncl := gh.BuildIssueSearchQuery(incl, "u", nil)
	qExcl := gh.BuildIssueSearchQuery(excl, "u", nil)
	if qIncl != qExcl {
		t.Errorf("filter_mode should not affect search query:\n  inclusive: %q\n  exclusive: %q", qIncl, qExcl)
	}
}

func TestBuildIssueSearchQuery_LabelWithSpacesIsQuoted(t *testing.T) {
	it := config.IssueTrackingConfig{
		DefaultAction: string(config.IssueModeIgnore),
		DevelopLabels: []string{"needs work"},
	}
	q := gh.BuildIssueSearchQuery(it, "", nil)
	if !strings.Contains(q, `label:"needs work"`) {
		t.Errorf("label with spaces should be quoted, got %q", q)
	}
}

func TestBuildIssueSearchQuery_QURLEncodeable(t *testing.T) {
	// Verify the query can be URL-encoded and decoded without loss, which is
	// what SearchIssues does internally via url.Values.Set.
	it := config.IssueTrackingConfig{
		DefaultAction: string(config.IssueModeIgnore),
		DevelopLabels: []string{"bug fix"},
		Assignees:     []string{"alice"},
	}
	q := gh.BuildIssueSearchQuery(it, "", []string{"org/repo"})
	encoded := url.QueryEscape(q)
	decoded, err := url.QueryUnescape(encoded)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if decoded != q {
		t.Errorf("round-trip mismatch:\n  orig:    %q\n  decoded: %q", q, decoded)
	}
}

func TestBuildIssueSearchQuery_DeduplicatesLabels(t *testing.T) {
	it := config.IssueTrackingConfig{
		DefaultAction:    string(config.IssueModeIgnore),
		DevelopLabels:    []string{"bug", "bug"}, // duplicate
		ReviewOnlyLabels: []string{"bug"},        // same label in another dimension
	}
	q := gh.BuildIssueSearchQuery(it, "", nil)
	count := strings.Count(q, "label:bug")
	if count != 1 {
		t.Errorf("expected label:bug to appear exactly once (deduped), got %d in %q", count, q)
	}
}

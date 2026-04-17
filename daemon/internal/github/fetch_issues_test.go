package github_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	gh "github.com/heimdallm/daemon/internal/github"
)

// ── fixtures ─────────────────────────────────────────────────────────────────

type issueFixture struct {
	ID          int64
	Number      int
	Title       string
	Labels      []string
	Assignees   []string
	CreatedAt   time.Time
	IsPR        bool
}

func (f issueFixture) marshal() map[string]any {
	labels := make([]map[string]string, len(f.Labels))
	for i, l := range f.Labels {
		labels[i] = map[string]string{"name": l}
	}
	assignees := make([]map[string]string, len(f.Assignees))
	for i, a := range f.Assignees {
		assignees[i] = map[string]string{"login": a}
	}
	body := map[string]any{
		"id":         f.ID,
		"number":     f.Number,
		"title":      f.Title,
		"state":      "open",
		"labels":     labels,
		"assignees":  assignees,
		"user":       map[string]string{"login": "author"},
		"html_url":   fmt.Sprintf("https://github.com/org/repo/issues/%d", f.Number),
		"created_at": f.CreatedAt.UTC().Format(time.RFC3339),
		"updated_at": f.CreatedAt.UTC().Format(time.RFC3339),
	}
	if f.IsPR {
		body["pull_request"] = map[string]string{"url": "…"}
	}
	return body
}

func issuesPageServer(t *testing.T, pages map[int][]issueFixture) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/repos/") || !strings.HasSuffix(r.URL.Path, "/issues") {
			http.NotFound(w, r)
			return
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		fixtures, ok := pages[page]
		if !ok {
			_ = json.NewEncoder(w).Encode([]any{})
			return
		}
		out := make([]map[string]any, len(fixtures))
		for i, f := range fixtures {
			out[i] = f.marshal()
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
}

func baseCfg() config.IssueTrackingConfig {
	return config.IssueTrackingConfig{
		Enabled:          true,
		FilterMode:       config.FilterModeExclusive,
		DevelopLabels:    []string{"bug", "enhancement"},
		ReviewOnlyLabels: []string{"question"},
		SkipLabels:       []string{"wontfix"},
		DefaultAction:    string(config.IssueModeIgnore),
	}
}

// ── basic classification / filtering ─────────────────────────────────────────

func TestFetchIssues_DropsPullRequests(t *testing.T) {
	srv := issuesPageServer(t, map[int][]issueFixture{
		1: {
			{ID: 1, Number: 1, Title: "real issue", Labels: []string{"bug"}, CreatedAt: time.Now()},
			{ID: 2, Number: 2, Title: "this is a PR", Labels: []string{"bug"}, CreatedAt: time.Now(), IsPR: true},
		},
	})
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	got, err := client.FetchIssues("org/repo", baseCfg(), "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 issue (PR filtered), got %d", len(got))
	}
	if got[0].Title != "real issue" {
		t.Errorf("wrong record kept: %q", got[0].Title)
	}
}

func TestFetchIssues_SkipLabelsWinOverEverything(t *testing.T) {
	srv := issuesPageServer(t, map[int][]issueFixture{
		1: {
			{ID: 10, Number: 10, Title: "closed by policy", Labels: []string{"wontfix", "bug"}, CreatedAt: time.Now()},
			{ID: 11, Number: 11, Title: "ok", Labels: []string{"bug"}, CreatedAt: time.Now()},
		},
	})
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	got, err := client.FetchIssues("org/repo", baseCfg(), "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Number != 11 {
		t.Errorf("expected only #11, got %+v", got)
	}
}

func TestFetchIssues_DefaultActionIgnoreDropsUntaggedIssues(t *testing.T) {
	srv := issuesPageServer(t, map[int][]issueFixture{
		1: {
			{ID: 20, Number: 20, Title: "no label", Labels: nil, CreatedAt: time.Now()},
		},
	})
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	got, err := client.FetchIssues("org/repo", baseCfg(), "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected default_action=ignore to drop untagged issue, got %d", len(got))
	}
}

func TestFetchIssues_DefaultActionReviewOnlyKeepsUntaggedIssues(t *testing.T) {
	cfg := baseCfg()
	cfg.DefaultAction = string(config.IssueModeReviewOnly)
	srv := issuesPageServer(t, map[int][]issueFixture{
		1: {
			{ID: 30, Number: 30, Title: "no label", Labels: nil, CreatedAt: time.Now()},
		},
	})
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	got, err := client.FetchIssues("org/repo", cfg, "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Mode != config.IssueModeReviewOnly {
		t.Errorf("expected 1 review_only issue, got %+v", got)
	}
}

func TestFetchIssues_AssignsModeFromClassifier(t *testing.T) {
	srv := issuesPageServer(t, map[int][]issueFixture{
		1: {
			{ID: 40, Number: 40, Title: "dev", Labels: []string{"bug"}, CreatedAt: time.Now()},
			{ID: 41, Number: 41, Title: "review", Labels: []string{"question"}, CreatedAt: time.Now()},
		},
	})
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	got, _ := client.FetchIssues("org/repo", baseCfg(), "")
	modes := map[int]config.IssueMode{}
	for _, i := range got {
		modes[i.Number] = i.Mode
	}
	if modes[40] != config.IssueModeDevelop {
		t.Errorf("#40 should be develop, got %q", modes[40])
	}
	if modes[41] != config.IssueModeReviewOnly {
		t.Errorf("#41 should be review_only, got %q", modes[41])
	}
}

// ── dimension filters ────────────────────────────────────────────────────────

func TestFetchIssues_OrganizationsFilter(t *testing.T) {
	cfg := baseCfg()
	cfg.Organizations = []string{"wanted-org"}
	srv := issuesPageServer(t, map[int][]issueFixture{
		1: {{ID: 50, Number: 50, Title: "x", Labels: []string{"bug"}, CreatedAt: time.Now()}},
	})
	defer srv.Close()
	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))

	got, _ := client.FetchIssues("wanted-org/repo", cfg, "")
	if len(got) != 1 {
		t.Errorf("expected issue kept for wanted-org, got %d", len(got))
	}
	got, _ = client.FetchIssues("other-org/repo", cfg, "")
	if len(got) != 0 {
		t.Errorf("expected issue dropped for other-org, got %d", len(got))
	}
}

func TestFetchIssues_OrganizationsFilter_CaseInsensitive(t *testing.T) {
	cfg := baseCfg()
	cfg.Organizations = []string{"Freepik-Company"}
	srv := issuesPageServer(t, map[int][]issueFixture{
		1: {{ID: 51, Number: 51, Title: "x", Labels: []string{"bug"}, CreatedAt: time.Now()}},
	})
	defer srv.Close()
	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))

	got, _ := client.FetchIssues("freepik-company/repo", cfg, "")
	if len(got) != 1 {
		t.Errorf("case-insensitive org match expected, got %d", len(got))
	}
}

func TestFetchIssues_AssigneesFilter(t *testing.T) {
	cfg := baseCfg()
	cfg.Assignees = []string{"alice"}
	srv := issuesPageServer(t, map[int][]issueFixture{
		1: {
			{ID: 60, Number: 60, Title: "alice's", Assignees: []string{"alice"}, Labels: []string{"bug"}, CreatedAt: time.Now()},
			{ID: 61, Number: 61, Title: "bob's", Assignees: []string{"bob"}, Labels: []string{"bug"}, CreatedAt: time.Now()},
			{ID: 62, Number: 62, Title: "unassigned", Labels: []string{"bug"}, CreatedAt: time.Now()},
		},
	})
	defer srv.Close()
	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))

	got, _ := client.FetchIssues("org/repo", cfg, "")
	if len(got) != 1 || got[0].Number != 60 {
		t.Errorf("expected only alice's issue, got %+v", got)
	}
}

// ── filter_mode ──────────────────────────────────────────────────────────────

func TestFetchIssues_FilterModeExclusive_AllMustPass(t *testing.T) {
	cfg := baseCfg()
	cfg.FilterMode = config.FilterModeExclusive
	cfg.Organizations = []string{"wanted-org"}
	cfg.Assignees = []string{"alice"}
	srv := issuesPageServer(t, map[int][]issueFixture{
		1: {
			{ID: 70, Number: 70, Assignees: []string{"alice"}, Labels: []string{"bug"}, CreatedAt: time.Now()},
			{ID: 71, Number: 71, Assignees: []string{"bob"}, Labels: []string{"bug"}, CreatedAt: time.Now()}, // wrong assignee
		},
	})
	defer srv.Close()
	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))

	got, _ := client.FetchIssues("wanted-org/repo", cfg, "")
	if len(got) != 1 || got[0].Number != 70 {
		t.Errorf("exclusive: only #70 should pass (all filters match), got %+v", got)
	}

	got, _ = client.FetchIssues("other-org/repo", cfg, "")
	if len(got) != 0 {
		t.Errorf("exclusive: no issue should pass when org filter fails, got %d", len(got))
	}
}

func TestFetchIssues_FilterModeInclusive_AtLeastOneDimensionPasses(t *testing.T) {
	cfg := baseCfg()
	cfg.FilterMode = config.FilterModeInclusive
	cfg.Organizations = []string{"wanted-org"}
	cfg.Assignees = []string{"alice"}
	srv := issuesPageServer(t, map[int][]issueFixture{
		1: {
			{ID: 80, Number: 80, Assignees: []string{"bob"}, Labels: []string{"bug"}, CreatedAt: time.Now()}, // org matches (we'll fetch via wanted-org)
			{ID: 81, Number: 81, Assignees: []string{"alice"}, Labels: []string{"bug"}, CreatedAt: time.Now()}, // assignee matches even in other-org
		},
	})
	defer srv.Close()
	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))

	// Fetched from wanted-org — org passes, so both issues are kept.
	got, _ := client.FetchIssues("wanted-org/repo", cfg, "")
	if len(got) != 2 {
		t.Errorf("inclusive + org matches: expected 2 issues, got %d", len(got))
	}

	// Fetched from other-org — org fails, assignee decides.
	got, _ = client.FetchIssues("other-org/repo", cfg, "")
	if len(got) != 1 || got[0].Number != 81 {
		t.Errorf("inclusive + only assignee matches: expected #81 only, got %+v", got)
	}
}

// ── sort ─────────────────────────────────────────────────────────────────────

func TestFetchIssues_SortsAssignedUserBeforeOthers(t *testing.T) {
	old := time.Now().Add(-48 * time.Hour)
	srv := issuesPageServer(t, map[int][]issueFixture{
		1: {
			{ID: 90, Number: 90, Labels: []string{"bug"}, CreatedAt: old},
			{ID: 91, Number: 91, Assignees: []string{"alice"}, Labels: []string{"bug"}, CreatedAt: time.Now()},
		},
	})
	defer srv.Close()
	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))

	got, _ := client.FetchIssues("org/repo", baseCfg(), "alice")
	if len(got) != 2 || got[0].Number != 91 {
		t.Errorf("issues assigned to authenticated user must come first, got %+v", got)
	}
}

func TestFetchIssues_SortsReviewOnlyBeforeDevelop(t *testing.T) {
	t0 := time.Now()
	srv := issuesPageServer(t, map[int][]issueFixture{
		1: {
			{ID: 100, Number: 100, Labels: []string{"bug"}, CreatedAt: t0.Add(-1 * time.Hour)}, // develop, older
			{ID: 101, Number: 101, Labels: []string{"question"}, CreatedAt: t0},                // review_only, newer
		},
	})
	defer srv.Close()
	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))

	got, _ := client.FetchIssues("org/repo", baseCfg(), "")
	if len(got) != 2 || got[0].Number != 101 {
		t.Errorf("review_only must sort before develop, got %+v", got)
	}
}

func TestFetchIssues_SortsOldestFirstWithinMode(t *testing.T) {
	t0 := time.Now()
	srv := issuesPageServer(t, map[int][]issueFixture{
		1: {
			{ID: 110, Number: 110, Labels: []string{"bug"}, CreatedAt: t0},
			{ID: 111, Number: 111, Labels: []string{"bug"}, CreatedAt: t0.Add(-24 * time.Hour)},
			{ID: 112, Number: 112, Labels: []string{"bug"}, CreatedAt: t0.Add(-72 * time.Hour)},
		},
	})
	defer srv.Close()
	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))

	got, _ := client.FetchIssues("org/repo", baseCfg(), "")
	if len(got) != 3 {
		t.Fatalf("expected 3 issues, got %d", len(got))
	}
	if got[0].Number != 112 || got[1].Number != 111 || got[2].Number != 110 {
		t.Errorf("expected oldest first, got %d → %d → %d", got[0].Number, got[1].Number, got[2].Number)
	}
}

// ── pagination ───────────────────────────────────────────────────────────────

func TestFetchIssues_Pagination(t *testing.T) {
	t0 := time.Now()
	page1 := make([]issueFixture, 100)
	for i := range page1 {
		page1[i] = issueFixture{
			ID: int64(200 + i), Number: 200 + i,
			Labels: []string{"bug"}, CreatedAt: t0,
		}
	}
	srv := issuesPageServer(t, map[int][]issueFixture{
		1: page1,
		2: {{ID: 300, Number: 300, Labels: []string{"bug"}, CreatedAt: t0}},
	})
	defer srv.Close()
	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))

	got, err := client.FetchIssues("org/repo", baseCfg(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 101 {
		t.Errorf("expected 101 issues across 2 pages, got %d", len(got))
	}
}

func TestFetchIssues_PaginationCapBounded(t *testing.T) {
	// Server that always returns a full page of issues. FetchIssues should
	// stop at its internal max-pages cap and return the accumulated results
	// without error instead of looping forever.
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		// Each page returns 100 unique issues so IDs don't collide.
		items := make([]map[string]any, 100)
		for i := range items {
			id := int64(page*100 + i)
			items[i] = issueFixture{
				ID: id, Number: int(id),
				Labels:    []string{"bug"},
				CreatedAt: time.Now(),
			}.marshal()
		}
		_ = json.NewEncoder(w).Encode(items)
	}))
	defer srv.Close()
	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))

	got, err := client.FetchIssues("org/repo", baseCfg(), "")
	if err != nil {
		t.Fatalf("cap should not surface an error, got %v", err)
	}
	// Never exceed the cap × issuesPerPage.
	if len(got) > 10*100 {
		t.Errorf("result exceeded cap: got %d issues", len(got))
	}
	// Never issue more requests than the cap.
	if c := atomic.LoadInt32(&calls); c > 10 {
		t.Errorf("requests exceeded cap: got %d", c)
	}
}

func TestFetchIssues_PaginationStopsAtPartialPage(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page != 1 {
			t.Errorf("unexpected fetch of page %d (first page was partial)", page)
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			issueFixture{ID: 400, Number: 400, Labels: []string{"bug"}, CreatedAt: time.Now()}.marshal(),
		})
	}))
	defer srv.Close()
	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))

	_, err := client.FetchIssues("org/repo", baseCfg(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("expected 1 HTTP call for partial page, got %d", got)
	}
}

// ── error surfaces ───────────────────────────────────────────────────────────

func TestFetchIssues_EmptyRepoReturnsError(t *testing.T) {
	client := gh.NewClient("fake")
	_, err := client.FetchIssues("", baseCfg(), "")
	if err == nil {
		t.Fatal("expected error for empty repo, got nil")
	}
}

func TestFetchIssues_HTTPErrorSurfaced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	}))
	defer srv.Close()
	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))

	_, err := client.FetchIssues("org/repo", baseCfg(), "")
	if err == nil {
		t.Fatal("expected error on 404, got nil")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should surface status code, got: %v", err)
	}
}

func TestFetchIssues_InvalidJSONSurfaced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer srv.Close()
	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))

	_, err := client.FetchIssues("org/repo", baseCfg(), "")
	if err == nil {
		t.Fatal("expected decode error, got nil")
	}
}

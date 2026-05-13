package github_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	gh "github.com/heimdallm/daemon/internal/github"
)

type repoItem struct {
	FullName string `json:"full_name"`
	Archived bool   `json:"archived"`
	Disabled bool   `json:"disabled"`
}

func searchResponse(items []repoItem) []byte {
	return searchResponseWithTotal(items, len(items))
}

func searchResponseWithTotal(items []repoItem, total int) []byte {
	body := map[string]any{
		"total_count": total,
		"items":       items,
	}
	data, _ := json.Marshal(body)
	return data
}

func TestFetchReposByTopic_EmptyArgsReturnNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("should not call API when args are empty, got %s", r.URL.String())
	}))
	defer srv.Close()

	client := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))

	if got, err := client.FetchReposByTopic("", []string{"org"}); err != nil || got != nil {
		t.Errorf("empty topic: got %v, %v", got, err)
	}
	if got, err := client.FetchReposByTopic("topic", nil); err != nil || got != nil {
		t.Errorf("empty orgs: got %v, %v", got, err)
	}
}

func TestFetchReposByTopic_SingleOrg(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search/repositories" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query().Get("q")
		if !strings.Contains(q, "topic:heimdallm-review") {
			t.Errorf("query missing topic filter: %q", q)
		}
		if !strings.Contains(q, "org:freepik-company") {
			t.Errorf("query missing org filter: %q", q)
		}
		if !strings.Contains(q, "archived:false") {
			t.Errorf("query missing archived:false: %q", q)
		}
		w.Write(searchResponse([]repoItem{
			{FullName: "freepik-company/ai-platform"},
			{FullName: "freepik-company/design-system"},
		}))
	}))
	defer srv.Close()

	client := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	got, err := client.FetchReposByTopic("heimdallm-review", []string{"freepik-company"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"freepik-company/ai-platform", "freepik-company/design-system"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestFetchReposByTopic_MultipleOrgsDeduped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		switch {
		case strings.Contains(q, "org:orgA"):
			w.Write(searchResponse([]repoItem{
				{FullName: "orgA/repo1"},
				{FullName: "shared/common"},
			}))
		case strings.Contains(q, "org:orgB"):
			w.Write(searchResponse([]repoItem{
				{FullName: "shared/common"},
				{FullName: "orgB/repo2"},
			}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	got, err := client.FetchReposByTopic("topic", []string{"orgA", "orgB"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"orgA/repo1", "orgB/repo2", "shared/common"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v (sorted + deduped)", got, want)
	}
}

func TestFetchReposByTopic_FiltersArchivedAndDisabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(searchResponse([]repoItem{
			{FullName: "org/alive"},
			{FullName: "org/archived", Archived: true},
			{FullName: "org/disabled", Disabled: true},
			{FullName: "", Archived: false},
		}))
	}))
	defer srv.Close()

	client := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	got, err := client.FetchReposByTopic("topic", []string{"org"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0] != "org/alive" {
		t.Errorf("filters failed, got %v", got)
	}
}

func TestFetchReposByTopic_Pagination(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		switch page {
		case 1:
			items := make([]repoItem, 100)
			for i := range items {
				items[i] = repoItem{FullName: fmt.Sprintf("org/repo-%03d", i)}
			}
			// total_count=101 signals a second page upstream.
			w.Write(searchResponseWithTotal(items, 101))
		case 2:
			w.Write(searchResponseWithTotal([]repoItem{
				{FullName: "org/repo-final"},
			}, 101))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	got, err := client.FetchReposByTopic("topic", []string{"org"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 101 {
		t.Errorf("expected 101 repos after pagination, got %d", len(got))
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Errorf("expected 2 API calls for pagination, got %d", calls)
	}
}

func TestFetchReposByTopic_PaginationStopsAtTotalCount(t *testing.T) {
	// Regression: when total_count is an exact multiple of per_page (100),
	// the loop used to make one extra request that returned 0 items.
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page != 1 {
			t.Errorf("unexpected second request on page %d — loop should have stopped", page)
			http.NotFound(w, r)
			return
		}
		items := make([]repoItem, 100)
		for i := range items {
			items[i] = repoItem{FullName: fmt.Sprintf("org/repo-%03d", i)}
		}
		w.Write(searchResponse(items))
	}))
	defer srv.Close()

	client := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	got, err := client.FetchReposByTopic("topic", []string{"org"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 100 {
		t.Errorf("expected 100 repos, got %d", len(got))
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("expected exactly 1 API call (total == 100 == per_page), got %d", calls)
	}
}

func TestFetchReposByTopic_PartialFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, _ := url.QueryUnescape(r.URL.Query().Get("q"))
		switch {
		case strings.Contains(q, "org:healthy"):
			w.Write(searchResponse([]repoItem{{FullName: "healthy/repo"}}))
		case strings.Contains(q, "org:broken"):
			http.Error(w, `{"message":"rate limit exceeded"}`, http.StatusForbidden)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	got, err := client.FetchReposByTopic("topic", []string{"healthy", "broken"})
	if err == nil {
		t.Fatal("expected partial failure error, got nil")
	}
	if !strings.Contains(err.Error(), "broken") {
		t.Errorf("error should mention failing org, got: %v", err)
	}
	if len(got) != 1 || got[0] != "healthy/repo" {
		t.Errorf("expected partial result %v, got %v", []string{"healthy/repo"}, got)
	}
}

func TestFetchReposByTopic_AllOrgsFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"forbidden"}`, http.StatusForbidden)
	}))
	defer srv.Close()

	client := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	got, err := client.FetchReposByTopic("topic", []string{"a", "b"})
	if err == nil {
		t.Fatal("expected error when all orgs fail, got nil")
	}
	if len(got) != 0 {
		t.Errorf("expected empty result, got %v", got)
	}
}

// ── IsRepoArchived ─────────────────────────────────────────────────────────

func TestIsRepoArchived_ActiveRepo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/org/active" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Write([]byte(`{"full_name":"org/active","archived":false}`))
	}))
	defer srv.Close()

	client := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	archived, err := client.IsRepoArchived("org/active")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if archived {
		t.Error("expected false for active repo")
	}
}

func TestIsRepoArchived_ArchivedRepo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"full_name":"org/old","archived":true}`))
	}))
	defer srv.Close()

	client := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	archived, err := client.IsRepoArchived("org/old")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !archived {
		t.Error("expected true for archived repo")
	}
}

func TestIsRepoArchived_DeletedRepo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	client := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	archived, err := client.IsRepoArchived("org/deleted")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !archived {
		t.Error("expected true for deleted (404) repo")
	}
}

func TestIsRepoArchived_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"rate limit"}`, http.StatusForbidden)
	}))
	defer srv.Close()

	client := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	_, err := client.IsRepoArchived("org/repo")
	if err == nil {
		t.Fatal("expected error on non-200/404 status")
	}
}

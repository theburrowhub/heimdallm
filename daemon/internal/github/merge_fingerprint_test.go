package github_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gh "github.com/heimdallm/daemon/internal/github"
)

// Pagination pages carry only the paginated connection: resending the whole
// merge-status query per page multiplied its cost for no data.
func TestGetMergeStatus_PagesSendTrimmedQueries(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		queries = append(queries, req.Query)
		body := mergeStatusJSON
		if len(queries) == 1 {
			body = strings.Replace(body,
				`"pageInfo": {"hasNextPage": false, "endCursor": null},
              "nodes": [
                {"__typename": "CheckRun", "name": "build"`,
				`"pageInfo": {"hasNextPage": true, "endCursor": "cursor1"},
              "nodes": [
                {"__typename": "CheckRun", "name": "build"`, 1)
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	if _, err := c.GetMergeStatus("acme/widgets", 7); err != nil {
		t.Fatalf("GetMergeStatus: %v", err)
	}
	if len(queries) != 2 {
		t.Fatalf("queries = %d, want the first page plus one check page", len(queries))
	}
	if !strings.Contains(queries[0], "branchProtectionRule") {
		t.Fatal("the first query must be the full merge-status query")
	}
	page := queries[1]
	for _, absent := range []string{"branchProtectionRule", "latestOpinionatedReviews", "reviewThreads", "viewer"} {
		if strings.Contains(page, absent) {
			t.Errorf("check page query must not select %s:\n%s", absent, page)
		}
	}
	if !strings.Contains(page, "after: $checkCursor") || !strings.Contains(page, "isRequired(pullRequestNumber: $number)") {
		t.Errorf("check page query is missing its connection:\n%s", page)
	}
}

func fingerprintServer(t *testing.T, state *string, calls *int) *gh.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		if r.Header.Get("Accept") != "application/vnd.github.merge-info-preview+json" {
			t.Errorf("Accept = %q, mergeStateStatus needs the preview", r.Header.Get("Accept"))
		}
		var req struct {
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		data := map[string]any{}
		var errs []any
		for i := 0; ; i++ {
			n, ok := req.Variables[fmt.Sprintf("number_%d", i)].(float64)
			if !ok {
				break
			}
			alias := fmt.Sprintf("b%d", i)
			switch int(n) {
			case 404:
				data[alias] = nil
				errs = append(errs, map[string]any{"type": "NOT_FOUND", "message": "x", "path": []any{alias}})
			case 405:
				data[alias] = nil
			default:
				data[alias] = map[string]any{"pullRequest": map[string]any{"number": n, "mergeStateStatus": *state}}
			}
		}
		body := map[string]any{"data": data}
		if len(errs) > 0 {
			body["errors"] = errs
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return gh.NewClient("fake", gh.WithBaseURL(srv.URL))
}

func TestMergeFingerprints_BatchedStableAndChangeSensitive(t *testing.T) {
	state, calls := "BLOCKED", 0
	c := fingerprintServer(t, &state, &calls)
	refs := []gh.PRRef{
		{Repo: "acme/a", Number: 1}, {Repo: "acme/a", Number: 1}, {Repo: "acme/b", Number: 2},
		{Repo: "acme/a", Number: 404}, {Repo: "acme/a", Number: 405}, {Repo: "bad", Number: 3},
	}
	first := c.MergeFingerprints(refs)
	if calls != 1 {
		t.Fatalf("calls = %d, want one batched query", calls)
	}
	if len(first) != 2 || first[gh.PRKey("acme/a", 1)] == "" || first[gh.PRKey("ACME/b", 2)] == "" {
		t.Fatalf("fingerprints = %v", first)
	}
	if first[gh.PRKey("acme/a", 1)] == first[gh.PRKey("acme/b", 2)] {
		t.Fatal("different PRs must not share a fingerprint")
	}
	again := c.MergeFingerprints(refs)
	if again[gh.PRKey("acme/a", 1)] != first[gh.PRKey("acme/a", 1)] {
		t.Fatal("an unchanged PR must keep its fingerprint")
	}
	state = "CLEAN"
	moved := c.MergeFingerprints(refs)
	if moved[gh.PRKey("acme/a", 1)] == first[gh.PRKey("acme/a", 1)] {
		t.Fatal("a state change must change the fingerprint")
	}
	if got := c.MergeFingerprints(nil); len(got) != 0 || calls != 3 {
		t.Fatalf("empty refs must not query (calls=%d)", calls)
	}
}

func TestMergeFingerprints_RateLimitedReturnsNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	if got := c.MergeFingerprints([]gh.PRRef{{Repo: "acme/a", Number: 1}}); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
	if got := c.MergeFingerprints([]gh.PRRef{{Repo: "acme/a", Number: 1}}); len(got) != 0 {
		t.Fatalf("breaker open: got %v", got)
	}
}

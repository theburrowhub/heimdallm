package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// repoMetaServer answers batched repository lookups from meta (nil = null,
// i.e. GraphQL could not resolve it) and REST /repos/{r} with a 301-followed
// canonical name, counting both.
func repoMetaServer(t *testing.T, meta map[string]map[string]any) (*Client, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	var gql, rest atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql" {
			rest.Add(1)
			_, _ = w.Write([]byte(`{"full_name":"rest/canonical","archived":true}`))
			return
		}
		gql.Add(1)
		var req batchRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		data := map[string]any{}
		for i := 0; ; i++ {
			owner, ok := req.Variables[fmt.Sprintf("owner_%d", i)]
			if !ok {
				break
			}
			repo := fmt.Sprintf("%v/%v", owner, req.Variables[fmt.Sprintf("name_%d", i)])
			if m, found := meta[repo]; found && m != nil {
				data[fmt.Sprintf("b%d", i)] = m
			} else {
				data[fmt.Sprintf("b%d", i)] = nil
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)
	c := NewClient("tok", WithBaseURL(srv.URL))
	c.SetCacheEnabled(false)
	return c, &gql, &rest
}

func TestPrefetchRepoMeta_ServesArchiveAndRenameLookups(t *testing.T) {
	c, gql, rest := repoMetaServer(t, map[string]map[string]any{
		"acme/live":    {"nameWithOwner": "acme/live", "isArchived": false},
		"acme/old":     {"nameWithOwner": "acme/new", "isArchived": false},
		"acme/frozen":  {"nameWithOwner": "acme/frozen", "isArchived": true},
		"acme/garbled": {"nameWithOwner": ""},
	})
	c.PrefetchRepoMeta([]string{"acme/live", "ACME/live", "acme/old", "acme/frozen", "acme/garbled", "acme/gone", "bad"})
	if gql.Load() != 1 {
		t.Fatalf("graphql = %d, want one batched query", gql.Load())
	}
	if a, err := c.IsRepoArchived("acme/frozen"); err != nil || !a {
		t.Fatalf("frozen archived=%v err=%v", a, err)
	}
	if a, err := c.IsRepoArchived("acme/live"); err != nil || a {
		t.Fatalf("live archived=%v err=%v", a, err)
	}
	if n, err := c.GetCanonicalFullName("acme/old"); err != nil || n != "acme/new" {
		t.Fatalf("canonical=%q err=%v", n, err)
	}
	if rest.Load() != 0 {
		t.Fatalf("rest = %d, prefetched repos must not hit REST", rest.Load())
	}
	// Unresolved repos fall back to REST, which follows rename redirects.
	if n, err := c.GetCanonicalFullName("acme/gone"); err != nil || n != "rest/canonical" {
		t.Fatalf("fallback canonical=%q err=%v", n, err)
	}
	if _, err := c.IsRepoArchived("acme/garbled"); err != nil || rest.Load() != 2 {
		t.Fatalf("rest=%d err=%v", rest.Load(), err)
	}
	// Expired answers are not used.
	c.repoMeta.mu.Lock()
	m := c.repoMeta.entries["acme/live"]
	m.fetchedAt = time.Now().Add(-2 * repoMetaMaxAge)
	c.repoMeta.entries["acme/live"] = m
	c.repoMeta.mu.Unlock()
	if _, err := c.IsRepoArchived("acme/live"); err != nil || rest.Load() != 3 {
		t.Fatalf("expired entry: rest=%d err=%v", rest.Load(), err)
	}
	c.PrefetchRepoMeta(nil)
	if gql.Load() != 1 {
		t.Fatal("empty prefetch must not query")
	}
}

func TestPrefetchRepoMeta_FailureLeavesRESTPath(t *testing.T) {
	c, gql, _ := repoMetaServer(t, nil)
	c.pauseRateLimit(time.Minute)
	c.PrefetchRepoMeta([]string{"acme/a"})
	if gql.Load() != 0 {
		t.Fatal("breaker must hold the batch")
	}
	if _, ok := c.cachedRepoMeta("acme/a"); ok {
		t.Fatal("nothing must be cached")
	}
}

// timelineGQLServer answers timeline queries with pages (newest first: pages[0]
// answers the first request) and team lookups with the bot's teams.
func timelineGQLServer(t *testing.T, pages [][]map[string]any, status int) (*Client, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	var timelineCalls, rest atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql" {
			rest.Add(1)
			_, _ = w.Write([]byte(`[{"event":"review_requested","created_at":"2026-10-01T10:00:00Z","requested_reviewer":{"login":"heimdallm-bot"}}]`))
			return
		}
		var req batchRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if strings.Contains(req.Query, "organization(") {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"b0": map[string]any{"teams": map[string]any{"nodes": []any{map[string]any{"slug": "core"}}}},
			}})
			return
		}
		n := int(timelineCalls.Add(1)) - 1
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		if n >= len(pages) {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": nil}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
			"timelineItems": map[string]any{
				"pageInfo": map[string]any{"hasPreviousPage": n+1 < len(pages), "startCursor": fmt.Sprintf("c%d", n)},
				"nodes":    pages[n],
			},
		}}}})
	}))
	t.Cleanup(srv.Close)
	return NewClient("tok", WithBaseURL(srv.URL)), &timelineCalls, &rest
}

func requested(at, typename, login, slug string) map[string]any {
	r := map[string]any{"__typename": typename, "login": login}
	if typename == "Team" {
		r = map[string]any{"__typename": "Team", "slug": slug, "organization": map[string]any{"login": "org"}}
	}
	return map[string]any{"__typename": "ReviewRequestedEvent", "createdAt": at, "actor": map[string]any{"login": "alice"}, "requestedReviewer": r}
}

func dismissed(at, author string) map[string]any {
	return map[string]any{"__typename": "ReviewDismissedEvent", "createdAt": at, "actor": map[string]any{"login": "alice"},
		"review": map[string]any{"author": map[string]any{"login": author}}}
}

func TestTimelineGraphQL_FiltersTeamsAndStopsAtTheLatestRelevantPage(t *testing.T) {
	c, calls, rest := timelineGQLServer(t, [][]map[string]any{
		{requested("2026-10-01T12:00:00Z", "User", "someone-else", "")}, // newest page: nothing for us
		{
			requested("2026-10-01T10:00:00Z", "Team", "", "core"),
			dismissed("2026-10-01T10:00:00Z", "heimdallm-bot"),
			requested("2026-10-01T09:00:00Z", "Team", "", "web"),
			dismissed("2026-10-01T08:00:00Z", "someone-else"),
			{"__typename": "ReviewRequestedEvent", "createdAt": "2026-10-01T07:00:00Z"},
		},
		{requested("2026-10-01T06:00:00Z", "User", "heimdallm-bot", "")}, // never fetched
	}, 0)
	events, err := c.GetPRTimelineEventsForReviewer("org/repo", 7, "heimdallm-bot")
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || rest.Load() != 0 {
		t.Fatalf("timeline pages=%d rest=%d, want two GraphQL pages and no REST", calls.Load(), rest.Load())
	}
	if len(events) != 2 || events[0].Event != "review_dismissed" || events[1].Event != "review_requested" || events[1].Actor != "alice" {
		t.Fatalf("events = %+v, want the same-second team re-request last", events)
	}
}

func TestTimelineGraphQL_NoEventsIsEmpty(t *testing.T) {
	c, _, _ := timelineGQLServer(t, [][]map[string]any{{}}, 0)
	events, err := c.GetPRTimelineEventsForReviewer("org/repo", 7, "heimdallm-bot")
	if err != nil || events == nil || len(events) != 0 {
		t.Fatalf("events=%v err=%v, want an empty non-nil slice", events, err)
	}
}

func TestTimelineGraphQL_FailureFallsBackToREST(t *testing.T) {
	c, _, rest := timelineGQLServer(t, nil, http.StatusBadGateway)
	events, err := c.GetPRTimelineEventsForReviewer("org/repo", 7, "heimdallm-bot")
	if err != nil || rest.Load() != 1 || len(events) != 1 {
		t.Fatalf("events=%v err=%v rest=%d", events, err, rest.Load())
	}
	// A missing PR on the GraphQL side also falls back.
	c2, _, rest2 := timelineGQLServer(t, nil, 0)
	if _, err := c2.GetPRTimelineEventsForReviewer("org/repo", 7, "heimdallm-bot"); err != nil || rest2.Load() != 1 {
		t.Fatalf("err=%v rest=%d", err, rest2.Load())
	}
}

func TestTimelineGraphQL_RateLimitDoesNotFallBack(t *testing.T) {
	c, _, rest := timelineGQLServer(t, nil, 0)
	c.ReviewerIdentityFor("heimdallm-bot", "org/repo")
	c.pauseRateLimit(time.Minute)
	_, err := c.GetPRTimelineEventsForReviewer("org/repo", 7, "heimdallm-bot")
	var rl *RateLimitError
	if !errors.As(err, &rl) || rest.Load() != 0 {
		t.Fatalf("err=%v rest=%d", err, rest.Load())
	}
}

func TestTimelineGraphQL_PageCapIsAnError(t *testing.T) {
	var pages [][]map[string]any
	for i := 0; i <= maxTimelineGraphQLPages; i++ {
		pages = append(pages, []map[string]any{requested("2026-10-01T10:00:00Z", "User", "other", "")})
	}
	c, _, _ := timelineGQLServer(t, pages, 0)
	if _, err := c.timelineEventsGraphQL("org/repo", 7, "heimdallm-bot"); err == nil {
		t.Fatal("want an error past the page cap (fail closed)")
	}
	if _, err := c.timelineEventsGraphQL("bad", 7, "heimdallm-bot"); err == nil {
		t.Fatal("want an error for a malformed repo")
	}
	c.SetGraphQLGate(func() error { return errors.New("no budget") })
	if _, err := c.timelineEventsGraphQL("org/repo", 7, "heimdallm-bot"); err == nil {
		t.Fatal("a refused gate must fail")
	}
}

func TestRESTTimeline_TeamRequestCounts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/graphql" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"b0": map[string]any{"teams": map[string]any{"nodes": []any{map[string]any{"slug": "core"}}}},
			}})
			return
		}
		_, _ = w.Write([]byte(`[{"event":"review_requested","created_at":"2026-10-01T10:00:00Z","requested_team":{"slug":"core"}},
			{"event":"review_requested","created_at":"2026-10-01T11:00:00Z","requested_team":{"slug":"web"}}]`))
	}))
	t.Cleanup(srv.Close)
	c := NewClient("tok", WithBaseURL(srv.URL))
	events, err := c.restTimelineEventsForReviewer("org/repo", 7, "heimdallm-bot")
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%v err=%v, want only the request to the bot's team", events, err)
	}
}

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

func prNode(repo string, number int, extra map[string]any) map[string]any {
	n := map[string]any{
		"databaseId": 1000 + number, "number": number, "title": fmt.Sprintf("PR %d", number),
		"url": fmt.Sprintf("https://github.com/%s/pull/%d", repo, number), "state": "OPEN",
		"isDraft": false, "updatedAt": "2026-10-01T10:00:00Z",
		"headRefOid": "aaaaaaaa", "headRefName": "feat", "baseRefOid": "bbbbbbbb", "baseRefName": "main",
		"author":         map[string]any{"login": "bob"},
		"repository":     map[string]any{"nameWithOwner": repo},
		"headRepository": map[string]any{"nameWithOwner": "fork/" + strings.Split(repo, "/")[1]},
		"assignees":      map[string]any{"nodes": []any{map[string]any{"login": "carol"}}},
		"reviewRequests": map[string]any{"nodes": []any{
			map[string]any{"requestedReviewer": map[string]any{"__typename": "User", "login": "octocat"}},
		}},
	}
	for k, v := range extra {
		n[k] = v
	}
	return n
}

func searchPage(next string, nodes ...map[string]any) map[string]any {
	return map[string]any{
		"pageInfo": map[string]any{"hasNextPage": next != "", "endCursor": next},
		"nodes":    nodes,
	}
}

// invServer serves /user and a GraphQL endpoint whose aliases are answered by
// respond(qualifier, cursor). It counts GraphQL and REST search requests.
type invServer struct {
	graphql, rest atomic.Int64
	client        *Client
}

func newInvServer(t *testing.T, respond func(q, cursor string) (any, *map[string]any)) *invServer {
	t.Helper()
	is := &invServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			_, _ = w.Write([]byte(`{"login":"octocat"}`))
		case "/search/issues":
			is.rest.Add(1)
			_, _ = w.Write([]byte(`{"items":[{"id":7,"number":7,"repository_url":"https://api.github.com/repos/rest/fallback"}]}`))
		case "/graphql":
			is.graphql.Add(1)
			var req batchRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			data := map[string]any{}
			var errs []any
			for i := 0; ; i++ {
				qv, ok := req.Variables[fmt.Sprintf("q_%d", i)]
				if !ok {
					break
				}
				q := qv.(string)
				cursor, _ := req.Variables[fmt.Sprintf("after_%d", i)].(string)
				val, gqlErr := respond(q, cursor)
				alias := fmt.Sprintf("b%d", i)
				if gqlErr != nil {
					(*gqlErr)["path"] = []any{alias}
					errs = append(errs, *gqlErr)
					data[alias] = nil
					continue
				}
				data[alias] = val
			}
			body := map[string]any{"data": data}
			if len(errs) > 0 {
				body["errors"] = errs
			}
			_ = json.NewEncoder(w).Encode(body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	is.client = NewClient("tok", WithBaseURL(srv.URL))
	return is
}

func TestFetchPRInventory_OneRequestForAllQualifiers(t *testing.T) {
	is := newInvServer(t, func(q, cursor string) (any, *map[string]any) {
		switch {
		case strings.Contains(q, "review-requested:octocat"):
			return searchPage("", prNode("acme/api", 1, nil), prNode("acme/api", 9, map[string]any{"state": "MERGED"})), nil
		case strings.Contains(q, "author:octocat"):
			return searchPage("", prNode("acme/web", 2, nil)), nil
		default:
			return searchPage("", prNode("acme/web", 2, nil), prNode("acme/ops", 3, nil)), nil
		}
	})
	inv, err := is.client.FetchPRInventory(0)
	if err != nil {
		t.Fatal(err)
	}
	if is.graphql.Load() != 1 || is.rest.Load() != 0 {
		t.Fatalf("graphql=%d rest=%d, want a single GraphQL request", is.graphql.Load(), is.rest.Load())
	}
	if len(inv.ReviewRequested) != 1 || len(inv.Authored) != 1 || len(inv.Assigned) != 2 {
		t.Fatalf("rr=%d au=%d as=%d (merged PRs must be dropped)", len(inv.ReviewRequested), len(inv.Authored), len(inv.Assigned))
	}
	pr := inv.ReviewRequested[0]
	if pr.ID != 1001 || pr.Repo != "acme/api" || pr.Head.SHA != "aaaaaaaa" || pr.Base.SHA != "bbbbbbbb" ||
		pr.State != "open" || pr.User.Login != "bob" || pr.HeadRepoFullName != "fork/api" || pr.HydratedAt.IsZero() {
		t.Fatalf("mapped PR = %+v", pr)
	}
	pr.ResolveRepo()
	if pr.Repo != "acme/api" {
		t.Fatalf("ResolveRepo must keep the base repo, got %q", pr.Repo)
	}
	if !pr.ReviewRequestedFor("octocat") || !pr.AssignedTo("carol") {
		t.Fatal("reviewers/assignees not mapped")
	}

	// The hydrated PR is now served without a request.
	got, err := is.client.GetPRFresh("acme/api", 1, time.Minute)
	if err != nil || got.ID != 1001 || is.graphql.Load() != 1 {
		t.Fatalf("GetPRFresh = %+v, %v (graphql=%d)", got, err, is.graphql.Load())
	}
	// A cached snapshot inside maxAge is reused; maxAge 0 refreshes.
	if _, err := is.client.FetchPRInventory(time.Minute); err != nil || is.graphql.Load() != 1 {
		t.Fatalf("reuse: graphql=%d err=%v", is.graphql.Load(), err)
	}
	if _, err := is.client.FetchPRInventory(0); err != nil || is.graphql.Load() != 2 {
		t.Fatalf("refresh: graphql=%d err=%v", is.graphql.Load(), err)
	}
}

func TestFetchPRInventory_PaginatesOnlyTheQualifierWithMore(t *testing.T) {
	var cursors []string
	is := newInvServer(t, func(q, cursor string) (any, *map[string]any) {
		if strings.Contains(q, "assignee:") {
			cursors = append(cursors, cursor)
			if cursor == "" {
				return searchPage("c1", prNode("acme/a", 1, nil)), nil
			}
			return searchPage("", prNode("acme/a", 2, nil)), nil
		}
		return searchPage(""), nil
	})
	inv, err := is.client.FetchPRInventory(0)
	if err != nil {
		t.Fatal(err)
	}
	if is.graphql.Load() != 2 || len(inv.Assigned) != 2 {
		t.Fatalf("graphql=%d assigned=%d", is.graphql.Load(), len(inv.Assigned))
	}
	if len(cursors) != 2 || cursors[1] != "c1" {
		t.Fatalf("cursors = %v", cursors)
	}
}

func TestFetchPRsToReview_FallsBackPerQualifier(t *testing.T) {
	is := newInvServer(t, func(q, cursor string) (any, *map[string]any) {
		if strings.Contains(q, "review-requested:") {
			return nil, &map[string]any{"type": "SERVICE_UNAVAILABLE", "message": "search timed out"}
		}
		return searchPage("", prNode("acme/a", 1, nil)), nil
	})
	prs, err := is.client.FetchPRsToReview()
	if err != nil {
		t.Fatal(err)
	}
	if is.rest.Load() != 1 || len(prs) != 1 || prs[0].Number != 7 {
		t.Fatalf("rest=%d prs=%v, want the REST search for the failed qualifier", is.rest.Load(), prs)
	}
	// Merge tracking reuses the same snapshot for author/assignee: no new
	// GraphQL request, no REST search.
	is.client.SetInventoryReuse(time.Minute)
	tracked, err := is.client.FetchMergeTrackingPRs(true)
	if err != nil || len(tracked) != 1 || !tracked[0].IsAuthor || !tracked[0].IsAssignee {
		t.Fatalf("tracked = %v err=%v", tracked, err)
	}
	if is.graphql.Load() != 1 || is.rest.Load() != 1 {
		t.Fatalf("graphql=%d rest=%d, merge tracking must reuse the inventory", is.graphql.Load(), is.rest.Load())
	}
}

func TestFetchPRInventory_AllFailedIsAnError(t *testing.T) {
	is := newInvServer(t, func(q, cursor string) (any, *map[string]any) {
		return nil, &map[string]any{"type": "FORBIDDEN", "message": "no"}
	})
	if _, err := is.client.FetchPRInventory(0); err == nil {
		t.Fatal("want error when every qualifier failed")
	}
	// FetchPRsToReview still answers through REST.
	prs, err := is.client.FetchPRsToReview()
	if err != nil || len(prs) != 1 {
		t.Fatalf("prs=%v err=%v", prs, err)
	}
}

func TestFetchPRInventory_UndecodablePageFailsQualifier(t *testing.T) {
	is := newInvServer(t, func(q, cursor string) (any, *map[string]any) {
		if strings.Contains(q, "author:") {
			return []int{1}, nil
		}
		return searchPage(""), nil
	})
	inv, err := is.client.FetchPRInventory(0)
	if err != nil {
		t.Fatal(err)
	}
	if _, bad := inv.Failed[qualifierAuthor]; !bad {
		t.Fatalf("failed = %v", inv.Failed)
	}
}

func TestFetchPRInventory_UserLookupFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	t.Cleanup(srv.Close)
	c := NewClient("tok", WithBaseURL(srv.URL))
	if _, err := c.FetchPRInventory(0); err == nil {
		t.Fatal("want error")
	}
}

func TestFetchPRInventory_RateLimitedIsAnError(t *testing.T) {
	is := newInvServer(t, func(q, cursor string) (any, *map[string]any) { return searchPage(""), nil })
	if _, err := is.client.AuthenticatedUser(); err != nil {
		t.Fatal(err)
	}
	is.client.pauseRateLimit(time.Minute)
	if _, err := is.client.FetchPRInventory(0); err == nil {
		t.Fatal("want rate limit error")
	}
}

func TestToPullRequest_TeamsBotsAndInvalid(t *testing.T) {
	var g gqlPR
	raw, _ := json.Marshal(prNode("acme/api", 4, map[string]any{
		"author":         nil,
		"headRepository": nil,
		"reviewRequests": map[string]any{"nodes": []any{
			map[string]any{"requestedReviewer": map[string]any{"__typename": "Team", "slug": "Core", "organization": map[string]any{"login": "acme"}}},
			map[string]any{"requestedReviewer": map[string]any{"__typename": "Bot", "login": "ci-bot"}},
			map[string]any{"requestedReviewer": map[string]any{"__typename": "Team", "slug": "nameless"}},
			map[string]any{"requestedReviewer": nil},
		}},
	}))
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	pr := g.toPullRequest(time.Now())
	if len(pr.RequestedTeams) != 2 || pr.RequestedTeams[0].Org != "acme" || pr.RequestedTeams[1].Org != "" {
		t.Fatalf("teams = %+v", pr.RequestedTeams)
	}
	if len(pr.RequestedReviewers) != 1 || pr.RequestedReviewers[0].Login != "ci-bot" || pr.User.Login != "" {
		t.Fatalf("reviewers = %+v user=%q", pr.RequestedReviewers, pr.User.Login)
	}
	var empty *gqlPR
	if empty.toPullRequest(time.Now()) != nil || (&gqlPR{Number: 1}).toPullRequest(time.Now()) != nil {
		t.Fatal("invalid nodes must map to nil")
	}
}

func TestRecentPR_CopiesAndExpires(t *testing.T) {
	c := NewClient("tok")
	old := &PullRequest{Repo: "a/b", Number: 1, Title: "old", HydratedAt: time.Now().Add(-time.Hour),
		RequestedReviewers: []User{{Login: "x"}}}
	newer := &PullRequest{Repo: "A/B", Number: 1, Title: "new", HydratedAt: time.Now()}
	c.rememberPRs([]*PullRequest{newer, old, {Repo: "", Number: 2, HydratedAt: time.Now()}, nil, {Repo: "a/b", Number: 3}})
	got, ok := c.RecentPR("a/b", 1, time.Minute)
	if !ok || got.Title != "new" {
		t.Fatalf("got %+v ok=%v (an older entry must not replace a newer one)", got, ok)
	}
	got.Title = "mutated"
	again, _ := c.RecentPR("a/b", 1, time.Minute)
	if again.Title != "new" {
		t.Fatal("RecentPR must return a copy")
	}
	if _, ok := c.RecentPR("a/b", 1, 0); ok {
		t.Fatal("maxAge 0 disables the cache")
	}
	if _, ok := c.RecentPR("a/b", 3, time.Minute); ok {
		t.Fatal("unhydrated PRs are not cached")
	}
}

func TestRememberPRs_BoundedKeepsNewestBatch(t *testing.T) {
	c := NewClient("tok")
	old := time.Now().Add(-time.Minute)
	var batch []*PullRequest
	for i := 0; i < prSnapshotMaxEntries; i++ {
		batch = append(batch, &PullRequest{Repo: "o/r", Number: i + 1, HydratedAt: old})
	}
	c.rememberPRs(batch)
	now := time.Now()
	c.rememberPRs([]*PullRequest{{Repo: "o/new", Number: 1, HydratedAt: now}})
	if len(c.snapshots.entries) != 1 {
		t.Fatalf("entries = %d, want only the newest batch", len(c.snapshots.entries))
	}
}

func TestGetPRFresh_FetchesWhenStale(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"id":5,"number":5,"head":{"sha":"abc","repo":{"full_name":"fork/r"}}}`))
	}))
	t.Cleanup(srv.Close)
	c := NewClient("tok", WithBaseURL(srv.URL))
	pr, err := c.GetPRFresh("o/r", 5, time.Minute)
	if err != nil || hits.Load() != 1 {
		t.Fatalf("pr=%v err=%v hits=%d", pr, err, hits.Load())
	}
	// The PR lives in the repo it was fetched from, not in the head fork.
	if pr.Repo != "o/r" {
		t.Fatalf("Repo = %q, want the base repository", pr.Repo)
	}
}

// prBatchServer answers aliased repository/pullRequest lookups. Numbers in
// gone resolve to a NOT_FOUND error; numbers in null resolve to null.
func prBatchServer(t *testing.T, gone, null map[int]bool) (*Client, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	var gql, rest atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql" {
			rest.Add(1)
			_, _ = w.Write([]byte(`{"state":"open","updated_at":"2026-10-01T10:00:00Z","head":{"sha":"rest"}}`))
			return
		}
		gql.Add(1)
		var req batchRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		data := map[string]any{}
		var errs []any
		for i := 0; ; i++ {
			nv, ok := req.Variables[fmt.Sprintf("number_%d", i)]
			if !ok {
				break
			}
			n := int(nv.(float64))
			repo := fmt.Sprintf("%v/%v", req.Variables[fmt.Sprintf("owner_%d", i)], req.Variables[fmt.Sprintf("name_%d", i)])
			alias := fmt.Sprintf("b%d", i)
			switch {
			case gone[n]:
				data[alias] = map[string]any{"pullRequest": nil}
				errs = append(errs, map[string]any{"type": "NOT_FOUND", "message": "gone", "path": []any{alias, "pullRequest"}})
			case null[n]:
				data[alias] = map[string]any{"pullRequest": nil}
			default:
				data[alias] = map[string]any{"pullRequest": prNode(repo, n, nil)}
			}
		}
		body := map[string]any{"data": data}
		if len(errs) > 0 {
			body["errors"] = errs
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return NewClient("tok", WithBaseURL(srv.URL)), &gql, &rest
}

func TestPrefetchPRs_AnswersStateChecksFromOneRequest(t *testing.T) {
	c, gql, rest := prBatchServer(t, map[int]bool{2: true}, map[int]bool{3: true})
	c.PrefetchPRs([]PRRef{
		{Repo: "acme/api", Number: 1}, {Repo: "acme/api", Number: 1}, {Repo: "acme/api", Number: 2},
		{Repo: "acme/api", Number: 3}, {Repo: "bad", Number: 4}, {Repo: "acme/api", Number: 0},
	})
	if gql.Load() != 1 {
		t.Fatalf("graphql = %d", gql.Load())
	}
	snap, err := c.GetPRSnapshotFresh("acme/api", 1, time.Minute)
	if err != nil || snap.HeadSHA != "aaaaaaaa" || !snap.ReviewRequestedFor("octocat") {
		t.Fatalf("snap=%+v err=%v", snap, err)
	}
	for _, n := range []int{2, 3} {
		_, err := c.GetPRSnapshotFresh("acme/api", n, time.Minute)
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
			t.Fatalf("#%d: err=%v, want a 404 so the watch is dropped", n, err)
		}
	}
	if rest.Load() != 0 {
		t.Fatalf("rest = %d, every check must come from the batch", rest.Load())
	}
	// Outside the window the REST snapshot is used.
	if _, err := c.GetPRSnapshotFresh("acme/api", 1, 0); err != nil || rest.Load() != 1 {
		t.Fatalf("rest=%d err=%v", rest.Load(), err)
	}
	if _, cached, _ := c.RecentPRSnapshot("acme/api", 99, time.Minute); cached {
		t.Fatal("unknown PR reported as cached")
	}
	c.PrefetchPRs(nil)
	if gql.Load() != 1 {
		t.Fatal("empty prefetch must not send a request")
	}
}

func TestPrefetchPRs_FailureFallsBack(t *testing.T) {
	c, gql, _ := prBatchServer(t, nil, nil)
	c.pauseRateLimit(time.Minute)
	c.PrefetchPRs([]PRRef{{Repo: "acme/api", Number: 1}})
	if gql.Load() != 0 {
		t.Fatal("breaker must hold the batch")
	}
	if _, cached, _ := c.RecentPRSnapshot("acme/api", 1, time.Minute); cached {
		t.Fatal("nothing should be cached")
	}
}

func TestRememberMissing_ExpiresOldEntries(t *testing.T) {
	c := NewClient("tok")
	c.rememberMissing(nil, time.Now())
	c.rememberMissing([]string{"old"}, time.Now().Add(-2*time.Hour))
	c.rememberMissing([]string{"new"}, time.Now())
	if _, ok := c.snapshots.missing["old"]; ok {
		t.Fatal("missing entries older than an hour must be dropped")
	}
}

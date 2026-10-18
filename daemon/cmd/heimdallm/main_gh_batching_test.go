package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/heimdallm/daemon/internal/store"
)

// gqlFakePR is one PR the fake GraphQL endpoint knows.
type gqlFakePR struct {
	repo      string
	number    int
	updatedAt time.Time
	users     []string
	teams     []string // team slugs requested
}

func (p gqlFakePR) node() map[string]any {
	var requests []any
	for _, u := range p.users {
		requests = append(requests, map[string]any{"requestedReviewer": map[string]any{"__typename": "User", "login": u}})
	}
	for _, s := range p.teams {
		requests = append(requests, map[string]any{"requestedReviewer": map[string]any{
			"__typename": "Team", "slug": s, "organization": map[string]any{"login": strings.Split(p.repo, "/")[0]},
		}})
	}
	return map[string]any{
		"databaseId": 5000 + p.number, "number": p.number, "title": fmt.Sprintf("PR %d", p.number),
		"url": "https://github.com/" + p.repo, "state": "OPEN", "isDraft": false,
		"updatedAt": p.updatedAt.UTC().Format(time.RFC3339), "headRefOid": fmt.Sprintf("sha%d", p.number),
		"headRefName": "feat", "baseRefOid": "base", "baseRefName": "main",
		"author":         map[string]any{"login": "alice"},
		"repository":     map[string]any{"nameWithOwner": p.repo},
		"assignees":      map[string]any{"nodes": []any{}},
		"reviewRequests": map[string]any{"nodes": requests},
	}
}

// fakeGitHubGraphQL serves the review-requested inventory, team lookups and
// batched PR lookups from prs, and counts REST requests by kind.
type fakeGitHubGraphQL struct {
	prs      []gqlFakePR
	teams    map[string][]string // org -> user's team slugs
	graphql  atomic.Int32
	restPull atomic.Int32
	restSrch atomic.Int32
}

func (f *fakeGitHubGraphQL) serve(t *testing.T) *gh.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/user":
			_ = json.NewEncoder(w).Encode(map[string]string{"login": "heimdallm-bot"})
		case r.URL.Path == "/search/issues":
			f.restSrch.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
		case strings.Contains(r.URL.Path, "/pulls/"):
			f.restPull.Add(1)
			http.Error(w, "must be served by the batch", http.StatusInternalServerError)
		case r.URL.Path == "/graphql":
			f.graphql.Add(1)
			var req struct {
				Variables map[string]any `json:"variables"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": f.answer(req.Variables)})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return gh.NewClient("token", gh.WithBaseURL(srv.URL))
}

func (f *fakeGitHubGraphQL) answer(vars map[string]any) map[string]any {
	data := map[string]any{}
	for i := 0; ; i++ {
		alias := fmt.Sprintf("b%d", i)
		if q, ok := vars[fmt.Sprintf("q_%d", i)].(string); ok {
			var nodes []any
			if strings.Contains(q, "review-requested:") {
				for _, p := range f.prs {
					nodes = append(nodes, p.node())
				}
			}
			data[alias] = map[string]any{"pageInfo": map[string]any{"hasNextPage": false}, "nodes": nodes}
			continue
		}
		if org, ok := vars[fmt.Sprintf("org_%d", i)].(string); ok {
			var nodes []any
			for _, s := range f.teams[org] {
				nodes = append(nodes, map[string]any{"slug": s})
			}
			data[alias] = map[string]any{"teams": map[string]any{"nodes": nodes}}
			continue
		}
		if n, ok := vars[fmt.Sprintf("number_%d", i)].(float64); ok {
			repo := fmt.Sprintf("%v/%v", vars[fmt.Sprintf("owner_%d", i)], vars[fmt.Sprintf("name_%d", i)])
			var found any
			for _, p := range f.prs {
				if p.repo == repo && p.number == int(n) {
					found = p.node()
				}
			}
			data[alias] = map[string]any{"pullRequest": found}
			continue
		}
		return data
	}
}

func batchingAdapter(t *testing.T, client *gh.Client) *tier2Adapter {
	t.Helper()
	if _, err := client.AuthenticatedUser(); err != nil {
		t.Fatalf("warm user: %v", err)
	}
	broker := sse.NewBroker()
	broker.Start()
	t.Cleanup(broker.Stop)
	cfg := &config.Config{GitHub: config.GitHubConfig{Repositories: []string{"org/repo"}}}
	login := "heimdallm-bot"
	return &tier2Adapter{
		ghClient: client, store: newMemStore(t), broker: broker,
		cfg: &cfg, cfgMu: &sync.Mutex{}, login: &login, loginMu: &sync.Mutex{},
		lastSkippedUpdatedAt: make(map[int64]time.Time),
	}
}

// One poll cycle is one GraphQL inventory request plus one team lookup: no
// REST search, no GET /pulls/N. Team requests count; PRs whose request is
// gone (stale search index) never become candidates.
func TestTier2Adapter_InventoryHydratesWithoutPerPRRequests(t *testing.T) {
	f := &fakeGitHubGraphQL{
		prs: []gqlFakePR{
			{repo: "org/repo", number: 1, users: []string{"Heimdallm-Bot"}},
			{repo: "org/repo", number: 2, teams: []string{"core"}},
			{repo: "org/repo", number: 3, teams: []string{"web"}},
			{repo: "org/repo", number: 4},
		},
		teams: map[string][]string{"org": {"core"}},
	}
	a := batchingAdapter(t, f.serve(t))

	got, err := a.FetchPRsToReview()
	if err != nil {
		t.Fatalf("FetchPRsToReview: %v", err)
	}
	var numbers []int
	for _, pr := range got {
		numbers = append(numbers, pr.Number)
		if pr.HeadSHA == "" {
			t.Errorf("#%d: candidate must carry the hydrated HEAD", pr.Number)
		}
	}
	if fmt.Sprint(numbers) != "[1 2]" {
		t.Fatalf("candidates = %v, want the direct and the team request", numbers)
	}
	if f.graphql.Load() != 2 || f.restSrch.Load() != 0 || f.restPull.Load() != 0 {
		t.Fatalf("graphql=%d search=%d pulls=%d, want 2/0/0", f.graphql.Load(), f.restSrch.Load(), f.restPull.Load())
	}

	// The review worker's hydration of a candidate is served by the inventory.
	pr, err := a.ghClient.GetPRFresh("org/repo", 2, reviewHydrationMaxAge)
	if err != nil || pr.Head.SHA != "sha2" || f.restPull.Load() != 0 {
		t.Fatalf("pr=%+v err=%v pulls=%d", pr, err, f.restPull.Load())
	}
}

// A batched prefetch answers every Tier 3 check; a team re-request counts as
// a re-request for the bot.
func TestTier3CheckItem_ServedFromBatchedPrefetch(t *testing.T) {
	now := time.Now()
	f := &fakeGitHubGraphQL{
		prs:   []gqlFakePR{{repo: "org/repo", number: 7, updatedAt: now, teams: []string{"core"}}},
		teams: map[string][]string{"org": {"core"}},
	}
	a := batchingAdapter(t, f.serve(t))
	prID, err := a.store.UpsertPR(&store.PR{GithubID: 4242, Repo: "org/repo", Number: 7, Title: "t", State: "open", UpdatedAt: now.Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.InsertReview(&store.Review{PRID: prID, CLIUsed: "claude", Issues: "[]", Suggestions: "[]",
		CreatedAt: now.Add(-time.Hour), HeadSHA: "old"}); err != nil {
		t.Fatal(err)
	}

	a.ghClient.PrefetchPRs([]gh.PRRef{{Repo: "org/repo", Number: 7}})
	changed, snap, err := a.CheckItem(context.Background(), tier3Item())
	if err != nil {
		t.Fatalf("CheckItem: %v", err)
	}
	if !changed || snap.HeadSHA != "sha7" {
		t.Fatalf("changed=%v snap=%+v, want the team re-request to count", changed, snap)
	}
	if f.restPull.Load() != 0 {
		t.Fatalf("pulls = %d, the check must come from the prefetch", f.restPull.Load())
	}

	// A PR the batch could not find is reported as a 404 (watch removed).
	a.ghClient.PrefetchPRs([]gh.PRRef{{Repo: "org/repo", Number: 8}})
	item := tier3Item()
	item.Number = 8
	_, _, err = a.CheckItem(context.Background(), item)
	var apiErr *gh.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
		t.Fatalf("err = %v, want 404", err)
	}
}

type fakeEnroller struct {
	enrolled, ifAbsent int
	err                error
}

func (f *fakeEnroller) Enroll(context.Context, string, string, int, int64) error {
	f.enrolled++
	return f.err
}

func (f *fakeEnroller) EnrollIfAbsent(context.Context, string, string, int, int64) (bool, error) {
	f.ifAbsent++
	return true, f.err
}

// A skipped run must not reset an existing watch's backoff — that reset kept
// every unchanged PR polled once a minute.
func TestEnrollAfterReview(t *testing.T) {
	pr := &gh.PullRequest{ID: 1, Number: 1, Repo: "o/r"}
	f := &fakeEnroller{}
	enrollAfterReview(context.Background(), f, pr, true, "test")
	enrollAfterReview(context.Background(), f, pr, false, "test")
	if f.enrolled != 1 || f.ifAbsent != 1 {
		t.Fatalf("enrolled=%d ifAbsent=%d", f.enrolled, f.ifAbsent)
	}
	f.err = errors.New("db down")
	enrollAfterReview(context.Background(), f, pr, false, "test") // logged, not fatal
}

package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// teamsServer answers aliased organization lookups: orgs maps an org login to
// its team slugs; "forbidden" and "error" orgs answer with those errors.
func teamsServer(t *testing.T, orgs map[string][]string) (*Client, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req batchRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		data := map[string]any{}
		var errs []any
		for i := 0; ; i++ {
			ov, ok := req.Variables[fmt.Sprintf("org_%d", i)]
			if !ok {
				break
			}
			org := ov.(string)
			alias := fmt.Sprintf("b%d", i)
			switch org {
			case "forbidden":
				data[alias] = nil
				errs = append(errs, map[string]any{"type": "FORBIDDEN", "message": "no", "path": []any{alias}})
			case "error":
				data[alias] = nil
				errs = append(errs, map[string]any{"type": "INTERNAL", "message": "boom", "path": []any{alias}})
			case "garbled":
				data[alias] = []int{1}
			case "null":
				data[alias] = nil
			default:
				var nodes []any
				for _, s := range orgs[org] {
					nodes = append(nodes, map[string]any{"slug": s})
				}
				data[alias] = map[string]any{"teams": map[string]any{"nodes": nodes}}
			}
		}
		body := map[string]any{"data": data}
		if len(errs) > 0 {
			body["errors"] = errs
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return NewClient("tok", WithBaseURL(srv.URL)), &calls
}

func TestPrefetchUserTeams_BatchesAndCaches(t *testing.T) {
	c, calls := teamsServer(t, map[string][]string{"acme": {"Core", "infra"}, "beta": nil})
	c.PrefetchUserTeams("octocat", []string{"acme", "ACME", "beta", "forbidden", "", "null"})
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want one batched query", calls.Load())
	}
	c.PrefetchUserTeams("octocat", []string{"acme", "beta", "forbidden", "null"})
	if calls.Load() != 1 {
		t.Fatal("cached orgs must not be fetched again")
	}
	id := c.ReviewerIdentityFor("octocat", "acme/api")
	if _, ok := id.Teams["core"]; !ok || id.Org != "acme" || calls.Load() != 1 {
		t.Fatalf("identity = %+v calls=%d", id, calls.Load())
	}
	// FORBIDDEN is definitive (no read:org): cached for the full TTL.
	if c.teams.orgs["forbidden"].ttl != teamCacheTTL {
		t.Fatal("forbidden org must be cached as no teams")
	}
	c.PrefetchUserTeams("", []string{"x"})
	if calls.Load() != 1 {
		t.Fatal("empty login is a no-op")
	}
}

func TestPrefetchUserTeams_FailuresRetrySoon(t *testing.T) {
	c, _ := teamsServer(t, nil)
	c.PrefetchUserTeams("octocat", []string{"error", "garbled"})
	for _, org := range []string{"error", "garbled"} {
		if c.teams.orgs[org].ttl != teamCacheFailTTL {
			t.Errorf("%s ttl = %v, want the short failure TTL", org, c.teams.orgs[org].ttl)
		}
	}
	c.pauseRateLimit(time.Minute)
	c.PrefetchUserTeams("octocat", []string{"later"})
	if c.teams.orgs["later"].ttl != teamCacheFailTTL {
		t.Fatal("a rate-limited lookup must retry soon")
	}
}

func TestReviewerIdentityFor_InvalidRepo(t *testing.T) {
	c := NewClient("tok")
	id := c.ReviewerIdentityFor("octocat", "not-a-repo")
	if id.Login != "octocat" || id.Org != "" || len(id.Teams) != 0 {
		t.Fatalf("id = %+v", id)
	}
}

func TestRequestedFor_UserAndTeams(t *testing.T) {
	id := ReviewerIdentity{Login: "octocat", Org: "acme", Teams: map[string]struct{}{"core": {}}}
	cases := []struct {
		name string
		pr   *PullRequest
		want bool
	}{
		{"direct", &PullRequest{RequestedReviewers: []User{{Login: "OctoCat"}}}, true},
		{"team without org (REST)", &PullRequest{RequestedTeams: []Team{{Slug: "Core"}}}, true},
		{"team in org", &PullRequest{RequestedTeams: []Team{{Slug: "core", Org: "ACME"}}}, true},
		{"team in another org", &PullRequest{RequestedTeams: []Team{{Slug: "core", Org: "other"}}}, false},
		{"other team", &PullRequest{RequestedTeams: []Team{{Slug: "web"}}}, false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		if got := tc.pr.RequestedFor(id); got != tc.want {
			t.Errorf("%s: got %v", tc.name, got)
		}
	}
	if (&PullRequest{RequestedTeams: []Team{{Slug: "core"}}}).RequestedFor(ReviewerIdentity{Login: "x"}) {
		t.Error("an identity without teams matches only directly")
	}
	snap := &PRSnapshot{RequestedTeams: []Team{{Slug: "core"}}}
	var nilSnap *PRSnapshot
	if !snap.RequestedFor(id) || nilSnap.RequestedFor(id) {
		t.Error("snapshot RequestedFor")
	}
	info := PRHeadInfo{RequestedReviewers: []string{"octocat"}}
	if !info.RequestedFor(id) || (PRHeadInfo{RequestedTeams: []Team{{Slug: "web"}}}).RequestedFor(id) {
		t.Error("head info RequestedFor")
	}
}

func TestBatchItemErrorHelpers(t *testing.T) {
	var be *BatchItemError
	if asBatchItemError(errors.New("x"), &be) {
		t.Fatal("plain error is not a BatchItemError")
	}
	if be.forbiddenOrNotFound() || (&BatchItemError{}).forbiddenOrNotFound() {
		t.Fatal("empty types")
	}
	if (&BatchItemError{Types: []string{"FORBIDDEN", "INTERNAL"}}).forbiddenOrNotFound() {
		t.Fatal("mixed types")
	}
}

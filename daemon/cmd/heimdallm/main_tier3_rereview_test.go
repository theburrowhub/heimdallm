package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/scheduler"
	"github.com/heimdallm/daemon/internal/store"
)

// tier3SnapshotAdapter serves GET /repos/org/repo/pulls/7 with the given
// requested_reviewers JSON and an updated_at newer than any LastSeen the
// tests use, so CheckItem always sees "updated".
func tier3SnapshotAdapter(t *testing.T, reviewersJSON string, seedReview bool) *tier2Adapter {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/org/repo/pulls/7" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"state":"open","user":{"login":"alice"},` +
			`"updated_at":"` + time.Now().UTC().Format(time.RFC3339) + `",` +
			`"head":{"sha":"new-sha"},"requested_reviewers":` + reviewersJSON + `}`))
	}))
	t.Cleanup(srv.Close)

	s := newMemStore(t)
	prID, err := s.UpsertPR(&store.PR{
		GithubID: 4242, Repo: "org/repo", Number: 7, Title: "t", State: "open",
		UpdatedAt: time.Now().Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("seed PR: %v", err)
	}
	if seedReview {
		if _, err := s.InsertReview(&store.Review{
			PRID: prID, CLIUsed: "claude", Issues: "[]", Suggestions: "[]",
			CreatedAt: time.Now().Add(-time.Hour), HeadSHA: "old-sha",
		}); err != nil {
			t.Fatalf("seed review: %v", err)
		}
	}

	login := "heimdallm-bot"
	cfg := &config.Config{GitHub: config.GitHubConfig{Repositories: []string{"org/repo"}}}
	return &tier2Adapter{
		ghClient: gh.NewClient("fake-token", gh.WithBaseURL(srv.URL)),
		store:    s,
		cfgMu:    &sync.Mutex{},
		cfg:      &cfg,
		loginMu:  &sync.Mutex{},
		login:    &login,
	}
}

func tier3Item() *scheduler.WatchItem {
	return &scheduler.WatchItem{
		Type: "pr", Repo: "org/repo", Number: 7, GithubID: 4242,
		LastSeen: time.Now().Add(-10 * time.Minute),
	}
}

// A reviewed PR whose updated_at moved (CI, comments, peer reviews) without
// the bot being re-requested must not reach HandleChange: the gate could only
// skip it, after three GitHub calls and a skip event per bump.
func TestTier3CheckItem_ReviewedPRWithoutReRequestIsUnchanged(t *testing.T) {
	a := tier3SnapshotAdapter(t, `[]`, true)
	changed, snap, err := a.CheckItem(context.Background(), tier3Item())
	if err != nil {
		t.Fatalf("CheckItem: %v", err)
	}
	if changed || snap != nil {
		t.Fatalf("changed=%v snap=%+v, want unchanged for a PR awaiting a re-request", changed, snap)
	}
}

func TestTier3CheckItem_ReRequestedPRIsChanged(t *testing.T) {
	a := tier3SnapshotAdapter(t, `[{"login":"Heimdallm-Bot"}]`, true)
	changed, snap, err := a.CheckItem(context.Background(), tier3Item())
	if err != nil {
		t.Fatalf("CheckItem: %v", err)
	}
	if !changed || snap == nil || snap.HeadSHA != "new-sha" {
		t.Fatalf("changed=%v snap=%+v, want changed with the fresh HEAD", changed, snap)
	}
}

// Without a stored review there is nothing to re-review yet: the first review
// keeps its existing path regardless of requested_reviewers.
func TestTier3CheckItem_UnreviewedPRIsChanged(t *testing.T) {
	a := tier3SnapshotAdapter(t, `[]`, false)
	changed, _, err := a.CheckItem(context.Background(), tier3Item())
	if err != nil {
		t.Fatalf("CheckItem: %v", err)
	}
	if !changed {
		t.Fatal("changed=false, want true for a PR with no prior review")
	}
}

// Before the bot login is known the filter cannot tell a request from no
// request, so it must stay out of the way.
func TestTier3CheckItem_UnknownLoginDoesNotFilter(t *testing.T) {
	a := tier3SnapshotAdapter(t, `[]`, true)
	empty := ""
	a.login = &empty
	changed, _, err := a.CheckItem(context.Background(), tier3Item())
	if err != nil {
		t.Fatalf("CheckItem: %v", err)
	}
	if !changed {
		t.Fatal("changed=false, want true while the bot login is unknown")
	}
}

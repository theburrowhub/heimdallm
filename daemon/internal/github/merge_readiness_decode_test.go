package github_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gh "github.com/heimdallm/daemon/internal/github"
)

// A payload where every optional object is null must still decode. GitHub omits
// these routinely: a PR from a deleted account has no author, a PR whose head
// fork was deleted has no headRepository.
func TestGetMergeStatus_ToleratesNullOptionalObjects(t *testing.T) {
	body := `{"data":{
		"viewer":{"login":"octocat"},
		"repository":{
			"nameWithOwner":"acme/widgets",
			"viewerPermission":"WRITE",
			"mergeCommitAllowed":true,"squashMergeAllowed":true,"rebaseMergeAllowed":true,
			"mergeQueue":null,
			"pullRequest":{
				"id":"PR_x","number":7,"title":"t","url":"u","state":"OPEN",
				"isDraft":false,"merged":false,"mergedAt":null,
				"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","reviewDecision":null,
				"isInMergeQueue":false,"mergeQueueEntry":null,"autoMergeRequest":null,
				"baseRefName":"main","headRefName":"feature","headRefOid":"abc",
				"headRepository":null,"headRepositoryOwner":null,"author":null,
				"assignees":{"nodes":[]},
				"latestOpinionatedReviews":{"nodes":[
					{"state":"APPROVED","submittedAt":"","authorCanPushToRepository":true,
					 "author":null,"commit":null}
				]},
				"reviewRequests":{"totalCount":0,"nodes":[{"requestedReviewer":null}]},
				"reviewThreads":{"pageInfo":{"hasNextPage":false},"nodes":[]},
				"commits":{"nodes":[]},
				"baseRef":null
			}
		}
	}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	st, err := c.GetMergeStatus("acme/widgets", 7)
	if err != nil {
		t.Fatalf("GetMergeStatus: %v", err)
	}
	if st.Author != "" || st.HeadRepo != "" || st.HeadRepoOwner != "" {
		t.Errorf("null objects should decode to empty, got %+v", st)
	}
	if st.AutoMerge != nil || st.Protection != nil {
		t.Error("null auto-merge and protection should stay nil")
	}
	if st.MergeQueueEnabled {
		t.Error("a null mergeQueue is not an enabled queue")
	}
	if len(st.Checks) != 0 {
		t.Errorf("no commits means no checks, got %v", st.Checks)
	}
	// A review whose author GitHub would not name still has to decode.
	if len(st.Reviews) != 1 || st.Reviews[0].Login != "" || st.Reviews[0].CommitOID != "" {
		t.Errorf("reviews = %+v", st.Reviews)
	}
	if len(st.ReviewRequests) != 0 {
		t.Errorf("a null requestedReviewer should be dropped, got %v", st.ReviewRequests)
	}
	// The repo slug falls back to what the caller asked for.
	if st.Repo != "acme/widgets" {
		t.Errorf("repo = %q", st.Repo)
	}
}

// GitHub can report more requested reviewers than it names — a team the token
// cannot see, for instance. Dropping the count silently would let the PR look
// unblocked.
func TestGetMergeStatus_AccountsForUnnamedReviewRequests(t *testing.T) {
	body := strings.Replace(mergeStatusJSON,
		`"reviewRequests": {"totalCount": 1, "nodes": [
          {"requestedReviewer": {"__typename": "Team", "slug": "platform"}}
        ]}`,
		`"reviewRequests": {"totalCount": 3, "nodes": [
          {"requestedReviewer": {"__typename": "Team", "slug": "platform"}}
        ]}`, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	st, err := c.GetMergeStatus("acme/widgets", 7)
	if err != nil {
		t.Fatalf("GetMergeStatus: %v", err)
	}
	if len(st.ReviewRequests) != 3 {
		t.Fatalf("review requests = %v, want three entries for a totalCount of 3", st.ReviewRequests)
	}
	var undisclosed int
	for _, r := range st.ReviewRequests {
		if strings.Contains(r, "undisclosed") {
			undisclosed++
		}
	}
	if undisclosed != 2 {
		t.Errorf("undisclosed reviewers = %d, want 2", undisclosed)
	}
}

// A rollup context type we do not know must read as pending, never as green.
func TestGetMergeStatus_UnknownCheckTypeIsPending(t *testing.T) {
	body := strings.Replace(mergeStatusJSON,
		`{"__typename": "StatusContext", "context": "legacy/ci",
                 "state": "SUCCESS", "isRequired": false,
                 "description": "all good", "targetUrl": "https://ci/legacy"}`,
		`{"__typename": "FutureCheckKind", "name": "mystery", "isRequired": true}`, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	st, err := c.GetMergeStatus("acme/widgets", 7)
	if err != nil {
		t.Fatalf("GetMergeStatus: %v", err)
	}
	var found bool
	for _, ch := range st.Checks {
		if ch.Name == "mystery" {
			found = true
			if ch.State != gh.CheckStatePending {
				t.Errorf("unknown check type = %q, want pending", ch.State)
			}
		}
	}
	if !found {
		t.Error("an unknown check type should still be listed")
	}
}

// A check run whose status GitHub has not seen before must not read as green.
func TestGetMergeStatus_NormalisesEveryCheckConclusion(t *testing.T) {
	cases := map[string]gh.CheckState{
		`"status":"COMPLETED","conclusion":"SUCCESS"`:         gh.CheckStateSuccess,
		`"status":"COMPLETED","conclusion":"SKIPPED"`:         gh.CheckStateNeutral,
		`"status":"COMPLETED","conclusion":"NEUTRAL"`:         gh.CheckStateNeutral,
		`"status":"COMPLETED","conclusion":"FAILURE"`:         gh.CheckStateFailure,
		`"status":"COMPLETED","conclusion":"TIMED_OUT"`:       gh.CheckStateFailure,
		`"status":"COMPLETED","conclusion":"CANCELLED"`:       gh.CheckStateFailure,
		`"status":"COMPLETED","conclusion":"ACTION_REQUIRED"`: gh.CheckStateFailure,
		`"status":"COMPLETED","conclusion":"STARTUP_FAILURE"`: gh.CheckStateFailure,
		`"status":"COMPLETED","conclusion":"STALE"`:           gh.CheckStateFailure,
		`"status":"COMPLETED","conclusion":"SOMETHING_NEW"`:   gh.CheckStateFailure,
		// A COMPLETED run with no conclusion should not read as green.
		`"status":"COMPLETED","conclusion":null`: gh.CheckStatePending,
		`"status":"QUEUED","conclusion":null`:    gh.CheckStatePending,
		`"status":"WAITING","conclusion":null`:   gh.CheckStatePending,
	}
	for fragment, want := range cases {
		t.Run(fragment, func(t *testing.T) {
			body := strings.Replace(mergeStatusJSON,
				`"status": "COMPLETED",
                 "conclusion": "FAILURE"`, fragment, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer srv.Close()

			c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
			st, err := c.GetMergeStatus("acme/widgets", 7)
			if err != nil {
				t.Fatalf("GetMergeStatus: %v", err)
			}
			for _, ch := range st.Checks {
				if ch.Name == "build" && ch.State != want {
					t.Errorf("state = %q, want %q", ch.State, want)
				}
			}
		})
	}
}

// StatusContext states have their own vocabulary.
func TestGetMergeStatus_NormalisesStatusContextStates(t *testing.T) {
	cases := map[string]gh.CheckState{
		"SUCCESS":  gh.CheckStateSuccess,
		"PENDING":  gh.CheckStatePending,
		"EXPECTED": gh.CheckStatePending,
		"FAILURE":  gh.CheckStateFailure,
		"ERROR":    gh.CheckStateFailure,
		"WHAT":     gh.CheckStatePending,
	}
	for state, want := range cases {
		t.Run(state, func(t *testing.T) {
			body := strings.Replace(mergeStatusJSON,
				`"context": "legacy/ci",
                 "state": "SUCCESS"`,
				`"context": "legacy/ci",
                 "state": "`+state+`"`, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer srv.Close()

			c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
			st, err := c.GetMergeStatus("acme/widgets", 7)
			if err != nil {
				t.Fatalf("GetMergeStatus: %v", err)
			}
			for _, ch := range st.Checks {
				if ch.Name == "legacy/ci" && ch.State != want {
					t.Errorf("state = %q, want %q", ch.State, want)
				}
			}
		})
	}
}

// A malformed timestamp must not fail the whole fetch.
func TestGetMergeStatus_ToleratesMalformedTimestamps(t *testing.T) {
	body := strings.Replace(mergeStatusJSON, `"startedAt": "2026-08-28T10:00:00Z"`, `"startedAt": "yesterday"`, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	st, err := c.GetMergeStatus("acme/widgets", 7)
	if err != nil {
		t.Fatalf("GetMergeStatus: %v", err)
	}
	for _, ch := range st.Checks {
		if ch.Name == "build" && ch.StartedAt != nil {
			t.Errorf("an unparseable timestamp should decode to nil, got %v", *ch.StartedAt)
		}
	}
}

// A repository with a merge queue configured must be reported, because a direct
// merge would jump the queue.
func TestGetMergeStatus_DetectsAConfiguredMergeQueue(t *testing.T) {
	body := strings.Replace(mergeStatusJSON, `"mergeQueue": null`, `"mergeQueue": {"id": "MQ_1"}`, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	st, err := c.GetMergeStatus("acme/widgets", 7)
	if err != nil {
		t.Fatalf("GetMergeStatus: %v", err)
	}
	if !st.MergeQueueEnabled {
		t.Error("a configured merge queue must be reported")
	}
}

func TestGetMergeStatus_DecodesAMergeQueueEntry(t *testing.T) {
	body := strings.Replace(mergeStatusJSON,
		`"isInMergeQueue": false,
        "mergeQueueEntry": null`,
		`"isInMergeQueue": true,
        "mergeQueueEntry": {"state": "queued", "position": 3}`, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	st, err := c.GetMergeStatus("acme/widgets", 7)
	if err != nil {
		t.Fatalf("GetMergeStatus: %v", err)
	}
	if !st.IsInMergeQueue || st.MergeQueueEntryState != "QUEUED" {
		t.Errorf("queue state = %v/%q", st.IsInMergeQueue, st.MergeQueueEntryState)
	}
}

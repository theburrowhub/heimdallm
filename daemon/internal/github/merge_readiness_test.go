package github_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gh "github.com/heimdallm/daemon/internal/github"
)

// mergeStatusJSON is a full GraphQL response for one PR. Inline rather than a
// fixture file, matching the convention in this package.
const mergeStatusJSON = `{
  "data": {
    "viewer": {"login": "octocat"},
    "repository": {
      "nameWithOwner": "acme/widgets",
      "viewerPermission": "WRITE",
      "mergeCommitAllowed": true,
      "squashMergeAllowed": true,
      "rebaseMergeAllowed": false,
      "mergeQueue": null,
      "pullRequest": {
        "id": "PR_kwDO",
        "number": 7,
        "title": "Add widget cache",
        "url": "https://github.com/acme/widgets/pull/7",
        "state": "OPEN",
        "isDraft": false,
        "merged": false,
        "mergedAt": null,
        "mergeable": "MERGEABLE",
        "mergeStateStatus": "BLOCKED",
        "reviewDecision": "REVIEW_REQUIRED",
        "isInMergeQueue": false,
        "mergeQueueEntry": null,
        "autoMergeRequest": {
          "enabledAt": "2026-08-28T11:00:00Z",
          "mergeMethod": "SQUASH",
          "enabledBy": {"login": "octocat"}
        },
        "baseRefName": "main",
        "headRefName": "feature",
        "headRefOid": "abc123",
        "headRepository": {"nameWithOwner": "acme/widgets", "isFork": false},
        "headRepositoryOwner": {"login": "acme"},
        "author": {"login": "octocat"},
        "assignees": {"nodes": [{"login": "octocat"}, {"login": "hubot"}]},
        "latestOpinionatedReviews": {"nodes": [
          {"state": "APPROVED", "submittedAt": "2026-08-28T10:00:00Z",
           "authorCanPushToRepository": true,
           "author": {"login": "reviewer"}, "commit": {"oid": "abc123"}}
        ]},
        "reviewRequests": {"totalCount": 1, "nodes": [
          {"requestedReviewer": {"__typename": "Team", "slug": "platform"}}
        ]},
        "reviewThreads": {
          "pageInfo": {"hasNextPage": false, "endCursor": null},
          "nodes": [
            {"id": "t1", "isResolved": false, "isOutdated": false, "isCollapsed": false, "resolvedBy": null},
            {"id": "t2", "isResolved": true, "isOutdated": false, "isCollapsed": true, "resolvedBy": {"login": "octocat"}}
          ]
        },
        "commits": {"nodes": [{"commit": {
          "oid": "abc123",
          "statusCheckRollup": {
            "state": "FAILURE",
            "contexts": {
              "totalCount": 3,
              "pageInfo": {"hasNextPage": false, "endCursor": null},
              "nodes": [
                {"__typename": "CheckRun", "name": "build", "status": "COMPLETED",
                 "conclusion": "FAILURE", "isRequired": true,
                 "detailsUrl": "https://ci/build", "startedAt": "2026-08-28T10:00:00Z",
                 "completedAt": "2026-08-28T10:05:00Z",
                 "checkSuite": {"app": {"name": "GitHub Actions"}}},
                {"__typename": "CheckRun", "name": "lint", "status": "IN_PROGRESS",
                 "conclusion": "SUCCESS", "isRequired": true,
                 "detailsUrl": "https://ci/lint",
                 "checkSuite": {"app": {"name": "GitHub Actions"}}},
                {"__typename": "StatusContext", "context": "legacy/ci",
                 "state": "SUCCESS", "isRequired": false,
                 "description": "all good", "targetUrl": "https://ci/legacy"}
              ]
            }
          }
        }}]},
        "baseRef": {
          "name": "main",
          "branchProtectionRule": {
            "requiresApprovingReviews": true,
            "requiredApprovingReviewCount": 2,
            "requiresCodeOwnerReviews": false,
            "requiresStatusChecks": true,
            "requiresStrictStatusChecks": true,
            "requiredStatusCheckContexts": ["build", "lint", "e2e"],
            "requiresConversationResolution": true,
            "requiresLinearHistory": false,
            "allowsForcePushes": false
          }
        }
      }
    }
  }
}`

func TestGetMergeStatus_DecodesEverythingTheEvaluatorNeeds(t *testing.T) {
	var gotQuery string
	var gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql" {
			t.Errorf("path = %q, want /graphql", r.URL.Path)
		}
		gotAccept = r.Header.Get("Accept")
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query string `json:"query"`
		}
		_ = json.Unmarshal(body, &req)
		gotQuery = req.Query
		_, _ = w.Write([]byte(mergeStatusJSON))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	st, err := c.GetMergeStatus("acme/widgets", 7)
	if err != nil {
		t.Fatalf("GetMergeStatus: %v", err)
	}

	// mergeStateStatus is behind a schema preview; without this header GitHub
	// rejects the field outright.
	if !strings.Contains(gotAccept, "merge-info-preview") {
		t.Errorf("Accept = %q, want the merge-info preview", gotAccept)
	}
	for _, field := range []string{
		"mergeStateStatus", "reviewDecision", "statusCheckRollup", "reviewThreads",
		"autoMergeRequest", "isInMergeQueue", "headRefOid",
		"latestOpinionatedReviews", "branchProtectionRule", "isRequired",
	} {
		if !strings.Contains(gotQuery, field) {
			t.Errorf("query is missing %q — the evaluator depends on it", field)
		}
	}

	if st.ViewerLogin != "octocat" || st.ViewerPermission != "WRITE" {
		t.Errorf("viewer = %q/%q", st.ViewerLogin, st.ViewerPermission)
	}
	if st.NodeID != "PR_kwDO" {
		t.Errorf("node id = %q — the auto-merge mutations need it", st.NodeID)
	}
	if st.MergeStateStatus != gh.MergeStateBlocked || st.Mergeable != gh.MergeableYes {
		t.Errorf("merge state = %q/%q", st.MergeStateStatus, st.Mergeable)
	}
	if st.HeadOID != "abc123" || st.BaseRef != "main" || st.HeadRef != "feature" {
		t.Errorf("refs = %q %q %q", st.HeadOID, st.BaseRef, st.HeadRef)
	}
	if st.AllowedMergeMethods.Rebase {
		t.Error("rebase is disabled on this repo and must not be reported as allowed")
	}
	if !st.AllowedMergeMethods.Allows("squash") {
		t.Error("squash should be allowed")
	}
	if st.AutoMerge == nil || st.AutoMerge.MergeMethod != "SQUASH" {
		t.Errorf("auto-merge = %+v", st.AutoMerge)
	}
	if len(st.Assignees) != 2 {
		t.Errorf("assignees = %v, want two", st.Assignees)
	}
	if len(st.Reviews) != 1 || st.Reviews[0].CommitOID != "abc123" || !st.Reviews[0].CanPush {
		t.Errorf("reviews = %+v", st.Reviews)
	}
	if len(st.ReviewRequests) != 1 || st.ReviewRequests[0] != "platform" {
		t.Errorf("review requests = %v", st.ReviewRequests)
	}
	if len(st.ReviewThreads) != 2 {
		t.Fatalf("threads = %d, want 2", len(st.ReviewThreads))
	}

	if len(st.Checks) != 3 {
		t.Fatalf("checks = %d, want 3", len(st.Checks))
	}
	byName := map[string]gh.CheckContext{}
	for _, c := range st.Checks {
		byName[c.Name] = c
	}
	if got := byName["build"]; got.State != gh.CheckStateFailure || !got.Required ||
		got.App != "GitHub Actions" || got.URL != "https://ci/build" {
		t.Errorf("build check = %+v", got)
	}
	if got := byName["build"].Duration(); got.Minutes() != 5 {
		t.Errorf("build duration = %v, want 5m", got)
	}
	// A check run that is still IN_PROGRESS carries a stale conclusion from the
	// previous run. Reading conclusion first would report it green.
	if got := byName["lint"]; got.State != gh.CheckStatePending {
		t.Errorf("lint state = %q, want pending despite conclusion=SUCCESS", got.State)
	}
	if got := byName["legacy/ci"]; got.Kind != "status" || got.State != gh.CheckStateSuccess {
		t.Errorf("legacy/ci = %+v", got)
	}

	if st.Protection == nil || st.Protection.RequiredApprovingReviewCount != 2 {
		t.Errorf("protection = %+v", st.Protection)
	}
	if st.ProtectionUnreadable {
		t.Error("protection was readable here")
	}
}

// The common case for a non-admin token: GitHub answers with data AND a
// FORBIDDEN error naming branchProtectionRule. Failing the whole query there
// would make merge tracking unusable for most people.
func TestGetMergeStatus_TolleratesForbiddenBranchProtection(t *testing.T) {
	body := strings.Replace(mergeStatusJSON,
		`"data": {`,
		`"errors": [{"type":"FORBIDDEN","message":"Resource not accessible by integration","path":["repository","pullRequest","baseRef","branchProtectionRule"]}],
		 "data": {`, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	st, err := c.GetMergeStatus("acme/widgets", 7)
	if err != nil {
		t.Fatalf("a FORBIDDEN on branchProtectionRule must not fail the query: %v", err)
	}
	if !st.ProtectionUnreadable {
		t.Error("the caller must be told protection could not be read, so it can fail closed")
	}
	if st.Protection != nil {
		t.Error("unreadable protection must not be reported as an empty rule set")
	}
	// Everything else must still be present.
	if st.HeadOID != "abc123" {
		t.Errorf("head oid = %q, the rest of the payload should survive", st.HeadOID)
	}
}

// A field error on anything OTHER than branch protection means we would be
// evaluating on incomplete gating data — that must fail.
func TestGetMergeStatus_RejectsForbiddenOnGatingFields(t *testing.T) {
	body := strings.Replace(mergeStatusJSON,
		`"data": {`,
		`"errors": [{"type":"FORBIDDEN","message":"nope","path":["repository","pullRequest","commits"]}],
		 "data": {`, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	if _, err := c.GetMergeStatus("acme/widgets", 7); err == nil {
		t.Fatal("a forbidden gating field must fail rather than silently degrade")
	}
}

// A whole-query error (no path) is fatal, as it always was.
func TestGetMergeStatus_WholeQueryErrorStillFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errors":[{"message":"Bad credentials"}]}`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	if _, err := c.GetMergeStatus("acme/widgets", 7); err == nil {
		t.Fatal("an error with no path is a whole-query failure and must be returned")
	}
}

func TestGetMergeStatus_MissingPRIsTyped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"viewer":{"login":"octocat"},"repository":{"pullRequest":null}}}`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	_, err := c.GetMergeStatus("acme/widgets", 7)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want a not-found error", err)
	}
}

func TestGetMergeStatus_RejectsMalformedRepoSlug(t *testing.T) {
	c := gh.NewClient("fake", gh.WithBaseURL("http://unused"))
	for _, slug := range []string{"", "no-slash", "a/b/c", "/leading", "trailing/"} {
		if _, err := c.GetMergeStatus(slug, 1); err == nil {
			t.Errorf("slug %q should be rejected", slug)
		}
	}
}

// A PR with more checks than one page must not be evaluated on the first page
// alone: a required check on page 2 would be invisible and the PR would read
// as green.
func TestGetMergeStatus_PaginatesCheckContexts(t *testing.T) {
	page := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page++
		body := mergeStatusJSON
		if page == 1 {
			// Ask for a second page.
			body = strings.Replace(body,
				`"pageInfo": {"hasNextPage": false, "endCursor": null},
              "nodes": [
                {"__typename": "CheckRun", "name": "build"`,
				`"pageInfo": {"hasNextPage": true, "endCursor": "cursor1"},
              "nodes": [
                {"__typename": "CheckRun", "name": "build"`, 1)
		} else {
			body = strings.Replace(body, `"name": "build"`, `"name": "page2-check"`, 1)
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	st, err := c.GetMergeStatus("acme/widgets", 7)
	if err != nil {
		t.Fatalf("GetMergeStatus: %v", err)
	}
	if page < 2 {
		t.Fatalf("requested %d pages, want the second page fetched", page)
	}
	var names []string
	for _, ch := range st.Checks {
		names = append(names, ch.Name)
	}
	found := false
	for _, n := range names {
		if n == "page2-check" {
			found = true
		}
	}
	if !found {
		t.Errorf("checks = %v, want the second page merged in", names)
	}
	if st.ChecksTruncated {
		t.Error("a fully paginated list is not truncated")
	}
}

// A connection that keeps asking for more pages past the cap must be reported
// as truncated, so the evaluator refuses to call the PR ready.
func TestGetMergeStatus_ReportsTruncationPastThePageCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always claims another page.
		body := strings.Replace(mergeStatusJSON,
			`"pageInfo": {"hasNextPage": false, "endCursor": null},
          "nodes": [
            {"id": "t1"`,
			`"pageInfo": {"hasNextPage": true, "endCursor": "c"},
          "nodes": [
            {"id": "t1"`, 1)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	st, err := c.GetMergeStatus("acme/widgets", 7)
	if err != nil {
		t.Fatalf("GetMergeStatus: %v", err)
	}
	if !st.ThreadsTruncated {
		t.Error("an endlessly paginating thread list must be reported as truncated")
	}
}

func TestGetMergeStatus_HandlesAPRWithNoChecksAtAll(t *testing.T) {
	body := strings.Replace(mergeStatusJSON,
		`"statusCheckRollup": {`, `"statusCheckRollupRemoved": {`, 1)
	body = strings.Replace(body, `"oid": "abc123",
          "statusCheckRollupRemoved"`, `"oid": "abc123",
          "statusCheckRollup": null,
          "unused"`, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	st, err := c.GetMergeStatus("acme/widgets", 7)
	if err != nil {
		t.Fatalf("GetMergeStatus: %v", err)
	}
	if len(st.Checks) != 0 {
		t.Errorf("checks = %v, want none", st.Checks)
	}
	if st.ChecksTruncated {
		t.Error("no checks is not truncation")
	}
}

func TestGetMergeStatus_HTTPFailureIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	if _, err := c.GetMergeStatus("acme/widgets", 7); err == nil {
		t.Fatal("a 500 must be reported")
	}
}

func TestMergeStatus_IsTrackedFor(t *testing.T) {
	st := &gh.MergeStatus{Author: "Octocat", Assignees: []string{"hubot", "@OctoCat"}}

	author, assignee := st.IsTrackedFor("octocat")
	if !author {
		t.Error("author comparison should be case-insensitive")
	}
	if !assignee {
		t.Error("assignee comparison should tolerate a leading @ and case")
	}

	if a, b := st.IsTrackedFor("someone"); a || b {
		t.Error("an unrelated login is neither")
	}
	var nilStatus *gh.MergeStatus
	if a, b := nilStatus.IsTrackedFor("octocat"); a || b {
		t.Error("a nil status tracks nobody")
	}
}

func TestMergeMethodSet_Allows(t *testing.T) {
	s := gh.MergeMethodSet{Squash: true, Rebase: true}
	for method, want := range map[string]bool{
		"squash": true, "SQUASH": true, " rebase ": true,
		"merge": false, "": false, "nonsense": false,
	} {
		if got := s.Allows(method); got != want {
			t.Errorf("Allows(%q) = %v, want %v", method, got, want)
		}
	}
}

func TestCheckContext_Duration(t *testing.T) {
	start := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	at := func(t time.Time) *time.Time { return &t }
	done := start.Add(90 * time.Second)
	skewed := start.Add(time.Minute)
	cases := map[string]struct {
		c    gh.CheckContext
		want time.Duration
	}{
		"complete":  {gh.CheckContext{StartedAt: at(start), CompletedAt: at(done)}, 90 * time.Second},
		"running":   {gh.CheckContext{StartedAt: at(start)}, 0},
		"unstarted": {gh.CheckContext{CompletedAt: at(start)}, 0},
		"queued":    {gh.CheckContext{}, 0},
		// Clock skew between GitHub's reporters is real; a negative duration is
		// nonsense, not something to render.
		"reversed": {gh.CheckContext{StartedAt: at(skewed), CompletedAt: at(start)}, 0},
	}
	for name, tc := range cases {
		if got := tc.c.Duration(); got != tc.want {
			t.Errorf("%s: duration = %v, want %v", name, got, tc.want)
		}
	}
}

func TestPartialGraphQLError_HasPath(t *testing.T) {
	e := &gh.PartialGraphQLError{Paths: []string{"repository.pullRequest.baseRef.branchProtectionRule"}}
	if !e.HasPath("branchProtectionRule") {
		t.Error("HasPath should match a path segment")
	}
	if e.HasPath("commits") {
		t.Error("HasPath must not match an absent segment")
	}
	var nilErr *gh.PartialGraphQLError
	if nilErr.HasPath("anything") {
		t.Error("a nil error has no paths")
	}
	if e.Error() == "" {
		t.Error("the error message should not be empty")
	}
}

// The pagination loops swallowed ANY partial error, not just the branch
// protection one the first page tolerates. A tolerated field error landing on
// the paginated connection itself decodes to an empty page with hasNextPage
// false, so the loop exited normally, the truncation flag stayed clear, and the
// evaluator could call a PR ready having seen only its first page of checks.
// This is the fail-open path the package exists to prevent.
func TestGetMergeStatus_UnexpectedPartialErrorMidPaginationMarksTruncated(t *testing.T) {
	cases := map[string]struct {
		firstPage  func(string) string
		errPath    string
		truncated  func(*gh.MergeStatus) bool
		wantThread bool
	}{
		"checks": {
			firstPage: func(body string) string {
				return strings.Replace(body,
					`"pageInfo": {"hasNextPage": false, "endCursor": null},
              "nodes": [
                {"__typename": "CheckRun", "name": "build"`,
					`"pageInfo": {"hasNextPage": true, "endCursor": "cursor1"},
              "nodes": [
                {"__typename": "CheckRun", "name": "build"`, 1)
			},
			errPath:   `["repository","pullRequest","commits"]`,
			truncated: func(st *gh.MergeStatus) bool { return st.ChecksTruncated },
		},
		"review threads": {
			firstPage: func(body string) string {
				return strings.Replace(body,
					`"reviewThreads": {
          "pageInfo": {"hasNextPage": false, "endCursor": null}`,
					`"reviewThreads": {
          "pageInfo": {"hasNextPage": true, "endCursor": "cursor1"}`, 1)
			},
			errPath:   `["repository","pullRequest","reviewThreads"]`,
			truncated: func(st *gh.MergeStatus) bool { return st.ThreadsTruncated },
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			page := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				page++
				if page == 1 {
					_, _ = w.Write([]byte(tc.firstPage(mergeStatusJSON)))
					return
				}
				// The second page comes back with the connection itself
				// missing, reported as a tolerated field error.
				_, _ = w.Write([]byte(strings.Replace(mergeStatusJSON,
					`"data": {`,
					`"errors": [{"type":"FORBIDDEN","message":"nope","path":`+tc.errPath+`}],
					 "data": {`, 1)))
			}))
			defer srv.Close()

			c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
			st, err := c.GetMergeStatus("acme/widgets", 7)
			if err != nil {
				t.Fatalf("GetMergeStatus: %v", err)
			}
			if page < 2 {
				t.Fatalf("requested %d pages, want the second fetched", page)
			}
			if !tc.truncated(st) {
				t.Error("a page we could not read must be reported as truncated, not as an empty tail")
			}
		})
	}
}

// The tolerated case still has to work across pages: a branch-protection
// FORBIDDEN is expected on every request a non-admin token makes.
func TestGetMergeStatus_ProtectionErrorMidPaginationIsStillTolerated(t *testing.T) {
	page := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page++
		body := mergeStatusJSON
		if page == 1 {
			body = strings.Replace(body,
				`"pageInfo": {"hasNextPage": false, "endCursor": null},
              "nodes": [
                {"__typename": "CheckRun", "name": "build"`,
				`"pageInfo": {"hasNextPage": true, "endCursor": "cursor1"},
              "nodes": [
                {"__typename": "CheckRun", "name": "build"`, 1)
		} else {
			body = strings.Replace(body, `"name": "build"`, `"name": "page2-check"`, 1)
		}
		body = strings.Replace(body,
			`"data": {`,
			`"errors": [{"type":"FORBIDDEN","message":"nope","path":["repository","pullRequest","baseRef","branchProtectionRule"]}],
			 "data": {`, 1)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	st, err := c.GetMergeStatus("acme/widgets", 7)
	if err != nil {
		t.Fatalf("GetMergeStatus: %v", err)
	}
	if st.ChecksTruncated {
		t.Error("an expected protection error must not be mistaken for an unreadable page")
	}
	var names []string
	for _, ch := range st.Checks {
		names = append(names, ch.Name)
	}
	found := false
	for _, n := range names {
		if n == "page2-check" {
			found = true
		}
	}
	if !found {
		t.Errorf("checks = %v, want the second page merged in", names)
	}
}

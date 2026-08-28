package github_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gh "github.com/heimdallm/daemon/internal/github"
)

func TestMergePRAtSHA_SendsTheExpectedSHA(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" || r.URL.Path != "/repos/acme/widgets/pulls/7/merge" {
			t.Errorf("%s %s, want PUT the merge path", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		_, _ = w.Write([]byte(`{"merged":true,"sha":"merged-sha","message":"Pull Request successfully merged"}`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	out, err := c.MergePRAtSHA("acme/widgets", 7, "squash", "abc123")
	if err != nil {
		t.Fatalf("MergePRAtSHA: %v", err)
	}
	if !out.Merged || out.SHA != "merged-sha" {
		t.Errorf("outcome = %+v", out)
	}
	// The sha is the whole point: without it GitHub would merge whatever the
	// head happens to be now.
	if gotBody["sha"] != "abc123" {
		t.Errorf("body sha = %v, want abc123", gotBody["sha"])
	}
	if gotBody["merge_method"] != "squash" {
		t.Errorf("body merge_method = %v", gotBody["merge_method"])
	}
}

func TestMergePRAtSHA_RequiresAnExpectedSHA(t *testing.T) {
	c := gh.NewClient("fake", gh.WithBaseURL("http://unused"))
	if _, err := c.MergePRAtSHA("acme/widgets", 7, "squash", ""); err == nil {
		t.Fatal("an empty expected sha must be rejected — it would disable the guard")
	}
}

func TestMergePRAtSHA_ClassifiesRejections(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"head moved", http.StatusConflict, `{"message":"Head branch was modified. Review and try the merge again."}`, gh.MergeReasonSHAMismatch},
		{"not mergeable", http.StatusMethodNotAllowed, `{"message":"Pull Request is not mergeable"}`, gh.MergeReasonNotMergeable},
		{"required checks", http.StatusMethodNotAllowed, `{"message":"Required status check \"build\" is failing"}`, gh.MergeReasonRequiredCheck},
		{"base policy", http.StatusMethodNotAllowed, `{"message":"Changes must be made through a pull request; base branch policy"}`, gh.MergeReasonBlocked},
		{"forbidden", http.StatusForbidden, `{"message":"Resource not accessible"}`, gh.MergeReasonForbidden},
		{"not found", http.StatusNotFound, `{"message":"Not Found"}`, gh.MergeReasonNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
			_, err := c.MergePRAtSHA("acme/widgets", 7, "squash", "abc123")
			var rejected *gh.MergeRejectedError
			if !errors.As(err, &rejected) {
				t.Fatalf("err = %v, want *MergeRejectedError", err)
			}
			if rejected.Reason != tc.want {
				t.Errorf("reason = %q, want %q", rejected.Reason, tc.want)
			}
		})
	}
}

func TestMergeRejectedError_TerminalOnlyForUnrecoverableStates(t *testing.T) {
	if !(&gh.MergeRejectedError{Reason: gh.MergeReasonForbidden}).Terminal() {
		t.Error("forbidden should be terminal")
	}
	if (&gh.MergeRejectedError{Reason: gh.MergeReasonRequiredCheck}).Terminal() {
		t.Error("a failing check is not terminal — it can go green")
	}
}

func TestUpdatePRBranch_SendsExpectedHeadSHAAndAcceptsAsync(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/widgets/pulls/7/update-branch" {
			t.Errorf("path = %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"message":"Updating pull request branch."}`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	out, err := c.UpdatePRBranch("acme/widgets", 7, "abc123")
	if err != nil {
		t.Fatalf("UpdatePRBranch: %v", err)
	}
	if !out.Accepted {
		t.Error("202 means accepted; GitHub does the work asynchronously")
	}
	if gotBody["expected_head_sha"] != "abc123" {
		t.Errorf("body = %v, want the expected head sha", gotBody)
	}
}

// A 422 is the signal to fall back to a local rebase, so it has to be
// distinguishable from every other failure.
func TestUpdatePRBranch_UnprocessableIsTheLocalRebaseSignal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"merge conflict between base and head"}`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	_, err := c.UpdatePRBranch("acme/widgets", 7, "abc123")
	var rejected *gh.UpdateBranchRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("err = %v, want *UpdateBranchRejectedError", err)
	}
	if rejected.Reason != gh.UpdateBranchReasonUnprocessable {
		t.Errorf("reason = %q, want unprocessable", rejected.Reason)
	}
}

func TestUpdatePRBranch_RequiresAnExpectedSHA(t *testing.T) {
	c := gh.NewClient("fake", gh.WithBaseURL("http://unused"))
	if _, err := c.UpdatePRBranch("acme/widgets", 7, ""); err == nil {
		t.Fatal("an empty expected sha must be rejected")
	}
}

func TestEnableAutoMerge_SendsUpperCaseMethodAndExpectedOID(t *testing.T) {
	var gotVars map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.Unmarshal(body, &req)
		gotVars = req.Variables
		if !strings.Contains(req.Query, "enablePullRequestAutoMerge") {
			t.Errorf("query = %q", req.Query)
		}
		_, _ = w.Write([]byte(`{"data":{"enablePullRequestAutoMerge":{"pullRequest":{
			"id":"PR_kwDO","autoMergeRequest":{"enabledAt":"2026-08-28T12:00:00Z",
			"mergeMethod":"SQUASH","enabledBy":{"login":"octocat"}}}}}}`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	am, err := c.EnableAutoMerge("PR_kwDO", "squash", "abc123")
	if err != nil {
		t.Fatalf("EnableAutoMerge: %v", err)
	}
	// The GraphQL enum is upper case; sending "squash" is a schema error.
	if gotVars["mergeMethod"] != "SQUASH" {
		t.Errorf("mergeMethod = %v, want SQUASH", gotVars["mergeMethod"])
	}
	// Pinning the arming to the evaluated commit closes the same window the
	// direct merge closes with `sha`.
	if gotVars["expectedHeadOid"] != "abc123" {
		t.Errorf("expectedHeadOid = %v", gotVars["expectedHeadOid"])
	}
	if am.MergeMethod != "SQUASH" || am.EnabledBy != "octocat" {
		t.Errorf("auto-merge = %+v", am)
	}
}

func TestEnableAutoMerge_ClassifiesFailures(t *testing.T) {
	cases := []struct {
		name    string
		message string
		want    string
	}{
		{"repo forbids it", "Auto merge is not allowed for this repository", gh.AutoMergeReasonNotAllowedForRepo},
		{"already mergeable", "Pull request is in clean status", gh.AutoMergeReasonCleanStatus},
		{"head moved", "expectedHeadOid does not match the current head", gh.AutoMergeReasonSHAMismatch},
		{"already on", "Auto merge is already enabled", gh.AutoMergeReasonAlreadyEnabled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				resp := map[string]any{"errors": []map[string]any{{"message": tc.message}}}
				_ = json.NewEncoder(w).Encode(resp)
			}))
			defer srv.Close()

			c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
			_, err := c.EnableAutoMerge("PR_kwDO", "squash", "abc123")
			var unavailable *gh.AutoMergeUnavailableError
			if !errors.As(err, &unavailable) {
				t.Fatalf("err = %v, want *AutoMergeUnavailableError", err)
			}
			if unavailable.Reason != tc.want {
				t.Errorf("reason = %q, want %q", unavailable.Reason, tc.want)
			}
		})
	}
}

func TestEnableAutoMerge_RequiresNodeIDAndOID(t *testing.T) {
	c := gh.NewClient("fake", gh.WithBaseURL("http://unused"))
	if _, err := c.EnableAutoMerge("", "squash", "abc"); err == nil {
		t.Error("an empty node id must be rejected")
	}
	if _, err := c.EnableAutoMerge("PR_x", "squash", ""); err == nil {
		t.Error("an empty expected head oid must be rejected")
	}
}

// Disabling something already disabled is the state we wanted, not a failure.
func TestDisableAutoMerge_TreatsAlreadyDisabledAsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errors":[{"message":"Auto merge is not enabled for this pull request"}]}`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	if err := c.DisableAutoMerge("PR_kwDO"); err != nil {
		t.Fatalf("disabling an already-disabled auto-merge should succeed: %v", err)
	}
}

// FetchMergeTrackingPRs deduplicates across the two search qualifiers and,
// unlike FetchPRs, keeps track of which one matched.
func TestFetchMergeTrackingPRs_MergesAuthorAndAssigneeFlags(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			_, _ = w.Write([]byte(`{"login":"octocat"}`))
			return
		case "/search/issues":
			q := r.URL.Query().Get("q")
			queries = append(queries, q)
			switch {
			case strings.Contains(q, "author:"):
				_, _ = w.Write([]byte(`{"items":[
					{"id":1,"number":7,"repository_url":"https://api.github.com/repos/acme/widgets"},
					{"id":2,"number":8,"repository_url":"https://api.github.com/repos/acme/widgets"}]}`))
			default:
				_, _ = w.Write([]byte(`{"items":[
					{"id":2,"number":8,"repository_url":"https://api.github.com/repos/acme/widgets"},
					{"id":3,"number":9,"repository_url":"https://api.github.com/repos/acme/other"}]}`))
			}
			return
		}
		t.Errorf("unexpected path %q", r.URL.Path)
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	prs, err := c.FetchMergeTrackingPRs(true)
	if err != nil {
		t.Fatalf("FetchMergeTrackingPRs: %v", err)
	}
	if len(prs) != 3 {
		t.Fatalf("got %d PRs, want 3 deduplicated", len(prs))
	}
	byID := map[int64]*gh.TrackedPR{}
	for _, pr := range prs {
		byID[pr.ID] = pr
	}
	if !byID[1].IsAuthor || byID[1].IsAssignee {
		t.Errorf("PR 1 flags = author:%v assignee:%v, want author only", byID[1].IsAuthor, byID[1].IsAssignee)
	}
	// The PR found by both qualifiers must carry both flags — FetchPRs loses
	// this, which is why merge tracking has its own fetcher.
	if !byID[2].IsAuthor || !byID[2].IsAssignee {
		t.Errorf("PR 2 flags = author:%v assignee:%v, want both", byID[2].IsAuthor, byID[2].IsAssignee)
	}
	if byID[3].IsAuthor || !byID[3].IsAssignee {
		t.Errorf("PR 3 flags = author:%v assignee:%v, want assignee only", byID[3].IsAuthor, byID[3].IsAssignee)
	}
	if byID[1].Repo != "acme/widgets" {
		t.Errorf("repo = %q, want it resolved from repository_url", byID[1].Repo)
	}
	// No repo: filter — a long list of them silently returns zero results.
	for _, q := range queries {
		if strings.Contains(q, "repo:") {
			t.Errorf("query %q must not carry a repo: filter", q)
		}
	}
}

func TestFetchMergeTrackingPRs_AuthorOnlyWhenAssigneesExcluded(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user" {
			_, _ = w.Write([]byte(`{"login":"octocat"}`))
			return
		}
		queries = append(queries, r.URL.Query().Get("q"))
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	if _, err := c.FetchMergeTrackingPRs(false); err != nil {
		t.Fatalf("FetchMergeTrackingPRs: %v", err)
	}
	if len(queries) != 1 {
		t.Fatalf("issued %d searches, want 1 when assignees are excluded", len(queries))
	}
	if strings.Contains(queries[0], "assignee:") {
		t.Errorf("query = %q, want author only", queries[0])
	}
}

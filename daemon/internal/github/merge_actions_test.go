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

func TestMergePR_StillWorksWithoutASHA(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		_, _ = w.Write([]byte(`{"merged":true,"sha":"x"}`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	if err := c.MergePR("acme/widgets", 7, ""); err != nil {
		t.Fatalf("MergePR: %v", err)
	}
	// The autonomous gate's path is unchanged: no sha, and squash by default.
	if _, present := gotBody["sha"]; present {
		t.Error("MergePR must not send a sha — MergePRAtSHA is the guarded path")
	}
	if gotBody["merge_method"] != "squash" {
		t.Errorf("merge_method = %v, want the squash default", gotBody["merge_method"])
	}
}

func TestMergePR_PropagatesRejections(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = w.Write([]byte(`{"message":"Pull Request is not mergeable"}`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	if err := c.MergePR("acme/widgets", 7, "squash"); err == nil {
		t.Fatal("a rejection must be reported")
	}
}

// A 200 whose body does not parse is still authoritative: GitHub merged it.
func TestMergePRAtSHA_TrustsA200WithASurprisingBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	out, err := c.MergePRAtSHA("acme/widgets", 7, "squash", "abc123")
	if err != nil {
		t.Fatalf("MergePRAtSHA: %v", err)
	}
	if !out.Merged {
		t.Error("a 200 means merged even when the body is unreadable")
	}
}

func TestMergePRAtSHA_UnknownStatusIsClassifiedAsUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte(`{"message":"?"}`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	_, err := c.MergePRAtSHA("acme/widgets", 7, "squash", "abc123")
	var rejected *gh.MergeRejectedError
	if !errors.As(err, &rejected) || rejected.Reason != gh.MergeReasonUnknown {
		t.Fatalf("err = %v, want an unknown-reason rejection", err)
	}
	if rejected.Terminal() {
		t.Error("an unclassified status is not terminal")
	}
	if rejected.Error() == "" {
		t.Error("the error should render")
	}
}

func TestMergePRAtSHA_422WithoutASHAMentionIsNotMergeable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"Validation failed"}`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	_, err := c.MergePRAtSHA("acme/widgets", 7, "squash", "abc123")
	var rejected *gh.MergeRejectedError
	if !errors.As(err, &rejected) || rejected.Reason != gh.MergeReasonNotMergeable {
		t.Fatalf("err = %v, want not_mergeable", err)
	}
}

func TestMergePRAtSHA_422MentioningSHAIsAMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"sha does not match"}`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	_, err := c.MergePRAtSHA("acme/widgets", 7, "squash", "abc123")
	var rejected *gh.MergeRejectedError
	if !errors.As(err, &rejected) || rejected.Reason != gh.MergeReasonSHAMismatch {
		t.Fatalf("err = %v, want sha_mismatch", err)
	}
}

func TestUpdatePRBranch_ClassifiesEveryRejection(t *testing.T) {
	cases := map[int]string{
		http.StatusUnprocessableEntity: gh.UpdateBranchReasonUnprocessable,
		http.StatusConflict:            gh.UpdateBranchReasonSHAMismatch,
		http.StatusForbidden:           gh.UpdateBranchReasonForbidden,
		http.StatusNotFound:            gh.UpdateBranchReasonNotFound,
		http.StatusTeapot:              gh.UpdateBranchReasonUnknown,
	}
	for status, want := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"message":"no"}`))
		}))
		c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
		_, err := c.UpdatePRBranch("acme/widgets", 7, "abc123")
		var rejected *gh.UpdateBranchRejectedError
		if !errors.As(err, &rejected) || rejected.Reason != want {
			t.Errorf("status %d: err = %v, want reason %q", status, err, want)
		}
		if rejected != nil && rejected.Error() == "" {
			t.Errorf("status %d: the error should render", status)
		}
		srv.Close()
	}
}

// A mutation that reports success but hands back no auto-merge request must not
// be recorded as armed — we would then wait forever for a merge nobody queued.
func TestEnableAutoMerge_EmptyResponseIsNotTreatedAsArmed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"enablePullRequestAutoMerge":{"pullRequest":{"id":"x"}}}}`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	_, err := c.EnableAutoMerge("PR_kwDO", "squash", "abc123")
	var unavailable *gh.AutoMergeUnavailableError
	if !errors.As(err, &unavailable) || unavailable.Reason != gh.AutoMergeReasonUnknown {
		t.Fatalf("err = %v, want an unknown-reason AutoMergeUnavailableError", err)
	}
	if unavailable.Error() == "" {
		t.Error("the error should render")
	}
}

// GitHub omitting enabledAt must not leave the armed timestamp at year zero,
// or the "wait a pass before merging directly" rule loses its anchor.
func TestEnableAutoMerge_DefaultsAMissingEnabledAt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"enablePullRequestAutoMerge":{"pullRequest":{
			"id":"x","autoMergeRequest":{"mergeMethod":"SQUASH"}}}}}`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	am, err := c.EnableAutoMerge("PR_kwDO", "squash", "abc123")
	if err != nil {
		t.Fatalf("EnableAutoMerge: %v", err)
	}
	if am.EnabledAt.IsZero() {
		t.Error("a missing enabledAt must be filled in, not left at the zero time")
	}
}

// An unrecognised mutation failure stays a plain error: guessing at a reason
// would make the caller act on a classification we did not earn.
func TestEnableAutoMerge_UnknownFailureIsNotClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errors":[{"message":"Something else entirely"}]}`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	_, err := c.EnableAutoMerge("PR_kwDO", "squash", "abc123")
	var unavailable *gh.AutoMergeUnavailableError
	if errors.As(err, &unavailable) {
		t.Fatalf("err = %v, want a plain error for an unrecognised failure", err)
	}
	if err == nil {
		t.Fatal("the failure must still be reported")
	}
}

func TestEnableAutoMerge_ForbiddenIsClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errors":[{"message":"Resource not accessible: forbidden"}]}`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	_, err := c.EnableAutoMerge("PR_kwDO", "squash", "abc123")
	var unavailable *gh.AutoMergeUnavailableError
	if !errors.As(err, &unavailable) || unavailable.Reason != gh.AutoMergeReasonForbidden {
		t.Fatalf("err = %v, want forbidden", err)
	}
}

func TestDisableAutoMerge_RequiresANodeIDAndReportsRealFailures(t *testing.T) {
	c := gh.NewClient("fake", gh.WithBaseURL("http://unused"))
	if err := c.DisableAutoMerge(""); err == nil {
		t.Error("an empty node id must be rejected")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errors":[{"message":"Bad credentials"}]}`))
	}))
	defer srv.Close()
	c2 := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	if err := c2.DisableAutoMerge("PR_x"); err == nil {
		t.Error("a real failure must be reported")
	}
}

func TestDisableAutoMerge_Succeeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"disablePullRequestAutoMerge":{"pullRequest":{"id":"x"}}}}`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	if err := c.DisableAutoMerge("PR_kwDO"); err != nil {
		t.Fatalf("DisableAutoMerge: %v", err)
	}
}

// A search failure on one qualifier must not wipe the other's results.
func TestFetchMergeTrackingPRs_PartialFailureKeepsWhatItGot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user" {
			_, _ = w.Write([]byte(`{"login":"octocat"}`))
			return
		}
		if strings.Contains(r.URL.Query().Get("q"), "assignee:") {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("boom"))
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"id":1,"number":7,"repository_url":"https://api.github.com/repos/acme/widgets"}]}`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	prs, err := c.FetchMergeTrackingPRs(true)
	if err == nil {
		t.Error("the partial failure should be reported alongside the results")
	}
	if len(prs) != 1 {
		t.Fatalf("got %d PRs, want the author results kept", len(prs))
	}
	if !prs[0].IsAuthor {
		t.Error("the surviving result should carry its flag")
	}
}

func TestFetchMergeTrackingPRs_ReportsAnUnresolvableUser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	if _, err := c.FetchMergeTrackingPRs(true); err == nil {
		t.Fatal("an unresolvable user must be reported")
	}
}

func TestPullRequest_AssignedTo(t *testing.T) {
	pr := &gh.PullRequest{Assignees: []gh.User{{Login: "Octocat"}, {Login: "hubot"}}}
	if !pr.AssignedTo("octocat") {
		t.Error("comparison should be case-insensitive")
	}
	if !pr.AssignedTo("@hubot") {
		t.Error("a leading @ should be tolerated")
	}
	if pr.AssignedTo("someone") {
		t.Error("an unrelated login is not an assignee")
	}
	if pr.AssignedTo("") {
		t.Error("an empty login matches nobody")
	}
	var nilPR *gh.PullRequest
	if nilPR.AssignedTo("octocat") {
		t.Error("a nil PR has no assignees")
	}
}

// A null item in a search page must not panic the dedup.
func TestFetchMergeTrackingPRs_SkipsNullSearchItems(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user" {
			_, _ = w.Write([]byte(`{"login":"octocat"}`))
			return
		}
		_, _ = w.Write([]byte(`{"items":[null,{"id":1,"number":7,"repository_url":"https://api.github.com/repos/acme/widgets"}]}`))
	}))
	defer srv.Close()

	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	prs, err := c.FetchMergeTrackingPRs(false)
	if err != nil {
		t.Fatalf("FetchMergeTrackingPRs: %v", err)
	}
	if len(prs) != 1 {
		t.Fatalf("got %d PRs, want the null skipped", len(prs))
	}
}

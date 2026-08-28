package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/theburrowhub/heimdallm/cli/internal/api"
)

func TestListMergeTracking_DecodesTheListing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/merge-tracking" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[
			{"pr_id":1,"repo":"acme/widgets","number":7,"title":"Add cache",
			 "phase":"blocked","block_reason":"checks_failing",
			 "block_detail":"1 required check is failing: build (GitHub Actions)",
			 "checks_required_failing":1,"checks_required_pending":0,
			 "is_author":true},
			{"pr_id":2,"repo":"acme/widgets","number":8,"phase":"merged"}
		]`))
	}))
	defer srv.Close()

	c := api.New(srv.URL, "")
	entries, err := c.ListMergeTracking()
	if err != nil {
		t.Fatalf("ListMergeTracking: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if !entries[0].BlockedByChecks() {
		t.Error("a checks_failing row must report as blocked by checks")
	}
	if entries[0].BlockDetail == "" {
		t.Error("the detail text is what the listing renders; it must survive decoding")
	}
	if entries[0].Terminal() {
		t.Error("a blocked row is not terminal")
	}
	if !entries[1].Terminal() {
		t.Error("a merged row is terminal")
	}
}

func TestGetMergeTracking_DecodesTheCheckBreakdown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/merge-tracking/1" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"pr_id":1,"repo":"acme/widgets","number":7,"phase":"blocked",
			"decision":{
				"ready":false,
				"blocks":[{"reason":"checks_failing","detail":"build is failing"}],
				"checks":[
					{"name":"build","state":"failure","required":true,
					 "app":"GitHub Actions","url":"https://ci/build",
					 "started_at":"2026-08-28T10:00:00Z",
					 "completed_at":"2026-08-28T10:05:00Z"},
					{"name":"coverage","state":"failure","required":false}
				],
				"checks_summary":{"total":2,"required_total":1,"required_failing":1,
				 "optional_failing":1,"missing_required":["e2e"]}
			}
		}`))
	}))
	defer srv.Close()

	c := api.New(srv.URL, "")
	entry, err := c.GetMergeTracking(1)
	if err != nil {
		t.Fatalf("GetMergeTracking: %v", err)
	}
	if entry.Decision == nil {
		t.Fatal("the decision must be decoded — it is what the detail view renders")
	}
	if len(entry.Decision.Checks) != 2 {
		t.Fatalf("got %d checks, want 2", len(entry.Decision.Checks))
	}
	build := entry.Decision.Checks[0]
	if build.Name != "build" || build.State != "failure" || !build.Required ||
		build.App != "GitHub Actions" || build.URL != "https://ci/build" {
		t.Errorf("check decoded incompletely: %+v", build)
	}
	if build.StartedAt == nil || build.CompletedAt == nil {
		t.Fatalf("timestamps did not decode: %v → %v", build.StartedAt, build.CompletedAt)
	}
	if build.CompletedAt.Sub(*build.StartedAt).Minutes() != 5 {
		t.Errorf("timestamps did not decode: %v → %v", *build.StartedAt, *build.CompletedAt)
	}
	// A check that has not started must stay distinguishable from one that
	// finished the instant it began: pointers, not the zero time.
	if queued := entry.Decision.Checks[1]; queued.StartedAt != nil || queued.CompletedAt != nil {
		t.Errorf("a check with no reported ends must decode to nil, got %v → %v",
			queued.StartedAt, queued.CompletedAt)
	}
	if got := entry.Decision.ChecksSummary.MissingRequired; len(got) != 1 || got[0] != "e2e" {
		t.Errorf("missing required = %v, want [e2e]", got)
	}
}

// dry_run is what makes the CLI's re-check a question rather than an action.
func TestEvaluateMergeTracking_SendsDryRun(t *testing.T) {
	var gotQuery string
	var gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"pr_id":1,"repo":"acme/widgets","number":7,"phase":"idle"}`))
	}))
	defer srv.Close()

	c := api.New(srv.URL, "")
	if _, err := c.EvaluateMergeTracking(1, true); err != nil {
		t.Fatalf("EvaluateMergeTracking: %v", err)
	}
	if gotMethod != "POST" {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotQuery != "dry_run=true" {
		t.Errorf("query = %q, want dry_run=true", gotQuery)
	}

	if _, err := c.EvaluateMergeTracking(1, false); err != nil {
		t.Fatalf("EvaluateMergeTracking: %v", err)
	}
	if gotQuery != "" {
		t.Errorf("query = %q, want none when dryRun is false", gotQuery)
	}
}

func TestMergeTrackingEntry_BlockedByChecksCoversEveryCheckReason(t *testing.T) {
	for _, reason := range []string{
		"checks_failing", "checks_pending", "required_check_missing", "checks_unknown",
	} {
		e := api.MergeTrackingEntry{BlockReason: reason}
		if !e.BlockedByChecks() {
			t.Errorf("%q should count as a check problem", reason)
		}
	}
	for _, reason := range []string{"", "draft", "conflicts", "changes_requested"} {
		e := api.MergeTrackingEntry{BlockReason: reason}
		if e.BlockedByChecks() {
			t.Errorf("%q must not count as a check problem", reason)
		}
	}
}

func TestListMergeTracking_ReportsDecodeFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"not":"an array"}`))
	}))
	defer srv.Close()

	c := api.New(srv.URL, "")
	if _, err := c.ListMergeTracking(); err == nil {
		t.Fatal("a malformed payload must be reported, not silently empty")
	}
}

// Compile-time guard: the CLI's shape must stay JSON-compatible with what the
// daemon sends. A field renamed on one side and not the other decodes to zero.
func TestMergeTrackingEntry_JSONTagsAreStable(t *testing.T) {
	var e api.MergeTrackingEntry
	raw := `{"pr_id":5,"checks_required_failing":3,"checks_required_pending":4,
	         "block_reason":"r","block_detail":"d","pre_rebase_sha":"sha",
	         "is_author":true,"is_assignee":true,"excluded":true}`
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if e.PRID != 5 || e.ChecksRequiredFailing != 3 || e.ChecksRequiredPending != 4 ||
		e.BlockReason != "r" || e.BlockDetail != "d" || e.PreRebaseSHA != "sha" ||
		!e.IsAuthor || !e.IsAssignee || !e.Excluded {
		t.Errorf("field decoded to zero — a json tag drifted: %+v", e)
	}
}

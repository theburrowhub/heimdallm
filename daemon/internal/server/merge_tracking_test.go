package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/server"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/heimdallm/daemon/internal/store"
)

func newMergeTrackingServer(t *testing.T) (*server.Server, *store.Store, int64) {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	broker := sse.NewBroker()
	broker.Start()
	t.Cleanup(broker.Stop)
	srv := server.NewWithOptions(s, broker, nil, "", server.Options{})

	now := time.Now().UTC()
	prID, err := s.UpsertPR(&store.PR{
		GithubID: 1, Repo: "acme/widgets", Number: 7,
		Title: "Add widget cache", Author: "octocat",
		URL: "https://github.com/acme/widgets/pull/7", State: "open",
		UpdatedAt: now, FetchedAt: now,
	})
	if err != nil {
		t.Fatalf("upsert pr: %v", err)
	}
	if _, err := s.EnsureMergeTracking(prID, "acme/widgets", 7); err != nil {
		t.Fatalf("ensure tracking: %v", err)
	}
	return srv, s, prID
}

func doJSON(t *testing.T, srv *server.Server, method, path string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// The listing is served from the store, so it must carry the check counts the
// UI needs to render its warning without a second request.
func TestHandleListMergeTracking_ReturnsCheckCountsAndBlockReason(t *testing.T) {
	srv, s, prID := newMergeTrackingServer(t)
	if err := s.RecordMergeTrackingDecision(prID, store.MergeDecisionRecord{
		Phase:                 store.MergePhaseBlocked,
		HeadSHA:               "abc123",
		BlockReason:           "checks_failing",
		BlockDetail:           "1 required check is failing: build (GitHub Actions)",
		DecisionJSON:          `{"ready":false,"checks":[{"name":"build","state":"failure","required":true}]}`,
		ChecksRequiredFailing: 1,
		ChecksRequiredPending: 2,
		At:                    time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record decision: %v", err)
	}

	code, body := doJSON(t, srv, "GET", "/merge-tracking")
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", code, body)
	}
	var got []map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, body)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	e := got[0]
	if e["block_reason"] != "checks_failing" {
		t.Errorf("block_reason = %v", e["block_reason"])
	}
	// The detail names the check; a bare count is not actionable.
	if detail, _ := e["block_detail"].(string); !strings.Contains(detail, "build") {
		t.Errorf("block_detail = %q, must name the failing check", detail)
	}
	if e["checks_required_failing"] != float64(1) || e["checks_required_pending"] != float64(2) {
		t.Errorf("check counts = %v/%v", e["checks_required_failing"], e["checks_required_pending"])
	}
	// The PR row is joined so the listing can show a title without N+1 calls.
	if e["title"] != "Add widget cache" || e["url"] == "" {
		t.Errorf("PR fields not joined: %v", e)
	}
	// The full decision is detail-only: the listing stays small.
	if _, present := e["decision"]; present {
		t.Error("the listing should omit the full decision payload")
	}
}

// The detail endpoint is what the PR view renders the per-check table from.
func TestHandleGetMergeTracking_IncludesTheFullDecision(t *testing.T) {
	srv, s, prID := newMergeTrackingServer(t)
	decision := `{"ready":false,"blocks":[{"reason":"checks_failing"}],` +
		`"checks":[{"name":"build","state":"failure","required":true,"app":"GitHub Actions","url":"https://ci/build"}],` +
		`"checks_summary":{"required_total":2,"required_failing":1}}`
	if err := s.RecordMergeTrackingDecision(prID, store.MergeDecisionRecord{
		Phase: store.MergePhaseBlocked, HeadSHA: "abc123",
		BlockReason: "checks_failing", DecisionJSON: decision,
		ChecksRequiredFailing: 1, At: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	code, body := doJSON(t, srv, "GET", "/merge-tracking/1")
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", code, body)
	}
	var got struct {
		Decision struct {
			Checks []struct {
				Name     string `json:"name"`
				State    string `json:"state"`
				Required bool   `json:"required"`
				App      string `json:"app"`
				URL      string `json:"url"`
			} `json:"checks"`
		} `json:"decision"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, body)
	}
	if len(got.Decision.Checks) != 1 {
		t.Fatalf("checks = %d, want the full breakdown", len(got.Decision.Checks))
	}
	c := got.Decision.Checks[0]
	if c.Name != "build" || c.State != "failure" || !c.Required ||
		c.App != "GitHub Actions" || c.URL != "https://ci/build" {
		t.Errorf("check detail incomplete: %+v", c)
	}
}

// Corrupt stored JSON must not take the endpoint down with it.
func TestHandleGetMergeTracking_SkipsInvalidStoredDecision(t *testing.T) {
	srv, s, prID := newMergeTrackingServer(t)
	if err := s.RecordMergeTrackingDecision(prID, store.MergeDecisionRecord{
		Phase: store.MergePhaseIdle, DecisionJSON: "{not json", At: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	code, body := doJSON(t, srv, "GET", "/merge-tracking/1")
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", code, body)
	}
	if strings.Contains(string(body), "not json") {
		t.Error("invalid stored JSON must be dropped, not echoed")
	}
}

func TestHandleGetMergeTracking_NotFound(t *testing.T) {
	srv, _, _ := newMergeTrackingServer(t)
	if code, _ := doJSON(t, srv, "GET", "/merge-tracking/9999"); code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", code)
	}
	if code, _ := doJSON(t, srv, "GET", "/merge-tracking/not-a-number"); code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", code)
	}
}

// Until main wires the callback the endpoint must say so rather than pretend.
func TestHandleEvaluateMergeTracking_503WhenUnwired(t *testing.T) {
	srv, _, _ := newMergeTrackingServer(t)
	code, body := doJSON(t, srv, "POST", "/merge-tracking/1/evaluate")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", code, body)
	}
}

func TestHandleEvaluateMergeTracking_PassesDryRunThrough(t *testing.T) {
	srv, _, _ := newMergeTrackingServer(t)
	var gotPRID int64
	var gotDryRun bool
	srv.SetMergeTrackEvaluateFn(func(_ context.Context, prID int64, dryRun bool) error {
		gotPRID, gotDryRun = prID, dryRun
		return nil
	})

	if code, body := doJSON(t, srv, "POST", "/merge-tracking/1/evaluate?dry_run=true"); code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", code, body)
	}
	if gotPRID != 1 || !gotDryRun {
		t.Errorf("callback got prID=%d dryRun=%v", gotPRID, gotDryRun)
	}

	// Without dry_run the handler does NOT run the action in-request: an arm,
	// a merge or a half-hour conflict-resolution agent run has no business
	// bound to an HTTP connection, where a client disconnect would cancel it
	// mid-rebase. It clears the cooldown and hands the work to the reconciler,
	// which owns the claim, the work gate and the retry accounting.
	gotPRID = 0
	if code, _ := doJSON(t, srv, "POST", "/merge-tracking/1/evaluate"); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if gotPRID != 0 {
		t.Error("the acting path must not run the action inside the request")
	}
}

// The cooldown is what makes the PR due, so the next cycle picks it up
// immediately instead of waiting out whatever backoff parked it.
func TestHandleEvaluateMergeTracking_ActingPathClearsTheCooldown(t *testing.T) {
	srv, s, prID := newMergeTrackingServer(t)
	srv.SetMergeTrackEvaluateFn(func(context.Context, int64, bool) error { return nil })

	if err := s.BlockMergeTracking(prID, "checks_failing", "build is red",
		time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatalf("block: %v", err)
	}
	if code, body := doJSON(t, srv, "POST", fmt.Sprintf("/merge-tracking/%d/evaluate", prID)); code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", code, body)
	}
	row, err := s.GetMergeTracking(prID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !row.CooldownUntil.IsZero() {
		t.Errorf("cooldown = %v, want cleared so the next cycle acts", row.CooldownUntil)
	}
}

func TestHandleEvaluateMergeTracking_SurfacesCallbackErrors(t *testing.T) {
	srv, _, _ := newMergeTrackingServer(t)
	srv.SetMergeTrackEvaluateFn(func(context.Context, int64, bool) error {
		return errors.New("github said no")
	})
	code, body := doJSON(t, srv, "POST", "/merge-tracking/1/evaluate?dry_run=true")
	if code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s", code, body)
	}
	if !strings.Contains(string(body), "github said no") {
		t.Errorf("body = %s, want the underlying error", body)
	}
}

// Per-PR exclusion needs no config edit, so it has to round-trip through the
// store and be reflected in the listing.
func TestHandleExcludeAndIncludeMergeTracking(t *testing.T) {
	srv, s, prID := newMergeTrackingServer(t)

	if code, body := doJSON(t, srv, "POST", "/merge-tracking/1/exclude"); code != http.StatusOK {
		t.Fatalf("exclude status = %d, body = %s", code, body)
	}
	row, err := s.GetMergeTracking(prID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !row.Excluded {
		t.Error("the row should be excluded")
	}

	if code, _ := doJSON(t, srv, "POST", "/merge-tracking/1/include"); code != http.StatusOK {
		t.Fatalf("include status = %d", code)
	}
	row, err = s.GetMergeTracking(prID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.Excluded {
		t.Error("the row should be included again")
	}
}

// The listing exposes PR titles, repos and check names, so it must be behind
// auth like /prs and /issues.
func TestMergeTrackingListing_RequiresAuth(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	broker := sse.NewBroker()
	broker.Start()
	t.Cleanup(broker.Stop)
	srv := server.NewWithOptions(s, broker, nil, "secret-token", server.Options{})

	req := httptest.NewRequest("GET", "/merge-tracking", nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 without a token", rec.Code)
	}

	req = httptest.NewRequest("GET", "/merge-tracking", nil)
	req.Header.Set("X-Heimdallm-Token", "secret-token")
	rec = httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d with a valid token, body = %s", rec.Code, rec.Body.String())
	}
}

func newPatchServer(t *testing.T) (*server.Server, string) {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	broker := sse.NewBroker()
	broker.Start()
	t.Cleanup(broker.Stop)
	srv := server.NewWithOptions(s, broker, nil, "", server.Options{})

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[ai]\nprimary = \"claude\"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	srv.SetConfigPath(cfgPath)
	srv.SetReloadFn(func() error { return nil })
	return srv, cfgPath
}

func patch(t *testing.T, srv *server.Server, path, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest("PATCH", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestPatchMergeTrackingRepoConfig_WritesTheSection(t *testing.T) {
	srv, cfgPath := newPatchServer(t)

	code, body := patch(t, srv, "/config/merge_tracking/repos/acme%2Fwidgets",
		`{"merge": true, "merge_method": "rebase"}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", code, body)
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	written := string(raw)
	for _, want := range []string{"merge_tracking", "acme/widgets", "rebase"} {
		if !strings.Contains(written, want) {
			t.Errorf("config should contain %q:\n%s", want, written)
		}
	}
	// The section is nested under merge_tracking.repos, not merged into the
	// autonomous block that the shared helper also serves.
	if strings.Contains(written, "[autonomous") {
		t.Errorf("the patch leaked into the autonomous section:\n%s", written)
	}
}

func TestDeleteMergeTrackingRepoConfigField_RemovesOnlyTheOverride(t *testing.T) {
	srv, cfgPath := newPatchServer(t)

	code, body := patch(t, srv, "/config/merge_tracking/repos/acme%2Fwidgets",
		`{"enabled": true, "merge": true}`)
	if code != http.StatusOK {
		t.Fatalf("seed PATCH status = %d, body = %s", code, body)
	}

	req := httptest.NewRequest(
		http.MethodDelete,
		"/config/merge_tracking/repos/acme%2Fwidgets/enabled",
		nil,
	)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d, body = %s", rec.Code, rec.Body.String())
	}

	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	written := string(raw)
	if strings.Contains(written, "enabled = true") {
		t.Errorf("enabled override should have been removed:\n%s", written)
	}
	if !strings.Contains(written, "merge = true") {
		t.Errorf("sibling override should be preserved:\n%s", written)
	}
}

func TestPatchMergeTrackingOrgConfig_WritesTheSection(t *testing.T) {
	srv, cfgPath := newPatchServer(t)

	code, body := patch(t, srv, "/config/merge_tracking/orgs/acme", `{"update_branch": true}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", code, body)
	}
	raw, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(raw), "update_branch") {
		t.Errorf("config should contain the org override:\n%s", raw)
	}
}

func TestPatchMergeTrackingConfig_RejectsBadInput(t *testing.T) {
	srv, _ := newPatchServer(t)

	cases := []struct {
		name string
		path string
		body string
		want int
	}{
		{"invalid repo slug", "/config/merge_tracking/repos/not-a-slug", `{"merge":true}`, http.StatusBadRequest},
		{"invalid org slug", "/config/merge_tracking/orgs/bad%2Fslug", `{"merge":true}`, http.StatusBadRequest},
		{"invalid json", "/config/merge_tracking/repos/acme%2Fwidgets", `{nope`, http.StatusBadRequest},
		// null means "remove", which DELETE is for; allowing it here would make
		// a typo silently drop a setting.
		{"null value", "/config/merge_tracking/repos/acme%2Fwidgets", `{"merge":null}`, http.StatusBadRequest},
		// An invalid merge_method must be caught before it reaches config.toml.
		{"invalid method", "/config/merge_tracking/repos/acme%2Fwidgets", `{"merge_method":"ff-only"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := patch(t, srv, tc.path, tc.body)
			if code != tc.want {
				t.Errorf("status = %d, want %d (body %s)", code, tc.want, body)
			}
		})
	}
}

func TestPatchMergeTrackingConfig_UnavailableWithoutAConfigPath(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	broker := sse.NewBroker()
	broker.Start()
	t.Cleanup(broker.Stop)
	srv := server.NewWithOptions(s, broker, nil, "", server.Options{})

	code, _ := patch(t, srv, "/config/merge_tracking/repos/acme%2Fwidgets", `{"merge":true}`)
	if code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 without a config path", code)
	}
}

// The autonomous handlers share the generalised implementation; a regression
// there would silently break an existing endpoint.
func TestPatchAutonomousConfig_StillWorksAfterTheGeneralisation(t *testing.T) {
	srv, cfgPath := newPatchServer(t)

	code, body := patch(t, srv, "/config/autonomous/repos/acme%2Fwidgets", `{"enabled": true}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", code, body)
	}
	raw, _ := os.ReadFile(cfgPath)
	written := string(raw)
	if !strings.Contains(written, "autonomous") || !strings.Contains(written, "acme/widgets") {
		t.Errorf("autonomous patch did not land:\n%s", written)
	}
	if strings.Contains(written, "merge_tracking") {
		t.Errorf("the autonomous patch leaked into merge_tracking:\n%s", written)
	}
}

func TestMergeTrackingEntry_TimestampsAreOmittedWhenUnset(t *testing.T) {
	srv, s, prID := newMergeTrackingServer(t)
	if err := s.RecordMergeTrackingDecision(prID, store.MergeDecisionRecord{
		Phase: store.MergePhaseIdle, At: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	_, body := doJSON(t, srv, "GET", "/merge-tracking")
	// A zero time rendered as year 1 in the payload is worse than absent: the
	// UI would show it.
	if strings.Contains(string(body), "0001-01-01") {
		t.Errorf("an unset timestamp leaked into the payload: %s", body)
	}
}

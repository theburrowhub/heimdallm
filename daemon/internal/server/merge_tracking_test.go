package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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

	gotDryRun = true
	if code, _ := doJSON(t, srv, "POST", "/merge-tracking/1/evaluate"); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if gotDryRun {
		t.Error("dry_run must default to false")
	}
}

func TestHandleEvaluateMergeTracking_SurfacesCallbackErrors(t *testing.T) {
	srv, _, _ := newMergeTrackingServer(t)
	srv.SetMergeTrackEvaluateFn(func(context.Context, int64, bool) error {
		return errors.New("github said no")
	})
	code, body := doJSON(t, srv, "POST", "/merge-tracking/1/evaluate")
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

package server_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/server"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/heimdallm/daemon/internal/store"
	"github.com/heimdallm/daemon/internal/workgate"
)

func setupServer(t *testing.T) (*server.Server, *store.Store) {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	broker := sse.NewBroker()
	broker.Start()
	t.Cleanup(broker.Stop)
	srv := server.New(s, broker, nil, "")
	return srv, s
}

func newStreamingTestServer(t *testing.T, handler http.Handler, writeTimeout time.Duration) *httptest.Server {
	t.Helper()
	ts := httptest.NewUnstartedServer(handler)
	ts.Config.WriteTimeout = writeTimeout
	ts.Start()
	t.Cleanup(ts.Close)
	return ts
}

func nextSSEEvent(scanner *bufio.Scanner) (string, string, error) {
	eventType := "message"
	var data []string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if len(data) > 0 {
				return eventType, strings.Join(data, "\n"), nil
			}
			eventType = "message"
		case strings.HasPrefix(line, "event:"):
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return "", "", err
	}
	return "", "", io.EOF
}

func TestHandlerHealth(t *testing.T) {
	srv, _ := setupServer(t)
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("health: status %d", w.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("health body: %v", body)
	}
}

func TestHealth_ReturnsDeepChecks(t *testing.T) {
	srv, _ := setupServer(t)
	lastPoll := time.Now().UTC().Add(-5 * time.Second)
	srv.SetHealthSnapshotFn(func() server.HealthSnapshot {
		return server.HealthSnapshot{
			LastPollAt:   lastPoll,
			PollInterval: time.Minute,
		}
	})

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("health: status %d", w.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	checks, ok := body["checks"].(map[string]any)
	if !ok {
		t.Fatalf("checks missing: %v", body)
	}
	if sqlite, ok := checks["sqlite"].(map[string]any); !ok || sqlite["ok"] != true {
		t.Fatalf("sqlite check not ok: %v", checks["sqlite"])
	}
	if last, ok := checks["last_poll"].(map[string]any); !ok || last["ok"] != true || last["at"] == nil {
		t.Fatalf("last_poll check not ok: %v", checks["last_poll"])
	}
}

func TestHealth_ReturnsServiceUnavailableForStalePoll(t *testing.T) {
	srv, _ := setupServer(t)
	srv.SetHealthSnapshotFn(func() server.HealthSnapshot {
		return server.HealthSnapshot{
			LastPollAt:   time.Now().UTC().Add(-3 * time.Minute),
			PollInterval: time.Minute,
		}
	})

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("health status: got %d want %d", w.Code, http.StatusServiceUnavailable)
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if body["status"] != "degraded" {
		t.Fatalf("health status body: %v", body)
	}
}

func TestHealth_ReturnsVersionAndStartedAt(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	broker := sse.NewBroker()
	broker.Start()
	t.Cleanup(broker.Stop)
	srv := server.NewWithOptions(s, broker, nil, "", server.Options{
		Version:   "v1.2.3-test",
		StartedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/health", nil)
	srv.Router().ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status: got %d want 200", rr.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["status"] != "ok" {
		t.Errorf("status field: got %v", got["status"])
	}
	if got["version"] != "v1.2.3-test" {
		t.Errorf("version: got %v", got["version"])
	}
	if got["started_at"] != "2026-01-02T03:04:05Z" {
		t.Errorf("started_at: got %v", got["started_at"])
	}
}

func TestEventsEmitsObservableHeartbeat(t *testing.T) {
	srv, _ := setupServer(t)
	srv.SetHealthSnapshotFn(func() server.HealthSnapshot {
		return server.HealthSnapshot{
			LastPollAt:   time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
			PollInterval: time.Minute,
		}
	})
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/events", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	seenHeartbeat := false
	var heartbeatData string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "event: heartbeat" {
			seenHeartbeat = true
			continue
		}
		if seenHeartbeat && strings.HasPrefix(line, "data: ") {
			heartbeatData = strings.TrimPrefix(line, "data: ")
			break
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan SSE: %v", err)
	}
	if heartbeatData == "" {
		t.Fatal("heartbeat event not received")
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(heartbeatData), &payload); err != nil {
		t.Fatalf("decode heartbeat: %v", err)
	}
	if payload["ts"] == "" {
		t.Fatalf("heartbeat payload missing liveness fields: %v", payload)
	}
	if payload["last_poll_at"] != "2026-01-02T03:04:05Z" {
		t.Fatalf("last_poll_at: got %v", payload["last_poll_at"])
	}
}

func TestEventsStreamSurvivesServerWriteTimeout(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	broker := sse.NewBroker()
	broker.Start()
	t.Cleanup(broker.Stop)
	srv := server.New(s, broker, nil, "")

	const writeTimeout = 75 * time.Millisecond
	ts := newStreamingTestServer(t, srv.Router(), writeTimeout)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/events", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	eventType, _, err := nextSSEEvent(scanner)
	if err != nil {
		t.Fatalf("read initial heartbeat: %v", err)
	}
	if eventType != sse.EventHeartbeat {
		t.Fatalf("initial event = %q, want %q", eventType, sse.EventHeartbeat)
	}

	// The server deadline is absolute, not idle-based. Publishing after it has
	// expired reproduces the production one-minute EOF in a fraction of a second.
	time.Sleep(2 * writeTimeout)
	broker.Publish(sse.Event{Type: "after_deadline", Data: `{"ok":true}`})
	eventType, data, err := nextSSEEvent(scanner)
	if err != nil {
		t.Fatalf("read event after server write timeout: %v", err)
	}
	if eventType != "after_deadline" || data != `{"ok":true}` {
		t.Fatalf("event after deadline = (%q, %q), want (after_deadline, {\"ok\":true})", eventType, data)
	}
}

func TestEventsRejectsWriterWithoutDeadlineControl(t *testing.T) {
	srv, _ := setupServer(t)
	req := httptest.NewRequest(http.MethodGet, "/events", nil)
	w := httptest.NewRecorder()

	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d want %d", w.Code, http.StatusInternalServerError)
	}
}

func TestHandlerListPRs(t *testing.T) {
	srv, s := setupServer(t)
	s.UpsertPR(&store.PR{GithubID: 1, Repo: "org/r", Number: 1, Title: "t", Author: "a", URL: "u", State: "open", UpdatedAt: time.Now(), FetchedAt: time.Now()})

	req := httptest.NewRequest("GET", "/prs", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("list prs: status %d", w.Code)
	}
	var prs []map[string]any
	json.NewDecoder(w.Body).Decode(&prs)
	if len(prs) != 1 {
		t.Errorf("expected 1 PR, got %d", len(prs))
	}
}

func TestHandlerListPRsFiltersSelfAuthored(t *testing.T) {
	srv, s := setupServer(t)
	srv.SetMeFn(func() (string, error) { return "ivan", nil })
	now := time.Now()
	s.UpsertPR(&store.PR{GithubID: 1, Repo: "org/r", Number: 1, Title: "own", Author: "Ivan", URL: "u1", State: "open", UpdatedAt: now, FetchedAt: now})
	s.UpsertPR(&store.PR{GithubID: 2, Repo: "org/r", Number: 2, Title: "team", Author: "teammate", URL: "u2", State: "open", UpdatedAt: now, FetchedAt: now})

	req := httptest.NewRequest("GET", "/prs", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list prs: status %d", w.Code)
	}
	var prs []map[string]any
	if err := json.NewDecoder(w.Body).Decode(&prs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(prs) != 1 {
		t.Fatalf("expected 1 non-self PR, got %d: %#v", len(prs), prs)
	}
	if prs[0]["author"] != "teammate" {
		t.Fatalf("author = %v, want teammate", prs[0]["author"])
	}
}

func TestHandlerGetPR(t *testing.T) {
	srv, s := setupServer(t)
	id, _ := s.UpsertPR(&store.PR{GithubID: 2, Repo: "org/r", Number: 2, Title: "t", Author: "a", URL: "u", State: "open", UpdatedAt: time.Now(), FetchedAt: time.Now()})

	req := httptest.NewRequest("GET", "/prs/"+itoa(id), nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("get pr: status %d", w.Code)
	}
}

func TestPRResponsesExposeDurableActiveAndFailureStatus(t *testing.T) {
	srv, s := setupServer(t)
	now := time.Date(2026, 8, 24, 10, 30, 0, 0, time.UTC)
	id, err := s.UpsertPR(&store.PR{
		GithubID: 1025, Repo: "org/repo", Number: 1025, Title: "review me",
		Author: "a", URL: "u", State: "open", UpdatedAt: now, FetchedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AdvanceReviewRetryBackoff(id, "head-sha", now); err != nil {
		t.Fatal(err)
	}

	getListStatus := func() map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/prs", nil)
		w := httptest.NewRecorder()
		srv.Router().ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET /prs status = %d, body %s", w.Code, w.Body.String())
		}
		var prs []map[string]any
		if err := json.NewDecoder(w.Body).Decode(&prs); err != nil {
			t.Fatal(err)
		}
		if len(prs) != 1 {
			t.Fatalf("GET /prs count = %d, want 1", len(prs))
		}
		status, ok := prs[0]["review_status"].(map[string]any)
		if !ok {
			t.Fatalf("review_status missing from list response: %#v", prs[0])
		}
		return status
	}

	active := getListStatus()
	if active["active"] != true || active["head_sha"] != "head-sha" || active["error"] != "" {
		t.Fatalf("active list status = %#v", active)
	}

	req := httptest.NewRequest(http.MethodGet, "/prs/"+itoa(id), nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /prs/{id} status = %d, body %s", w.Code, w.Body.String())
	}
	var detail map[string]any
	if err := json.NewDecoder(w.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	detailPR, ok := detail["pr"].(map[string]any)
	if !ok {
		t.Fatalf("detail PR missing: %#v", detail)
	}
	detailStatus, ok := detailPR["review_status"].(map[string]any)
	if !ok || detailStatus["active"] != true {
		t.Fatalf("active detail status = %#v", detailStatus)
	}

	failedAt := now.Add(5 * time.Minute)
	if err := s.MarkReviewRetryFailure(id, "head-sha", failedAt, "Review timed out before completion."); err != nil {
		t.Fatal(err)
	}
	failed := getListStatus()
	if failed["active"] != false || failed["error"] != "Review timed out before completion." ||
		failed["failed_at"] != failedAt.Format(time.RFC3339) ||
		failed["retry_at"] != failedAt.Add(5*time.Minute).Format(time.RFC3339) {
		t.Fatalf("terminal list status = %#v", failed)
	}
}

func TestHandlerCancelReviewIsScopedToRequestedPR(t *testing.T) {
	srv, _ := setupServer(t)
	var cancelledID int64
	srv.SetCancelReviewFn(func(prID int64) (bool, error) {
		cancelledID = prID
		return prID == 73, nil
	})

	req := httptest.NewRequest(http.MethodPost, "/prs/73/cancel", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusAccepted || cancelledID != 73 {
		t.Fatalf("cancel exact PR = status %d, callback id %d, body %s", w.Code, cancelledID, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/prs/74/cancel", nil)
	w = httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusConflict || cancelledID != 74 {
		t.Fatalf("inactive PR cancel = status %d, callback id %d, body %s", w.Code, cancelledID, w.Body.String())
	}
}

func TestHandlerCancelReviewReportsUnavailableAndCallbackErrors(t *testing.T) {
	t.Run("invalid id", func(t *testing.T) {
		srv, _ := setupServer(t)
		req := httptest.NewRequest(http.MethodPost, "/prs/not-a-number/cancel", nil)
		w := httptest.NewRecorder()
		srv.Router().ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", w.Code)
		}
	})

	t.Run("not configured", func(t *testing.T) {
		srv, _ := setupServer(t)
		req := httptest.NewRequest(http.MethodPost, "/prs/1/cancel", nil)
		w := httptest.NewRecorder()
		srv.Router().ServeHTTP(w, req)
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", w.Code)
		}
	})

	t.Run("callback failure", func(t *testing.T) {
		srv, _ := setupServer(t)
		srv.SetCancelReviewFn(func(int64) (bool, error) {
			return false, fmt.Errorf("signal failed")
		})
		req := httptest.NewRequest(http.MethodPost, "/prs/1/cancel", nil)
		w := httptest.NewRecorder()
		srv.Router().ServeHTTP(w, req)
		if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "signal failed") {
			t.Fatalf("callback failure = status %d, body %s", w.Code, w.Body.String())
		}
	})
}

func TestHandlerGetConfig(t *testing.T) {
	srv, _ := setupServer(t)
	req := httptest.NewRequest("GET", "/config", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("get config: status %d", w.Code)
	}
}

// TestHandlerGetConfig_ExposesRepoFirstSeenAt guards the response shape that
// main.go's configFn produces for auto-discovered repos: each entry in
// repo_overrides gets a first_seen_at Unix-seconds integer enriched from the
// repo_first_seen store row. The Flutter app reads this to show NEW badges,
// so a silent rename or re-nesting would break the UI. The store-reading path
// itself lives in cmd/heimdallm/main.go and is covered at runtime — this test
// pins the JSON contract so that contract cannot drift.
func TestHandlerGetConfig_ExposesRepoFirstSeenAt(t *testing.T) {
	srv, _ := setupServer(t)
	seen := int64(1713571200) // 2024-04-20T00:00:00Z, arbitrary but fixed
	srv.SetConfigFn(func() map[string]any {
		return map[string]any{
			"repo_overrides": map[string]any{
				"acme/api": map[string]any{
					"first_seen_at": seen,
				},
			},
		}
	})

	req := httptest.NewRequest("GET", "/config", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v (body: %s)", err, w.Body.String())
	}
	overrides, ok := body["repo_overrides"].(map[string]any)
	if !ok {
		t.Fatalf("repo_overrides missing or wrong type: %T: %v", body["repo_overrides"], body["repo_overrides"])
	}
	entry, ok := overrides["acme/api"].(map[string]any)
	if !ok {
		t.Fatalf("repo_overrides[acme/api] missing or wrong type: %T: %v", overrides["acme/api"], overrides["acme/api"])
	}
	// JSON numbers unmarshal to float64 when decoding into map[string]any.
	got, ok := entry["first_seen_at"].(float64)
	if !ok {
		t.Fatalf("first_seen_at missing or wrong type: %T: %v", entry["first_seen_at"], entry["first_seen_at"])
	}
	if int64(got) != seen {
		t.Errorf("first_seen_at = %d, want %d", int64(got), seen)
	}
}

// TestGetConfig_ExposesNeverApproveWithIssues pins the JSON contract for the
// never_approve_with_issues field added in main.go's GET /config result map
// (global) and repoAIOverrideMap/orgAIOverrideMap (per-repo override), the
// same way TestHandlerGetConfig_ExposesRepoFirstSeenAt pins first_seen_at.
// The real map-building logic lives in cmd/heimdallm/main.go (package main,
// not importable here); this test guards that whatever main.go's configFn
// produces survives the HTTP handler unchanged.
func TestGetConfig_ExposesNeverApproveWithIssues(t *testing.T) {
	srv, _ := setupServer(t)
	srv.SetConfigFn(func() map[string]any {
		return map[string]any{
			"never_approve_with_issues": true,
			"repo_overrides": map[string]any{
				"org/repo1": map[string]any{
					"never_approve_with_issues": false,
				},
			},
		}
	})

	req := httptest.NewRequest("GET", "/config", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v (body: %s)", err, w.Body.String())
	}
	if body["never_approve_with_issues"] != true {
		t.Errorf("global never_approve_with_issues = %v, want true", body["never_approve_with_issues"])
	}
	overrides, ok := body["repo_overrides"].(map[string]any)
	if !ok {
		t.Fatalf("repo_overrides missing or wrong type: %T: %v", body["repo_overrides"], body["repo_overrides"])
	}
	ro, ok := overrides["org/repo1"].(map[string]any)
	if !ok {
		t.Fatalf("repo_overrides[org/repo1] missing or wrong type: %T: %v", overrides["org/repo1"], overrides["org/repo1"])
	}
	if ro["never_approve_with_issues"] != false {
		t.Errorf("repo override never_approve_with_issues = %v, want false", ro["never_approve_with_issues"])
	}
}

func TestHandlerPutConfig(t *testing.T) {
	srv, _ := setupServer(t)
	body := `{"poll_interval":"5m"}`
	req := httptest.NewRequest("PUT", "/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("put config: status %d, body: %s", w.Code, w.Body.String())
	}
}

// TestHandlerPutConfig_PersistFailureReturns500 guards #550 + #565: a failed
// persist must surface as 500 with the offending keys, not a misleading 200 OK
// that makes the UI believe a never-persisted save succeeded.
//
// The write is now atomic (#565): handlePutConfig calls SetConfigs once and the
// whole batch is rolled back on any error, so on failure NONE of the keys
// persisted. The handler therefore reports the full attempted key set, sorted,
// which is exactly what closing the store produces here — every write fails and
// the response lists all keys deterministically regardless of map order.
func TestHandlerPutConfig_PersistFailureReturns500(t *testing.T) {
	srv, s := setupServer(t)
	// Close the store so the writes fail (sql: database is closed) while the
	// pure key/value validation above still passes.
	s.Close()

	// Several writable keys, deliberately not in alphabetical order in the
	// payload, so the assertion proves sort.Strings ordered the result.
	body := `{"review_mode":"single","poll_interval":"5m","retention_days":90}`
	req := httptest.NewRequest("PUT", "/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on persist failure, got %d (body: %s)", w.Code, w.Body.String())
	}
	var resp struct {
		Error      string   `json:"error"`
		FailedKeys []string `json:"failed_keys"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v (body: %s)", err, w.Body.String())
	}
	if resp.Error == "" {
		t.Errorf("expected non-empty error message, got body: %s", w.Body.String())
	}
	want := []string{"poll_interval", "retention_days", "review_mode"} // sorted
	if !reflect.DeepEqual(resp.FailedKeys, want) {
		t.Errorf("failed_keys = %v, want %v (exact set, sorted)", resp.FailedKeys, want)
	}
}

func TestHandlerShutdown(t *testing.T) {
	srv, _ := setupServer(t)
	shutdown := make(chan struct{}, 1)
	srv.SetShutdownFn(func() {
		shutdown <- struct{}{}
	})

	req := httptest.NewRequest("POST", "/shutdown", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("shutdown: status %d, body: %s", w.Code, w.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["status"] != "shutdown queued" {
		t.Fatalf("status = %q, want shutdown queued", body["status"])
	}
	select {
	case <-shutdown:
	case <-time.After(time.Second):
		t.Fatal("shutdown callback was not called")
	}
}

func TestHandlerShutdownNotConfigured(t *testing.T) {
	srv, _ := setupServer(t)
	req := httptest.NewRequest("POST", "/shutdown", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

func TestHandlerShutdownRequiresAuthWhenTokenSet(t *testing.T) {
	srv := setupServerWithToken(t, "secret-token")
	shutdown := make(chan struct{}, 1)
	srv.SetShutdownFn(func() {
		shutdown <- struct{}{}
	})

	req := httptest.NewRequest("POST", "/shutdown", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("POST /shutdown without token: status = %d, want 401", w.Code)
	}

	req = httptest.NewRequest("POST", "/shutdown", nil)
	req.Header.Set("X-Heimdallm-Token", "secret-token")
	w = httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("POST /shutdown with token: status = %d, want 202", w.Code)
	}
	select {
	case <-shutdown:
	case <-time.After(time.Second):
		t.Fatal("shutdown callback was not called")
	}
}

func TestUpdatePreparationLifecycle(t *testing.T) {
	srv := setupServerWithToken(t, "secret-token")
	prepareCalls := 0
	cancelCalls := 0
	sealCalls := 0
	confirmCalls := 0
	srv.SetUpdatePreparationFns(
		func(leaseID string) (server.UpdatePreparationStatus, error) {
			prepareCalls++
			return server.UpdatePreparationStatus{
				State:       "draining",
				PID:         42,
				LeaseID:     leaseID,
				Active:      map[string]int{"reviews": 1},
				ActiveTotal: 1,
			}, nil
		},
		func(leaseID string) (server.UpdatePreparationStatus, error) {
			cancelCalls++
			return server.UpdatePreparationStatus{
				State:   "running",
				PID:     42,
				LeaseID: leaseID,
				Active:  map[string]int{},
			}, nil
		},
	)
	srv.SetUpdateSealFn(func(leaseID string) (server.UpdatePreparationStatus, error) {
		sealCalls++
		return server.UpdatePreparationStatus{
			State:   "ready",
			PID:     42,
			LeaseID: leaseID,
			Sealed:  true,
			Active:  map[string]int{},
		}, nil
	})
	srv.SetUpdateConfirmFn(func(leaseID string) (server.UpdatePreparationStatus, error) {
		confirmCalls++
		return server.UpdatePreparationStatus{
			State:               "ready",
			PID:                 84,
			Version:             "v1.2.3",
			LeaseID:             leaseID,
			Sealed:              true,
			BootstrapAuthorized: true,
			BootID:              "test-boot-id",
			Active:              map[string]int{},
		}, nil
	})

	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		req := httptest.NewRequest(method, "/update/prepare", nil)
		w := httptest.NewRecorder()
		srv.Router().ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s without token: status = %d, want 401", method, w.Code)
		}
	}
	unauthorizedSeal := httptest.NewRequest(http.MethodPost, "/update/seal", nil)
	unauthorizedSeal.Header.Set(server.HeaderUpdateLease, "owner-a")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, unauthorizedSeal)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("seal without token: status = %d, want 401", w.Code)
	}
	unauthorizedConfirm := httptest.NewRequest(http.MethodPost, "/update/confirm", nil)
	unauthorizedConfirm.Header.Set(server.HeaderUpdateLease, "owner-a")
	w = httptest.NewRecorder()
	srv.Router().ServeHTTP(w, unauthorizedConfirm)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("confirm without token: status = %d, want 401", w.Code)
	}

	req := httptest.NewRequest(http.MethodPost, "/update/prepare", nil)
	req.Header.Set("X-Heimdallm-Token", "secret-token")
	w = httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("prepare without lease id: status = %d, want 400", w.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/update/prepare", nil)
	req.Header.Set("X-Heimdallm-Token", "secret-token")
	req.Header.Set(server.HeaderUpdateLease, "owner-a")
	w = httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("prepare: status = %d, body = %s", w.Code, w.Body.String())
	}
	var prepared server.UpdatePreparationStatus
	if err := json.NewDecoder(w.Body).Decode(&prepared); err != nil {
		t.Fatalf("decode prepare: %v", err)
	}
	if prepared.State != "draining" || prepared.PID != 42 || prepared.LeaseID != "owner-a" || prepared.ActiveTotal != 1 {
		t.Fatalf("prepare response = %+v", prepared)
	}

	req = httptest.NewRequest(http.MethodPost, "/update/seal", nil)
	req.Header.Set("X-Heimdallm-Token", "secret-token")
	req.Header.Set(server.HeaderUpdateLease, "owner-a")
	w = httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("seal: status = %d, body = %s", w.Code, w.Body.String())
	}
	var sealed server.UpdatePreparationStatus
	if err := json.NewDecoder(w.Body).Decode(&sealed); err != nil {
		t.Fatalf("decode seal: %v", err)
	}
	if sealed.State != "ready" || !sealed.Sealed || sealed.LeaseID != "owner-a" {
		t.Fatalf("seal response = %+v", sealed)
	}

	req = httptest.NewRequest(http.MethodPost, "/update/confirm", nil)
	req.Header.Set("X-Heimdallm-Token", "secret-token")
	req.Header.Set(server.HeaderUpdateLease, "owner-a")
	w = httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest || confirmCalls != 0 {
		t.Fatalf("confirm without expected boot id: status = %d, calls = %d; want 400 and 0", w.Code, confirmCalls)
	}

	req.Header.Set(server.HeaderExpectedUpdateBootID, "stale-process")
	w = httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusConflict || confirmCalls != 0 {
		t.Fatalf("confirm for stale process: status = %d, calls = %d; want 409 and 0", w.Code, confirmCalls)
	}

	req.Header.Set(server.HeaderExpectedUpdateBootID, "test-boot-id")
	w = httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("confirm: status = %d, body = %s", w.Code, w.Body.String())
	}
	var confirmed server.UpdatePreparationStatus
	if err := json.NewDecoder(w.Body).Decode(&confirmed); err != nil {
		t.Fatalf("decode confirm: %v", err)
	}
	if !confirmed.BootstrapAuthorized || !confirmed.Sealed || confirmed.LeaseID != "owner-a" || confirmed.PID != 84 || confirmed.BootID != "test-boot-id" {
		t.Fatalf("confirm response = %+v", confirmed)
	}

	req = httptest.NewRequest(http.MethodDelete, "/update/prepare", nil)
	req.Header.Set("X-Heimdallm-Token", "secret-token")
	req.Header.Set(server.HeaderUpdateLease, "owner-a")
	w = httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest || cancelCalls != 0 {
		t.Fatalf("cancel without expected boot id: status = %d, calls = %d; want 400 and 0", w.Code, cancelCalls)
	}
	req.Header.Set(server.HeaderExpectedUpdateBootID, "stale-process")
	w = httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusConflict || cancelCalls != 0 {
		t.Fatalf("cancel for stale process: status = %d, calls = %d; want 409 and 0", w.Code, cancelCalls)
	}
	req.Header.Set(server.HeaderExpectedUpdateBootID, "test-boot-id")
	w = httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("cancel: status = %d, body = %s", w.Code, w.Body.String())
	}
	var cancelled server.UpdatePreparationStatus
	if err := json.NewDecoder(w.Body).Decode(&cancelled); err != nil {
		t.Fatalf("decode cancel: %v", err)
	}
	if cancelled.State != "running" || cancelled.LeaseID != "owner-a" {
		t.Fatalf("cancel response = %+v, want running with echoed owner", cancelled)
	}
	if prepareCalls != 1 || sealCalls != 1 || confirmCalls != 1 || cancelCalls != 1 {
		t.Fatalf("callback calls = prepare %d, seal %d, confirm %d, cancel %d; want 1 each", prepareCalls, sealCalls, confirmCalls, cancelCalls)
	}
}

func TestUpdatePreparationMapsLeaseConflictWithoutLeakingOwner(t *testing.T) {
	srv := setupServerWithToken(t, "secret-token")
	srv.SetUpdatePreparationFns(
		func(string) (server.UpdatePreparationStatus, error) {
			return server.UpdatePreparationStatus{}, server.ErrUpdateLeaseConflict
		},
		func(string) (server.UpdatePreparationStatus, error) {
			return server.UpdatePreparationStatus{}, server.ErrUpdateLeaseConflict
		},
	)
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		req := httptest.NewRequest(method, "/update/prepare", nil)
		req.Header.Set("X-Heimdallm-Token", "secret-token")
		req.Header.Set(server.HeaderUpdateLease, "stale-owner-that-must-not-leak")
		req.Header.Set(server.HeaderExpectedUpdateBootID, "test-boot-id")
		w := httptest.NewRecorder()
		srv.Router().ServeHTTP(w, req)
		if w.Code != http.StatusConflict {
			t.Fatalf("%s status = %d, want 409", method, w.Code)
		}
		if strings.Contains(w.Body.String(), "stale-owner") {
			t.Fatalf("%s response leaked lease owner: %s", method, w.Body.String())
		}
	}
}

func TestUpdatePreparationMapsInvalidLeaseToBadRequest(t *testing.T) {
	srv := setupServerWithToken(t, "secret-token")
	srv.SetUpdatePreparationFns(
		func(string) (server.UpdatePreparationStatus, error) {
			return server.UpdatePreparationStatus{}, server.ErrUpdateLeaseInvalid
		},
		func(string) (server.UpdatePreparationStatus, error) {
			return server.UpdatePreparationStatus{}, server.ErrUpdateLeaseInvalid
		},
	)
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		req := httptest.NewRequest(method, "/update/prepare", nil)
		req.Header.Set("X-Heimdallm-Token", "secret-token")
		req.Header.Set(server.HeaderUpdateLease, "invalid-owner")
		req.Header.Set(server.HeaderExpectedUpdateBootID, "test-boot-id")
		w := httptest.NewRecorder()
		srv.Router().ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400", method, w.Code)
		}
	}
}

func TestUpdateCancellationRejectsUnconfirmedReplacement(t *testing.T) {
	srv := setupServerWithToken(t, "secret-token")
	srv.SetUpdatePreparationFns(
		func(string) (server.UpdatePreparationStatus, error) {
			return server.UpdatePreparationStatus{}, nil
		},
		func(string) (server.UpdatePreparationStatus, error) {
			return server.UpdatePreparationStatus{}, server.ErrUpdateBootstrapNotAuthorized
		},
	)
	req := httptest.NewRequest(http.MethodDelete, "/update/prepare", nil)
	req.Header.Set("X-Heimdallm-Token", "secret-token")
	req.Header.Set(server.HeaderUpdateLease, "owner-that-must-not-leak")
	req.Header.Set(server.HeaderExpectedUpdateBootID, "test-boot-id")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("cancel unconfirmed replacement status = %d, want 409", w.Code)
	}
	if strings.Contains(w.Body.String(), "owner-that-must-not-leak") {
		t.Fatalf("cancellation response leaked lease owner: %s", w.Body.String())
	}
}

func TestUpdatePreparationUnavailable(t *testing.T) {
	srv, _ := setupServer(t)
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		req := httptest.NewRequest(method, "/update/prepare", nil)
		w := httptest.NewRecorder()
		srv.Router().ServeHTTP(w, req)
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status = %d, want 503", method, w.Code)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/update/seal", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST /update/seal status = %d, want 503", w.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/update/confirm", nil)
	w = httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST /update/confirm status = %d, want 503", w.Code)
	}
}

func itoa(n int64) string {
	return fmt.Sprintf("%d", n)
}

func setupServerWithToken(t *testing.T, token string) *server.Server {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	broker := sse.NewBroker()
	broker.Start()
	t.Cleanup(broker.Stop)
	return server.NewWithOptions(s, broker, nil, token, server.Options{UpdateBootID: "test-boot-id"})
}

func TestHandlerLogsStream_RequiresAuth(t *testing.T) {
	srv := setupServerWithToken(t, "secret-token")
	req := httptest.NewRequest("GET", "/logs/stream", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without token, got %d", w.Code)
	}
}

func TestHandlerLogsStream_WithToken(t *testing.T) {
	srv := setupServerWithToken(t, "secret-token")
	t.Setenv("HEIMDALLM_DATA_DIR", t.TempDir())
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/logs/stream", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("X-Heimdallm-Token", "secret-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /logs/stream: %v", err)
	}
	defer resp.Body.Close()

	// The isolated data directory has no log file, so the endpoint emits its
	// diagnostic SSE line and closes cleanly.
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 with valid token, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("expected Content-Type text/event-stream, got %q", ct)
	}
}

func TestLogsStreamSurvivesServerWriteTimeout(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("HEIMDALLM_DATA_DIR", dataDir)
	logPath := filepath.Join(dataDir, server.DaemonLogFileName)
	if err := os.WriteFile(logPath, []byte("before deadline\n"), 0o600); err != nil {
		t.Fatalf("write initial log: %v", err)
	}

	srv, _ := setupServer(t)
	const writeTimeout = 75 * time.Millisecond
	ts := newStreamingTestServer(t, srv.Router(), writeTimeout)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/logs/stream", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /logs/stream: %v", err)
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	eventType, data, err := nextSSEEvent(scanner)
	if err != nil {
		t.Fatalf("read initial log line: %v", err)
	}
	if eventType != "log_line" || !strings.Contains(data, "before deadline") {
		t.Fatalf("initial log event = (%q, %q)", eventType, data)
	}

	time.Sleep(2 * writeTimeout)
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open log for append: %v", err)
	}
	if _, err := f.WriteString("after deadline\n"); err != nil {
		_ = f.Close()
		t.Fatalf("append log: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close appended log: %v", err)
	}

	eventType, data, err = nextSSEEvent(scanner)
	if err != nil {
		t.Fatalf("read log line after server write timeout: %v", err)
	}
	if eventType != "log_line" || !strings.Contains(data, "after deadline") {
		t.Fatalf("log event after deadline = (%q, %q)", eventType, data)
	}
}

func TestLogsStreamRejectsWriterWithoutDeadlineControl(t *testing.T) {
	srv := setupServerWithToken(t, "secret-token")
	req := httptest.NewRequest(http.MethodGet, "/logs/stream", nil)
	req.Header.Set("X-Heimdallm-Token", "secret-token")
	w := httptest.NewRecorder()

	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d want %d", w.Code, http.StatusInternalServerError)
	}
}

func TestPublicEndpointsRequireAuthWhenTokenSet(t *testing.T) {
	srv := setupServerWithToken(t, "secret-token")

	paths := []string{"/me", "/prs", "/stats"}
	for _, path := range paths {
		// Without token → 401
		req := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		srv.Router().ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without token: expected 401, got %d", path, w.Code)
		}

		// With valid token → not 401
		req2 := httptest.NewRequest("GET", path, nil)
		req2.Header.Set("X-Heimdallm-Token", "secret-token")
		w2 := httptest.NewRecorder()
		srv.Router().ServeHTTP(w2, req2)
		if w2.Code == http.StatusUnauthorized {
			t.Errorf("GET %s with valid token: expected not-401, got 401", path)
		}
	}
}

func TestHandlerTriggerReviewRateLimit(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()
	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()

	// Server with max 2 concurrent reviews
	srv := server.NewWithOptions(s, broker, nil, "test-token", server.Options{MaxConcurrentReviews: 2})

	// Wire a review function that blocks until gate is closed
	gate := make(chan struct{})
	srv.SetTriggerReviewFn(func(prID int64) error {
		<-gate
		return nil
	})

	// Seed 3 PRs
	now := time.Now()
	for i := 1; i <= 3; i++ {
		s.UpsertPR(&store.PR{
			GithubID: int64(i), Repo: "org/r", Number: i,
			Title: "t", Author: "a", URL: "u", State: "open",
			UpdatedAt: now, FetchedAt: now,
		})
	}

	token := "test-token"

	// Fire 2 concurrent reviews — should succeed
	for i := 1; i <= 2; i++ {
		req := httptest.NewRequest("POST", fmt.Sprintf("/prs/%d/review", i), nil)
		req.Header.Set("X-Heimdallm-Token", token)
		w := httptest.NewRecorder()
		srv.Router().ServeHTTP(w, req)
		if w.Code != http.StatusAccepted {
			t.Errorf("review %d: expected 202, got %d", i, w.Code)
		}
	}

	// Brief wait for goroutines to acquire semaphore
	time.Sleep(10 * time.Millisecond)

	// Third review should be rejected with 429
	req := httptest.NewRequest("POST", "/prs/3/review", nil)
	req.Header.Set("X-Heimdallm-Token", token)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429 when semaphore full, got %d", w.Code)
	}

	// Release goroutines
	close(gate)
}

func TestHandlerListIssues(t *testing.T) {
	srv, s := setupServer(t)
	now := time.Now()
	id, err := s.UpsertIssue(&store.Issue{
		GithubID: 100, Repo: "org/repo", Number: 7, Title: "bug: crash",
		Body: "details", Author: "alice", Assignees: `["bob"]`, Labels: `["bug"]`,
		State: "open", CreatedAt: now, FetchedAt: now,
	})
	if err != nil {
		t.Fatalf("upsert issue: %v", err)
	}
	s.InsertIssueReview(&store.IssueReview{
		IssueID: id, CLIUsed: "claude", Summary: "triage summary",
		Triage: `{"severity":"high","category":"bug"}`, NextSteps: `["fix it"]`,
		ActionTaken: "review_only", CreatedAt: now,
	})

	req := httptest.NewRequest("GET", "/issues", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list issues: status %d, body: %s", w.Code, w.Body.String())
	}
	var issues []map[string]any
	json.NewDecoder(w.Body).Decode(&issues)
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}
	iss := issues[0]
	if iss["title"] != "bug: crash" {
		t.Errorf("title = %v", iss["title"])
	}
	// Verify assignees/labels are arrays, not strings
	if assignees, ok := iss["assignees"].([]any); !ok || len(assignees) != 1 {
		t.Errorf("assignees should be parsed array, got %T: %v", iss["assignees"], iss["assignees"])
	}
	if labels, ok := iss["labels"].([]any); !ok || len(labels) != 1 {
		t.Errorf("labels should be parsed array, got %T: %v", iss["labels"], iss["labels"])
	}
	// Verify latest_review is attached
	rev, ok := iss["latest_review"].(map[string]any)
	if !ok || rev == nil {
		t.Fatalf("expected latest_review, got %v", iss["latest_review"])
	}
	if rev["summary"] != "triage summary" {
		t.Errorf("review summary = %v", rev["summary"])
	}
	// Verify triage is parsed object, not string
	if _, ok := rev["triage"].(map[string]any); !ok {
		t.Errorf("triage should be parsed object, got %T: %v", rev["triage"], rev["triage"])
	}
}

func TestHandlerGetIssue(t *testing.T) {
	srv, s := setupServer(t)
	now := time.Now()
	id, _ := s.UpsertIssue(&store.Issue{
		GithubID: 200, Repo: "org/repo", Number: 8, Title: "feat request",
		Body: "details", Author: "bob", Assignees: `[]`, Labels: `["enhancement"]`,
		State: "open", CreatedAt: now, FetchedAt: now,
	})
	s.InsertIssueReview(&store.IssueReview{
		IssueID: id, CLIUsed: "gemini", Summary: "looks good",
		Triage: `{"severity":"low","category":"feature"}`, NextSteps: `[]`,
		ActionTaken: "review_only", CreatedAt: now,
	})

	req := httptest.NewRequest("GET", "/issues/"+itoa(id), nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("get issue: status %d, body: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	json.NewDecoder(w.Body).Decode(&body)
	iss, ok := body["issue"].(map[string]any)
	if !ok {
		t.Fatalf("expected issue key")
	}
	if iss["title"] != "feat request" {
		t.Errorf("title = %v", iss["title"])
	}
	reviews, ok := body["reviews"].([]any)
	if !ok || len(reviews) != 1 {
		t.Fatalf("expected 1 review, got %v", body["reviews"])
	}
}

func TestHandlerGetIssue_NotFound(t *testing.T) {
	srv, _ := setupServer(t)
	req := httptest.NewRequest("GET", "/issues/9999", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestHandlerDismissIssue(t *testing.T) {
	srv, s := setupServer(t)
	now := time.Now()
	id, _ := s.UpsertIssue(&store.Issue{
		GithubID: 300, Repo: "org/r", Number: 10, Title: "t",
		Body: "b", Author: "a", Assignees: `[]`, Labels: `[]`,
		State: "open", CreatedAt: now, FetchedAt: now,
	})

	req := httptest.NewRequest("POST", "/issues/"+itoa(id)+"/dismiss", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("dismiss issue: status %d, body: %s", w.Code, w.Body.String())
	}

	issues, _ := s.ListIssues()
	if len(issues) != 0 {
		t.Errorf("expected 0 issues after dismiss, got %d", len(issues))
	}
}

func TestHandlerUndismissIssue(t *testing.T) {
	srv, s := setupServer(t)
	now := time.Now()
	id, _ := s.UpsertIssue(&store.Issue{
		GithubID: 400, Repo: "org/r", Number: 11, Title: "t",
		Body: "b", Author: "a", Assignees: `[]`, Labels: `[]`,
		State: "open", CreatedAt: now, FetchedAt: now,
	})
	s.DismissIssue(id)

	req := httptest.NewRequest("POST", "/issues/"+itoa(id)+"/undismiss", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("undismiss issue: status %d, body: %s", w.Code, w.Body.String())
	}

	issues, _ := s.ListIssues()
	if len(issues) != 1 {
		t.Errorf("expected 1 issue after undismiss, got %d", len(issues))
	}
}

func TestHandlerTriggerIssueReview(t *testing.T) {
	srv, s := setupServer(t)
	now := time.Now()
	id, _ := s.UpsertIssue(&store.Issue{
		GithubID: 500, Repo: "org/r", Number: 12, Title: "t",
		Body: "b", Author: "a", Assignees: `[]`, Labels: `[]`,
		State: "open", CreatedAt: now, FetchedAt: now,
	})

	triggered := make(chan int64, 1)
	srv.SetTriggerIssueReviewFn(func(issueID int64) error {
		triggered <- issueID
		return nil
	})

	req := httptest.NewRequest("POST", "/issues/"+itoa(id)+"/review", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("trigger issue review: status %d, body: %s", w.Code, w.Body.String())
	}

	select {
	case got := <-triggered:
		if got != id {
			t.Errorf("triggered with issue_id %d, expected %d", got, id)
		}
	case <-time.After(2 * time.Second):
		t.Error("trigger callback not called within 2s")
	}
}

func TestHandlerTriggerIssueReview_NotConfigured(t *testing.T) {
	srv, s := setupServer(t)
	now := time.Now()
	id, _ := s.UpsertIssue(&store.Issue{
		GithubID: 600, Repo: "org/r", Number: 13, Title: "t",
		Body: "b", Author: "a", Assignees: `[]`, Labels: `[]`,
		State: "open", CreatedAt: now, FetchedAt: now,
	})

	req := httptest.NewRequest("POST", "/issues/"+itoa(id)+"/review", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when trigger not configured, got %d", w.Code)
	}
}

func TestHandlerTriggerIssueRefine(t *testing.T) {
	srv, s := setupServer(t)
	now := time.Now()
	id, _ := s.UpsertIssue(&store.Issue{
		GithubID: 610, Repo: "org/r", Number: 15, Title: "t",
		Body: "b", Author: "a", Assignees: `[]`, Labels: `[]`,
		State: "open", CreatedAt: now, FetchedAt: now,
	})

	type call struct {
		id    int64
		force bool
	}
	triggered := make(chan call, 1)
	srv.SetTriggerIssueRefineFn(func(issueID int64, force bool) error {
		triggered <- call{id: issueID, force: force}
		return nil
	})

	req := httptest.NewRequest("POST", "/issues/"+itoa(id)+"/refine?force=true", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("trigger issue refine: status %d, body: %s", w.Code, w.Body.String())
	}

	select {
	case got := <-triggered:
		if got.id != id || !got.force {
			t.Errorf("triggered with %+v, expected id=%d force=true", got, id)
		}
	case <-time.After(2 * time.Second):
		t.Error("refine callback not called within 2s")
	}
}

func TestHandlerTriggerIssueRefine_NotConfigured(t *testing.T) {
	srv, s := setupServer(t)
	now := time.Now()
	id, _ := s.UpsertIssue(&store.Issue{
		GithubID: 611, Repo: "org/r", Number: 16, Title: "t",
		Body: "b", Author: "a", Assignees: `[]`, Labels: `[]`,
		State: "open", CreatedAt: now, FetchedAt: now,
	})

	req := httptest.NewRequest("POST", "/issues/"+itoa(id)+"/refine", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when refine not configured, got %d", w.Code)
	}
}

func TestHandlerTriggerIssueRefine_NotFound(t *testing.T) {
	srv, _ := setupServer(t)
	srv.SetTriggerIssueRefineFn(func(issueID int64, force bool) error {
		t.Fatalf("callback should not be called for unknown issue")
		return nil
	})

	req := httptest.NewRequest("POST", "/issues/999/refine", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown issue, got %d", w.Code)
	}
}

func TestHandlerPromoteIssue(t *testing.T) {
	srv, s := setupServer(t)
	now := time.Now()
	id, _ := s.UpsertIssue(&store.Issue{
		GithubID: 800, Repo: "org/r", Number: 20, Title: "t",
		Body: "b", Author: "a", Assignees: `[]`, Labels: `[]`,
		State: "open", CreatedAt: now, FetchedAt: now,
	})

	promoted := make(chan int64, 1)
	srv.SetTriggerPromoteFn(func(issueID int64) error {
		promoted <- issueID
		return nil
	})

	req := httptest.NewRequest("POST", "/issues/"+itoa(id)+"/promote", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("promote issue: status %d, body: %s", w.Code, w.Body.String())
	}
	var body map[string]string
	json.NewDecoder(w.Body).Decode(&body)
	if body["status"] != "promotion applied" {
		t.Errorf("promote issue: unexpected body %v", body)
	}

	select {
	case got := <-promoted:
		if got != id {
			t.Errorf("promoted with issue_id %d, expected %d", got, id)
		}
	case <-time.After(2 * time.Second):
		t.Error("promote callback not called within 2s")
	}
}

func TestHandlerPromoteIssue_Conflict(t *testing.T) {
	srv, s := setupServer(t)
	now := time.Now()
	id, _ := s.UpsertIssue(&store.Issue{
		GithubID: 802, Repo: "org/r", Number: 22, Title: "t",
		Body: "b", Author: "a", Assignees: `[]`, Labels: `[]`,
		State: "open", CreatedAt: now, FetchedAt: now,
	})

	srv.SetTriggerPromoteFn(func(issueID int64) error {
		return fmt.Errorf("%w: already in development", server.ErrPromoteConflict)
	})

	req := httptest.NewRequest("POST", "/issues/"+itoa(id)+"/promote", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("promote issue: status %d, want 409; body: %s", w.Code, w.Body.String())
	}
}

func TestHandlerPromoteIssue_UpdateDrainIsRetryableConflict(t *testing.T) {
	srv, s := setupServer(t)
	now := time.Now()
	id, _ := s.UpsertIssue(&store.Issue{
		GithubID: 803, Repo: "org/r", Number: 23, Title: "t",
		Body: "b", Author: "a", Assignees: `[]`, Labels: `[]`,
		State: "open", CreatedAt: now, FetchedAt: now,
	})
	srv.SetTriggerPromoteFn(func(int64) error {
		return workgate.ErrDraining
	})

	req := httptest.NewRequest("POST", "/issues/"+itoa(id)+"/promote", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("promote during update: status %d, want 409; body: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Retry-After"); got != "5" {
		t.Fatalf("Retry-After = %q, want 5", got)
	}
}

func TestHandlerPromoteIssue_NotConfigured(t *testing.T) {
	srv, s := setupServer(t)
	now := time.Now()
	id, _ := s.UpsertIssue(&store.Issue{
		GithubID: 801, Repo: "org/r", Number: 21, Title: "t",
		Body: "b", Author: "a", Assignees: `[]`, Labels: `[]`,
		State: "open", CreatedAt: now, FetchedAt: now,
	})

	req := httptest.NewRequest("POST", "/issues/"+itoa(id)+"/promote", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when promote not configured, got %d", w.Code)
	}
}

func TestIssueEndpointsRequireAuthWhenTokenSet(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()
	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()
	srv := server.New(s, broker, nil, "secret-token")

	now := time.Now()
	id, _ := s.UpsertIssue(&store.Issue{
		GithubID: 700, Repo: "org/r", Number: 14, Title: "t",
		Body: "b", Author: "a", Assignees: `[]`, Labels: `[]`,
		State: "open", CreatedAt: now, FetchedAt: now,
	})
	issueID := fmt.Sprintf("%d", id)

	// GET endpoints — protected via sensitiveGETPaths
	getPaths := []string{"/issues", "/issues/" + issueID}
	for _, path := range getPaths {
		req := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		srv.Router().ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without token: expected 401, got %d", path, w.Code)
		}

		req2 := httptest.NewRequest("GET", path, nil)
		req2.Header.Set("X-Heimdallm-Token", "secret-token")
		w2 := httptest.NewRecorder()
		srv.Router().ServeHTTP(w2, req2)
		if w2.Code == http.StatusUnauthorized {
			t.Errorf("GET %s with valid token: unexpected 401", path)
		}
	}

	// POST endpoints — protected via method-based auth (all POST requires token)
	postPaths := []string{
		"/issues/" + issueID + "/review",
		"/issues/" + issueID + "/refine",
		"/issues/" + issueID + "/promote",
		"/issues/" + issueID + "/dismiss",
		"/issues/" + issueID + "/undismiss",
	}
	for _, path := range postPaths {
		req := httptest.NewRequest("POST", path, nil)
		w := httptest.NewRecorder()
		srv.Router().ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("POST %s without token: expected 401, got %d", path, w.Code)
		}

		req2 := httptest.NewRequest("POST", path, nil)
		req2.Header.Set("X-Heimdallm-Token", "secret-token")
		w2 := httptest.NewRecorder()
		srv.Router().ServeHTTP(w2, req2)
		if w2.Code == http.StatusUnauthorized {
			t.Errorf("POST %s with valid token: unexpected 401", path)
		}
	}
}

func TestHandlerPutConfig_IssueTracking_Accepted(t *testing.T) {
	srv, _ := setupServer(t)
	body := `{"issue_tracking":{"enabled":true,"filter_mode":"exclusive","default_action":"ignore","develop_labels":["feature","bug"],"review_only_labels":["question"],"skip_labels":["wontfix"],"organizations":[],"assignees":[]}}`
	req := httptest.NewRequest("PUT", "/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestHandlerPutConfig_IssueTracking_InvalidFilterMode(t *testing.T) {
	srv, _ := setupServer(t)
	// filter_mode "weird" with enabled=true should trip validateIssueTracking.
	body := `{"issue_tracking":{"enabled":true,"filter_mode":"weird","default_action":"ignore"}}`
	req := httptest.NewRequest("PUT", "/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestHandlerPutConfig_IssueTracking_InvalidDefaultAction(t *testing.T) {
	srv, _ := setupServer(t)
	body := `{"issue_tracking":{"enabled":true,"filter_mode":"exclusive","default_action":"delete_the_repo"}}`
	req := httptest.NewRequest("PUT", "/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestHandlerPutConfig_IssueTracking_PersistsAndIsReadable(t *testing.T) {
	// End-to-end: PUT → ListConfigs → ApplyStore → cfg reflects the change.
	// This is the scenario that is broken on main today and the reason the
	// web UI's "Save & reload" silently loses values on refresh.
	srv, s := setupServer(t)
	body := `{"issue_tracking":{"enabled":true,"filter_mode":"inclusive","default_action":"review_only","develop_labels":["feature"]}}`
	req := httptest.NewRequest("PUT", "/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}

	rows, err := s.ListConfigs()
	if err != nil {
		t.Fatalf("ListConfigs: %v", err)
	}
	raw, ok := rows["issue_tracking"]
	if !ok {
		t.Fatalf("store: expected issue_tracking row, got keys %v", rows)
	}

	cfg := newCfgWithPrimary()
	cfg.GitHub.PollInterval = "5m"
	if err := cfg.ApplyStore(map[string]string{"issue_tracking": raw}); err != nil {
		t.Fatalf("ApplyStore: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate after ApplyStore: %v", err)
	}
	it := cfg.GitHub.IssueTracking
	if !it.Enabled || it.FilterMode != config.FilterModeInclusive || it.DefaultAction != "review_only" {
		t.Errorf("round-trip: got %+v", it)
	}
	if len(it.DevelopLabels) != 1 || it.DevelopLabels[0] != "feature" {
		t.Errorf("DevelopLabels = %v, want [feature]", it.DevelopLabels)
	}
}

// newCfgWithPrimary builds a minimal valid Config for tests that want to run
// cfg.Validate() after ApplyStore (Validate requires ai.primary).
func newCfgWithPrimary() *config.Config {
	c := &config.Config{}
	c.AI.Primary = "claude"
	return c
}

// ── read-only round-tripping (#86) ─────────────────────────────────────────

func putConfigRequest(body string) *http.Request {
	req := httptest.NewRequest("PUT", "/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestHandlerPutConfig_ReadOnlyKeys_Accepted(t *testing.T) {
	// The web UI round-trips these fields verbatim from GET /config as a
	// forward-compat safeguard; the handler must accept them (no 400) even
	// though it won't persist them.
	cases := []struct {
		name string
		body string
	}{
		{"repositories", `{"repositories":["org/monitored"]}`},
		{"non_monitored", `{"non_monitored":["org/archived"]}`},
		{"repo_overrides", `{"repo_overrides":{"org/a":{"primary":"claude"}}}`},
		{"org_overrides", `{"org_overrides":{"org":{"primary":"claude"}}}`},
		{"server_port", `{"server_port":7842}`},
		{"all-at-once", `{"repositories":[],"non_monitored":[],"repo_overrides":{},"org_overrides":{},"server_port":7842}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := setupServer(t)
			w := httptest.NewRecorder()
			srv.Router().ServeHTTP(w, putConfigRequest(tc.body))
			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
			}
		})
	}
}

func TestHandlerPutConfig_ReadOnlyKeys_NotPersisted(t *testing.T) {
	// Accepted but never written: the configs table must stay untouched so
	// reload doesn't have to re-examine them and ApplyStore's
	// "unknown/bootstrap-only" branches stay dormant.
	srv, s := setupServer(t)
	body := `{"repositories":["org/y"],"non_monitored":["org/x"],"repo_overrides":{"org/a":{"primary":"claude"}},"org_overrides":{"org":{"primary":"gemini"}},"server_port":7842}`
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, putConfigRequest(body))
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}

	rows, err := s.ListConfigs()
	if err != nil {
		t.Fatalf("ListConfigs: %v", err)
	}
	for _, banned := range []string{"repositories", "non_monitored", "repo_overrides", "org_overrides", "server_port"} {
		if _, leaked := rows[banned]; leaked {
			t.Errorf("read-only key %q was persisted (rows: %v)", banned, rows)
		}
	}
}

func TestHandlerPutConfig_WritableAndReadOnly_Mixed(t *testing.T) {
	// A realistic save: the Flutter UI sends writable fields (poll_interval,
	// agent_configs) next to the round-tripped read-only ones (repositories,
	// non_monitored). Writables land in the store; read-only keys are
	// silently dropped.
	srv, s := setupServer(t)
	body := `{"poll_interval":"30m","agent_configs":{"claude":{"model":"x","permission_mode":"acceptEdits"}},"repositories":["org/y"],"non_monitored":["org/x"]}`
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, putConfigRequest(body))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}

	rows, err := s.ListConfigs()
	if err != nil {
		t.Fatalf("ListConfigs: %v", err)
	}
	if rows["poll_interval"] != "30m" {
		t.Errorf("poll_interval = %q, want 30m", rows["poll_interval"])
	}
	if got := rows["agent_configs"]; got == "" {
		t.Errorf("agent_configs was not persisted (rows: %v)", rows)
	} else if !strings.Contains(got, `"permission_mode":"acceptEdits"`) || !strings.Contains(got, `"model":"x"`) {
		t.Errorf("agent_configs payload missing fields: %s", got)
	}
	if _, ok := rows["repositories"]; ok {
		t.Errorf("repositories unexpectedly persisted")
	}
	if _, ok := rows["non_monitored"]; ok {
		t.Errorf("non_monitored unexpectedly persisted")
	}
}

func TestHandlerPutConfig_AgentConfigs_RejectsDangerouslySkipPerms(t *testing.T) {
	// Security gate M-5: --dangerously-skip-permissions cannot be enabled
	// over HTTP. The handler must 400 with a message that points the
	// operator at config.toml so true never lands in sqlite.
	srv, _ := setupServer(t)
	body := `{"agent_configs":{"claude":{"dangerously_skip_perms":true}}}`
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, putConfigRequest(body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (body: %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "dangerously_skip_perms") {
		t.Errorf("error message must name the rejected key, got: %s", w.Body.String())
	}
}

func TestHandlerPutConfig_AgentConfigs_AllowsDisablingDangerouslySkipPerms(t *testing.T) {
	srv, s := setupServer(t)
	body := `{"agent_configs":{"claude":{"dangerously_skip_perms":false}}}`
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, putConfigRequest(body))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}
	rows, err := s.ListConfigs()
	if err != nil {
		t.Fatalf("ListConfigs: %v", err)
	}
	if got := rows["agent_configs"]; !strings.Contains(got, `"dangerously_skip_perms":false`) {
		t.Fatalf("explicit safety downgrade was not persisted: %q", got)
	}
}

func TestHandlerPutConfig_AgentConfigs_RejectsBadPermissionMode(t *testing.T) {
	srv, _ := setupServer(t)
	body := `{"agent_configs":{"claude":{"permission_mode":"bypassPermissions"}}}`
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, putConfigRequest(body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestHandlerPutConfig_AgentConfigs_EnforcesProviderPolicy(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{
			name:       "Codex sandbox override rejected",
			body:       `{"agent_configs":{"codex":{"extra_flags":"--sandbox danger-full-access"}}}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "Gemini yolo approval rejected",
			body:       `{"agent_configs":{"gemini":{"approval_mode":"yolo"}}}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "Gemini typed auto edit accepted",
			body:       `{"agent_configs":{"gemini":{"approval_mode":"auto_edit"}}}`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "Option-shaped model rejected",
			body:       `{"agent_configs":{"claude":{"model":"--dangerously-skip-permissions"}}}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "Option-shaped effort rejected",
			body:       `{"agent_configs":{"claude":{"effort":"--dangerously-skip-permissions"}}}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "Legacy free-form model rejected in new config writes",
			body:       `{"agent_configs":{"claude":{"extra_flags":"--model opus"}}}`,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := setupServer(t)
			w := httptest.NewRecorder()
			srv.Router().ServeHTTP(w, putConfigRequest(tc.body))
			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", w.Code, tc.wantStatus, w.Body.String())
			}
		})
	}
}

func TestHandlerUpsertAgent_RejectsProviderPolicyFlags(t *testing.T) {
	srv, _ := setupServer(t)
	body := `{"id":"unsafe-codex","name":"Unsafe Codex","cli":"codex","cli_flags":"--sandbox=danger-full-access"}`
	req := httptest.NewRequest("POST", "/agents", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (body: %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "--sandbox") {
		t.Fatalf("error must identify rejected flag, got: %s", w.Body.String())
	}
}

func TestHandlerUpsertAgent_AcceptsSafeLegacyTypedProfileFlags(t *testing.T) {
	srv, s := setupServer(t)
	body := `{"id":"legacy-claude","name":"Legacy Claude","cli":"claude","cli_flags":"--model opus --max-turns 5 --effort HIGH --verbose"}`
	req := httptest.NewRequest("POST", "/agents", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}
	agents, err := s.ListAgents()
	if err != nil {
		t.Fatalf("list agents: %v", err)
	}
	if len(agents) != 1 || agents[0].CLIFlags != "--model opus --max-turns 5 --effort HIGH --verbose" {
		t.Fatalf("legacy profile flags were not preserved for runtime migration: %+v", agents)
	}
}

func TestHandlerUpsertAgent_RequiresCLIForFlags(t *testing.T) {
	srv, _ := setupServer(t)
	body := `{"id":"missing-cli","name":"Missing CLI","cli_flags":"--model safe-model"}`
	req := httptest.NewRequest("POST", "/agents", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (body: %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "cli is required") {
		t.Fatalf("error must explain the effective-provider requirement, got: %s", w.Body.String())
	}
}

func TestHandlerPutConfig_AgentConfigs_RejectsUnknownSubkey(t *testing.T) {
	srv, _ := setupServer(t)
	body := `{"agent_configs":{"claude":{"new_secret_field":true}}}`
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, putConfigRequest(body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestHandlerPutConfig_AgentConfigs_RejectsUnknownCLI(t *testing.T) {
	srv, _ := setupServer(t)
	body := `{"agent_configs":{"not-a-cli":{"model":"x"}}}`
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, putConfigRequest(body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestHandlerPutConfig_UnknownKey_StillRejected(t *testing.T) {
	// Security regression guard: the read-only escape hatch must NOT become
	// a catch-all. Keys that are neither writable nor read-only still 400.
	srv, _ := setupServer(t)
	body := `{"not_a_real_key":"whatever"}`
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, putConfigRequest(body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown key, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestHandlerPutConfig_ServerPort_RangeStillValidated(t *testing.T) {
	// server_port moves from writable to read-only, but its numeric-range
	// pre-check (handlers.go:362) still fires — a client sending a
	// privileged port is still rejected so the bug-class "validate before
	// accept" stays intact even for read-only keys.
	srv, _ := setupServer(t)
	body := `{"server_port":80}`
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, putConfigRequest(body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for port 80, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestHandleActivity_DefaultsToToday(t *testing.T) {
	srv, s := setupServer(t)
	now := time.Now()
	yesterday := now.Add(-26 * time.Hour)
	if _, err := s.InsertActivity(yesterday, "acme", "acme/api", "pr", 1, "Old", "review", "minor", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertActivity(now, "acme", "acme/api", "pr", 2, "New", "review", "major", map[string]any{"cli_used": "claude"}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/activity", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Entries []struct {
			Repo    string         `json:"repo"`
			Action  string         `json:"action"`
			Outcome string         `json:"outcome"`
			Details map[string]any `json:"details"`
		} `json:"entries"`
		Count     int  `json:"count"`
		Truncated bool `json:"truncated"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Entries) != 1 {
		t.Fatalf("want 1 entry (today only), got %d", len(resp.Entries))
	}
	if resp.Entries[0].Outcome != "major" {
		t.Errorf("outcome = %q", resp.Entries[0].Outcome)
	}
	if resp.Entries[0].Details["cli_used"] != "claude" {
		t.Errorf("details: %+v", resp.Entries[0].Details)
	}
}

func TestHandleActivity_ExplicitDate(t *testing.T) {
	srv, s := setupServer(t)
	loc := time.Now().Location()
	day := time.Date(2026, 4, 18, 12, 0, 0, 0, loc)
	if _, err := s.InsertActivity(day, "acme", "acme/api", "pr", 1, "Old", "review", "minor", nil); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/activity?date=2026-04-18", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp struct {
		Count int `json:"count"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Count != 1 {
		t.Errorf("count = %d, want 1", resp.Count)
	}
}

func TestHandleActivity_BadDateFormat(t *testing.T) {
	srv, _ := setupServer(t)
	req := httptest.NewRequest("GET", "/activity?date=2026/04/20", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleActivity_DateAndRangeMutuallyExclusive(t *testing.T) {
	srv, _ := setupServer(t)
	req := httptest.NewRequest("GET", "/activity?date=2026-04-20&from=2026-04-19&to=2026-04-20", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleActivity_FromWithoutTo(t *testing.T) {
	srv, _ := setupServer(t)
	req := httptest.NewRequest("GET", "/activity?from=2026-04-19", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleActivity_ToBeforeFrom(t *testing.T) {
	srv, _ := setupServer(t)
	req := httptest.NewRequest("GET", "/activity?from=2026-04-20&to=2026-04-18", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleActivity_LimitOutOfRange(t *testing.T) {
	srv, _ := setupServer(t)
	for _, v := range []string{"0", "-1", "5001", "abc"} {
		req := httptest.NewRequest("GET", "/activity?limit="+v, nil)
		w := httptest.NewRecorder()
		srv.Router().ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("limit=%q: status = %d, want 400", v, w.Code)
		}
	}
}

func TestHandleActivity_DisabledReturns503(t *testing.T) {
	srv, _ := setupServer(t)
	srv.SetConfigFn(func() map[string]any {
		return map[string]any{"activity_log_enabled": false}
	})
	req := httptest.NewRequest("GET", "/activity", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

func TestHandleActivity_RequiresAuth(t *testing.T) {
	srv := setupServerWithToken(t, "secret-token")
	req := httptest.NewRequest("GET", "/activity", nil)
	// no token
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestHandleActivity_FilterByRepoAndAction(t *testing.T) {
	srv, s := setupServer(t)
	now := time.Now()
	_, _ = s.InsertActivity(now, "acme", "acme/api", "pr", 1, "t", "review", "minor", nil)
	_, _ = s.InsertActivity(now, "acme", "acme/api", "issue", 2, "t", "triage", "major", nil)
	_, _ = s.InsertActivity(now, "globex", "globex/w", "pr", 3, "t", "review", "minor", nil)

	req := httptest.NewRequest("GET", "/activity?repo=acme/api&action=review", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		Entries []struct{ Repo, Action string } `json:"entries"`
		Count   int                             `json:"count"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Count != 1 {
		t.Fatalf("count = %d, want 1", resp.Count)
	}
	if resp.Entries[0].Repo != "acme/api" || resp.Entries[0].Action != "review" {
		t.Errorf("wrong entry: %+v", resp.Entries[0])
	}
}

func TestHandleActivity_FilterByItemTypeAndOutcome(t *testing.T) {
	srv, s := setupServer(t)
	now := time.Now()
	_, _ = s.InsertActivity(now, "acme", "acme/api", "pr", 1, "t", "review_skipped", "draft", nil)
	_, _ = s.InsertActivity(now, "acme", "acme/api", "pr", 2, "t", "review_skipped", "not_open", nil)
	_, _ = s.InsertActivity(now, "acme", "acme/api", "issue", 3, "t", "triage", "draft", nil)

	req := httptest.NewRequest("GET", "/activity?item_type=pr&action=review_skipped&outcome=draft", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		Entries []struct {
			ItemType   string `json:"item_type"`
			ItemNumber int    `json:"item_number"`
			Action     string `json:"action"`
			Outcome    string `json:"outcome"`
		} `json:"entries"`
		Count int `json:"count"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Count != 1 {
		t.Fatalf("count = %d, want 1", resp.Count)
	}
	if resp.Entries[0].ItemType != "pr" ||
		resp.Entries[0].ItemNumber != 1 ||
		resp.Entries[0].Action != "review_skipped" ||
		resp.Entries[0].Outcome != "draft" {
		t.Errorf("wrong entry: %+v", resp.Entries[0])
	}
}

func TestHandleActivity_RejectsInvalidFilterValues(t *testing.T) {
	cases := []struct {
		name  string
		query url.Values
	}{
		{
			name:  "invalid item_type",
			query: url.Values{"item_type": {"repo"}},
		},
		{
			name:  "invalid action",
			query: url.Values{"action": {"reviewSkipped"}},
		},
		{
			name:  "empty repo",
			query: url.Values{"repo": {""}},
		},
		{
			name:  "too long outcome",
			query: url.Values{"outcome": {strings.Repeat("x", 513)}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := setupServer(t)
			req := httptest.NewRequest("GET", "/activity?"+tc.query.Encode(), nil)
			w := httptest.NewRecorder()
			srv.Router().ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body = %s)", w.Code, w.Body.String())
			}
		})
	}
}

func TestHandleActivity_RejectsTooManyFilterValues(t *testing.T) {
	srv, _ := setupServer(t)
	q := url.Values{}
	for i := 0; i < 51; i++ {
		q.Add("repo", fmt.Sprintf("acme/api-%d", i))
	}

	req := httptest.NewRequest("GET", "/activity?"+q.Encode(), nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body = %s)", w.Code, w.Body.String())
	}
}

func TestHandlerPutConfigValueValidation(t *testing.T) {
	srv := setupServerWithToken(t, "secret-token")

	cases := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{
			name:       "valid poll_interval 5m",
			body:       `{"poll_interval":"5m"}`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "valid arbitrary poll_interval 3m",
			body:       `{"poll_interval":"3m"}`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "invalid poll_interval below floor 30s",
			body:       `{"poll_interval":"30s"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid poll_interval above ceiling 48h",
			body:       `{"poll_interval":"48h"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid poll_interval unparseable",
			body:       `{"poll_interval":"nonsense"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "valid retention_days 90",
			body:       `{"retention_days":90}`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "retention_days too high 9999",
			body:       `{"retention_days":9999}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "valid server_port 8080",
			body:       `{"server_port":8080}`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "server_port too low 80",
			body:       `{"server_port":80}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "valid review_mode single",
			body:       `{"review_mode":"single"}`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "invalid review_mode batch",
			body:       `{"review_mode":"batch"}`,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("PUT", "/config",
				strings.NewReader(tc.body))
			req.Header.Set("X-Heimdallm-Token", "secret-token")
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			srv.Router().ServeHTTP(w, req)
			if w.Code != tc.wantStatus {
				t.Errorf("%s: expected %d, got %d (body: %s)",
					tc.name, tc.wantStatus, w.Code, w.Body.String())
			}
		})
	}
}

func TestHandleStats_IncludesActivityCount24h(t *testing.T) {
	srv, s := setupServer(t)
	// Insert one recent activity and one old (>24h).
	now := time.Now()
	if _, err := s.InsertActivity(now.Add(-1*time.Hour), "a", "a/b", "pr", 1, "t", "review", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertActivity(now.Add(-30*time.Hour), "a", "a/b", "pr", 2, "t", "review", "", nil); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/stats", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	v, ok := body["activity_count_24h"]
	if !ok {
		t.Fatalf("activity_count_24h missing from response: %v", body)
	}
	// JSON numbers unmarshal to float64 when decoding into map[string]any.
	got, ok := v.(float64)
	if !ok || got != 1 {
		t.Errorf("activity_count_24h = %v, want 1", v)
	}
}

// writeTempTOML creates a temp config.toml with the given content and returns
// its path. The file is cleaned up by t.Cleanup.
func writeTempTOML(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp TOML: %v", err)
	}
	return path
}

func TestHandlePatchConfig(t *testing.T) {
	// Valid TOML with ai.primary set (required by Config.Validate).
	tomlContent := "[ai]\nprimary = \"claude\"\nfallback = \"gemini\"\n"
	tomlPath := writeTempTOML(t, tomlContent)

	srv := setupServerWithToken(t, "test-token")
	srv.SetConfigPath(tomlPath)

	body := `{"ai":{"primary":"openai"}}`
	req := httptest.NewRequest("PATCH", "/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}

	// Read the TOML back and verify primary was updated but fallback preserved.
	m, err := config.ReadTOMLMap(tomlPath)
	if err != nil {
		t.Fatalf("read TOML after PATCH: %v", err)
	}
	ai, ok := m["ai"].(map[string]any)
	if !ok {
		t.Fatalf("expected [ai] section in TOML, got %v", m)
	}
	if ai["primary"] != "openai" {
		t.Errorf("primary = %v, want openai", ai["primary"])
	}
	if ai["fallback"] != "gemini" {
		t.Errorf("fallback = %v, want gemini (should be preserved)", ai["fallback"])
	}
}

func TestHandlePatchConfig_RepoListsWriteTOML(t *testing.T) {
	tomlContent := "[ai]\nprimary = \"claude\"\n"
	tomlPath := writeTempTOML(t, tomlContent)

	srv := setupServerWithToken(t, "test-token")
	srv.SetConfigPath(tomlPath)

	body := `{"github":{"repositories":["org/monitored"],"non_monitored":["org/disabled"]}}`
	req := httptest.NewRequest("PATCH", "/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}

	m, err := config.ReadTOMLMap(tomlPath)
	if err != nil {
		t.Fatalf("read TOML after PATCH: %v", err)
	}
	gh, ok := m["github"].(map[string]any)
	if !ok {
		t.Fatalf("expected [github] section in TOML, got %v", m)
	}
	repos, ok := gh["repositories"].([]any)
	if !ok || len(repos) != 1 || repos[0] != "org/monitored" {
		t.Fatalf("repositories = %v, want [org/monitored]", gh["repositories"])
	}
	nonMonitored, ok := gh["non_monitored"].([]any)
	if !ok || len(nonMonitored) != 1 || nonMonitored[0] != "org/disabled" {
		t.Fatalf("non_monitored = %v, want [org/disabled]", gh["non_monitored"])
	}
}

func TestHandlePatchConfig_RejectsDangerouslySkipPermsEnable(t *testing.T) {
	// Security gate M-5 must be explicit: HTTP callers cannot enable the
	// bypass, and a rejected payload must not partially apply safe siblings.
	tomlContent := "[ai]\nprimary = \"claude\"\n"
	tomlPath := writeTempTOML(t, tomlContent)

	srv := setupServerWithToken(t, "test-token")
	srv.SetConfigPath(tomlPath)

	body := `{"ai":{"agents":{"claude":{"dangerously_skip_perms":true,"permission_mode":"acceptEdits"}}}}`
	req := httptest.NewRequest("PATCH", "/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("PATCH enable = %d, want 400 (body: %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "cannot be enabled") {
		t.Fatalf("PATCH error is not actionable: %s", w.Body.String())
	}
	got, err := os.ReadFile(tomlPath)
	if err != nil {
		t.Fatalf("read TOML after rejected PATCH: %v", err)
	}
	if string(got) != tomlContent {
		t.Fatalf("rejected PATCH partially changed TOML:\n%s", got)
	}
}

func TestHandlePatchConfig_DisablesAndCanonicalizesTrustedDangerousAlias(t *testing.T) {
	tomlContent := "[AI]\n" +
		"primary = \"claude\"\n" +
		"[AI.Agents.claude]\n" +
		"DANGEROUSLY_SKIP_PERMS = true\n" +
		"permission_mode = \"default\"\n"
	tomlPath := writeTempTOML(t, tomlContent)

	srv := setupServerWithToken(t, "test-token")
	srv.SetConfigPath(tomlPath)

	// HTTP may reduce privilege. False must persist rather than being silently
	// stripped, while legal sibling fields still apply atomically.
	body := `{"ai":{"agents":{"claude":{"dangerously_skip_perms":false,"permission_mode":"acceptEdits"}}}}`
	req := httptest.NewRequest("PATCH", "/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("PATCH should succeed, got %d (body: %s)", w.Code, w.Body.String())
	}

	m, err := config.ReadTOMLMap(tomlPath)
	if err != nil {
		t.Fatalf("read TOML after PATCH: %v", err)
	}
	for key := range m {
		if strings.EqualFold(key, "ai") && key != "ai" {
			t.Fatalf("legacy structural alias %q survived PATCH: %v", key, m)
		}
	}
	ai, _ := m["ai"].(map[string]any)
	for key := range ai {
		if strings.EqualFold(key, "agents") && key != "agents" {
			t.Fatalf("legacy agents alias %q survived PATCH: %v", key, ai)
		}
	}
	agents, _ := ai["agents"].(map[string]any)
	claude, _ := agents["claude"].(map[string]any)
	if got, ok := claude["dangerously_skip_perms"].(bool); !ok || got {
		t.Fatalf("dangerously_skip_perms was not disabled by HTTP PATCH: %v", claude)
	}
	for key := range claude {
		if strings.EqualFold(key, "dangerously_skip_perms") && key != "dangerously_skip_perms" {
			t.Fatalf("legacy dangerous alias %q survived PATCH: %v", key, claude)
		}
	}
	if claude["permission_mode"] != "acceptEdits" {
		t.Fatalf("legal sibling field did not land: %v", claude)
	}
	for i := 0; i < 32; i++ {
		loaded, err := config.Load(tomlPath)
		if err != nil {
			t.Fatalf("Load iteration %d after PATCH: %v", i, err)
		}
		if loaded.AI.Agents["claude"].DangerouslySkipPerms {
			t.Fatalf("Load iteration %d re-enabled dangerously_skip_perms", i)
		}
	}
}

func TestHandlePatchConfig_MigratesLegacyTOMLBaseBeforeInnocuousPatch(t *testing.T) {
	tomlContent := "[github]\n" +
		"poll_interval = \"5m\"\n\n" +
		"[ai]\n" +
		"primary = \"codex\"\n\n" +
		"[ai.agents.codex]\n" +
		"extra_flags = \"--model gpt-5 --json\"\n"
	tomlPath := writeTempTOML(t, tomlContent)

	srv := setupServerWithToken(t, "test-token")
	srv.SetConfigPath(tomlPath)

	req := httptest.NewRequest("PATCH", "/config", strings.NewReader(`{"github":{"poll_interval":"10m"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("innocuous PATCH over legacy TOML = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	m, err := config.ReadTOMLMap(tomlPath)
	if err != nil {
		t.Fatalf("read migrated TOML: %v", err)
	}
	githubConfig, _ := m["github"].(map[string]any)
	if githubConfig["poll_interval"] != "10m" {
		t.Fatalf("innocuous PATCH did not land: %v", githubConfig)
	}
	ai, _ := m["ai"].(map[string]any)
	agents, _ := ai["agents"].(map[string]any)
	codex, _ := agents["codex"].(map[string]any)
	if codex["model"] != "gpt-5" || codex["extra_flags"] != "--json" {
		t.Fatalf("legacy TOML base was not migrated safely: %v", codex)
	}
}

func TestHandlePatchConfig_ReportsAmbiguousTrustedAliases(t *testing.T) {
	tomlContent := "[ai]\n" +
		"primary = \"codex\"\n" +
		"[ai.agents.codex]\n" +
		"Model = \"first\"\n" +
		"MODEL = \"second\"\n"
	tomlPath := writeTempTOML(t, tomlContent)

	srv := setupServerWithToken(t, "test-token")
	srv.SetConfigPath(tomlPath)
	req := httptest.NewRequest(
		"PATCH",
		"/config",
		strings.NewReader(`{"github":{"poll_interval":"10m"}}`),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("ambiguous trusted base PATCH = %d, want 400 (body: %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "ambiguous aliases") {
		t.Fatalf("ambiguous trusted base error is not actionable: %s", w.Body.String())
	}
	got, err := os.ReadFile(tomlPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != tomlContent {
		t.Fatalf("rejected PATCH changed ambiguous TOML:\n%s", got)
	}
}

func TestHandlePatchConfig_RejectsNewLegacyTypedFlagWithoutChangingTOML(t *testing.T) {
	tomlContent := "[ai]\nprimary = \"codex\"\n"
	tomlPath := writeTempTOML(t, tomlContent)

	srv := setupServerWithToken(t, "test-token")
	srv.SetConfigPath(tomlPath)

	body := `{"ai":{"agents":{"codex":{"extra_flags":"--model gpt-5"}}}}`
	req := httptest.NewRequest("PATCH", "/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("new legacy typed flag PATCH = %d, want 400 (body: %s)", w.Code, w.Body.String())
	}
	got, err := os.ReadFile(tomlPath)
	if err != nil {
		t.Fatalf("read TOML after rejected PATCH: %v", err)
	}
	if string(got) != tomlContent {
		t.Fatalf("rejected PATCH changed TOML:\n%s", got)
	}
}

func TestHandlePatchConfig_RejectsDangerouslySkipPermsCaseAliases(t *testing.T) {
	// JSON preserves key casing while downstream decoding is case-insensitive.
	// Reject aliases explicitly instead of relying on a silent scrub.
	tomlContent := "[ai]\nprimary = \"claude\"\n"
	tomlPath := writeTempTOML(t, tomlContent)

	srv := setupServerWithToken(t, "test-token")
	srv.SetConfigPath(tomlPath)

	body := `{"ai":{"agents":{` +
		`"claude":{"Dangerously_Skip_Perms":true},` +
		`"gemini":{"DANGEROUSLY_SKIP_PERMS":true},` +
		`"codex":{"dANGEROUSLY_skip_perms":true}` +
		`}}}`
	req := httptest.NewRequest("PATCH", "/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("PATCH aliases = %d, want 400 (body: %s)", w.Code, w.Body.String())
	}
	got, err := os.ReadFile(tomlPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != tomlContent {
		t.Fatalf("rejected alias PATCH changed TOML:\n%s", got)
	}
}

func TestHandlePatchConfig_RejectsCaseVariantAgentTree(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "top-level AI",
			body: `{"AI":{"agents":{"claude":{"dangerously_skip_perms":true}}}}`,
		},
		{
			name: "global Agents",
			body: `{"ai":{"Agents":{"claude":{"dangerously_skip_perms":true}}}}`,
		},
		{
			name: "repo Agents",
			body: `{"ai":{"repos":{"org/repo":{"Agents":{"claude":{"dangerously_skip_perms":true}}}}}}`,
		},
		{
			name: "org Agents",
			body: `{"ai":{"orgs":{"org":{"Agents":{"claude":{"dangerously_skip_perms":true}}}}}}`,
		},
		{
			name: "CLI name",
			body: `{"ai":{"agents":{"Codex":{"extra_flags":"--sandbox danger-full-access"}}}}`,
		},
		{
			name: "agent field",
			body: `{"ai":{"agents":{"codex":{"Extra_Flags":"--sandbox danger-full-access"}}}}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tomlPath := writeTempTOML(t, "[ai]\nprimary = \"claude\"\n")
			srv := setupServerWithToken(t, "test-token")
			srv.SetConfigPath(tomlPath)

			req := httptest.NewRequest("PATCH", "/config", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Heimdallm-Token", "test-token")
			w := httptest.NewRecorder()
			srv.Router().ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for case-variant config tree, got %d (body: %s)", w.Code, w.Body.String())
			}
			got, err := os.ReadFile(tomlPath)
			if err != nil {
				t.Fatalf("read config after rejected PATCH: %v", err)
			}
			if string(got) != "[ai]\nprimary = \"claude\"\n" {
				t.Fatalf("rejected PATCH changed config:\n%s", got)
			}
		})
	}
}

func TestHandleScopedPatchConfig_RejectsCaseVariantAgentTree(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{
			name: "repo",
			path: "/config/repos/" + url.PathEscape("org/repo"),
		},
		{
			name: "org",
			path: "/config/orgs/" + url.PathEscape("org"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tomlPath := writeTempTOML(t, "[ai]\nprimary = \"claude\"\n")
			srv := setupServerWithToken(t, "test-token")
			srv.SetConfigPath(tomlPath)

			body := `{"Agents":{"claude":{"dangerously_skip_perms":true}}}`
			req := httptest.NewRequest("PATCH", tc.path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Heimdallm-Token", "test-token")
			w := httptest.NewRecorder()
			srv.Router().ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for case-variant scoped tree, got %d (body: %s)", w.Code, w.Body.String())
			}
		})
	}
}

func TestHandlePatchConfig_RejectsUnsupportedScopedAgentSafetyDowngrade(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
	}{
		{
			name: "repo endpoint",
			path: "/config/repos/" + url.PathEscape("org/repo"),
			body: `{"agents":{"claude":{"dangerously_skip_perms":false}}}`,
		},
		{
			name: "org endpoint",
			path: "/config/orgs/" + url.PathEscape("org"),
			body: `{"agents":{"claude":{"dangerously_skip_perms":false}}}`,
		},
		{
			name: "repo in global endpoint",
			path: "/config",
			body: `{"ai":{"repos":{"org/repo":{"agents":{"claude":{"dangerously_skip_perms":false}}}}}}`,
		},
		{
			name: "org in global endpoint",
			path: "/config",
			body: `{"ai":{"orgs":{"org":{"agents":{"claude":{"dangerously_skip_perms":false}}}}}}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tomlContent := "[ai]\nprimary = \"claude\"\n"
			tomlPath := writeTempTOML(t, tomlContent)
			srv := setupServerWithToken(t, "test-token")
			srv.SetConfigPath(tomlPath)

			req := httptest.NewRequest("PATCH", tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Heimdallm-Token", "test-token")
			w := httptest.NewRecorder()
			srv.Router().ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("scoped agent PATCH = %d, want 400 (body: %s)", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "not supported") {
				t.Fatalf("scoped rejection is not actionable: %s", w.Body.String())
			}
			got, err := os.ReadFile(tomlPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tomlContent {
				t.Fatalf("rejected scoped PATCH changed TOML:\n%s", got)
			}
		})
	}
}

func TestHandlePatchConfig_RejectsDangerouslySkipPermsInRepoOverride(t *testing.T) {
	tomlContent := "[ai]\nprimary = \"claude\"\n"
	tomlPath := writeTempTOML(t, tomlContent)

	srv := setupServerWithToken(t, "test-token")
	srv.SetConfigPath(tomlPath)

	body := `{"ai":{"repos":{"org/repo":{"agents":{"claude":{"dangerously_skip_perms":true}}}}}}`
	req := httptest.NewRequest("PATCH", "/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("repo PATCH enable = %d, want 400 (body: %s)", w.Code, w.Body.String())
	}
	got, err := os.ReadFile(tomlPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != tomlContent {
		t.Fatalf("rejected repo PATCH changed TOML:\n%s", got)
	}
}

func TestHandlePatchConfig_RejectsDangerouslySkipPermsInOrgOverride(t *testing.T) {
	tomlContent := "[ai]\nprimary = \"claude\"\n"
	tomlPath := writeTempTOML(t, tomlContent)

	srv := setupServerWithToken(t, "test-token")
	srv.SetConfigPath(tomlPath)

	body := `{"ai":{"orgs":{"org":{"agents":{"claude":{"dangerously_skip_perms":true}}}}}}`
	req := httptest.NewRequest("PATCH", "/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("org PATCH enable = %d, want 400 (body: %s)", w.Code, w.Body.String())
	}
	got, err := os.ReadFile(tomlPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != tomlContent {
		t.Fatalf("rejected org PATCH changed TOML:\n%s", got)
	}
}

func TestHandlePatchConfig_RejectsDangerouslySkipPermsAcrossAllAgents(t *testing.T) {
	// Same gate applied to every CLI under ai.agents — an attacker may
	// try flipping the flag on a less-watched agent.
	tomlContent := "[ai]\nprimary = \"claude\"\n"
	tomlPath := writeTempTOML(t, tomlContent)

	srv := setupServerWithToken(t, "test-token")
	srv.SetConfigPath(tomlPath)

	body := `{"ai":{"agents":{` +
		`"claude":{"dangerously_skip_perms":true},` +
		`"gemini":{"dangerously_skip_perms":true},` +
		`"codex":{"dangerously_skip_perms":true},` +
		`"opencode":{"dangerously_skip_perms":true}` +
		`}}}`
	req := httptest.NewRequest("PATCH", "/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("multi-agent PATCH enable = %d, want 400 (body: %s)", w.Code, w.Body.String())
	}
	got, err := os.ReadFile(tomlPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != tomlContent {
		t.Fatalf("rejected multi-agent PATCH changed TOML:\n%s", got)
	}
}

func TestHandlePatchConfig_RejectsNull(t *testing.T) {
	tomlContent := "[ai]\nprimary = \"claude\"\n"
	tomlPath := writeTempTOML(t, tomlContent)

	srv := setupServerWithToken(t, "test-token")
	srv.SetConfigPath(tomlPath)

	// Sending null for a field should be rejected with 400.
	body := `{"ai":{"primary":null}}`
	req := httptest.NewRequest("PATCH", "/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for null value, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestHandlePatchRepoConfig(t *testing.T) {
	// TOML with a global primary and an existing repo override.
	tomlContent := "[ai]\nprimary = \"claude\"\n\n[ai.repos.\"org/repo1\"]\nprimary = \"gemini\"\nfallback = \"openai\"\n"
	tomlPath := writeTempTOML(t, tomlContent)

	srv := setupServerWithToken(t, "test-token")
	srv.SetConfigPath(tomlPath)

	// PATCH only primary — fallback must be preserved.
	body := `{"primary":"claude-new"}`
	req := httptest.NewRequest("PATCH", "/config/repos/"+url.PathEscape("org/repo1"), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}

	// Verify TOML on disk reflects the change.
	m, err := config.ReadTOMLMap(tomlPath)
	if err != nil {
		t.Fatalf("read TOML after PATCH: %v", err)
	}
	ai, ok := m["ai"].(map[string]any)
	if !ok {
		t.Fatalf("expected [ai] section, got %v", m)
	}
	repos, ok := ai["repos"].(map[string]any)
	if !ok {
		t.Fatalf("expected [ai.repos] section, got %v", ai)
	}
	repo1, ok := repos["org/repo1"].(map[string]any)
	if !ok {
		t.Fatalf("expected [ai.repos.\"org/repo1\"] section, got %v", repos)
	}
	if repo1["primary"] != "claude-new" {
		t.Errorf("primary = %v, want claude-new", repo1["primary"])
	}
	if repo1["fallback"] != "openai" {
		t.Errorf("fallback = %v, want openai (should be preserved)", repo1["fallback"])
	}
}

func TestHandlePatchRepoConfig_CreatesNewSection(t *testing.T) {
	// TOML with only a global primary — no repo overrides yet.
	tomlContent := "[ai]\nprimary = \"claude\"\n"
	tomlPath := writeTempTOML(t, tomlContent)

	srv := setupServerWithToken(t, "test-token")
	srv.SetConfigPath(tomlPath)

	body := `{"pr_draft":true}`
	req := httptest.NewRequest("PATCH", "/config/repos/"+url.PathEscape("org/new-repo"), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}

	// Verify TOML now contains a section for org/new-repo with pr_draft.
	m, err := config.ReadTOMLMap(tomlPath)
	if err != nil {
		t.Fatalf("read TOML after PATCH: %v", err)
	}
	ai, ok := m["ai"].(map[string]any)
	if !ok {
		t.Fatalf("expected [ai] section, got %v", m)
	}
	repos, ok := ai["repos"].(map[string]any)
	if !ok {
		t.Fatalf("expected [ai.repos] section, got %v", ai)
	}
	newRepo, ok := repos["org/new-repo"].(map[string]any)
	if !ok {
		t.Fatalf("expected [ai.repos.\"org/new-repo\"] section, got %v", repos)
	}
	if newRepo["pr_draft"] != true {
		t.Errorf("pr_draft = %v, want true", newRepo["pr_draft"])
	}
}

func TestHandlePatchOrgConfig(t *testing.T) {
	tomlContent := "[ai]\nprimary = \"claude\"\n\n[ai.orgs.\"org\"]\nprimary = \"gemini\"\nfallback = \"openai\"\n"
	tomlPath := writeTempTOML(t, tomlContent)

	srv := setupServerWithToken(t, "test-token")
	srv.SetConfigPath(tomlPath)

	body := `{"primary":"codex","issue_tracking":{"enabled":true,"develop_labels":["ready"]}}`
	req := httptest.NewRequest("PATCH", "/config/orgs/"+url.PathEscape("org"), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}

	m, err := config.ReadTOMLMap(tomlPath)
	if err != nil {
		t.Fatalf("read TOML after PATCH: %v", err)
	}
	ai, ok := m["ai"].(map[string]any)
	if !ok {
		t.Fatalf("expected [ai] section, got %v", m)
	}
	orgs, ok := ai["orgs"].(map[string]any)
	if !ok {
		t.Fatalf("expected [ai.orgs] section, got %v", ai)
	}
	org, ok := orgs["org"].(map[string]any)
	if !ok {
		t.Fatalf("expected [ai.orgs.org] section, got %v", orgs)
	}
	if org["primary"] != "codex" {
		t.Errorf("primary = %v, want codex", org["primary"])
	}
	if org["fallback"] != "openai" {
		t.Errorf("fallback = %v, want openai (should be preserved)", org["fallback"])
	}
	it, ok := org["issue_tracking"].(map[string]any)
	if !ok {
		t.Fatalf("expected issue_tracking section, got %v", org)
	}
	labels, ok := it["develop_labels"].([]any)
	if !ok || len(labels) != 1 || labels[0] != "ready" {
		t.Fatalf("develop_labels = %v, want [ready]", it["develop_labels"])
	}
}

func TestHandlePatchOrgConfig_RejectsInvalidOrg(t *testing.T) {
	tomlPath := writeTempTOML(t, "[ai]\nprimary = \"claude\"\n")

	srv := setupServerWithToken(t, "test-token")
	srv.SetConfigPath(tomlPath)

	req := httptest.NewRequest("PATCH", "/config/orgs/"+url.PathEscape("bad org"), strings.NewReader(`{"primary":"codex"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestHandlePatchRepoConfig_RejectsInvalidRepo(t *testing.T) {
	cases := []struct {
		name string
		repo string
	}{
		{name: "missing slash", repo: "ownerrepo"},
		{name: "empty owner", repo: "/repo1"},
		{name: "empty name", repo: "org/"},
		{name: "special chars in owner", repo: "bad org/repo1"},
		{name: "special chars in name", repo: "org/bad repo"},
		{name: "path traversal name", repo: "org/.."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tomlPath := writeTempTOML(t, "[ai]\nprimary = \"claude\"\n")
			srv := setupServerWithToken(t, "test-token")
			srv.SetConfigPath(tomlPath)

			req := httptest.NewRequest("PATCH", "/config/repos/"+url.PathEscape(tc.repo), strings.NewReader(`{"primary":"codex"}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Heimdallm-Token", "test-token")
			w := httptest.NewRecorder()
			srv.Router().ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for repo %q, got %d (body: %s)", tc.repo, w.Code, w.Body.String())
			}
		})
	}
}

func TestHandleDeleteRepoField_TopLevel(t *testing.T) {
	tomlContent := "[ai]\nprimary = \"claude\"\n\n[ai.repos.\"org/repo1\"]\nprimary = \"gemini\"\npr_draft = true\n"
	tomlPath := writeTempTOML(t, tomlContent)

	srv := setupServerWithToken(t, "test-token")
	srv.SetConfigPath(tomlPath)

	req := httptest.NewRequest("DELETE", "/config/repos/"+url.PathEscape("org/repo1")+"/pr_draft", nil)
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}

	m, err := config.ReadTOMLMap(tomlPath)
	if err != nil {
		t.Fatalf("read TOML after DELETE: %v", err)
	}
	ai, ok := m["ai"].(map[string]any)
	if !ok {
		t.Fatalf("expected [ai] section, got %v", m)
	}
	repos, ok := ai["repos"].(map[string]any)
	if !ok {
		t.Fatalf("expected [ai.repos] section, got %v", ai)
	}
	repo1, ok := repos["org/repo1"].(map[string]any)
	if !ok {
		t.Fatalf("expected [ai.repos.\"org/repo1\"] section, got %v", repos)
	}
	if _, found := repo1["pr_draft"]; found {
		t.Errorf("pr_draft should have been deleted, still present: %v", repo1)
	}
	if repo1["primary"] != "gemini" {
		t.Errorf("primary = %v, want gemini (should be preserved)", repo1["primary"])
	}
}

func TestHandleDeleteRepoField_NestedPath(t *testing.T) {
	tomlContent := "[ai]\nprimary = \"claude\"\n\n[ai.repos.\"org/repo1\".issue_tracking]\ndevelop_labels = [\"ready\"]\nfilter_mode = \"exclusive\"\n"
	tomlPath := writeTempTOML(t, tomlContent)

	srv := setupServerWithToken(t, "test-token")
	srv.SetConfigPath(tomlPath)

	req := httptest.NewRequest("DELETE", "/config/repos/"+url.PathEscape("org/repo1")+"/issue_tracking/develop_labels", nil)
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}

	m, err := config.ReadTOMLMap(tomlPath)
	if err != nil {
		t.Fatalf("read TOML after DELETE: %v", err)
	}
	ai, ok := m["ai"].(map[string]any)
	if !ok {
		t.Fatalf("expected [ai] section, got %v", m)
	}
	repos, ok := ai["repos"].(map[string]any)
	if !ok {
		t.Fatalf("expected [ai.repos] section, got %v", ai)
	}
	repo1, ok := repos["org/repo1"].(map[string]any)
	if !ok {
		t.Fatalf("expected [ai.repos.\"org/repo1\"] section, got %v", repos)
	}
	issueTracking, ok := repo1["issue_tracking"].(map[string]any)
	if !ok {
		t.Fatalf("expected issue_tracking section, got %v", repo1)
	}
	if _, found := issueTracking["develop_labels"]; found {
		t.Errorf("develop_labels should have been deleted, still present: %v", issueTracking)
	}
	if issueTracking["filter_mode"] != "exclusive" {
		t.Errorf("filter_mode = %v, want exclusive (should be preserved)", issueTracking["filter_mode"])
	}
}

func TestHandleDeleteOrgField_NestedPath(t *testing.T) {
	tomlContent := "[ai]\nprimary = \"claude\"\n\n[ai.orgs.\"org\".issue_tracking]\ndevelop_labels = [\"ready\"]\nfilter_mode = \"exclusive\"\n"
	tomlPath := writeTempTOML(t, tomlContent)

	srv := setupServerWithToken(t, "test-token")
	srv.SetConfigPath(tomlPath)

	req := httptest.NewRequest("DELETE", "/config/orgs/"+url.PathEscape("org")+"/issue_tracking/develop_labels", nil)
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}

	m, err := config.ReadTOMLMap(tomlPath)
	if err != nil {
		t.Fatalf("read TOML after DELETE: %v", err)
	}
	ai, ok := m["ai"].(map[string]any)
	if !ok {
		t.Fatalf("expected [ai] section, got %v", m)
	}
	orgs, ok := ai["orgs"].(map[string]any)
	if !ok {
		t.Fatalf("expected [ai.orgs] section, got %v", ai)
	}
	org, ok := orgs["org"].(map[string]any)
	if !ok {
		t.Fatalf("expected [ai.orgs.org] section, got %v", orgs)
	}
	issueTracking, ok := org["issue_tracking"].(map[string]any)
	if !ok {
		t.Fatalf("expected issue_tracking section, got %v", org)
	}
	if _, found := issueTracking["develop_labels"]; found {
		t.Errorf("develop_labels should have been deleted, still present: %v", issueTracking)
	}
	if issueTracking["filter_mode"] != "exclusive" {
		t.Errorf("filter_mode = %v, want exclusive (should be preserved)", issueTracking["filter_mode"])
	}
}

func TestHandleDeleteRepoField_Idempotent(t *testing.T) {
	tomlContent := "[ai]\nprimary = \"claude\"\n"
	tomlPath := writeTempTOML(t, tomlContent)

	srv := setupServerWithToken(t, "test-token")
	srv.SetConfigPath(tomlPath)

	// DELETE for a non-existent repo/field — should be idempotent and return 200.
	req := httptest.NewRequest("DELETE", "/config/repos/"+url.PathEscape("org/nonexistent")+"/pr_draft", nil)
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (idempotent), got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestHandleDeleteManagedCloneRequiresAuthAndCallsCallback(t *testing.T) {
	srv := setupServerWithToken(t, "test-token")
	var gotRepo string
	srv.SetCleanCloneFn(func(ctx context.Context, repo string) error {
		gotRepo = repo
		return nil
	})
	path := "/config/clones/" + url.PathEscape("org/repo")

	req := httptest.NewRequest("DELETE", path, nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("DELETE without token status = %d, want 401", w.Code)
	}

	req = httptest.NewRequest("DELETE", path, nil)
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w = httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE with token status = %d, body: %s", w.Code, w.Body.String())
	}
	if gotRepo != "org/repo" {
		t.Fatalf("callback repo = %q, want org/repo", gotRepo)
	}
}

func TestHandleDeleteManagedCloneSurfacesCallbackError(t *testing.T) {
	srv := setupServerWithToken(t, "test-token")
	srv.SetCleanCloneFn(func(ctx context.Context, repo string) error {
		return fmt.Errorf("repoctx: clone target %q exists but is not managed", "/tmp/heimdallm/org/repo")
	})
	req := httptest.NewRequest("DELETE", "/config/clones/"+url.PathEscape("org/repo"), nil)
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if strings.Contains(w.Body.String(), "/tmp/heimdallm") {
		t.Fatalf("response leaked clone path: %s", w.Body.String())
	}
}

func TestHandleDeleteManagedCloneReportsUpdateDrainAsConflict(t *testing.T) {
	srv := setupServerWithToken(t, "test-token")
	srv.SetCleanCloneFn(func(context.Context, string) error {
		return workgate.ErrDraining
	})
	req := httptest.NewRequest("DELETE", "/config/clones/"+url.PathEscape("org/repo"), nil)
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
}

func TestHandleDeleteManagedClonesRequiresAuthAndCallsCallback(t *testing.T) {
	srv := setupServerWithToken(t, "test-token")
	called := false
	srv.SetCleanClonesFn(func(ctx context.Context) (int, error) {
		called = true
		return 3, nil
	})

	req := httptest.NewRequest("DELETE", "/config/clones", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("DELETE without token status = %d, want 401", w.Code)
	}

	req = httptest.NewRequest("DELETE", "/config/clones", nil)
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w = httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE with token status = %d, body: %s", w.Code, w.Body.String())
	}
	if !called {
		t.Fatal("expected cleanup callback to be called")
	}
	if !strings.Contains(w.Body.String(), `"removed":3`) {
		t.Fatalf("body = %s, want removed count", w.Body.String())
	}
}

func TestHandleListPRs_StateFilter(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	broker := sse.NewBroker()
	broker.Start()
	t.Cleanup(broker.Stop)
	srv := server.New(s, broker, nil, "test-token")

	now := time.Now()
	s.UpsertPR(&store.PR{GithubID: 10, Repo: "org/r", Number: 10, Title: "open pr", Author: "a", URL: "u", State: "open", UpdatedAt: now, FetchedAt: now})
	s.UpsertPR(&store.PR{GithubID: 11, Repo: "org/r", Number: 11, Title: "closed pr", Author: "a", URL: "u", State: "closed", UpdatedAt: now, FetchedAt: now})

	doReq := func(path string) []map[string]any {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("X-Heimdallm-Token", "test-token")
		w := httptest.NewRecorder()
		srv.Router().ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: status %d, body: %s", path, w.Code, w.Body.String())
		}
		var prs []map[string]any
		if err := json.NewDecoder(w.Body).Decode(&prs); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return prs
	}

	// No filter → both PRs returned
	if got := doReq("/prs"); len(got) != 2 {
		t.Errorf("GET /prs: expected 2, got %d", len(got))
	}

	// state=open → only the open PR
	if got := doReq("/prs?state=open"); len(got) != 1 {
		t.Errorf("GET /prs?state=open: expected 1, got %d", len(got))
	} else if got[0]["state"] != "open" {
		t.Errorf("GET /prs?state=open: got state %v", got[0]["state"])
	}

	// state=closed → only the closed PR
	if got := doReq("/prs?state=closed"); len(got) != 1 {
		t.Errorf("GET /prs?state=closed: expected 1, got %d", len(got))
	} else if got[0]["state"] != "closed" {
		t.Errorf("GET /prs?state=closed: got state %v", got[0]["state"])
	}

	// state=open,closed → both PRs
	if got := doReq("/prs?state=open,closed"); len(got) != 2 {
		t.Errorf("GET /prs?state=open,closed: expected 2, got %d", len(got))
	}
}

func TestHandleListIssues_StateFilter(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	broker := sse.NewBroker()
	broker.Start()
	t.Cleanup(broker.Stop)
	srv := server.New(s, broker, nil, "test-token")

	now := time.Now()
	s.UpsertIssue(&store.Issue{
		GithubID: 20, Repo: "org/r", Number: 20, Title: "open issue",
		Body: "b", Author: "a", Assignees: `[]`, Labels: `[]`,
		State: "open", CreatedAt: now, FetchedAt: now,
	})
	s.UpsertIssue(&store.Issue{
		GithubID: 21, Repo: "org/r", Number: 21, Title: "closed issue",
		Body: "b", Author: "a", Assignees: `[]`, Labels: `[]`,
		State: "closed", CreatedAt: now, FetchedAt: now,
	})

	doReq := func(path string) []map[string]any {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("X-Heimdallm-Token", "test-token")
		w := httptest.NewRecorder()
		srv.Router().ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: status %d, body: %s", path, w.Code, w.Body.String())
		}
		var issues []map[string]any
		if err := json.NewDecoder(w.Body).Decode(&issues); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return issues
	}

	// No filter → both issues returned
	if got := doReq("/issues"); len(got) != 2 {
		t.Errorf("GET /issues: expected 2, got %d", len(got))
	}

	// state=open → only the open issue
	if got := doReq("/issues?state=open"); len(got) != 1 {
		t.Errorf("GET /issues?state=open: expected 1, got %d", len(got))
	} else if got[0]["state"] != "open" {
		t.Errorf("GET /issues?state=open: got state %v", got[0]["state"])
	}

	// state=closed → only the closed issue
	if got := doReq("/issues?state=closed"); len(got) != 1 {
		t.Errorf("GET /issues?state=closed: expected 1, got %d", len(got))
	} else if got[0]["state"] != "closed" {
		t.Errorf("GET /issues?state=closed: got state %v", got[0]["state"])
	}

	// state=open,closed → both issues
	if got := doReq("/issues?state=open,closed"); len(got) != 2 {
		t.Errorf("GET /issues?state=open,closed: expected 2, got %d", len(got))
	}
}

// TestHandlerGetConfig_ExposesAutonomousAndCircuitBreaker guards that GET
// /config always includes the autonomous and circuit_breaker top-level keys
// with the correct snake_case field names. The Flutter UI reads these keys to
// render the autonomous-mode panel — a silent rename or re-nesting would break
// the UI without a test failure.
func TestHandlerGetConfig_ExposesAutonomousAndCircuitBreaker(t *testing.T) {
	srv, _ := setupServer(t)
	srv.SetConfigFn(func() map[string]any {
		return map[string]any{
			"autonomous": map[string]any{
				"enabled":           true,
				"auto_merge":        false,
				"merge_method":      "squash",
				"take_others_tasks": false,
				"reassign_on_take":  false,
				"dev_max_turns":     0,
				"dev_effort":        "high",
				"dev_timeout":       "45m",
				"claim_lease":       "2h",
				"orgs":              map[string]any{},
				"repos":             map[string]any{},
			},
			"circuit_breaker": map[string]any{
				"per_pr_24h":                 3,
				"per_repo_hr":                20,
				"per_review_failure_repo_hr": 20,
				"per_issue_24h":              3,
				"per_issue_repo_hr":          10,
				"per_impl_repo_hr":           5,
			},
		}
	})

	req := httptest.NewRequest("GET", "/config", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v (body: %s)", err, w.Body.String())
	}

	// Verify autonomous section.
	autonomous, ok := body["autonomous"].(map[string]any)
	if !ok {
		t.Fatalf("autonomous missing or wrong type: %T: %v", body["autonomous"], body["autonomous"])
	}
	if autonomous["enabled"] != true {
		t.Errorf("autonomous.enabled = %v, want true", autonomous["enabled"])
	}
	if autonomous["merge_method"] != "squash" {
		t.Errorf("autonomous.merge_method = %v, want squash", autonomous["merge_method"])
	}
	if autonomous["dev_effort"] != "high" {
		t.Errorf("autonomous.dev_effort = %v, want high", autonomous["dev_effort"])
	}
	if autonomous["claim_lease"] != "2h" {
		t.Errorf("autonomous.claim_lease = %v, want 2h", autonomous["claim_lease"])
	}
	if _, ok := autonomous["orgs"]; !ok {
		t.Errorf("autonomous.orgs missing")
	}
	if _, ok := autonomous["repos"]; !ok {
		t.Errorf("autonomous.repos missing")
	}

	// Verify circuit_breaker section.
	cb, ok := body["circuit_breaker"].(map[string]any)
	if !ok {
		t.Fatalf("circuit_breaker missing or wrong type: %T: %v", body["circuit_breaker"], body["circuit_breaker"])
	}
	// JSON numbers decode to float64 in map[string]any.
	if cb["per_pr_24h"].(float64) != 3 {
		t.Errorf("circuit_breaker.per_pr_24h = %v, want 3", cb["per_pr_24h"])
	}
	if cb["per_repo_hr"].(float64) != 20 {
		t.Errorf("circuit_breaker.per_repo_hr = %v, want 20", cb["per_repo_hr"])
	}
	if cb["per_review_failure_repo_hr"].(float64) != 20 {
		t.Errorf("circuit_breaker.per_review_failure_repo_hr = %v, want 20", cb["per_review_failure_repo_hr"])
	}
	if cb["per_impl_repo_hr"].(float64) != 5 {
		t.Errorf("circuit_breaker.per_impl_repo_hr = %v, want 5", cb["per_impl_repo_hr"])
	}
}

// TestHandlePatchConfig_AutonomousGlobalPersists verifies that a global PATCH
// containing an autonomous section is accepted and written to TOML.
func TestHandlePatchConfig_AutonomousGlobalPersists(t *testing.T) {
	tomlContent := "[ai]\nprimary = \"claude\"\n"
	tomlPath := writeTempTOML(t, tomlContent)

	srv := setupServerWithToken(t, "test-token")
	srv.SetConfigPath(tomlPath)

	body := `{"autonomous":{"enabled":true,"dev_max_turns":20}}`
	req := httptest.NewRequest("PATCH", "/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}

	m, err := config.ReadTOMLMap(tomlPath)
	if err != nil {
		t.Fatalf("read TOML after PATCH: %v", err)
	}
	autonomous, ok := m["autonomous"].(map[string]any)
	if !ok {
		t.Fatalf("autonomous section missing in TOML after PATCH: %v", m)
	}
	if autonomous["enabled"] != true {
		t.Errorf("autonomous.enabled = %v, want true", autonomous["enabled"])
	}
	if autonomous["dev_max_turns"] != int64(20) {
		t.Errorf("autonomous.dev_max_turns = %v (%T), want 20", autonomous["dev_max_turns"], autonomous["dev_max_turns"])
	}
}

// TestHandlePatchConfig_CircuitBreakerGlobalPersists verifies that a global PATCH
// containing a circuit_breaker section is accepted and written to TOML.
func TestHandlePatchConfig_CircuitBreakerGlobalPersists(t *testing.T) {
	tomlContent := "[ai]\nprimary = \"claude\"\n"
	tomlPath := writeTempTOML(t, tomlContent)

	srv := setupServerWithToken(t, "test-token")
	srv.SetConfigPath(tomlPath)

	body := `{"circuit_breaker":{"per_impl_repo_hr":9,"per_review_failure_repo_hr":27}}`
	req := httptest.NewRequest("PATCH", "/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}

	m, err := config.ReadTOMLMap(tomlPath)
	if err != nil {
		t.Fatalf("read TOML after PATCH: %v", err)
	}
	cb, ok := m["circuit_breaker"].(map[string]any)
	if !ok {
		t.Fatalf("circuit_breaker section missing in TOML after PATCH: %v", m)
	}
	if cb["per_impl_repo_hr"] != int64(9) {
		t.Errorf("circuit_breaker.per_impl_repo_hr = %v (%T), want 9", cb["per_impl_repo_hr"], cb["per_impl_repo_hr"])
	}
	if cb["per_review_failure_repo_hr"] != int64(27) {
		t.Errorf("circuit_breaker.per_review_failure_repo_hr = %v (%T), want 27", cb["per_review_failure_repo_hr"], cb["per_review_failure_repo_hr"])
	}
}

// TestHandlePatchAutonomousRepoConfig verifies that PATCH
// /config/autonomous/repos/{repo} writes autonomous.repos.<repo> into TOML.
func TestHandlePatchAutonomousRepoConfig(t *testing.T) {
	tomlContent := "[ai]\nprimary = \"claude\"\n"
	tomlPath := writeTempTOML(t, tomlContent)

	srv := setupServerWithToken(t, "test-token")
	srv.SetConfigPath(tomlPath)

	body := `{"enabled":true,"auto_merge":false}`
	req := httptest.NewRequest("PATCH", "/config/autonomous/repos/"+url.PathEscape("org/myrepo"), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}

	m, err := config.ReadTOMLMap(tomlPath)
	if err != nil {
		t.Fatalf("read TOML after PATCH: %v", err)
	}
	autonomous, ok := m["autonomous"].(map[string]any)
	if !ok {
		t.Fatalf("autonomous section missing in TOML: %v", m)
	}
	repos, ok := autonomous["repos"].(map[string]any)
	if !ok {
		t.Fatalf("autonomous.repos section missing in TOML: %v", autonomous)
	}
	myrepo, ok := repos["org/myrepo"].(map[string]any)
	if !ok {
		t.Fatalf("autonomous.repos[\"org/myrepo\"] missing in TOML: %v", repos)
	}
	if myrepo["enabled"] != true {
		t.Errorf("autonomous.repos[org/myrepo].enabled = %v, want true", myrepo["enabled"])
	}
	if myrepo["auto_merge"] != false {
		t.Errorf("autonomous.repos[org/myrepo].auto_merge = %v, want false", myrepo["auto_merge"])
	}
}

// TestHandlePatchAutonomousOrgConfig verifies that PATCH
// /config/autonomous/orgs/{org} writes autonomous.orgs.<org> into TOML.
func TestHandlePatchAutonomousOrgConfig(t *testing.T) {
	tomlContent := "[ai]\nprimary = \"claude\"\n"
	tomlPath := writeTempTOML(t, tomlContent)

	srv := setupServerWithToken(t, "test-token")
	srv.SetConfigPath(tomlPath)

	body := `{"enabled":false,"dev_max_turns":10}`
	req := httptest.NewRequest("PATCH", "/config/autonomous/orgs/myorg", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}

	m, err := config.ReadTOMLMap(tomlPath)
	if err != nil {
		t.Fatalf("read TOML after PATCH: %v", err)
	}
	autonomous, ok := m["autonomous"].(map[string]any)
	if !ok {
		t.Fatalf("autonomous section missing in TOML: %v", m)
	}
	orgs, ok := autonomous["orgs"].(map[string]any)
	if !ok {
		t.Fatalf("autonomous.orgs section missing in TOML: %v", autonomous)
	}
	myorg, ok := orgs["myorg"].(map[string]any)
	if !ok {
		t.Fatalf("autonomous.orgs[\"myorg\"] missing in TOML: %v", orgs)
	}
	if myorg["enabled"] != false {
		t.Errorf("autonomous.orgs[myorg].enabled = %v, want false", myorg["enabled"])
	}
	if myorg["dev_max_turns"] != int64(10) {
		t.Errorf("autonomous.orgs[myorg].dev_max_turns = %v (%T), want 10", myorg["dev_max_turns"], myorg["dev_max_turns"])
	}
}

func TestHandlePatchAutonomousConfigRejectsAgentsWithoutPersisting(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
	}{
		{
			name: "global config repo override",
			path: "/config",
			body: `{"autonomous":{"repos":{"org/repo":{"agents":{"codex":{"extra_flags":"--sandbox danger-full-access"}}}}}}`,
		},
		{
			name: "repo endpoint",
			path: "/config/autonomous/repos/" + url.PathEscape("org/repo"),
			body: `{"agents":{"codex":{"extra_flags":"--sandbox danger-full-access"}}}`,
		},
		{
			name: "org endpoint",
			path: "/config/autonomous/orgs/org",
			body: `{"agents":{"codex":{"extra_flags":"--sandbox danger-full-access"}}}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			const tomlContent = "[ai]\nprimary = \"claude\"\n\n[autonomous]\nenabled = true\n"
			tomlPath := writeTempTOML(t, tomlContent)
			srv := setupServerWithToken(t, "test-token")
			srv.SetConfigPath(tomlPath)

			req := httptest.NewRequest(http.MethodPatch, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Heimdallm-Token", "test-token")
			w := httptest.NewRecorder()
			srv.Router().ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusBadRequest, w.Body.String())
			}
			got, err := os.ReadFile(tomlPath)
			if err != nil {
				t.Fatalf("read TOML after rejected PATCH: %v", err)
			}
			if string(got) != tomlContent {
				t.Fatalf("rejected PATCH changed TOML:\n%s", got)
			}
		})
	}
}

// TestHandlerGetConfig_ExposesPolling guards the [polling] section in the GET
// /config response. The Flutter UI and CLI Config tab read these keys, so a
// silent rename or re-nesting would break consumers.
func TestHandlerGetConfig_ExposesPolling(t *testing.T) {
	srv, _ := setupServer(t)
	srv.SetConfigFn(func() map[string]any {
		return map[string]any{
			"polling": map[string]any{
				"poll_interval":               "3m",
				"min_interval":                "1m",
				"max_interval":                "15m",
				"adaptive":                    true,
				"discovery_interval":          "5m",
				"tier3_interval":              "30s",
				"rate_limit_safety_threshold": int64(100),
				"use_etag":                    true,
				"use_graphql":                 false,
			},
		}
	})

	req := httptest.NewRequest("GET", "/config", nil)
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v (body: %s)", err, w.Body.String())
	}
	polling, ok := body["polling"].(map[string]any)
	if !ok {
		t.Fatalf("polling missing or wrong type: %T: %v", body["polling"], body["polling"])
	}

	checks := map[string]any{
		"poll_interval":               "3m",
		"min_interval":                "1m",
		"max_interval":                "15m",
		"adaptive":                    true,
		"discovery_interval":          "5m",
		"tier3_interval":              "30s",
		"rate_limit_safety_threshold": float64(100), // JSON numbers decode to float64
		"use_etag":                    true,
		"use_graphql":                 false,
	}
	for key, want := range checks {
		got := polling[key]
		if got != want {
			t.Errorf("polling[%q] = %v (%T), want %v (%T)", key, got, got, want, want)
		}
	}
}

// TestHandlePatchConfig_PollingSectionPersists verifies that PATCH /config
// with {"polling":{...}} round-trips to TOML correctly. Mirrors the pattern
// used by TestHandlePatchConfig and its siblings.
func TestHandlePatchConfig_PollingSectionPersists(t *testing.T) {
	tomlContent := "[ai]\nprimary = \"claude\"\n"
	tomlPath := writeTempTOML(t, tomlContent)

	srv := setupServerWithToken(t, "test-token")
	srv.SetConfigPath(tomlPath)

	body := `{"polling":{"adaptive":true,"max_interval":"30m"}}`
	req := httptest.NewRequest("PATCH", "/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}

	m, err := config.ReadTOMLMap(tomlPath)
	if err != nil {
		t.Fatalf("read TOML after PATCH: %v", err)
	}
	polling, ok := m["polling"].(map[string]any)
	if !ok {
		t.Fatalf("expected [polling] section in TOML, got %v", m)
	}
	if polling["adaptive"] != true {
		t.Errorf("adaptive = %v, want true", polling["adaptive"])
	}
	if polling["max_interval"] != "30m" {
		t.Errorf("max_interval = %v, want 30m", polling["max_interval"])
	}
}

// TestHandlePatchConfig_PollingIntAndBoolFields verifies that integer
// (rate_limit_safety_threshold) and pointer-bool (use_etag) fields in the
// [polling] section persist to TOML via PATCH /config.
func TestHandlePatchConfig_PollingIntAndBoolFields(t *testing.T) {
	tomlContent := "[ai]\nprimary = \"claude\"\n"
	tomlPath := writeTempTOML(t, tomlContent)

	srv := setupServerWithToken(t, "test-token")
	srv.SetConfigPath(tomlPath)

	body := `{"polling":{"rate_limit_safety_threshold":200,"use_etag":false}}`
	req := httptest.NewRequest("PATCH", "/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}

	m, err := config.ReadTOMLMap(tomlPath)
	if err != nil {
		t.Fatalf("read TOML after PATCH: %v", err)
	}
	polling, ok := m["polling"].(map[string]any)
	if !ok {
		t.Fatalf("expected [polling] section in TOML, got %v", m)
	}
	// TOML round-trip: JSON integers arrive as float64 after NormalizeNumbers
	// converts them to int64; TOML writes int64; reads back as int64.
	threshold := polling["rate_limit_safety_threshold"]
	if threshold != int64(200) {
		t.Errorf("rate_limit_safety_threshold = %v (%T), want int64(200)", threshold, threshold)
	}
	if polling["use_etag"] != false {
		t.Errorf("use_etag = %v, want false", polling["use_etag"])
	}
}

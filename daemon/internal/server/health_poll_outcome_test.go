package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/server"
)

func lastPollCheck(t *testing.T, srv *server.Server) (int, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	checks, _ := body["checks"].(map[string]any)
	last, ok := checks["last_poll"].(map[string]any)
	if !ok {
		t.Fatalf("last_poll check missing: %v", body)
	}
	return w.Code, last
}

// Failing polls are reported next to the liveness verdict, never in it: the
// loop is alive, so /health stays 200 and last_poll stays ok (#504, #897).
func TestHealth_ReportsFailingPollsWithoutChangingTheVerdict(t *testing.T) {
	srv, _ := setupServer(t)
	now := time.Now().UTC()
	success := now.Add(-time.Hour)
	failedAt := now.Add(-10 * time.Second)
	srv.SetHealthSnapshotFn(func() server.HealthSnapshot {
		return server.HealthSnapshot{
			LastPollAt:              failedAt,
			PollInterval:            time.Minute,
			LastPollSuccessAt:       success,
			ConsecutivePollFailures: 3,
			LastPollErrorKind:       "auth",
			LastPollErrorAt:         failedAt,
		}
	})

	code, last := lastPollCheck(t, srv)
	if code != http.StatusOK || last["ok"] != true {
		t.Fatalf("code %d, last_poll %v: failing polls must not change the liveness verdict", code, last)
	}
	if last["consecutive_failures"] != float64(3) || last["last_error_kind"] != "auth" {
		t.Errorf("last_poll = %v, want 3 failures of kind auth", last)
	}
	if last["last_success_at"] != success.Format(time.RFC3339) || last["last_error_at"] != failedAt.Format(time.RFC3339) {
		t.Errorf("last_poll = %v, want the success and error times", last)
	}
}

func TestHealth_HealthyPollsReportNoError(t *testing.T) {
	srv, _ := setupServer(t)
	at := time.Now().UTC().Add(-5 * time.Second)
	srv.SetHealthSnapshotFn(func() server.HealthSnapshot {
		return server.HealthSnapshot{LastPollAt: at, PollInterval: time.Minute, LastPollSuccessAt: at}
	})

	_, last := lastPollCheck(t, srv)
	if last["consecutive_failures"] != float64(0) || last["last_success_at"] != at.Format(time.RFC3339) {
		t.Errorf("last_poll = %v, want 0 failures and the success time", last)
	}
	for _, key := range []string{"last_error_kind", "last_error_at"} {
		if _, ok := last[key]; ok {
			t.Errorf("last_poll carries %s while polls succeed: %v", key, last)
		}
	}
}

// A daemon whose polls have failed since boot has no success to report, and
// still answers 200 while it has not polled at all.
func TestHealth_NoSuccessYet(t *testing.T) {
	srv, _ := setupServer(t)
	srv.SetHealthSnapshotFn(func() server.HealthSnapshot {
		return server.HealthSnapshot{PollInterval: time.Minute, ConsecutivePollFailures: 1, LastPollErrorKind: "error"}
	})

	code, last := lastPollCheck(t, srv)
	if code != http.StatusOK || last["status"] != "unknown" {
		t.Fatalf("code %d, last_poll %v, want the unchanged cold-start answer", code, last)
	}
	if _, ok := last["last_success_at"]; ok {
		t.Errorf("last_success_at present without a success: %v", last)
	}
	if _, ok := last["last_error_at"]; ok {
		t.Errorf("last_error_at present without an error time: %v", last)
	}
	if last["last_error_kind"] != "error" {
		t.Errorf("last_poll = %v, want kind error", last)
	}
}

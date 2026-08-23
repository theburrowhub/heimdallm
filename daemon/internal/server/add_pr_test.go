package server_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/server"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/heimdallm/daemon/internal/store"
)

// TestHandleAddPR_AddsRepoFetchesAndReviews is the end-to-end guard for the
// Activity "ADD" action: POST /prs/add must add the repo to the monitored
// list (and strip it from non_monitored), fetch+store the PR, and trigger a
// review.
func TestHandleAddPR_AddsRepoFetchesAndReviews(t *testing.T) {
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
	if err := os.WriteFile(cfgPath, []byte(
		"[ai]\nprimary = \"claude\"\n\n"+
			"[github]\nrepositories = [\"org/existing\"]\nnon_monitored = [\"org/repo\"]\n",
	), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	srv.SetConfigPath(cfgPath)
	srv.SetReloadFn(func() error { return nil })

	var addCalls int32
	srv.SetAddPRFn(func(repo string, number int) (*store.PR, error) {
		atomic.AddInt32(&addCalls, 1)
		if repo != "org/repo" || number != 123 {
			t.Errorf("addPRFn got %s#%d, want org/repo#123", repo, number)
		}
		id, err := s.UpsertPR(&store.PR{
			GithubID: 999, Repo: repo, Number: number, State: "open",
			UpdatedAt: time.Now(), FetchedAt: time.Now(),
		})
		if err != nil {
			return nil, err
		}
		return &store.PR{ID: id, Repo: repo, Number: number}, nil
	})

	var reviewedID int64
	done := make(chan struct{})
	srv.SetTriggerReviewFn(func(prID int64) error {
		atomic.StoreInt64(&reviewedID, prID)
		close(done)
		return errors.New("review trigger failed")
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/prs/add",
		strings.NewReader(`{"url":"https://github.com/org/repo/pull/123"}`))
	srv.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202, body=%s", rr.Code, rr.Body.String())
	}
	if atomic.LoadInt32(&addCalls) != 1 {
		t.Errorf("addPRFn called %d times, want 1", addCalls)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("triggerReviewFn was not called")
	}
	if atomic.LoadInt64(&reviewedID) == 0 {
		t.Error("triggerReviewFn called with zero prID")
	}

	// config.toml now monitors org/repo and no longer lists it as non_monitored.
	m, err := config.ReadTOMLMap(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	gh := m["github"].(map[string]any)
	if !tomlListContains(gh["repositories"], "org/repo") {
		t.Errorf("org/repo not added to repositories: %v", gh["repositories"])
	}
	if tomlListContains(gh["non_monitored"], "org/repo") {
		t.Errorf("org/repo still in non_monitored: %v", gh["non_monitored"])
	}
	if !tomlListContains(gh["repositories"], "org/existing") {
		t.Errorf("pre-existing repo was dropped: %v", gh["repositories"])
	}
}

// TestHandleAddPR_RejectsBadURL verifies invalid input is a 400 and never
// touches config or the review trigger.
func TestHandleAddPR_RejectsBadURL(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	broker := sse.NewBroker()
	broker.Start()
	t.Cleanup(broker.Stop)
	srv := server.NewWithOptions(s, broker, nil, "", server.Options{})
	srv.SetAddPRFn(func(string, int) (*store.PR, error) {
		t.Fatal("addPRFn must not be called on a bad URL")
		return nil, nil
	})
	srv.SetTriggerReviewFn(func(int64) error {
		t.Fatal("triggerReviewFn must not be called on a bad URL")
		return nil
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/prs/add",
		strings.NewReader(`{"url":"https://gitlab.com/o/r/pull/1"}`))
	srv.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400, body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandleAddPR_RejectsMalformedJSON(t *testing.T) {
	srv := server.NewWithOptions(nil, nil, nil, "", server.Options{})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/prs/add", strings.NewReader(`{"url":`))
	srv.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400, body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandleAddPR_RejectsWhenCallbacksAreMissing(t *testing.T) {
	srv := server.NewWithOptions(nil, nil, nil, "", server.Options{})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/prs/add",
		strings.NewReader(`{"url":"https://github.com/org/repo/pull/123"}`))
	srv.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d want 503, body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandleAddPR_ReportsConfigWriteFailure(t *testing.T) {
	srv := server.NewWithOptions(nil, nil, nil, "", server.Options{})
	srv.SetConfigPath(filepath.Join(t.TempDir(), "missing", "config.toml"))
	srv.SetAddPRFn(func(string, int) (*store.PR, error) {
		t.Fatal("addPRFn must not be called when config update fails")
		return nil, nil
	})
	srv.SetTriggerReviewFn(func(int64) error {
		t.Fatal("triggerReviewFn must not be called when config update fails")
		return nil
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/prs/add",
		strings.NewReader(`{"url":"https://github.com/org/repo/pull/123"}`))
	srv.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d want 500, body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandleAddPR_ReportsFetchFailure(t *testing.T) {
	srv := server.NewWithOptions(nil, nil, nil, "", server.Options{})
	srv.SetAddPRFn(func(repo string, number int) (*store.PR, error) {
		if repo != "org/repo" || number != 123 {
			t.Fatalf("addPRFn got %s#%d, want org/repo#123", repo, number)
		}
		return nil, errors.New("not found")
	})
	srv.SetTriggerReviewFn(func(int64) error {
		t.Fatal("triggerReviewFn must not be called when fetch fails")
		return nil
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/prs/add",
		strings.NewReader(`{"url":"https://github.com/org/repo/pull/123"}`))
	srv.Router().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status: got %d want 502, body=%s", rr.Code, rr.Body.String())
	}
}

func tomlListContains(raw any, target string) bool {
	list, ok := raw.([]any)
	if !ok {
		return false
	}
	for _, it := range list {
		if s, ok := it.(string); ok && s == target {
			return true
		}
	}
	return false
}

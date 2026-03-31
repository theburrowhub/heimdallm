package server_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/heimdallr/daemon/internal/server"
	"github.com/heimdallr/daemon/internal/sse"
	"github.com/heimdallr/daemon/internal/store"
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
	srv := server.New(s, broker, nil)
	return srv, s
}

func TestHandlerHealth(t *testing.T) {
	srv, _ := setupServer(t)
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("health: status %d", w.Code)
	}
	var body map[string]string
	json.NewDecoder(w.Body).Decode(&body)
	if body["status"] != "ok" {
		t.Errorf("health body: %v", body)
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

func TestHandlerGetConfig(t *testing.T) {
	srv, _ := setupServer(t)
	req := httptest.NewRequest("GET", "/config", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("get config: status %d", w.Code)
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

func itoa(n int64) string {
	return fmt.Sprintf("%d", n)
}

package server_test

import (
	"context"
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
	srv := server.New(s, broker, nil, "")
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
	return server.New(s, broker, nil, token)
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

	// Use a context with a short deadline so the polling loop exits.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req := httptest.NewRequest("GET", "/logs/stream", nil).WithContext(ctx)
	req.Header.Set("X-Heimdallr-Token", "secret-token")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	// Log file won't exist in CI/test env; endpoint should return 200 with SSE not-found message
	// and exit cleanly. If the log file DOES exist (dev machine), the handler exits when ctx is done.
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 with valid token, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("expected Content-Type text/event-stream, got %q", ct)
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
		req2.Header.Set("X-Heimdallr-Token", "secret-token")
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
		req.Header.Set("X-Heimdallr-Token", token)
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
	req.Header.Set("X-Heimdallr-Token", token)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429 when semaphore full, got %d", w.Code)
	}

	// Release goroutines
	close(gate)
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
			name:       "invalid poll_interval 2m",
			body:       `{"poll_interval":"2m"}`,
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
			req.Header.Set("X-Heimdallr-Token", "secret-token")
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

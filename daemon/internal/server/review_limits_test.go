package server_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleReviewLimits(t *testing.T) {
	srv, _ := setupServer(t)

	get := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		srv.Router().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/review-limits", nil))
		return w
	}

	if w := get(); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unwired: status %d, want 503", w.Code)
	}

	srv.SetReviewLimitsFn(func() (any, error) {
		return []map[string]any{{"kind": "global", "windows": []map[string]any{{"window": "hour", "used": 3, "limit": 10}}}}, nil
	})
	w := get()
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", w.Code)
	}
	var body []map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil || len(body) != 1 || body[0]["kind"] != "global" {
		t.Fatalf("body = %v (err %v)", body, err)
	}

	srv.SetReviewLimitsFn(func() (any, error) { return nil, errors.New("db locked") })
	if w := get(); w.Code != http.StatusInternalServerError {
		t.Fatalf("lookup error: status %d, want 500", w.Code)
	}
}

// Review budgets reveal repo/org names and review volume, so the endpoint is
// token-gated like /stats.
func TestHandleReviewLimits_RequiresAuth(t *testing.T) {
	srv := setupServerWithToken(t, "secret-token")
	srv.SetReviewLimitsFn(func() (any, error) { return []any{}, nil })

	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/review-limits", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("without token: status %d, want 401", w.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/review-limits", nil)
	req.Header.Set("X-Heimdallm-Token", "secret-token")
	w = httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("with token: status %d, want 200", w.Code)
	}
}

package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestHandlerAgentModels(t *testing.T) {
	srv, _ := setupServer(t)
	type contextKey struct{}
	const contextValue = "request-model-discovery"
	want := map[string][]string{
		"claude": {"claude-opus-4-1", "claude-sonnet-4-0"},
		"codex":  {"gpt-5.2-codex"},
	}
	var gotContextValue any
	srv.SetModelDiscoveryFn(func(ctx context.Context) map[string][]string {
		gotContextValue = ctx.Value(contextKey{})
		return want
	})

	req := httptest.NewRequest(http.MethodGet, "/agents/models", nil)
	req = req.WithContext(context.WithValue(req.Context(), contextKey{}, contextValue))
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /agents/models status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if gotContextValue != contextValue {
		t.Fatal("model discovery did not receive the request context")
	}
	var got map[string][]string
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("models = %#v, want %#v", got, want)
	}
}

func TestHandlerAgentModelsNotConfigured(t *testing.T) {
	srv, _ := setupServer(t)
	req := httptest.NewRequest(http.MethodGet, "/agents/models", nil)
	w := httptest.NewRecorder()

	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /agents/models status = %d, want 503; body: %s", w.Code, w.Body.String())
	}
}

func TestHandlerAgentModelsNormalizesNilCatalog(t *testing.T) {
	srv, _ := setupServer(t)
	srv.SetModelDiscoveryFn(func(context.Context) map[string][]string { return nil })
	req := httptest.NewRequest(http.MethodGet, "/agents/models", nil)
	w := httptest.NewRecorder()

	srv.Router().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET /agents/models status = %d, want 200", w.Code)
	}
	if got := w.Body.String(); got != "{}\n" {
		t.Fatalf("GET /agents/models body = %q, want empty object", got)
	}
}

func TestHandlerAgentModelsRequiresAuth(t *testing.T) {
	srv := setupServerWithToken(t, "secret-token")
	called := false
	srv.SetModelDiscoveryFn(func(context.Context) map[string][]string {
		called = true
		return map[string][]string{"codex": {"gpt-5.2-codex"}}
	})

	req := httptest.NewRequest(http.MethodGet, "/agents/models", nil)
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("GET /agents/models without token status = %d, want 401", w.Code)
	}
	if called {
		t.Fatal("model discovery was called before authentication")
	}

	req = httptest.NewRequest(http.MethodGet, "/agents/models", nil)
	req.Header.Set("X-Heimdallm-Token", "secret-token")
	w = httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /agents/models with token status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !called {
		t.Fatal("model discovery was not called after authentication")
	}
}

package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/heimdallm/daemon/internal/agentcatalog"
)

func fakeCatalog() *agentcatalog.Store {
	return agentcatalog.NewStore(agentcatalog.Detector{
		Home:    "/h",
		Resolve: func(id string) string { return map[string]string{"copilot": "/bin/copilot"}[id] },
		Stat:    func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
		Run: func(_ context.Context, _ string, args ...string) (string, error) {
			return "1.2.3", nil
		},
	})
}

func TestCLIAgentsEndpoints(t *testing.T) {
	srv, _ := setupServer(t)
	do := func(method, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		srv.Router().ServeHTTP(w, httptest.NewRequest(method, path, nil))
		return w
	}
	if w := do(http.MethodGet, "/cli-agents"); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unwired list: %d", w.Code)
	}
	if w := do(http.MethodPost, "/cli-agents/rescan"); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unwired rescan: %d", w.Code)
	}
	if w := do(http.MethodGet, "/cli-agents/copilot"); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unwired get: %d", w.Code)
	}

	srv.SetAgentCatalog(fakeCatalog())
	w := do(http.MethodGet, "/cli-agents")
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d", w.Code)
	}
	var body struct {
		Agents    []agentcatalog.Agent `json:"agents"`
		ScannedAt string               `json:"scanned_at"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil || len(body.Agents) != len(agentcatalog.Catalog) || body.ScannedAt == "" {
		t.Fatalf("list body = %+v (%v)", body, err)
	}
	if w := do(http.MethodPost, "/cli-agents/rescan"); w.Code != http.StatusOK {
		t.Fatalf("rescan: %d", w.Code)
	}
	w = do(http.MethodGet, "/cli-agents/copilot")
	var one agentcatalog.Agent
	if err := json.NewDecoder(w.Body).Decode(&one); err != nil || !one.Installed || one.Version != "1.2.3" {
		t.Fatalf("get copilot = %+v (%v)", one, err)
	}
	if w := do(http.MethodGet, "/cli-agents/nope"); w.Code != http.StatusNotFound {
		t.Fatalf("unknown agent: %d", w.Code)
	}
}

func TestCLIAgentsRequireAuth(t *testing.T) {
	srv := setupServerWithToken(t, "secret-token")
	srv.SetAgentCatalog(fakeCatalog())
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/cli-agents"},
		{http.MethodGet, "/cli-agents/copilot"},
		{http.MethodPost, "/cli-agents/rescan"},
	} {
		w := httptest.NewRecorder()
		srv.Router().ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without token: %d, want 401", tc.method, tc.path, w.Code)
		}
	}
}

type fakeKeyProvider struct {
	key, source string
	usageErr    error
}

func (f *fakeKeyProvider) SetKey(k string) error {
	if k == "bad key" {
		return errors.New("openrouter: API key must be printable ASCII without spaces")
	}
	f.key, f.source = k, "stored"
	return nil
}
func (f *fakeKeyProvider) ClearKey() error   { f.key, f.source = "", ""; return nil }
func (f *fakeKeyProvider) KeySource() string { return f.source }
func (f *fakeKeyProvider) Usage(context.Context) (any, error) {
	if f.usageErr != nil {
		return nil, f.usageErr
	}
	return map[string]any{"usage": 1.5, "limit": 10}, nil
}

func TestAgentKeyEndpoints(t *testing.T) {
	srv, _ := setupServer(t)
	srv.SetAgentCatalog(fakeCatalog())
	p := &fakeKeyProvider{}
	srv.SetAPIKeyProvider("openrouter", p)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		srv.Router().ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}
	if w := do(http.MethodGet, "/cli-agents/claude/key", ""); w.Code != http.StatusNotFound {
		t.Fatalf("agent without a key: %d", w.Code)
	}
	if w := do(http.MethodGet, "/cli-agents/openrouter/key", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"configured":false`) {
		t.Fatalf("get key = %d %s", w.Code, w.Body)
	}
	if w := do(http.MethodPut, "/cli-agents/openrouter/key", `{nope`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad JSON = %d", w.Code)
	}
	if w := do(http.MethodPut, "/cli-agents/openrouter/key", `{"api_key":"bad key"}`); w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), "bad key") {
		t.Fatalf("invalid key = %d %s (must not echo the value)", w.Code, w.Body)
	}
	w := do(http.MethodPut, "/cli-agents/openrouter/key", `{"api_key":"sk-or-secret"}`)
	if w.Code != 200 || strings.Contains(w.Body.String(), "sk-or-secret") || !strings.Contains(w.Body.String(), `"source":"stored"`) {
		t.Fatalf("put key = %d %s", w.Code, w.Body)
	}
	if p.key != "sk-or-secret" {
		t.Fatal("key not stored")
	}
	if w := do(http.MethodGet, "/cli-agents/openrouter/key", ""); strings.Contains(w.Body.String(), "sk-or-secret") {
		t.Fatal("GET must never return the key")
	}
	if w := do(http.MethodGet, "/cli-agents/openrouter/usage", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"usage":1.5`) {
		t.Fatalf("usage = %d %s", w.Code, w.Body)
	}
	p.usageErr = errors.New("401 from provider")
	if w := do(http.MethodGet, "/cli-agents/openrouter/usage", ""); w.Code != http.StatusBadGateway {
		t.Fatalf("usage error = %d", w.Code)
	}
	if w := do(http.MethodDelete, "/cli-agents/openrouter/key", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"configured":false`) {
		t.Fatalf("delete = %d %s", w.Code, w.Body)
	}
}

func TestAgentKeyEndpointsRequireAuth(t *testing.T) {
	srv := setupServerWithToken(t, "secret-token")
	srv.SetAPIKeyProvider("openrouter", &fakeKeyProvider{})
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		w := httptest.NewRecorder()
		srv.Router().ServeHTTP(w, httptest.NewRequest(m, "/cli-agents/openrouter/key", strings.NewReader(`{"api_key":"x"}`)))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s without token = %d", m, w.Code)
		}
	}
}

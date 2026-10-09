package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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

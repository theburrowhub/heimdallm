package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/heimdallm/daemon/internal/server"
)

// compat.json at the repository root is the single source of truth the release
// pipeline publishes (versions.json and every component release); the constant
// the daemon serves must never drift from it.
func TestAPIVersionMatchesCompatManifest(t *testing.T) {
	raw, err := os.ReadFile("../../../compat.json")
	if err != nil {
		t.Fatalf("read compat.json: %v", err)
	}
	var manifest struct {
		DaemonAPI *int `json:"daemon_api"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("decode compat.json: %v", err)
	}
	if manifest.DaemonAPI == nil {
		t.Fatal("compat.json has no daemon_api")
	}
	if *manifest.DaemonAPI != server.APIVersion {
		t.Fatalf("compat.json daemon_api = %d, server.APIVersion = %d", *manifest.DaemonAPI, server.APIVersion)
	}
}

func TestHealthReportsAPIVersion(t *testing.T) {
	for _, starting := range []bool{true, false} {
		srv := server.NewWithOptions(nil, nil, nil, "", server.Options{Version: "v-test"})
		if starting {
			srv.MarkStarting()
		}
		w := httptest.NewRecorder()
		srv.Router().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))
		var body map[string]any
		if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
			t.Fatalf("starting=%v: decode health: %v", starting, err)
		}
		if body["api_version"] != float64(server.APIVersion) {
			t.Fatalf("starting=%v: api_version = %v, want %d", starting, body["api_version"], server.APIVersion)
		}
	}
}

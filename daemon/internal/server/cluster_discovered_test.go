package server_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/heimdallm/daemon/internal/config"
)

// With cluster.discovery off — the default — the route still answers, so the
// GUI can say "discovery is switched off" instead of showing an empty list that
// looks like "nothing is out there".
func TestDiscoveredIsExplicitlyDisabledWhenOff(t *testing.T) {
	f := newHub(t, nil, config.RoutingConfig{})

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/cluster/discovered"},
		{http.MethodPost, "/cluster/discovered/scan"},
	} {
		rec := f.do(t, tc.method, tc.path, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s = %d, want 200", tc.method, tc.path, rec.Code)
		}
		body := decode(t, rec)
		if body["enabled"] != false {
			t.Errorf("%s %s: enabled = %v, want false", tc.method, tc.path, body["enabled"])
		}
		peers, ok := body["peers"].([]any)
		if !ok {
			t.Fatalf("%s %s: peers = %#v, want an array", tc.method, tc.path, body["peers"])
		}
		if len(peers) != 0 {
			t.Errorf("%s %s: got %d peers, want none", tc.method, tc.path, len(peers))
		}
		if _, present := body["last_scan"]; present {
			t.Errorf("%s %s: last_scan should be omitted before any scan", tc.method, tc.path)
		}
	}
}

// peers must be [] and never null: a client that has to distinguish the two is
// a client that will get it wrong.
func TestDiscoveredPeersIsAlwaysAnArray(t *testing.T) {
	f := newHub(t, nil, config.RoutingConfig{})
	rec := f.do(t, http.MethodGet, "/cluster/discovered", "")

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decoding %s: %v", rec.Body.String(), err)
	}
	if string(raw["peers"]) != "[]" {
		t.Fatalf("peers = %s, want []", raw["peers"])
	}
}

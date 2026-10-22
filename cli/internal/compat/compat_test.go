package compat

import (
	"encoding/json"
	"os"
	"testing"
)

// compat.json at the repository root is what the release pipeline publishes;
// the minimum compiled into the CLI must never drift from it.
func TestMinDaemonAPIMatchesCompatManifest(t *testing.T) {
	raw, err := os.ReadFile("../../../compat.json")
	if err != nil {
		t.Fatalf("read compat.json: %v", err)
	}
	var manifest struct {
		DaemonAPI    int            `json:"daemon_api"`
		MinDaemonAPI map[string]int `json:"min_daemon_api"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("decode compat.json: %v", err)
	}
	got, ok := manifest.MinDaemonAPI["cli"]
	if !ok {
		t.Fatal("compat.json has no min_daemon_api.cli")
	}
	if got != MinDaemonAPI {
		t.Fatalf("compat.json min_daemon_api.cli = %d, compat.MinDaemonAPI = %d", got, MinDaemonAPI)
	}
	if MinDaemonAPI > manifest.DaemonAPI {
		t.Fatalf("the CLI needs daemon API %d but this daemon serves %d", MinDaemonAPI, manifest.DaemonAPI)
	}
}

func TestEffectiveDaemonAPI(t *testing.T) {
	for reported, want := range map[int]int{0: BaselineDaemonAPI, -3: BaselineDaemonAPI, 1: 1, 4: 4} {
		if got := EffectiveDaemonAPI(reported); got != want {
			t.Errorf("EffectiveDaemonAPI(%d) = %d, want %d", reported, got, want)
		}
	}
}

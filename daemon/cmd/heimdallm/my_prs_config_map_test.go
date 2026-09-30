package main

import (
	"testing"

	"github.com/heimdallm/daemon/internal/config"
)

func boolRef(b bool) *bool { return &b }

// Same trap as merge_tracking: without the projection the settings screen
// reads [my_prs] back as defaults and the toggles reset themselves.
func TestMyPRsConfigMap_ResolvesDefaults(t *testing.T) {
	got := myPRsConfigMap(&config.Config{MyPRs: config.MyPRsConfig{StaleAfter: "3d", DigestTime: "10:00"}})
	want := map[string]any{
		"enabled": true, "include_assigned": true, "stale_after": "3d",
		"notify_transitions": false, "digest_enabled": true, "digest_time": "10:00",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("unexpected keys: %v", got)
	}
}

func TestMyPRsConfigMap_CarriesExplicitValues(t *testing.T) {
	got := myPRsConfigMap(&config.Config{MyPRs: config.MyPRsConfig{
		Enabled: boolRef(false), IncludeAssigned: boolRef(false), DigestEnabled: boolRef(false),
		StaleAfter: "12h", NotifyTransitions: true, DigestTime: "08:30",
	}})
	for k, v := range map[string]any{
		"enabled": false, "include_assigned": false, "digest_enabled": false,
		"stale_after": "12h", "notify_transitions": true, "digest_time": "08:30",
	} {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
}

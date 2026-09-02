package main

// GET /config never emitted the [cluster] section. The app could PATCH
// cluster.role successfully, the daemon would persist and honour it, and the
// settings screen would still read the role back as "standalone" on the next
// load — the setting appeared to reset itself.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/heimdallm/daemon/internal/config"
)

func TestClusterConfigMap_CarriesRoleAndIdentityBack(t *testing.T) {
	got := clusterConfigMap(config.ClusterConfig{
		Role:            "hub",
		InstanceID:      "hub-1",
		InstanceName:    "Local hub",
		DefaultInstance: "hub-1",
		ProbeInterval:   "30s",
		Routing:         config.RoutingConfig{Mode: config.ModeDispatch},
	})

	want := map[string]any{
		"role":             "hub",
		"instance_id":      "hub-1",
		"instance_name":    "Local hub",
		"default_instance": "hub-1",
		"probe_interval":   "30s",
		"routing_mode":     "dispatch",
	}
	for key, expected := range want {
		if got[key] != expected {
			t.Errorf("%s = %v, want %v", key, got[key], expected)
		}
	}
	if len(got) != len(want) {
		t.Errorf("projected %d fields, want %d: %v", len(got), len(want), got)
	}
}

// The Flutter dropdown's `value` must always be one of its own `items`, so the
// zero-value TOML representation of "not configured" has to resolve to the
// same string the dropdown offers, not the empty string.
func TestClusterConfigMap_ResolvesEmptyRoleToStandalone(t *testing.T) {
	got := clusterConfigMap(config.ClusterConfig{})

	if got["role"] != config.RoleStandalone {
		t.Errorf("role = %v, want %q", got["role"], config.RoleStandalone)
	}
	if got["routing_mode"] != config.ModeAssignment {
		t.Errorf("routing_mode = %v, want %q", got["routing_mode"], config.ModeAssignment)
	}
}

// Role comparisons elsewhere (e.g. the reload-time warning) are case-folded,
// so the projection should normalise rather than assume the operator typed
// the TOML value in lowercase.
func TestClusterConfigMap_NormalisesCase(t *testing.T) {
	got := clusterConfigMap(config.ClusterConfig{Role: "  Hub  ", Routing: config.RoutingConfig{Mode: "DISPATCH"}})

	if got["role"] != "hub" {
		t.Errorf("role = %v, want %q", got["role"], "hub")
	}
	if got["routing_mode"] != "dispatch" {
		t.Errorf("routing_mode = %v, want %q", got["routing_mode"], "dispatch")
	}
}

// The projection must never leak a registered instance's inline secret, in the
// map itself or through any straightforward JSON re-encoding of it.
func TestClusterConfigMap_NeverLeaksInstanceTokens(t *testing.T) {
	got := clusterConfigMap(config.ClusterConfig{
		Role: "hub",
		Instances: map[string]config.InstanceConfig{
			"srv-a": {Name: "Server A", BaseURL: "https://10.0.0.1:7842", Token: "super-secret-token"},
		},
	})

	if _, ok := got["instances"]; ok {
		t.Fatalf("instances must not be projected at all, got %v", got)
	}

	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "super-secret-token") {
		t.Fatalf("projection leaked the instance token: %s", b)
	}
}

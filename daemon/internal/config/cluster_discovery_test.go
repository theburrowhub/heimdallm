package config

import (
	"strings"
	"testing"
)

func TestValidateClusterDiscovery(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{"empty means off", "", false},
		{"off", "off", false},
		{"mdns", "mdns", false},
		{"case folded", "MDNS", false},
		{"padded", "  mdns  ", false},
		{"unknown mode", "bonjour", true},
		{"typo", "mdsn", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Config{Cluster: ClusterConfig{Role: RoleHub, Discovery: tt.value}}
			err := c.validateCluster()
			if tt.wantErr && err == nil {
				t.Fatalf("discovery %q was accepted, want an error", tt.value)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("discovery %q was rejected: %v", tt.value, err)
			}
			if tt.wantErr && !strings.Contains(err.Error(), "cluster.discovery") {
				t.Fatalf("error should name the key, got %v", err)
			}
		})
	}
}

func TestDiscoveryEnabled(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{"", false},
		{"off", false},
		{"mdns", true},
		{"MDNS", true},
		{" mdns ", true},
	}
	for _, tt := range tests {
		c := &Config{Cluster: ClusterConfig{Discovery: tt.value}}
		if got := c.DiscoveryEnabled(); got != tt.want {
			t.Errorf("DiscoveryEnabled() with %q = %v, want %v", tt.value, got, tt.want)
		}
	}
}

// A daemon told only to advertise itself is in nobody's registry yet, but it
// still needs an identity to announce. ClusterEnabled gates that identity, so
// discovery has to switch it on by itself.
func TestDiscoveryAloneEnablesTheCluster(t *testing.T) {
	off := &Config{}
	if off.ClusterEnabled() {
		t.Fatal("a bare config should not be cluster-enabled")
	}

	on := &Config{Cluster: ClusterConfig{Discovery: DiscoveryMDNS}}
	if !on.ClusterEnabled() {
		t.Fatal("discovery = mdns should make the daemon cluster-enabled")
	}

	explicitlyOff := &Config{Cluster: ClusterConfig{Discovery: DiscoveryOff}}
	if explicitlyOff.ClusterEnabled() {
		t.Fatal("discovery = off should leave the daemon inert")
	}
}

// The whole feature must stay invisible on a config that never opts in.
func TestDiscoveryDefaultsToOffAndIsNotWrittenBack(t *testing.T) {
	c := &Config{Cluster: ClusterConfig{Role: RoleHub}}
	c.applyClusterDefaults()
	if c.Cluster.Discovery != "" {
		t.Fatalf("applyClusterDefaults wrote discovery = %q; it should stay empty so a "+
			"rewritten config.toml does not grow a key for an unused feature", c.Cluster.Discovery)
	}
	if c.DiscoveryEnabled() {
		t.Fatal("an unset discovery key should read as off")
	}
}

func TestClusterDiscoveryEnvOverride(t *testing.T) {
	t.Setenv("HEIMDALLM_CLUSTER_DISCOVERY", "mdns")
	c := &Config{}
	c.applyEnvOverrides()
	if c.Cluster.Discovery != "mdns" {
		t.Fatalf("discovery = %q, want mdns", c.Cluster.Discovery)
	}
	if !c.DiscoveryEnabled() {
		t.Fatal("the env override should enable discovery")
	}
}

// The container warning tells the operator to set
// HEIMDALLM_CLUSTER_DISCOVERY=off. A log line whose whole purpose is to be
// actionable must name a knob that actually works, including when the value
// arrives in the case or spacing a shell happens to give it.
func TestClusterDiscoveryEnvOverrideCanTurnDiscoveryOff(t *testing.T) {
	for _, value := range []string{"off", "OFF", " off ", "Off"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("HEIMDALLM_CLUSTER_DISCOVERY", value)
			c := &Config{Cluster: ClusterConfig{Role: RoleHub, Discovery: DiscoveryMDNS}}
			c.applyEnvOverrides()

			if c.DiscoveryEnabled() {
				t.Fatalf("HEIMDALLM_CLUSTER_DISCOVERY=%q did not turn discovery off", value)
			}
			if err := c.validateCluster(); err != nil {
				t.Fatalf("the value the warning recommends failed validation: %v", err)
			}
		})
	}
}

// And the other direction, in the shapes a shell or compose file produces.
func TestClusterDiscoveryEnvOverrideAcceptsMixedCaseMdns(t *testing.T) {
	for _, value := range []string{"mdns", "MDNS", " mdns "} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("HEIMDALLM_CLUSTER_DISCOVERY", value)
			c := &Config{}
			c.applyEnvOverrides()

			if !c.DiscoveryEnabled() {
				t.Fatalf("HEIMDALLM_CLUSTER_DISCOVERY=%q did not enable discovery", value)
			}
			if err := c.validateCluster(); err != nil {
				t.Fatalf("validation rejected %q: %v", value, err)
			}
		})
	}
}

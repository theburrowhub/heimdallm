package config

import (
	"bytes"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// The daemon rewrites config.toml on first boot and on every scoped PATCH. A
// plain single-daemon install must not end up carrying an inert [cluster] table
// for a feature it is not using — hence `omitempty` on the field.
func TestClusterSectionOmittedWhenUnused(t *testing.T) {
	c := &Config{}
	c.AI.Primary = "claude"
	c.applyDefaults()

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if strings.Contains(buf.String(), "[cluster") {
		t.Errorf("a cluster-free config emitted a [cluster] table:\n%s", buf.String())
	}
}

func TestClusterSectionWrittenWhenUsed(t *testing.T) {
	c := &Config{}
	c.AI.Primary = "claude"
	c.Cluster.Role = RoleHub
	c.Cluster.InstanceID = "hub-1"
	c.Cluster.Instances = map[string]InstanceConfig{
		"srv-a": {BaseURL: "http://10.0.0.11:7842", Token: "t"},
	}
	c.applyDefaults()

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		t.Fatalf("encode: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"[cluster]", "hub-1", "srv-a", "10.0.0.11"} {
		if !strings.Contains(out, want) {
			t.Errorf("encoded config is missing %q:\n%s", want, out)
		}
	}

	// And it must load back: a section the daemon writes but cannot read would
	// silently drop the registry on the next boot.
	var round Config
	if _, err := toml.Decode(out, &round); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if round.Cluster.InstanceID != "hub-1" || len(round.Cluster.Instances) != 1 {
		t.Errorf("round trip lost the cluster config: %+v", round.Cluster)
	}
}

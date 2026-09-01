package instances

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heimdallm/daemon/internal/config"
)

func boolPtr(b bool) *bool { return &b }

// cfgWith builds a validated Config carrying the given cluster settings.
func cfgWith(role, selfID, defaultInstance string, insts map[string]config.InstanceConfig, routing config.RoutingConfig) *config.Config {
	c := &config.Config{}
	c.AI.Primary = "claude"
	c.Cluster.Role = role
	c.Cluster.InstanceID = selfID
	c.Cluster.DefaultInstance = defaultInstance
	c.Cluster.Instances = insts
	c.Cluster.Routing = routing
	return c
}

func TestNewRegistryEmpty(t *testing.T) {
	// The no-[cluster] case: an empty registry must be safe to use, not nil.
	r := NewRegistry(cfgWith("", "", "", nil, config.RoutingConfig{}))
	if !r.Empty() {
		t.Error("Empty() = false on a config with no instances")
	}
	if r.Len() != 0 {
		t.Errorf("Len() = %d, want 0", r.Len())
	}
	if got := r.List(); len(got) != 0 {
		t.Errorf("List() = %v, want empty", got)
	}
	if got := r.Enabled(); len(got) != 0 {
		t.Errorf("Enabled() = %v, want empty", got)
	}
	if _, ok := r.Self(); ok {
		t.Error("Self() reported an instance on an empty registry")
	}
	if _, err := r.Require("ghost"); err == nil {
		t.Error("Require() on an empty registry should error")
	}
}

func TestNewRegistryNilConfig(t *testing.T) {
	r := NewRegistry(nil)
	if r == nil || !r.Empty() {
		t.Fatal("NewRegistry(nil) must return a usable empty registry")
	}
}

func TestNewRegistryResolvesAndSorts(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "api_token")
	if err := os.WriteFile(tokenFile, []byte("file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HEIMDALLM_TEST_REG_TOKEN", "env-token")

	cfg := cfgWith(config.RoleHub, "hub-1", "hub-1", map[string]config.InstanceConfig{
		"zulu":  {Name: "Zulu", BaseURL: "http://z:7842/", Token: "z-token"},
		"hub-1": {Name: "Hub", BaseURL: "http://127.0.0.1:7842", TokenFile: tokenFile, Labels: []string{"macos", "local"}},
		"srv-a": {BaseURL: "http://a:7842", TokenEnv: "HEIMDALLM_TEST_REG_TOKEN"},
	}, config.RoutingConfig{})

	r := NewRegistry(cfg)
	if r.Len() != 3 {
		t.Fatalf("Len() = %d, want 3", r.Len())
	}

	ids := make([]string, 0, 3)
	for _, inst := range r.List() {
		ids = append(ids, inst.ID)
	}
	if got := strings.Join(ids, ","); got != "hub-1,srv-a,zulu" {
		t.Errorf("List() order = %q, want sorted \"hub-1,srv-a,zulu\"", got)
	}

	hub, ok := r.Get("hub-1")
	if !ok {
		t.Fatal("Get(hub-1) not found")
	}
	if hub.Token != "file-token" {
		t.Errorf("hub token = %q, want file-token", hub.Token)
	}
	if !hub.Self {
		t.Error("hub-1 should be marked Self")
	}
	if len(hub.Labels) != 2 {
		t.Errorf("hub labels = %v, want 2 entries", hub.Labels)
	}

	// A trailing slash on base_url would produce "//prs" once a path is
	// appended, which some proxies normalise and others do not.
	zulu, _ := r.Get("zulu")
	if zulu.BaseURL != "http://z:7842" {
		t.Errorf("zulu base URL = %q, want the trailing slash trimmed", zulu.BaseURL)
	}
	if zulu.Self {
		t.Error("zulu must not be marked Self")
	}

	srv, _ := r.Get("srv-a")
	if srv.Token != "env-token" {
		t.Errorf("srv-a token = %q, want env-token", srv.Token)
	}
	// Name falls back to the id so the UI never shows a blank label.
	if srv.Name != "srv-a" {
		t.Errorf("srv-a name = %q, want the id as fallback", srv.Name)
	}

	self, ok := r.Self()
	if !ok || self.ID != "hub-1" {
		t.Errorf("Self() = %+v, %v; want hub-1", self, ok)
	}
	if r.SelfID() != "hub-1" {
		t.Errorf("SelfID() = %q, want hub-1", r.SelfID())
	}
}

// A token that cannot be resolved must not stop the hub from booting: it is
// recorded on the entry, excluded from Enabled, and still visible in List so
// the operator can see what is broken.
func TestNewRegistryTokenFailureIsNotFatal(t *testing.T) {
	cfg := cfgWith(config.RoleHub, "good", "", map[string]config.InstanceConfig{
		"good": {BaseURL: "http://g:7842", Token: "t"},
		"bad":  {BaseURL: "http://b:7842", TokenEnv: "HEIMDALLM_TEST_DEFINITELY_UNSET_XYZ"},
	}, config.RoutingConfig{})

	r := NewRegistry(cfg)
	if r.Len() != 2 {
		t.Fatalf("Len() = %d, want both instances listed", r.Len())
	}
	bad, _ := r.Get("bad")
	if bad.TokenErr == nil {
		t.Error("bad.TokenErr = nil, want the resolution failure recorded")
	}
	if bad.Usable() {
		t.Error("an instance with an unresolvable token must not be Usable")
	}
	if got := r.EnabledIDs(); strings.Join(got, ",") != "good" {
		t.Errorf("EnabledIDs() = %v, want only [good]", got)
	}
}

func TestRegistryEnabledExcludesDisabled(t *testing.T) {
	cfg := cfgWith("", "", "", map[string]config.InstanceConfig{
		"on":  {BaseURL: "http://on:7842", Token: "t"},
		"off": {BaseURL: "http://off:7842", Token: "t", Enabled: boolPtr(false)},
	}, config.RoutingConfig{})
	r := NewRegistry(cfg)
	if got := r.EnabledIDs(); strings.Join(got, ",") != "on" {
		t.Errorf("EnabledIDs() = %v, want only [on]", got)
	}
	// Disabled instances stay listed so the GUI can offer to re-enable them.
	if r.Len() != 2 {
		t.Errorf("Len() = %d, want 2", r.Len())
	}
}

func TestRegistryRequire(t *testing.T) {
	cfg := cfgWith("", "", "", map[string]config.InstanceConfig{
		"a": {BaseURL: "http://a:7842", Token: "t"},
	}, config.RoutingConfig{})
	r := NewRegistry(cfg)
	if _, err := r.Require("a"); err != nil {
		t.Errorf("Require(a) = %v, want nil", err)
	}
	if _, err := r.Require("ghost"); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("Require(ghost) error = %v, want it to name the missing id", err)
	}
}

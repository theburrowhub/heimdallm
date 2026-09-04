package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// baseValidConfig returns the minimum Config that passes Validate, so cluster
// tests only have to express what they are actually testing.
func baseValidConfig() *Config {
	c := &Config{}
	c.AI.Primary = "claude"
	c.applyDefaults()
	return c
}

// Nothing is filled in for a daemon that is not clustered, so a config.toml the
// daemon rewrites does not grow values for a feature nobody is using.
func TestClusterDefaultsAreNotAppliedWhenUnused(t *testing.T) {
	c := baseValidConfig()
	if c.Cluster.Routing.Mode != "" {
		t.Errorf("routing mode = %q, want it left empty", c.Cluster.Routing.Mode)
	}
	if c.Cluster.ProbeInterval != "" {
		t.Errorf("probe interval = %q, want it left empty", c.Cluster.ProbeInterval)
	}
	if c.Cluster.Role != "" {
		t.Errorf("role = %q, want empty", c.Cluster.Role)
	}
}

func TestClusterDefaultsAppliedOnceClusteringIsOn(t *testing.T) {
	c := &Config{}
	c.AI.Primary = "claude"
	c.Cluster.Role = RoleHub
	c.applyDefaults()

	if c.Cluster.Routing.Mode != ModeAssignment {
		t.Errorf("routing mode = %q, want %q", c.Cluster.Routing.Mode, ModeAssignment)
	}
	if c.Cluster.ProbeInterval != DefaultClusterProbeInterval {
		t.Errorf("probe interval = %q, want %q", c.Cluster.ProbeInterval, DefaultClusterProbeInterval)
	}
}

// A config that never mentions [cluster] must look exactly like a
// single-daemon install: nothing enabled, no hub, no instances. This is the
// no-regression invariant the whole feature rests on.
func TestClusterInertByDefault(t *testing.T) {
	c := baseValidConfig()
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() on a cluster-free config: %v", err)
	}
	if c.ClusterEnabled() {
		t.Error("ClusterEnabled() = true on a config with no [cluster]")
	}
	if c.IsHub() {
		t.Error("IsHub() = true on a config with no [cluster]")
	}
	if got := c.EnabledInstanceIDs(); len(got) != 0 {
		t.Errorf("EnabledInstanceIDs() = %v, want empty", got)
	}
	if c.Cluster.Routing.Configured() {
		t.Error("Routing.Configured() = true with no rules")
	}
}

func TestClusterEnabled(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*Config)
		want bool
	}{
		{"empty", func(*Config) {}, false},
		{"explicit standalone", func(c *Config) { c.Cluster.Role = RoleStandalone }, false},
		{"standalone mixed case", func(c *Config) { c.Cluster.Role = "StandAlone" }, false},
		{"hub role", func(c *Config) { c.Cluster.Role = RoleHub }, true},
		{"worker role", func(c *Config) { c.Cluster.Role = RoleWorker }, true},
		{"registry only", func(c *Config) {
			c.Cluster.Instances = map[string]InstanceConfig{"a": {BaseURL: "http://a:7842", Token: "t"}}
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := baseValidConfig()
			tt.mut(c)
			if got := c.ClusterEnabled(); got != tt.want {
				t.Errorf("ClusterEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsHubCaseInsensitive(t *testing.T) {
	c := baseValidConfig()
	c.Cluster.Role = "HUB"
	if !c.IsHub() {
		t.Error("IsHub() = false for role \"HUB\"")
	}
}

func TestInstanceIsEnabled(t *testing.T) {
	// Absent means enabled: registering an instance and having it silently
	// ignored would be the wrong default.
	if !(InstanceConfig{}).IsEnabled() {
		t.Error("zero InstanceConfig should be enabled")
	}
	if !(InstanceConfig{Enabled: boolPtr(true)}).IsEnabled() {
		t.Error("explicit true should be enabled")
	}
	if (InstanceConfig{Enabled: boolPtr(false)}).IsEnabled() {
		t.Error("explicit false should be disabled")
	}
}

func TestEnabledInstanceIDsSortedAndFiltered(t *testing.T) {
	c := baseValidConfig()
	c.Cluster.Instances = map[string]InstanceConfig{
		"zulu":  {BaseURL: "http://z:7842", Token: "t"},
		"alpha": {BaseURL: "http://a:7842", Token: "t"},
		"mike":  {BaseURL: "http://m:7842", Token: "t", Enabled: boolPtr(false)},
		"bravo": {BaseURL: "http://b:7842", Token: "t"},
	}
	got := c.EnabledInstanceIDs()
	want := []string{"alpha", "bravo", "zulu"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("EnabledInstanceIDs() = %v, want %v (sorted, disabled excluded)", got, want)
	}
}

func TestResolvedInstanceNameFallsBackToID(t *testing.T) {
	c := baseValidConfig()
	c.Cluster.Instances = map[string]InstanceConfig{
		"named":   {Name: "Prod box", BaseURL: "http://a:7842", Token: "t"},
		"unnamed": {BaseURL: "http://b:7842", Token: "t"},
		"blank":   {Name: "   ", BaseURL: "http://c:7842", Token: "t"},
	}
	if got := c.ResolvedInstanceName("named"); got != "Prod box" {
		t.Errorf("named = %q, want %q", got, "Prod box")
	}
	if got := c.ResolvedInstanceName("unnamed"); got != "unnamed" {
		t.Errorf("unnamed = %q, want the id", got)
	}
	if got := c.ResolvedInstanceName("blank"); got != "blank" {
		t.Errorf("whitespace name = %q, want the id", got)
	}
	if got := c.ResolvedInstanceName("missing"); got != "missing" {
		t.Errorf("missing = %q, want the id", got)
	}
}

func TestResolveInstanceToken(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "api_token")
	if err := os.WriteFile(tokenFile, []byte("  file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	emptyFile := filepath.Join(dir, "empty_token")
	if err := os.WriteFile(emptyFile, []byte("\n  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HEIMDALLM_TEST_INSTANCE_TOKEN", " env-token ")
	t.Setenv("HEIMDALLM_TEST_EMPTY_TOKEN", "   ")

	c := baseValidConfig()
	c.Cluster.Instances = map[string]InstanceConfig{
		"inline":     {BaseURL: "http://a:7842", Token: "inline-token"},
		"fromenv":    {BaseURL: "http://b:7842", TokenEnv: "HEIMDALLM_TEST_INSTANCE_TOKEN"},
		"fromfile":   {BaseURL: "http://c:7842", TokenFile: tokenFile},
		"emptyenv":   {BaseURL: "http://d:7842", TokenEnv: "HEIMDALLM_TEST_EMPTY_TOKEN"},
		"unsetenv":   {BaseURL: "http://e:7842", TokenEnv: "HEIMDALLM_TEST_DEFINITELY_UNSET"},
		"emptyfile":  {BaseURL: "http://f:7842", TokenFile: emptyFile},
		"nosuchfile": {BaseURL: "http://g:7842", TokenFile: filepath.Join(dir, "nope")},
		"nosource":   {BaseURL: "http://h:7842"},
		"relative":   {BaseURL: "http://i:7842", TokenFile: "relative/api_token"},
	}

	t.Run("sources", func(t *testing.T) {
		for id, want := range map[string]string{
			"inline":   "inline-token",
			"fromenv":  "env-token", // trimmed
			"fromfile": "file-token",
		} {
			got, err := c.ResolveInstanceToken(id)
			if err != nil {
				t.Errorf("%s: unexpected error %v", id, err)
				continue
			}
			if got != want {
				t.Errorf("%s = %q, want %q", id, got, want)
			}
		}
	})

	t.Run("errors", func(t *testing.T) {
		// Each of these must be an error rather than an empty token: talking
		// to an instance with no credentials would surface much later as a
		// confusing 401 from a machine the operator did not suspect.
		for _, id := range []string{"emptyenv", "unsetenv", "emptyfile", "nosuchfile", "nosource", "relative", "missing"} {
			if got, err := c.ResolveInstanceToken(id); err == nil {
				t.Errorf("%s: expected an error, got token %q", id, got)
			}
		}
	})
}

func TestValidateInstanceID(t *testing.T) {
	valid := []string{"a", "A1", "hub-1", "srv_a", "s" + strings.Repeat("x", 62)}
	for _, id := range valid {
		if err := ValidateInstanceID(id); err != nil {
			t.Errorf("ValidateInstanceID(%q) = %v, want nil", id, err)
		}
	}
	// Rejected shapes are the ones that would be dangerous or ambiguous once
	// interpolated into /instances/{id}/proxy/*.
	invalid := []string{
		"", "-lead", "_lead", "has space", "has/slash", "has.dot", "has%2e",
		"..", "üñî", "s" + strings.Repeat("x", 63),
	}
	for _, id := range invalid {
		if err := ValidateInstanceID(id); err == nil {
			t.Errorf("ValidateInstanceID(%q) = nil, want an error", id)
		}
	}
}

func TestValidateInstanceBaseURL(t *testing.T) {
	valid := []string{
		"http://127.0.0.1:7842",
		"https://heimdallm.internal",
		"http://10.0.0.11:7842/prefix",
	}
	for _, raw := range valid {
		if err := ValidateInstanceBaseURL(raw); err != nil {
			t.Errorf("ValidateInstanceBaseURL(%q) = %v, want nil", raw, err)
		}
	}
	invalid := map[string]string{
		"empty":       "",
		"whitespace":  "   ",
		"no scheme":   "127.0.0.1:7842",
		"file":        "file:///etc/passwd",
		"ftp":         "ftp://host",
		"no host":     "http://",
		"credentials": "http://user:pass@host:7842",
		"query":       "http://host:7842?x=1",
		"fragment":    "http://host:7842#frag",
	}
	for name, raw := range invalid {
		if err := ValidateInstanceBaseURL(raw); err == nil {
			t.Errorf("%s: ValidateInstanceBaseURL(%q) = nil, want an error", name, raw)
		}
	}
}

func TestValidateClusterRejectsBadConfig(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*Config)
		want string
	}{
		{
			name: "unknown role",
			mut:  func(c *Config) { c.Cluster.Role = "leader" },
			want: "cluster.role",
		},
		{
			name: "bad instance id",
			mut: func(c *Config) {
				c.Cluster.Instances = map[string]InstanceConfig{"bad id": {BaseURL: "http://a:7842", Token: "t"}}
			},
			want: "cluster.instances key",
		},
		{
			name: "bad base url",
			mut: func(c *Config) {
				c.Cluster.Instances = map[string]InstanceConfig{"a": {BaseURL: "nope", Token: "t"}}
			},
			want: "base_url",
		},
		{
			name: "no token source",
			mut: func(c *Config) {
				c.Cluster.Instances = map[string]InstanceConfig{"a": {BaseURL: "http://a:7842"}}
			},
			want: "token",
		},
		{
			name: "two token sources",
			mut: func(c *Config) {
				c.Cluster.Instances = map[string]InstanceConfig{
					"a": {BaseURL: "http://a:7842", Token: "t", TokenEnv: "X"},
				}
			},
			want: "more than one",
		},
		{
			name: "bad instance_id",
			mut:  func(c *Config) { c.Cluster.InstanceID = "has/slash" },
			want: "cluster.instance_id",
		},
		{
			name: "bad probe interval",
			mut:  func(c *Config) { c.Cluster.ProbeInterval = "soon" },
			want: "probe_interval",
		},
		{
			name: "negative probe interval",
			mut:  func(c *Config) { c.Cluster.ProbeInterval = "-5s" },
			want: "probe_interval",
		},
		{
			name: "bad routing mode",
			mut:  func(c *Config) { c.Cluster.Routing.Mode = "broadcast" },
			want: "routing.mode",
		},
		{
			name: "bad round robin op",
			mut:  func(c *Config) { c.Cluster.Routing.RoundRobinOps = []string{"review", "deploy"} },
			want: "round_robin_ops",
		},
		{
			name: "default_instance not registered",
			mut: func(c *Config) {
				c.Cluster.Instances = map[string]InstanceConfig{"a": {BaseURL: "http://a:7842", Token: "t"}}
				c.Cluster.DefaultInstance = "ghost"
			},
			want: "cluster.default_instance references unknown instance",
		},
		{
			name: "pool member not registered",
			mut: func(c *Config) {
				c.Cluster.Instances = map[string]InstanceConfig{"a": {BaseURL: "http://a:7842", Token: "t"}}
				c.Cluster.Routing.RoundRobinPool = []string{"a", "ghost"}
			},
			want: "round_robin_pool references unknown instance",
		},
		{
			name: "org rule to unknown instance",
			mut: func(c *Config) {
				c.Cluster.Instances = map[string]InstanceConfig{"a": {BaseURL: "http://a:7842", Token: "t"}}
				c.Cluster.Routing.Orgs = map[string]string{"acme": "ghost"}
			},
			want: "references unknown instance",
		},
		{
			name: "repo rule to unknown instance",
			mut: func(c *Config) {
				c.Cluster.Instances = map[string]InstanceConfig{"a": {BaseURL: "http://a:7842", Token: "t"}}
				c.Cluster.Routing.Repos = map[string]string{"acme/tools": "ghost"}
			},
			want: "references unknown instance",
		},
		{
			name: "bad org slug in routing",
			mut: func(c *Config) {
				c.Cluster.Instances = map[string]InstanceConfig{"a": {BaseURL: "http://a:7842", Token: "t"}}
				c.Cluster.Routing.Orgs = map[string]string{"not a slug": "a"}
			},
			want: "cluster.routing.orgs key",
		},
		{
			name: "bad repo slug in routing",
			mut: func(c *Config) {
				c.Cluster.Instances = map[string]InstanceConfig{"a": {BaseURL: "http://a:7842", Token: "t"}}
				c.Cluster.Routing.Repos = map[string]string{"../escape": "a"}
			},
			want: "cluster.routing.repos key",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := baseValidConfig()
			tt.mut(c)
			err := c.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want an error containing %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Validate() = %q, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestValidateClusterAcceptsCompleteConfig(t *testing.T) {
	c := baseValidConfig()
	c.Cluster.Role = RoleHub
	c.Cluster.InstanceID = "hub-1"
	c.Cluster.InstanceName = "Local hub"
	c.Cluster.DefaultInstance = "hub-1"
	c.Cluster.Instances = map[string]InstanceConfig{
		"hub-1": {Name: "Local hub", BaseURL: "http://127.0.0.1:7842", Token: "t", Labels: []string{"macos"}},
		"srv-a": {Name: "srv-a", BaseURL: "http://10.0.0.11:7842", TokenEnv: "HEIMDALLM_SRV_A", Enabled: boolPtr(true)},
	}
	c.Cluster.Routing = RoutingConfig{
		Mode:           ModeDispatch,
		RoundRobinPool: []string{"hub-1", "srv-a"},
		RoundRobinOps:  []string{OpReview, OpMerge, OpIssue},
		Orgs:           map[string]string{"theburrowhub": "srv-a"},
		Repos:          map[string]string{"theburrowhub/heimdallm": "hub-1"},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
	if !c.IsHub() {
		t.Error("IsHub() = false")
	}
	if !c.Cluster.Routing.Configured() {
		t.Error("Routing.Configured() = false with rules present")
	}
}

// A worker carries the routing rules the hub pushed to it but has no registry
// of its own, so reference checks must not fire.
func TestValidateClusterWorkerWithoutRegistry(t *testing.T) {
	c := baseValidConfig()
	c.Cluster.Role = RoleWorker
	c.Cluster.InstanceID = "srv-a"
	c.Cluster.DefaultInstance = "hub-1"
	c.Cluster.Routing = RoutingConfig{
		Orgs:  map[string]string{"theburrowhub": "srv-a"},
		Repos: map[string]string{"theburrowhub/heimdallm": "hub-1"},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() on a registry-less worker = %v, want nil", err)
	}
}

func TestRoundRobinsOp(t *testing.T) {
	// Empty means every op, so an operator who sets a pool but forgets the op
	// list still gets round robin rather than silent single-instance work.
	if !(RoutingConfig{}).RoundRobinsOp(OpReview) {
		t.Error("empty RoundRobinOps should include every op")
	}
	r := RoutingConfig{RoundRobinOps: []string{"Review", OpMerge}}
	if !r.RoundRobinsOp(OpReview) {
		t.Error("case-insensitive match failed for review")
	}
	if !r.RoundRobinsOp(OpMerge) {
		t.Error("merge should be included")
	}
	if r.RoundRobinsOp(OpIssue) {
		t.Error("issue should not be included")
	}
}

func TestClusterEnvOverrides(t *testing.T) {
	t.Setenv("HEIMDALLM_CLUSTER_ROLE", RoleHub)
	t.Setenv("HEIMDALLM_INSTANCE_ID", "env-hub")
	t.Setenv("HEIMDALLM_INSTANCE_NAME", "Env hub")
	t.Setenv("HEIMDALLM_CLUSTER_DEFAULT_INSTANCE", "env-hub")
	t.Setenv("HEIMDALLM_CLUSTER_PROBE_INTERVAL", "15s")

	c := &Config{}
	c.AI.Primary = "claude"
	c.applyDefaults()
	c.applyEnvOverrides()

	if c.Cluster.Role != RoleHub {
		t.Errorf("role = %q, want %q", c.Cluster.Role, RoleHub)
	}
	if c.Cluster.InstanceID != "env-hub" {
		t.Errorf("instance_id = %q, want env-hub", c.Cluster.InstanceID)
	}
	if c.Cluster.InstanceName != "Env hub" {
		t.Errorf("instance_name = %q, want %q", c.Cluster.InstanceName, "Env hub")
	}
	if c.Cluster.DefaultInstance != "env-hub" {
		t.Errorf("default_instance = %q, want env-hub", c.Cluster.DefaultInstance)
	}
	if c.Cluster.ProbeInterval != "15s" {
		t.Errorf("probe_interval = %q, want 15s", c.Cluster.ProbeInterval)
	}
}

func TestExpandTokenFilePath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory in this environment")
	}

	expanded, err := expandTokenFilePath("~/heimdallm/api_token")
	if err != nil {
		t.Fatalf("expandTokenFilePath(~/...) = %v", err)
	}
	if !strings.HasPrefix(expanded, home) {
		t.Errorf("expanded = %q, want it under %q", expanded, home)
	}

	if got, err := expandTokenFilePath("~"); err != nil || got != filepath.Clean(home) {
		t.Errorf("expandTokenFilePath(~) = %q, %v", got, err)
	}
	if got, err := expandTokenFilePath("/etc/heimdallm/../api_token"); err != nil || got != "/etc/api_token" {
		t.Errorf("expandTokenFilePath cleaned to %q, %v", got, err)
	}
	// A relative path would resolve against whatever directory the daemon
	// happened to be started from.
	if _, err := expandTokenFilePath("relative/api_token"); err == nil {
		t.Error("expandTokenFilePath accepted a relative path")
	}
	if _, err := expandTokenFilePath(""); err == nil {
		t.Error("expandTokenFilePath accepted an empty path")
	}
}

func TestClusterDefaultsPreserveExplicitValues(t *testing.T) {
	c := &Config{}
	c.AI.Primary = "claude"
	c.Cluster.Routing.Mode = ModeDispatch
	c.Cluster.ProbeInterval = "5s"
	c.applyDefaults()

	if c.Cluster.Routing.Mode != ModeDispatch {
		t.Errorf("mode = %q, want the explicit value preserved", c.Cluster.Routing.Mode)
	}
	if c.Cluster.ProbeInterval != "5s" {
		t.Errorf("probe_interval = %q, want the explicit value preserved", c.Cluster.ProbeInterval)
	}
}

func TestClusterTakeoverThresholdDefaultsAndValidation(t *testing.T) {
	// Unset must land on the documented default, never on zero: a zero read by
	// a bare >= comparison means "take over on the first missed probe", which
	// is theburrowhub/heimdallm#765.
	c := &Config{}
	c.AI.Primary = "claude"
	c.Cluster.Role = RoleHub
	c.applyDefaults()
	if got := c.Cluster.TakeoverAfterFailedProbes; got == nil || *got != DefaultTakeoverAfterFailedProbes {
		t.Errorf("takeover_after_failed_probes = %v, want the default %d",
			got, DefaultTakeoverAfterFailedProbes)
	}

	nine := 9
	c.Cluster.TakeoverAfterFailedProbes = &nine
	c.applyDefaults()
	if got := c.Cluster.TakeoverAfterFailedProbes; got == nil || *got != 9 {
		t.Errorf("takeover_after_failed_probes = %v, want the explicit 9 preserved", got)
	}

	zero := 0
	c.Cluster.TakeoverAfterFailedProbes = &zero
	err := c.validateCluster()
	if err == nil {
		t.Fatal("validateCluster() = nil for a negative takeover_after_failed_probes, want an error")
	}
	if !strings.Contains(err.Error(), "takeover_after_failed_probes") {
		t.Errorf("error %q does not name the offending key", err)
	}
}

// Clustering stays inert without [cluster]: applyClusterDefaults must not grow
// a value in a config.toml the daemon rewrites for a feature nobody enabled.
func TestClusterTakeoverThresholdStaysUnsetWithoutCluster(t *testing.T) {
	c := &Config{}
	c.AI.Primary = "claude"
	c.applyDefaults()
	if c.Cluster.TakeoverAfterFailedProbes != nil {
		t.Errorf("takeover_after_failed_probes = %v on a non-clustered config, want nil — "+
			"a non-nil value makes ClusterConfig non-empty and writes an inert [cluster] table",
			*c.Cluster.TakeoverAfterFailedProbes)
	}
}

// A [cluster] section must survive a TOML round trip, or the daemon would
// silently drop the registry the first time it rewrote its own config.
func TestClusterTOMLRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	contents := `
[ai]
primary = "claude"

[cluster]
role = "hub"
instance_id = "hub-1"
instance_name = "Local hub"
default_instance = "hub-1"
probe_interval = "45s"

[cluster.instances.hub-1]
name = "Local hub"
base_url = "http://127.0.0.1:7842"
token = "t"
labels = ["macos", "local"]

[cluster.instances."srv-a"]
base_url = "http://10.0.0.11:7842"
token_env = "HEIMDALLM_SRV_A"
enabled = false

[cluster.routing]
mode = "dispatch"
round_robin_pool = ["hub-1"]
round_robin_ops = ["review", "merge"]

[cluster.routing.orgs]
theburrowhub = "hub-1"

[cluster.routing.repos]
"theburrowhub/heimdallm" = "hub-1"
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.IsHub() || cfg.Cluster.InstanceID != "hub-1" {
		t.Errorf("cluster = %+v", cfg.Cluster)
	}
	if cfg.Cluster.ProbeInterval != "45s" {
		t.Errorf("probe_interval = %q", cfg.Cluster.ProbeInterval)
	}
	if len(cfg.Cluster.Instances) != 2 {
		t.Fatalf("instances = %v", cfg.Cluster.Instances)
	}
	hub := cfg.Cluster.Instances["hub-1"]
	if hub.Token != "t" || len(hub.Labels) != 2 || !hub.IsEnabled() {
		t.Errorf("hub entry = %+v", hub)
	}
	srv := cfg.Cluster.Instances["srv-a"]
	if srv.TokenEnv != "HEIMDALLM_SRV_A" || srv.IsEnabled() {
		t.Errorf("srv-a entry = %+v", srv)
	}
	if cfg.Cluster.Routing.Mode != ModeDispatch ||
		cfg.Cluster.Routing.Orgs["theburrowhub"] != "hub-1" ||
		cfg.Cluster.Routing.Repos["theburrowhub/heimdallm"] != "hub-1" {
		t.Errorf("routing = %+v", cfg.Cluster.Routing)
	}
	if got := cfg.EnabledInstanceIDs(); len(got) != 1 || got[0] != "hub-1" {
		t.Errorf("EnabledInstanceIDs() = %v, want only the enabled hub", got)
	}
}

// Requiring the hub to list itself would be a chicken-and-egg trap: registering
// the FIRST remote instance would fail validation because the hub had not
// registered itself yet. The runtime seeds the entry instead.
func TestValidateClusterAllowsAHubMissingFromItsOwnRegistry(t *testing.T) {
	c := baseValidConfig()
	c.Cluster.Role = RoleHub
	c.Cluster.InstanceID = "hub-1"
	c.Cluster.Instances = map[string]InstanceConfig{
		"srv-a": {BaseURL: "http://10.0.0.11:7842", Token: "t"},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

// A rule routing work to the hub must persist even when config.toml carries no
// entry for it: the runtime seeds that entry, and refusing the write would make
// the hub the one instance nothing can be routed to.
func TestValidateClusterAcceptsRulesReferencingTheSelfID(t *testing.T) {
	c := baseValidConfig()
	c.Cluster.Role = RoleHub
	c.Cluster.InstanceID = "hub-1"
	c.Cluster.DefaultInstance = "hub-1"
	c.Cluster.Instances = map[string]InstanceConfig{
		"srv-a": {BaseURL: "http://10.0.0.11:7842", Token: "t"},
	}
	c.Cluster.Routing = RoutingConfig{
		RoundRobinPool: []string{"hub-1", "srv-a"},
		Orgs:           map[string]string{"acme": "hub-1"},
		Repos:          map[string]string{"acme/tools": "hub-1"},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}

	// An id that is neither registered nor this daemon is still rejected.
	c.Cluster.Routing.Orgs["other"] = "ghost"
	if err := c.Validate(); err == nil {
		t.Error("Validate() = nil for a rule pointing at an unknown instance")
	}
}

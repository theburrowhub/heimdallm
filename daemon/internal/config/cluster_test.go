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

func TestClusterDefaults(t *testing.T) {
	c := baseValidConfig()
	if c.Cluster.Routing.Mode != ModeAssignment {
		t.Errorf("routing mode = %q, want %q", c.Cluster.Routing.Mode, ModeAssignment)
	}
	if c.Cluster.ProbeInterval != DefaultClusterProbeInterval {
		t.Errorf("probe interval = %q, want %q", c.Cluster.ProbeInterval, DefaultClusterProbeInterval)
	}
	// Role must stay empty: defaulting it would make the daemon rewrite a
	// [cluster] section into configs that never asked for one.
	if c.Cluster.Role != "" {
		t.Errorf("role = %q, want empty", c.Cluster.Role)
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
		{
			name: "hub missing from its own registry",
			mut: func(c *Config) {
				c.Cluster.Role = RoleHub
				c.Cluster.InstanceID = "hub-1"
				c.Cluster.Instances = map[string]InstanceConfig{"other": {BaseURL: "http://o:7842", Token: "t"}}
			},
			want: "has no entry for this daemon's instance_id",
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

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withConfig points the CLI config helpers at a temp dir holding the given
// cli.toml contents.
func withConfig(t *testing.T, contents string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "heimdallm"), 0o700); err != nil {
		t.Fatal(err)
	}
	if contents != "" {
		path := filepath.Join(dir, "heimdallm", "cli.toml")
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// A config written before instances existed must keep working untouched.
func TestResolveLegacyFlatConfig(t *testing.T) {
	withConfig(t, "host = \"http://127.0.0.1:7842\"\ntoken = \"legacy\"\n")

	cfg, err := loadCLIConfig()
	if err != nil {
		t.Fatalf("loadCLIConfig: %v", err)
	}

	host, token, err := cfg.resolve("")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if host != "http://127.0.0.1:7842" || token != "legacy" {
		t.Errorf("resolve() = %q/%q, want the legacy pair", host, token)
	}

	// The flat pair is also visible as an instance called "local", so both
	// config shapes present the same view to the rest of the CLI.
	if got := cfg.instanceIDs(); strings.Join(got, ",") != localInstanceID {
		t.Errorf("instanceIDs() = %v, want [%s]", got, localInstanceID)
	}
	host, token, err = cfg.resolve(localInstanceID)
	if err != nil || host != "http://127.0.0.1:7842" || token != "legacy" {
		t.Errorf("resolve(local) = %q/%q, %v", host, token, err)
	}
}

func TestResolveSingleInstance(t *testing.T) {
	withConfig(t, `
[instances.srv-a]
name = "Server A"
host = "http://10.0.0.11:7842"
token = "a-token"
`)
	cfg, err := loadCLIConfig()
	if err != nil {
		t.Fatalf("loadCLIConfig: %v", err)
	}

	// A sole instance needs no default_instance and no --instance.
	host, token, err := cfg.resolve("")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if host != "http://10.0.0.11:7842" || token != "a-token" {
		t.Errorf("resolve() = %q/%q", host, token)
	}
}

func TestResolveSeveralInstances(t *testing.T) {
	withConfig(t, `
default_instance = "srv-b"

[instances.srv-a]
host = "http://10.0.0.11:7842"
token = "a"

[instances.srv-b]
host = "http://10.0.0.12:7842"
token = "b"
`)
	cfg, err := loadCLIConfig()
	if err != nil {
		t.Fatalf("loadCLIConfig: %v", err)
	}

	host, token, err := cfg.resolve("")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if host != "http://10.0.0.12:7842" || token != "b" {
		t.Errorf("resolve() = %q/%q, want the default_instance srv-b", host, token)
	}

	host, _, err = cfg.resolve("srv-a")
	if err != nil || host != "http://10.0.0.11:7842" {
		t.Errorf("resolve(srv-a) = %q, %v", host, err)
	}

	if got := strings.Join(cfg.instanceIDs(), ","); got != "srv-a,srv-b" {
		t.Errorf("instanceIDs() = %q, want them sorted", got)
	}
}

// Ambiguity has to be an error naming the choices, not a silent pick: sending
// a review to the wrong machine is worse than refusing to guess.
func TestResolveAmbiguousWithoutDefault(t *testing.T) {
	withConfig(t, `
[instances.srv-a]
host = "http://10.0.0.11:7842"
token = "a"

[instances.srv-b]
host = "http://10.0.0.12:7842"
token = "b"
`)
	cfg, err := loadCLIConfig()
	if err != nil {
		t.Fatalf("loadCLIConfig: %v", err)
	}

	if _, _, err := cfg.resolve(""); err == nil {
		t.Fatal("resolve() with two instances and no default = nil error")
	} else {
		for _, want := range []string{"srv-a", "srv-b", "--instance"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q is missing %q", err, want)
			}
		}
	}
}

func TestResolveUnknownInstance(t *testing.T) {
	withConfig(t, `
[instances.srv-a]
host = "http://10.0.0.11:7842"
token = "a"
`)
	cfg, _ := loadCLIConfig()
	_, _, err := cfg.resolve("ghost")
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("resolve(ghost) = %v, want an error naming the id", err)
	}
}

// An explicit [instances.local] entry wins over the legacy flat pair rather
// than being shadowed by it.
func TestExplicitLocalEntryWinsOverLegacyPair(t *testing.T) {
	withConfig(t, `
host = "http://legacy:7842"
token = "legacy"

[instances.local]
host = "http://explicit:7842"
token = "explicit"
`)
	cfg, _ := loadCLIConfig()
	host, token, err := cfg.resolve(localInstanceID)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if host != "http://explicit:7842" || token != "explicit" {
		t.Errorf("resolve(local) = %q/%q, want the explicit entry", host, token)
	}
}

func TestResolveEmptyConfig(t *testing.T) {
	withConfig(t, "")
	cfg := &cliConfig{}
	host, token, err := cfg.resolve("")
	if err != nil || host != "" || token != "" {
		t.Errorf("resolve() on an empty config = %q/%q, %v; want empties and no error", host, token, err)
	}
}

func TestDockerContainerNameOverride(t *testing.T) {
	// The hardcoded name would only ever find the first daemon container, so a
	// multi-instance Docker setup needs to point at a specific one.
	if got := dockerContainerName(); got != "heimdallm" {
		t.Errorf("dockerContainerName() = %q, want the default", got)
	}
	t.Setenv("HEIMDALLM_DOCKER_CONTAINER", "  heimdallm-b  ")
	if got := dockerContainerName(); got != "heimdallm-b" {
		t.Errorf("dockerContainerName() = %q, want the trimmed override", got)
	}
}

func TestSaveAndReloadInstances(t *testing.T) {
	withConfig(t, "")
	cfg := &cliConfig{
		DefaultInstance: "srv-a",
		Instances: map[string]cliInstance{
			"srv-a": {Name: "Server A", Host: "http://10.0.0.11:7842", Token: "a"},
		},
	}
	if err := saveCLIConfig(cfg); err != nil {
		t.Fatalf("saveCLIConfig: %v", err)
	}

	reloaded, err := loadCLIConfig()
	if err != nil {
		t.Fatalf("loadCLIConfig: %v", err)
	}
	if reloaded.DefaultInstance != "srv-a" {
		t.Errorf("default_instance = %q", reloaded.DefaultInstance)
	}
	host, token, err := reloaded.resolve("")
	if err != nil || host != "http://10.0.0.11:7842" || token != "a" {
		t.Errorf("resolve() = %q/%q, %v", host, token, err)
	}
}

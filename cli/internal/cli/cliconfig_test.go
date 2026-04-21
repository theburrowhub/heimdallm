package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigDirDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home dir")
	}
	want := filepath.Join(home, ".config", "heimdallm")
	if got := configDir(); got != want {
		t.Errorf("configDir() = %q, want %q", got, want)
	}
}

func TestConfigDirXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-test")
	want := "/tmp/xdg-test/heimdallm"
	if got := configDir(); got != want {
		t.Errorf("configDir() = %q, want %q", got, want)
	}
}

func TestSaveAndLoadCLIConfig(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	cfg := &cliConfig{
		Host:  "http://myhost:9999",
		Token: "abc123def456",
	}

	if err := saveCLIConfig(cfg); err != nil {
		t.Fatalf("saveCLIConfig: %v", err)
	}

	loaded, err := loadCLIConfig()
	if err != nil {
		t.Fatalf("loadCLIConfig: %v", err)
	}

	if loaded.Host != cfg.Host {
		t.Errorf("Host = %q, want %q", loaded.Host, cfg.Host)
	}
	if loaded.Token != cfg.Token {
		t.Errorf("Token = %q, want %q", loaded.Token, cfg.Token)
	}
}

func TestSaveCLIConfigPermissions(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	cfg := &cliConfig{Host: "http://localhost:7842", Token: "secret"}
	if err := saveCLIConfig(cfg); err != nil {
		t.Fatalf("saveCLIConfig: %v", err)
	}

	info, err := os.Stat(configPath())
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}

	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("config permissions = %04o, want 0600", perm)
	}
}

func TestSaveCLIConfigDirPermissions(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	cfg := &cliConfig{Host: "http://localhost:7842", Token: "secret"}
	if err := saveCLIConfig(cfg); err != nil {
		t.Fatalf("saveCLIConfig: %v", err)
	}

	info, err := os.Stat(configDir())
	if err != nil {
		t.Fatalf("stat config dir: %v", err)
	}

	perm := info.Mode().Perm()
	if perm != 0700 {
		t.Errorf("config dir permissions = %04o, want 0700", perm)
	}
}

func TestLoadCLIConfigMissing(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	_, err := loadCLIConfig()
	if err == nil {
		t.Error("loadCLIConfig should fail when file is missing")
	}
}

func TestSaveAndLoadPartialConfig(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	cfg := &cliConfig{Host: "http://example.com:8080"}
	if err := saveCLIConfig(cfg); err != nil {
		t.Fatalf("saveCLIConfig: %v", err)
	}

	loaded, err := loadCLIConfig()
	if err != nil {
		t.Fatalf("loadCLIConfig: %v", err)
	}

	if loaded.Host != cfg.Host {
		t.Errorf("Host = %q, want %q", loaded.Host, cfg.Host)
	}
	if loaded.Token != "" {
		t.Errorf("Token = %q, want empty", loaded.Token)
	}
}

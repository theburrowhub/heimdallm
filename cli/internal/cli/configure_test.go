package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestConfigureInteractiveDefaults(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	input := "\nmytoken123\n"
	cmd := newConfigureCmd()
	cmd.SetIn(strings.NewReader(input))
	cmd.SetOut(&bytes.Buffer{})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("configure: %v", err)
	}

	cfg, err := loadCLIConfig()
	if err != nil {
		t.Fatalf("loadCLIConfig: %v", err)
	}

	if cfg.Host != "http://localhost:7842" {
		t.Errorf("Host = %q, want default", cfg.Host)
	}
	if cfg.Token != "mytoken123" {
		t.Errorf("Token = %q, want %q", cfg.Token, "mytoken123")
	}
}

func TestConfigureInteractiveCustomHost(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	input := "http://remote:9999\nsecrettoken\n"
	cmd := newConfigureCmd()
	cmd.SetIn(strings.NewReader(input))
	cmd.SetOut(&bytes.Buffer{})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("configure: %v", err)
	}

	cfg, err := loadCLIConfig()
	if err != nil {
		t.Fatalf("loadCLIConfig: %v", err)
	}

	if cfg.Host != "http://remote:9999" {
		t.Errorf("Host = %q, want %q", cfg.Host, "http://remote:9999")
	}
	if cfg.Token != "secrettoken" {
		t.Errorf("Token = %q, want %q", cfg.Token, "secrettoken")
	}
}

func TestConfigurePreservesExistingValues(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	initial := &cliConfig{Host: "http://old:1234", Token: "oldtoken"}
	if err := saveCLIConfig(initial); err != nil {
		t.Fatalf("saveCLIConfig: %v", err)
	}

	input := "\nnewtoken\n"
	cmd := newConfigureCmd()
	cmd.SetIn(strings.NewReader(input))
	cmd.SetOut(&bytes.Buffer{})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("configure: %v", err)
	}

	cfg, err := loadCLIConfig()
	if err != nil {
		t.Fatalf("loadCLIConfig: %v", err)
	}

	if cfg.Host != "http://old:1234" {
		t.Errorf("Host = %q, want preserved %q", cfg.Host, "http://old:1234")
	}
	if cfg.Token != "newtoken" {
		t.Errorf("Token = %q, want %q", cfg.Token, "newtoken")
	}
}

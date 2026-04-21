package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

type cliConfig struct {
	Host  string `toml:"host,omitempty"`
	Token string `toml:"token,omitempty"`
}

func configDir() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "heimdallm")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "heimdallm")
	}
	return filepath.Join(home, ".config", "heimdallm")
}

func configPath() string {
	return filepath.Join(configDir(), "cli.toml")
}

func loadCLIConfig() (*cliConfig, error) {
	var cfg cliConfig
	if _, err := toml.DecodeFile(configPath(), &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func saveCLIConfig(cfg *cliConfig) error {
	dir := configDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}

	if err := os.WriteFile(configPath(), buf.Bytes(), 0600); err != nil {
		return fmt.Errorf("writing config file: %w", err)
	}
	return nil
}

func discoverDockerToken() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "docker", "exec", "heimdallm", "cat", "/data/api_token").Output()
	if err != nil {
		return "", fmt.Errorf("docker discovery failed: %w", err)
	}

	token := strings.TrimSpace(string(out))
	if token == "" {
		return "", fmt.Errorf("empty token from container")
	}
	return token, nil
}

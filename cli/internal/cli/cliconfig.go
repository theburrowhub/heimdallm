package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// cliConfig is ~/.config/heimdallm/cli.toml.
//
// Host/Token remain the single-daemon form every existing install has on disk.
// Instances adds the multi-instance form; the two coexist so an upgrade needs
// no migration step and a downgrade keeps working.
type cliConfig struct {
	Host  string `toml:"host,omitempty"`
	Token string `toml:"token,omitempty"`

	// DefaultInstance names the entry used when --instance is not given.
	DefaultInstance string `toml:"default_instance,omitempty"`

	// Instances are the registered daemons, keyed by id.
	Instances map[string]cliInstance `toml:"instances,omitempty"`
}

// cliInstance is one daemon the CLI can talk to.
type cliInstance struct {
	Name  string `toml:"name,omitempty"`
	Host  string `toml:"host"`
	Token string `toml:"token,omitempty"`
}

// localInstanceID is the id given to a legacy flat host/token pair when it is
// folded into the instance map.
const localInstanceID = "local"

// resolvedInstances returns every configured instance, folding a legacy flat
// host/token pair in as "local" so both config shapes present the same view.
func (c *cliConfig) resolvedInstances() map[string]cliInstance {
	out := make(map[string]cliInstance, len(c.Instances)+1)
	for id, inst := range c.Instances {
		out[id] = inst
	}
	if c.Host != "" || c.Token != "" {
		if _, exists := out[localInstanceID]; !exists {
			out[localInstanceID] = cliInstance{Name: "local", Host: c.Host, Token: c.Token}
		}
	}
	return out
}

// instanceIDs returns the configured ids in a stable order.
func (c *cliConfig) instanceIDs() []string {
	resolved := c.resolvedInstances()
	ids := make([]string, 0, len(resolved))
	for id := range resolved {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// resolve returns the host and token for an instance id. An empty id means the
// default: the configured default_instance, else the sole instance, else the
// legacy flat pair.
func (c *cliConfig) resolve(id string) (host, token string, err error) {
	resolved := c.resolvedInstances()
	if id == "" {
		id = c.DefaultInstance
	}
	if id == "" {
		switch len(resolved) {
		case 0:
			return c.Host, c.Token, nil
		case 1:
			for only := range resolved {
				id = only
			}
		default:
			return "", "", fmt.Errorf(
				"several instances are configured (%s) and no default_instance is set; pass --instance",
				strings.Join(c.instanceIDs(), ", "),
			)
		}
	}
	inst, ok := resolved[id]
	if !ok {
		return "", "", fmt.Errorf("unknown instance %q; configured: %s",
			id, strings.Join(c.instanceIDs(), ", "))
	}
	return inst.Host, inst.Token, nil
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

// dockerContainerName returns the container to read the API token from.
// Overridable because a multi-instance setup runs several daemon containers and
// the hardcoded name would only ever find the first one.
func dockerContainerName() string {
	if name := strings.TrimSpace(os.Getenv("HEIMDALLM_DOCKER_CONTAINER")); name != "" {
		return name
	}
	return "heimdallm"
}

func discoverDockerToken() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "docker", "exec", dockerContainerName(), "cat", "/data/api_token").Output()
	if err != nil {
		return "", fmt.Errorf("docker discovery failed: %w", err)
	}

	token := strings.TrimSpace(string(out))
	if token == "" {
		return "", fmt.Errorf("empty token from container")
	}
	return token, nil
}

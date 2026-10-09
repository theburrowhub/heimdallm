package api

import (
	"encoding/json"
	"fmt"
	"time"
)

// CLIAgent is one entry of the daemon's installed-agent catalog.
type CLIAgent struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	Executable  bool     `json:"executable"`
	Installed   bool     `json:"installed"`
	Configured  bool     `json:"configured"`
	Version     string   `json:"version"`
	Path        string   `json:"path"`
	ConfigAgent string   `json:"config_agent"`
	InstallHint string   `json:"install_hint"`
	Models      []string `json:"models"`
}

// CLIAgentCatalog is the GET /cli-agents body.
type CLIAgentCatalog struct {
	Agents    []CLIAgent `json:"agents"`
	ScannedAt time.Time  `json:"scanned_at"`
}

func decodeCatalog(data []byte) (*CLIAgentCatalog, error) {
	var cat CLIAgentCatalog
	if err := json.Unmarshal(data, &cat); err != nil {
		return nil, fmt.Errorf("parsing agent catalog: %w", err)
	}
	return &cat, nil
}

// ListCLIAgents returns the agents the daemon found on its machine.
func (c *Client) ListCLIAgents() (*CLIAgentCatalog, error) {
	data, err := c.do("GET", "/cli-agents")
	if err != nil {
		return nil, err
	}
	return decodeCatalog(data)
}

// RescanCLIAgents asks the daemon to look for installed agents again.
func (c *Client) RescanCLIAgents() (*CLIAgentCatalog, error) {
	data, err := c.do("POST", "/cli-agents/rescan")
	if err != nil {
		return nil, err
	}
	return decodeCatalog(data)
}

// StateLabel is how the agent's detection reads in a list: installed with
// its version, or whether a provider has an API key.
func (a CLIAgent) StateLabel() string {
	if a.Kind == "provider" {
		if a.Installed {
			return "API key set"
		}
		return "no API key"
	}
	if !a.Installed {
		return "not installed"
	}
	if a.Version != "" {
		return "installed " + DisplayText(a.Version, 40)
	}
	return "installed"
}

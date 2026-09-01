package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// InstanceState is the last observed condition of one instance.
type InstanceState struct {
	Reachable           bool    `json:"reachable"`
	Status              string  `json:"status"`
	Version             string  `json:"version"`
	Role                string  `json:"role"`
	UptimeSeconds       float64 `json:"uptime_seconds"`
	LastSeenAt          string  `json:"last_seen_at"`
	LastError           string  `json:"last_error"`
	ConsecutiveFailures int     `json:"consecutive_failures"`
}

// Instance is one registered daemon as the hub reports it.
type Instance struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	BaseURL       string         `json:"base_url"`
	Enabled       bool           `json:"enabled"`
	Self          bool           `json:"self"`
	Labels        []string       `json:"labels"`
	TokenError    string         `json:"token_error"`
	AssignedRepos int            `json:"assigned_repos"`
	IsFallback    bool           `json:"is_fallback"`
	InPool        bool           `json:"in_pool"`
	State         *InstanceState `json:"state"`
}

// DisplayName is the label to render, never empty.
func (i Instance) DisplayName() string {
	if strings.TrimSpace(i.Name) != "" {
		return i.Name
	}
	return i.ID
}

// ClusterRegistry is the hub's view of the fleet.
type ClusterRegistry struct {
	Role      string     `json:"role"`
	SelfID    string     `json:"self_id"`
	SelfName  string     `json:"self_name"`
	Instances []Instance `json:"instances"`
}

// RoutingRules are the org/repo to instance assignments.
type RoutingRules struct {
	Mode            string            `json:"mode"`
	RoundRobinPool  []string          `json:"round_robin_pool"`
	RoundRobinOps   []string          `json:"round_robin_ops"`
	Orgs            map[string]string `json:"orgs"`
	Repos           map[string]string `json:"repos"`
	DefaultInstance string            `json:"default_instance"`
	ResolvedPool    []string          `json:"resolved_pool"`
	Enabled         bool              `json:"enabled"`
}

// PropagateResult is one instance's outcome from a config push.
type PropagateResult struct {
	InstanceID  string   `json:"instance_id"`
	Name        string   `json:"name"`
	OK          bool     `json:"ok"`
	Skipped     bool     `json:"skipped"`
	Error       string   `json:"error"`
	AppliedKeys []string `json:"applied_keys"`
}

// PropagateReport is a whole config push.
type PropagateReport struct {
	Results      []PropagateResult `json:"results"`
	SkippedLocal []string          `json:"skipped_local"`
	Failures     int               `json:"failures"`
}

// ErrNotAHub is returned when the daemon has no control plane, which is the
// normal answer on a plain single-daemon install rather than a failure.
var ErrNotAHub = errors.New("this daemon is not a cluster hub")

// ListInstances fetches the registry. Returns ErrNotAHub on a daemon without a
// control plane so callers can print a helpful message instead of an HTTP code.
func (c *Client) ListInstances() (*ClusterRegistry, error) {
	data, err := c.doCluster(http.MethodGet, "/instances", nil)
	if err != nil {
		return nil, err
	}
	var out ClusterRegistry
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decoding instances: %w", err)
	}
	return &out, nil
}

// GetRouting fetches the routing rules.
func (c *Client) GetRouting() (*RoutingRules, error) {
	data, err := c.doCluster(http.MethodGet, "/cluster/routing", nil)
	if err != nil {
		return nil, err
	}
	var out RoutingRules
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decoding routing: %w", err)
	}
	return &out, nil
}

// PutRouting replaces the supplied routing fields. Omitted fields are left
// untouched; supplied maps replace the stored ones wholesale.
func (c *Client) PutRouting(body map[string]any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encoding routing: %w", err)
	}
	_, err = c.doCluster(http.MethodPut, "/cluster/routing", payload)
	return err
}

// PropagateConfig pushes shared config to the other instances.
func (c *Client) PropagateConfig(targets []string) (*PropagateReport, error) {
	body := map[string]any{}
	if len(targets) > 0 {
		body["targets"] = targets
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encoding propagate request: %w", err)
	}
	data, err := c.doCluster(http.MethodPost, "/cluster/propagate", payload)
	if err != nil {
		return nil, err
	}
	var out PropagateReport
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decoding propagate report: %w", err)
	}
	return &out, nil
}

// doCluster is do() with a JSON body and the control-plane status handling: a
// 404 means "not a hub", and 207 (partial success) is a real answer to read.
func (c *Client) doCluster(method, path string, body []byte) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := c.newRequest(method, path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	data, err := readLimited(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotAHub
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, DisplayText(strings.TrimSpace(string(data)), 300))
	}
	return data, nil
}

package instances

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"
)

const (
	// HeaderToken is the daemon's API token header.
	HeaderToken = "X-Heimdallm-Token"
	// HeaderDaemon marks a response as coming from a Heimdallm daemon.
	HeaderDaemon = "X-Heimdallm-Daemon"
)

const (
	// defaultTimeout bounds ordinary control-plane calls. Deliberately short:
	// the hub probes and propagates on a ticker, and a hung instance must not
	// stall the whole batch.
	defaultTimeout = 15 * time.Second
	// maxResponseBytes caps what we will read from another instance. A remote
	// daemon is semi-trusted (the operator registered it), but it is still a
	// network peer and must not be able to exhaust the hub's memory.
	maxResponseBytes = 8 << 20 // 8 MiB
	// maxDisplayRunes bounds any string we echo back into the API or the UI.
	maxDisplayRunes = 256
)

// Client talks to one remote instance's HTTP API.
//
// It is intentionally thin: the hub is a control plane, not a second
// implementation of the daemon. Anything the GUI needs beyond these calls goes
// through the transparent proxy instead of growing a method here.
type Client struct {
	instance Instance
	http     *http.Client
}

// NewClient builds a client for inst. Pass a non-nil httpClient in tests.
func NewClient(inst Instance, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	}
	return &Client{instance: inst, http: httpClient}
}

// Instance returns the registry entry this client targets.
func (c *Client) Instance() Instance { return c.instance }

// Health is the subset of GET /health the control plane needs. It is the only
// unauthenticated route, which makes it the right probe: a wrong or rotated
// token still yields a reachable instance rather than a silent outage.
type Health struct {
	Status        string         `json:"status"`
	Version       string         `json:"version"`
	StartedAt     string         `json:"started_at"`
	UptimeSeconds float64        `json:"uptime_seconds"`
	InstanceID    string         `json:"instance_id"`
	InstanceName  string         `json:"instance_name"`
	Role          string         `json:"role"`
	Checks        map[string]any `json:"checks"`
}

// Health probes GET /health.
//
// A non-2xx response is not automatically unreachable: the daemon answers 503
// while starting, or whenever a dependency (SQLite, NATS, a stale poll) is
// degraded, and in both cases the body is still a valid, parseable Health
// payload proving this instance is up and answering. Only a transport
// failure (no response at all) or a body that does not even decode as a
// daemon's counts as an error — that is the signal Prober uses to flip an
// instance to unreachable. Without this, a hub's own /health going 503 for a
// lagging poll cycle would make it flap itself "unreachable" on every probe.
func (c *Client) Health(ctx context.Context) (Health, error) {
	var h Health
	body, _, doErr := c.do(ctx, http.MethodGet, "/health", nil, false)
	if doErr != nil {
		var statusErr *StatusError
		if !errors.As(doErr, &statusErr) || len(body) == 0 {
			return h, doErr
		}
		// A non-2xx WITH a body: fall through and try to decode it below.
	}
	if err := json.Unmarshal(body, &h); err != nil {
		return h, fmt.Errorf("instances: %s returned an unparseable /health body: %w", c.instance.ID, err)
	}
	h.Status = Sanitize(h.Status)
	h.Version = Sanitize(h.Version)
	h.InstanceID = Sanitize(h.InstanceID)
	h.InstanceName = Sanitize(h.InstanceName)
	h.Role = Sanitize(h.Role)
	return h, nil
}

// GetConfig fetches the instance's flat config map.
func (c *Client) GetConfig(ctx context.Context) (map[string]any, error) {
	body, _, err := c.do(ctx, http.MethodGet, "/config", nil, true)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("instances: %s returned an unparseable /config body: %w", c.instance.ID, err)
	}
	return out, nil
}

// PatchConfig applies a TOML-shaped patch. The remote daemon validates, writes
// atomically and reloads itself, so the hub does not have to sequence any of
// that: a rejected patch leaves the instance exactly as it was.
func (c *Client) PatchConfig(ctx context.Context, patch map[string]any) (map[string]any, error) {
	payload, err := json.Marshal(patch)
	if err != nil {
		return nil, fmt.Errorf("instances: encoding config patch for %s: %w", c.instance.ID, err)
	}
	body, _, err := c.do(ctx, http.MethodPatch, "/config", payload, true)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if len(body) > 0 {
		// A daemon that applied the patch but answered with something we
		// cannot parse is not a failure worth rolling back; the patch landed.
		_ = json.Unmarshal(body, &out)
	}
	return out, nil
}

// PartitionPush is the PUT /cluster/partition body: the ownership partition
// this instance should enforce. Mirrors server.putPartitionRequest — kept as
// a separate type here rather than imported, the same way every other
// request/response shape in this file is a plain local struct, so this
// package never depends on internal/server.
type PartitionPush struct {
	InstanceID      string            `json:"instance_id"`
	DefaultInstance string            `json:"default_instance"`
	Orgs            map[string]string `json:"orgs"`
	Repos           map[string]string `json:"repos"`
	HubInstanceID   string            `json:"hub_instance_id"`
	HubVersion      string            `json:"hub_version"`
}

// PutPartition pushes push to the instance's PUT /cluster/partition. Unlike
// PatchConfig this targets a route available on ANY daemon, hub or worker: a
// worker is its primary audience, since it is the only way a worker's Router
// learns the partition it must enforce instead of failing open. A 404
// response (surfaced to the caller as a *StatusError) means the remote
// predates this endpoint — callers fall back to PatchConfig's [cluster]
// overlay for that case.
func (c *Client) PutPartition(ctx context.Context, push PartitionPush) (map[string]any, error) {
	payload, err := json.Marshal(push)
	if err != nil {
		return nil, fmt.Errorf("instances: encoding partition push for %s: %w", c.instance.ID, err)
	}
	body, _, err := c.do(ctx, http.MethodPut, "/cluster/partition", payload, true)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if len(body) > 0 {
		_ = json.Unmarshal(body, &out)
	}
	return out, nil
}

// Reload asks the instance to re-read its config from disk.
func (c *Client) Reload(ctx context.Context) error {
	_, _, err := c.do(ctx, http.MethodPost, "/reload", nil, true)
	return err
}

// TriggerPRReview queues a review for a PR the instance already knows about.
func (c *Client) TriggerPRReview(ctx context.Context, prID int64) error {
	_, _, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/prs/%d/review", prID), nil, true)
	return err
}

// TriggerIssueReview queues a triage run for an issue.
func (c *Client) TriggerIssueReview(ctx context.Context, issueID int64) error {
	_, _, err := c.do(ctx, http.MethodPost, fmt.Sprintf("/issues/%d/review", issueID), nil, true)
	return err
}

// EvaluateMergeTracking runs one merge-tracking evaluation for a PR.
func (c *Client) EvaluateMergeTracking(ctx context.Context, prID int64, dryRun bool) error {
	path := fmt.Sprintf("/merge-tracking/%d/evaluate?dry_run=%t", prID, dryRun)
	_, _, err := c.do(ctx, http.MethodPost, path, nil, true)
	return err
}

// AddPR asks the instance to ingest a PR by URL. This is how an operation can
// be dispatched to an instance that does not own the repo: it adopts the PR
// first, then reviews it.
func (c *Client) AddPR(ctx context.Context, prURL string) ([]byte, error) {
	payload, err := json.Marshal(map[string]string{"url": prURL})
	if err != nil {
		return nil, fmt.Errorf("instances: encoding add-PR body for %s: %w", c.instance.ID, err)
	}
	body, _, err := c.do(ctx, http.MethodPost, "/prs/add", payload, true)
	return body, err
}

// StatusError is returned when an instance answers with a non-2xx status. It
// carries the code so callers can distinguish "unreachable" (network error)
// from "reachable but refused" (401 on a rotated token, 503 while starting).
type StatusError struct {
	InstanceID string
	Path       string
	Status     int
	Body       string
}

func (e *StatusError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("instances: %s %s returned HTTP %d", e.InstanceID, e.Path, e.Status)
	}
	return fmt.Sprintf("instances: %s %s returned HTTP %d: %s", e.InstanceID, e.Path, e.Status, e.Body)
}

// Starting reports whether the instance answered "not ready yet". The daemon
// gates every route but /health behind a 503 while booting, so this is an
// expected transient rather than a real failure.
func (e *StatusError) Starting() bool { return e.Status == http.StatusServiceUnavailable }

// Unauthorized reports whether the instance rejected our token.
func (e *StatusError) Unauthorized() bool {
	return e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden
}

func (c *Client) do(ctx context.Context, method, path string, body []byte, auth bool) ([]byte, int, error) {
	if c.instance.BaseURL == "" {
		return nil, 0, fmt.Errorf("instances: %s has no base_url", c.instance.ID)
	}
	if auth {
		if c.instance.TokenErr != nil {
			return nil, 0, fmt.Errorf("instances: %s token unavailable: %w", c.instance.ID, c.instance.TokenErr)
		}
		if c.instance.Token == "" {
			return nil, 0, fmt.Errorf("instances: %s has no API token", c.instance.ID)
		}
	}

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.instance.BaseURL+path, reader)
	if err != nil {
		return nil, 0, fmt.Errorf("instances: building request to %s: %w", c.instance.ID, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if auth {
		req.Header.Set(HeaderToken, c.instance.Token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("instances: %s is unreachable: %w", c.instance.ID, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		_ = resp.Body.Close()
	}()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("instances: reading response from %s: %w", c.instance.ID, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return raw, resp.StatusCode, &StatusError{
			InstanceID: c.instance.ID,
			Path:       path,
			Status:     resp.StatusCode,
			Body:       Sanitize(string(raw)),
		}
	}
	return raw, resp.StatusCode, nil
}

// Sanitize makes a string from a remote instance safe to store, log and render.
// It strips control characters (an ANSI escape in a TUI or a newline in a log
// line is a real injection vector) and caps the length.
func Sanitize(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	count := 0
	for _, r := range s {
		if count >= maxDisplayRunes {
			b.WriteString("…")
			break
		}
		if r == unicode.ReplacementChar || (unicode.IsControl(r) && r != '\t') {
			continue
		}
		if r == '\t' {
			r = ' '
		}
		b.WriteRune(r)
		count++
	}
	return strings.TrimSpace(b.String())
}

package instances

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeDaemon is an httptest server standing in for a remote instance.
type fakeDaemon struct {
	*httptest.Server
	mu       sync.Mutex
	requests []recordedRequest
	token    string
}

type recordedRequest struct {
	Method string
	Path   string
	Query  string
	Token  string
	Body   string
}

func newFakeDaemon(t *testing.T, handler func(http.ResponseWriter, *http.Request)) *fakeDaemon {
	t.Helper()
	f := &fakeDaemon{token: "secret"}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := new(strings.Builder)
		if r.Body != nil {
			_, _ = io.Copy(body, r.Body)
		}
		f.mu.Lock()
		f.requests = append(f.requests, recordedRequest{
			Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery,
			Token: r.Header.Get(HeaderToken), Body: body.String(),
		})
		f.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeDaemon) seen() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedRequest(nil), f.requests...)
}

func (f *fakeDaemon) instance(id string) Instance {
	return Instance{ID: id, Name: id, BaseURL: f.URL, Token: "secret", Enabled: true}
}

func TestClientHealth(t *testing.T) {
	f := newFakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("path = %q, want /health", r.URL.Path)
		}
		w.Header().Set(HeaderDaemon, "1")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok", "version": "0.9.0", "uptime_seconds": 42.5,
			"instance_id": "srv-a", "instance_name": "Server A", "role": "worker",
		})
	})

	c := NewClient(f.instance("srv-a"), f.Client())
	h, err := c.Health(context.Background())
	if err != nil {
		t.Fatalf("Health() = %v", err)
	}
	if h.Status != "ok" || h.Version != "0.9.0" || h.InstanceID != "srv-a" || h.Role != "worker" {
		t.Errorf("Health() = %+v, want the fake daemon's values", h)
	}
	if h.UptimeSeconds != 42.5 {
		t.Errorf("uptime = %v, want 42.5", h.UptimeSeconds)
	}
	// /health is the unauthenticated route, so probing must not require a
	// token: a rotated token should still show the instance as reachable.
	if got := f.seen()[0].Token; got != "" {
		t.Errorf("health request carried a token %q, want none", got)
	}
}

func TestClientHealthSanitizesRemoteStrings(t *testing.T) {
	f := newFakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":        "ok",
			"instance_name": "evil\x1b[31mred\nnewline",
			"version":       "1.0\x00",
		})
	})
	h, err := NewClient(f.instance("a"), f.Client()).Health(context.Background())
	if err != nil {
		t.Fatalf("Health() = %v", err)
	}
	// A remote instance is a network peer; an ANSI escape or newline in a name
	// that lands in a TUI or a log line is an injection vector.
	if strings.ContainsAny(h.InstanceName, "\x1b\n") {
		t.Errorf("instance_name = %q, want control characters stripped", h.InstanceName)
	}
	if strings.Contains(h.Version, "\x00") {
		t.Errorf("version = %q, want the NUL stripped", h.Version)
	}
}

func TestClientHealthUnparseableBody(t *testing.T) {
	f := newFakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>not a daemon</html>"))
	})
	if _, err := NewClient(f.instance("a"), f.Client()).Health(context.Background()); err == nil {
		t.Error("Health() = nil error on a non-JSON body")
	}
}

func TestClientPatchConfigSendsTokenAndBody(t *testing.T) {
	f := newFakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	c := NewClient(f.instance("srv-a"), f.Client())
	patch := map[string]any{"ai": map[string]any{"review_mode": "multi"}}
	if _, err := c.PatchConfig(context.Background(), patch); err != nil {
		t.Fatalf("PatchConfig() = %v", err)
	}
	req := f.seen()[0]
	if req.Method != http.MethodPatch || req.Path != "/config" {
		t.Errorf("request = %s %s, want PATCH /config", req.Method, req.Path)
	}
	if req.Token != "secret" {
		t.Errorf("token = %q, want the instance token", req.Token)
	}
	if !strings.Contains(req.Body, "review_mode") {
		t.Errorf("body = %q, want the patch", req.Body)
	}
}

// A daemon that applied the patch but answered with something unparseable did
// still apply it; treating that as a failure would make the operator retry a
// change that already landed.
func TestClientPatchConfigTolerantOfUnparseableSuccess(t *testing.T) {
	f := newFakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("OK"))
	})
	if _, err := NewClient(f.instance("a"), f.Client()).PatchConfig(context.Background(), map[string]any{"x": 1}); err != nil {
		t.Errorf("PatchConfig() = %v, want nil on a 200 with a non-JSON body", err)
	}
}

func TestClientStatusErrorClassification(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		starting     bool
		unauthorized bool
	}{
		{"starting", http.StatusServiceUnavailable, true, false},
		{"unauthorized", http.StatusUnauthorized, false, true},
		{"forbidden", http.StatusForbidden, false, true},
		{"server error", http.StatusInternalServerError, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(`{"error":"nope"}`))
			})
			err := NewClient(f.instance("a"), f.Client()).Reload(context.Background())
			var se *StatusError
			if !errors.As(err, &se) {
				t.Fatalf("error = %v, want a *StatusError", err)
			}
			if se.Status != tt.status {
				t.Errorf("Status = %d, want %d", se.Status, tt.status)
			}
			if se.Starting() != tt.starting {
				t.Errorf("Starting() = %v, want %v", se.Starting(), tt.starting)
			}
			if se.Unauthorized() != tt.unauthorized {
				t.Errorf("Unauthorized() = %v, want %v", se.Unauthorized(), tt.unauthorized)
			}
			if !strings.Contains(se.Error(), "a") {
				t.Errorf("Error() = %q, want it to name the instance", se.Error())
			}
		})
	}
}

func TestClientOperations(t *testing.T) {
	f := newFakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})
	c := NewClient(f.instance("a"), f.Client())
	ctx := context.Background()

	if err := c.TriggerPRReview(ctx, 42); err != nil {
		t.Errorf("TriggerPRReview() = %v", err)
	}
	if err := c.TriggerIssueReview(ctx, 7); err != nil {
		t.Errorf("TriggerIssueReview() = %v", err)
	}
	if err := c.EvaluateMergeTracking(ctx, 99, true); err != nil {
		t.Errorf("EvaluateMergeTracking() = %v", err)
	}
	if _, err := c.AddPR(ctx, "https://github.com/acme/tools/pull/3"); err != nil {
		t.Errorf("AddPR() = %v", err)
	}

	seen := f.seen()
	want := []struct{ method, path, query string }{
		{http.MethodPost, "/prs/42/review", ""},
		{http.MethodPost, "/issues/7/review", ""},
		{http.MethodPost, "/merge-tracking/99/evaluate", "dry_run=true"},
		{http.MethodPost, "/prs/add", ""},
	}
	if len(seen) != len(want) {
		t.Fatalf("got %d requests, want %d", len(seen), len(want))
	}
	for i, w := range want {
		if seen[i].Method != w.method || seen[i].Path != w.path || seen[i].Query != w.query {
			t.Errorf("request %d = %s %s?%s, want %s %s?%s",
				i, seen[i].Method, seen[i].Path, seen[i].Query, w.method, w.path, w.query)
		}
	}
}

func TestClientRefusesWithoutCredentials(t *testing.T) {
	ctx := context.Background()

	// No base URL at all.
	if err := NewClient(Instance{ID: "a"}, nil).Reload(ctx); err == nil {
		t.Error("Reload() = nil with no base_url")
	}
	// Token failed to resolve: the call must fail locally with the real reason
	// rather than going out unauthenticated and coming back as a puzzling 401.
	inst := Instance{ID: "a", BaseURL: "http://127.0.0.1:1", Enabled: true, TokenErr: errors.New("token file missing")}
	err := NewClient(inst, nil).Reload(ctx)
	if err == nil || !strings.Contains(err.Error(), "token file missing") {
		t.Errorf("Reload() = %v, want the token resolution error", err)
	}
	// Declared but empty token.
	if err := NewClient(Instance{ID: "a", BaseURL: "http://127.0.0.1:1", Enabled: true}, nil).Reload(ctx); err == nil {
		t.Error("Reload() = nil with an empty token")
	}
}

func TestClientUnreachable(t *testing.T) {
	// Port 1 on loopback refuses connections fast and deterministically.
	inst := Instance{ID: "down", BaseURL: "http://127.0.0.1:1", Token: "t", Enabled: true}
	_, err := NewClient(inst, nil).Health(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Errorf("Health() = %v, want an unreachable error", err)
	}
}

func TestSanitize(t *testing.T) {
	tests := map[string]string{
		"":                       "",
		"plain":                  "plain",
		"  padded  ":             "padded",
		"esc\x1b[31mape":         "esc[31mape",
		"line\nbreak":            "linebreak",
		"null\x00byte":           "nullbyte",
		"tab\there":              "tab here",
		strings.Repeat("x", 300): strings.Repeat("x", 256) + "…",
	}
	for in, want := range tests {
		if got := Sanitize(in); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const apiContractToken = "contract-token"

type contractRequest struct {
	method string
	path   string
	accept string
	token  string
}

func newContractServer(t *testing.T, status int, contentType, body string) (*httptest.Server, <-chan contractRequest) {
	t.Helper()

	requests := make(chan contractRequest, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- contractRequest{
			method: r.Method,
			path:   r.URL.RequestURI(),
			accept: r.Header.Get("Accept"),
			token:  r.Header.Get("X-Heimdallm-Token"),
		}
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, requests
}

func assertContractRequest(t *testing.T, requests <-chan contractRequest, method, path, accept string) {
	t.Helper()

	var got contractRequest
	select {
	case got = <-requests:
	case <-time.After(time.Second):
		t.Fatal("client did not send a request")
	}

	if got.method != method {
		t.Errorf("request method = %q, want %q", got.method, method)
	}
	if got.path != path {
		t.Errorf("request path = %q, want %q", got.path, path)
	}
	if got.accept != accept {
		t.Errorf("Accept header = %q, want %q", got.accept, accept)
	}
	if got.token != apiContractToken {
		t.Errorf("X-Heimdallm-Token header = %q, want %q", got.token, apiContractToken)
	}

	select {
	case extra := <-requests:
		t.Errorf("client sent an unexpected additional request: %s %s", extra.method, extra.path)
	default:
	}
}

func TestReadEndpointContracts(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
		call func(*Client) error
	}{
		{
			name: "health",
			path: "/health",
			body: `{"status":"ok","version":"0.9.0"}`,
			call: func(c *Client) error {
				health, err := c.GetHealth()
				if err != nil {
					return err
				}
				if health.Status != "ok" || health.Version != "0.9.0" {
					return fmt.Errorf("decoded health = %#v", health)
				}
				return nil
			},
		},
		{
			name: "PR list",
			path: "/prs",
			body: `[{"id":41,"repo":"acme/widget","number":7,"latest_review":{"id":5,"severity":"high"}}]`,
			call: func(c *Client) error {
				prs, err := c.ListPRs()
				if err != nil {
					return err
				}
				if len(prs) != 1 || prs[0].ID != 41 || prs[0].Repo != "acme/widget" ||
					prs[0].LatestReview == nil || prs[0].LatestReview.Severity != "high" {
					return fmt.Errorf("decoded PRs = %#v", prs)
				}
				return nil
			},
		},
		{
			name: "issue list",
			path: "/issues",
			body: `[{"id":42,"repo":"acme/widget","number":8,"latest_review":{"id":6,"action_taken":"review_only","triage":{"severity":"medium"}}}]`,
			call: func(c *Client) error {
				issues, err := c.ListIssues()
				if err != nil {
					return err
				}
				if len(issues) != 1 || issues[0].ID != 42 || issues[0].Repo != "acme/widget" ||
					issues[0].LatestReview == nil || issues[0].LatestReview.ActionTaken != "review_only" {
					return fmt.Errorf("decoded issues = %#v", issues)
				}
				return nil
			},
		},
		{
			name: "config",
			path: "/config",
			body: `{"server_port":7842,"repositories":["acme/widget"]}`,
			call: func(c *Client) error {
				cfg, err := c.GetConfig()
				if err != nil {
					return err
				}
				if cfg["server_port"] != float64(7842) {
					return fmt.Errorf("decoded config = %#v", cfg)
				}
				return nil
			},
		},
		{
			name: "stats",
			path: "/stats",
			body: `{"total_reviews":9,"activity_count_24h":4,"by_severity":{"high":2}}`,
			call: func(c *Client) error {
				stats, err := c.GetStats()
				if err != nil {
					return err
				}
				if stats.TotalReviews != 9 || stats.ActivityCount24h != 4 || stats.BySeverity["high"] != 2 {
					return fmt.Errorf("decoded stats = %#v", stats)
				}
				return nil
			},
		},
		{
			name: "activity",
			path: "/activity",
			body: `{"entries":[{"id":3,"repo":"acme/widget","action":"review"}],"count":1,"truncated":true}`,
			call: func(c *Client) error {
				activity, err := c.GetActivity()
				if err != nil {
					return err
				}
				if activity.Count != 1 || !activity.Truncated || len(activity.Entries) != 1 ||
					activity.Entries[0].Repo != "acme/widget" {
					return fmt.Errorf("decoded activity = %#v", activity)
				}
				return nil
			},
		},
		{
			name: "PR detail",
			path: "/prs/41",
			body: `{"pr":{"id":41,"number":7},"reviews":[{"id":5,"severity":"high"}]}`,
			call: func(c *Client) error {
				detail, err := c.GetPR(41)
				if err != nil {
					return err
				}
				if detail.PR.ID != 41 || len(detail.Reviews) != 1 || detail.Reviews[0].Severity != "high" {
					return fmt.Errorf("decoded PR detail = %#v", detail)
				}
				return nil
			},
		},
		{
			name: "issue detail",
			path: "/issues/42",
			body: `{"issue":{"id":42,"number":8},"reviews":[{"id":6,"action_taken":"review_only"}]}`,
			call: func(c *Client) error {
				detail, err := c.GetIssue(42)
				if err != nil {
					return err
				}
				if detail.Issue.ID != 42 || len(detail.Reviews) != 1 ||
					detail.Reviews[0].ActionTaken != "review_only" {
					return fmt.Errorf("decoded issue detail = %#v", detail)
				}
				return nil
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			srv, requests := newContractServer(t, http.StatusOK, "application/json", tc.body)
			client := New(srv.URL+"/", apiContractToken)

			if err := tc.call(client); err != nil {
				t.Fatalf("API call failed: %v", err)
			}
			assertContractRequest(t, requests, http.MethodGet, tc.path, "application/json")
		})
	}
}

func TestMutationEndpointContractsAndErrors(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		status int
		body   string
		call   func(*Client) error
	}{
		{"queue PR review", "/prs/41/review", http.StatusAccepted, `{"status":"review queued"}`, func(c *Client) error {
			return c.TriggerPRReview(41)
		}},
		{"queue issue review", "/issues/42/review", http.StatusAccepted, `{"status":"review queued"}`, func(c *Client) error {
			return c.TriggerIssueReview(42)
		}},
		{"promote issue", "/issues/42/promote", http.StatusAccepted, `{"status":"promotion applied"}`, func(c *Client) error {
			return c.PromoteIssue(42)
		}},
		{"dismiss issue", "/issues/42/dismiss", http.StatusOK, `{"status":"dismissed"}`, func(c *Client) error {
			return c.DismissIssue(42)
		}},
		{"undismiss issue", "/issues/42/undismiss", http.StatusOK, `{"status":"undismissed"}`, func(c *Client) error {
			return c.UndismissIssue(42)
		}},
		{"shutdown", "/shutdown", http.StatusAccepted, `{"status":"shutdown queued"}`, func(c *Client) error {
			return c.Shutdown()
		}},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Run("success", func(t *testing.T) {
				srv, requests := newContractServer(t, tc.status, "application/json", tc.body)
				client := New(srv.URL+"/", apiContractToken)

				if err := tc.call(client); err != nil {
					t.Fatalf("API call failed: %v", err)
				}
				assertContractRequest(t, requests, http.MethodPost, tc.path, "application/json")
			})

			t.Run("error", func(t *testing.T) {
				srv, requests := newContractServer(t, http.StatusConflict, "text/plain", "  operation unavailable  \n")
				client := New(srv.URL, apiContractToken)

				err := tc.call(client)
				if err == nil {
					t.Fatal("API call error = nil, want conflict")
				}
				if got, want := err.Error(), "HTTP 409: operation unavailable"; got != want {
					t.Errorf("API call error = %q, want %q", got, want)
				}
				assertContractRequest(t, requests, http.MethodPost, tc.path, "application/json")
			})
		})
	}
}

func TestStreamEventsEndpointContract(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
		wantEvents  []SSEEvent
		wantErr     string
	}{
		{
			name:        "comments and multiline data",
			status:      http.StatusOK,
			contentType: "text/event-stream",
			body: ": keep-alive\n\n" +
				"event: review_completed\n" +
				"data: {\"repo\":\"acme/widget\",\n" +
				"data: \"pr_number\":7}\n\n" +
				"event: issue_review_completed\n" +
				"data: {\"issue_number\":8}\n\n",
			wantEvents: []SSEEvent{
				{Type: "review_completed", Data: "{\"repo\":\"acme/widget\",\n\"pr_number\":7}"},
				{Type: "issue_review_completed", Data: `{"issue_number":8}`},
			},
		},
		{
			name:        "unauthorized response",
			status:      http.StatusUnauthorized,
			contentType: "text/plain",
			body:        "  invalid API token  \n",
			wantErr:     "SSE HTTP 401: invalid API token",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			srv, requests := newContractServer(t, tc.status, tc.contentType, tc.body)
			events := make(chan SSEEvent, len(tc.wantEvents)+1)
			client := New(srv.URL+"/", apiContractToken)

			err := client.StreamEvents(context.Background(), events)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("StreamEvents() error = %v", err)
				}
			} else {
				if err == nil {
					t.Fatalf("StreamEvents() error = nil, want %q", tc.wantErr)
				}
				if err.Error() != tc.wantErr {
					t.Errorf("StreamEvents() error = %q, want %q", err, tc.wantErr)
				}
			}

			for i, want := range tc.wantEvents {
				select {
				case got := <-events:
					if got != want {
						t.Errorf("event %d = %#v, want %#v", i, got, want)
					}
				default:
					t.Fatalf("event %d was not delivered", i)
				}
			}
			select {
			case extra := <-events:
				t.Errorf("unexpected additional event: %#v", extra)
			default:
			}

			assertContractRequest(t, requests, http.MethodGet, "/events", "text/event-stream")
		})
	}
}

func TestStreamEventsReturnsWhenContextCanceledWithBlockedSend(t *testing.T) {
	eventFlushed := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("response writer does not support flushing")
		}
		fmt.Fprint(w, "event: heartbeat\ndata: {}\n\n")
		flusher.Flush()
		close(eventFlushed)
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	client := New(srv.URL, "")
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan SSEEvent)
	done := make(chan error, 1)
	go func() {
		done <- client.StreamEvents(ctx, events)
	}()

	select {
	case <-eventFlushed:
	case <-time.After(time.Second):
		t.Fatal("server did not flush SSE event")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected context cancellation error")
		}
	case <-time.After(time.Second):
		t.Fatal("StreamEvents did not return after context cancellation")
	}
}

func TestGetHealthParsesDaemonVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"ok","version":"0.8.0","started_at":"2026-08-04T09:20:26Z"}`)
	}))
	t.Cleanup(srv.Close)

	h, err := New(srv.URL, "").GetHealth()
	if err != nil {
		t.Fatalf("GetHealth: %v", err)
	}
	if h.Version != "0.8.0" {
		t.Errorf("Version = %q, want 0.8.0", h.Version)
	}
	if h.Status != "ok" {
		t.Errorf("Status = %q, want ok", h.Status)
	}
}

// A daemon built before version stamping omits the field entirely; GetHealth
// must succeed with an empty Version rather than failing the whole fetch.
func TestGetHealthToleratesMissingVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"ok"}`)
	}))
	t.Cleanup(srv.Close)

	h, err := New(srv.URL, "").GetHealth()
	if err != nil {
		t.Fatalf("GetHealth: %v", err)
	}
	if h.Version != "" {
		t.Errorf("Version = %q, want empty", h.Version)
	}
}

// The daemon answers 503 with status "degraded" when a check fails, but the body
// still carries the version (daemon/internal/server/handlers.go). That is exactly
// when an operator needs to know which build is running, so 503 must decode.
func TestGetHealthDecodesDegraded503(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"status":"degraded","version":"0.8.0","started_at":"2026-08-04T09:20:26Z"}`)
	}))
	t.Cleanup(srv.Close)

	h, err := New(srv.URL, "").GetHealth()
	if err != nil {
		t.Fatalf("GetHealth on 503: %v", err)
	}
	if h.Version != "0.8.0" {
		t.Errorf("Version = %q, want 0.8.0", h.Version)
	}
	if h.Status != "degraded" {
		t.Errorf("Status = %q, want degraded", h.Status)
	}
}

// Health() shares GetHealth's transport, so a degraded-but-reachable daemon is
// not a connectivity failure for the SSE watchdog either.
func TestHealthTreatsDegradedAsReachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"status":"degraded","version":"0.8.0"}`)
	}))
	t.Cleanup(srv.Close)

	if err := New(srv.URL, "").Health(); err != nil {
		t.Errorf("Health() on a reachable degraded daemon = %v, want nil", err)
	}
}

// Statuses other than 200/503, and bodies that are not health payloads, remain
// errors — tolerating 503 must not swallow a 401 or an HTML error page.
func TestGetHealthErrorsOnUnexpectedStatusAndGarbage(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"unauthorized", http.StatusUnauthorized, `{"error":"unauthorized"}`},
		{"gateway html", http.StatusBadGateway, `<html>502 Bad Gateway</html>`},
		{"undecodable 200", http.StatusOK, `not json at all`},
		{"undecodable 503", http.StatusServiceUnavailable, `<html>upstream down</html>`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			t.Cleanup(srv.Close)

			if _, err := New(srv.URL, "").GetHealth(); err == nil {
				t.Errorf("GetHealth on %s = nil error, want error", tc.name)
			}
		})
	}
}

// Status is rendered into a TUI row and a terminal line, so control bytes would
// corrupt the layout and an unbounded string would overrun it. The daemon only
// emits "ok"/"degraded" today; this keeps a future value from breaking callers.
func TestHealthDisplayStatus(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "degraded", "degraded"},
		{"empty", "", ""},
		// Dropping ESC is what defuses the sequence; the remaining "[31m" is
		// inert literal text, so it is deliberately kept rather than parsed out.
		{"defuses ANSI by dropping ESC", "deg\x1b[31mraded\n", "deg[31mraded"},
		{"strips tabs and CR", "de\tgra\rded", "degraded"},
		{"caps length", strings.Repeat("x", 80), strings.Repeat("x", 32)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &Health{Status: tc.in}
			if got := h.DisplayStatus(); got != tc.want {
				t.Errorf("DisplayStatus() = %q, want %q", got, tc.want)
			}
		})
	}
	var nilH *Health
	if got := nilH.DisplayStatus(); got != "" {
		t.Errorf("nil receiver DisplayStatus() = %q, want empty", got)
	}
}

// The cap is documented in runes, so it must count runes: a byte-based check
// truncated multibyte input at ~11 characters and could still emit 32+ bytes.
func TestDisplayFieldsCapInRunes(t *testing.T) {
	multibyte := strings.Repeat("é", 80) // 2 bytes per rune
	h := &Health{Status: multibyte, Version: multibyte}

	gotStatus := h.DisplayStatus()
	if n := len([]rune(gotStatus)); n != statusDisplayMax {
		t.Errorf("DisplayStatus() kept %d runes, want %d", n, statusDisplayMax)
	}
	gotVersion := h.DisplayVersion()
	if n := len([]rune(gotVersion)); n != versionDisplayMax {
		t.Errorf("DisplayVersion() kept %d runes, want %d", n, versionDisplayMax)
	}
}

// Version reaches the same TUI row and terminal line as Status, so it gets the
// same treatment — it was previously printed raw and unbounded.
func TestHealthDisplayVersion(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "0.8.0", "0.8.0"},
		{"describe output", "0.8.0-3-g86f49fa-dirty", "0.8.0-3-g86f49fa-dirty"},
		{"empty", "", ""},
		{"drops control bytes", "0.8\x00.0\n", "0.8.0"},
		{"caps length", strings.Repeat("v", 200), strings.Repeat("v", versionDisplayMax)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &Health{Version: tc.in}
			if got := h.DisplayVersion(); got != tc.want {
				t.Errorf("DisplayVersion() = %q, want %q", got, tc.want)
			}
		})
	}
	var nilH *Health
	if got := nilH.DisplayVersion(); got != "" {
		t.Errorf("nil receiver DisplayVersion() = %q, want empty", got)
	}
}

// GetHealth embeds the raw response body in its error (TrimSpace only touches the
// ends), so a proxy's HTML 502 can carry interior newlines or ESC bytes. That
// error is rendered on the same TUI row as Status and Version, so it needs the
// same treatment — DisplayText is the entry point for that third string.
func TestDisplayText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"plain", "connection refused", 60, "connection refused"},
		{"interior newlines", "HTTP 502: <html>\n<body>\nBad Gateway\n</html>", 60,
			"HTTP 502: <html><body>Bad Gateway</html>"},
		{"drops ESC", "HTTP 502: \x1b[2Jcleared", 60, "HTTP 502: [2Jcleared"},
		{"caps in runes", "ééééé", 3, "ééé"},
		{"empty", "", 60, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DisplayText(tc.in, tc.max); got != tc.want {
				t.Errorf("DisplayText(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
			}
		})
	}
}

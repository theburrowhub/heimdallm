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

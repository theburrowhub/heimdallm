package server

import "testing"

// The long-lived SSE routes proxied to another instance must not be cut off
// by the server's absolute 60s WriteTimeout — the same deadline handleSSE
// itself neutralizes for a local stream (stream_writer.go). Every other
// proxied path keeps the ordinary timeout unmodified.
func TestIsStreamProxyPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/events", true},
		{"/logs/stream", true},
		{"/prs", false},
		{"/repos", false},
		{"/config", false},
		{"", false},
		// Must be an exact match, not a prefix: a sibling path that merely
		// starts with the same string is not the stream route.
		{"/events-extra", false},
		{"/logs/streaming", false},
	}
	for _, tt := range tests {
		if got := isStreamProxyPath(tt.path); got != tt.want {
			t.Errorf("isStreamProxyPath(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

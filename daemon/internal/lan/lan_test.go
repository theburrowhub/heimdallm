package lan

import (
	"strings"
	"testing"
)

func TestEncodeDecodeTXTRoundTrip(t *testing.T) {
	want := Peer{
		InstanceID:   "mac-sergio-a1b2",
		InstanceName: "Sergio's laptop",
		Role:         "worker",
		Version:      "0.8.17",
		Scheme:       "https",
	}
	got := decodeTXT(encodeTXT(want))
	if got.InstanceID != want.InstanceID || got.InstanceName != want.InstanceName ||
		got.Role != want.Role || got.Version != want.Version || got.Scheme != want.Scheme {
		t.Fatalf("round trip changed the peer:\n got %+v\nwant %+v", got, want)
	}
}

func TestDecodeTXTIgnoresUnknownKeys(t *testing.T) {
	// A future version adding a field must not make its daemons invisible to
	// an older one, so anything unrecognised is skipped rather than rejected.
	got := decodeTXT([]string{
		"id=srv-a",
		"future_field=whatever",
		"noequalssign",
		"=emptykey",
	})
	if got.InstanceID != "srv-a" {
		t.Fatalf("id = %q, want srv-a", got.InstanceID)
	}
}

func TestDecodeTXTToleratesMissingKeys(t *testing.T) {
	got := decodeTXT([]string{"id=srv-a"})
	if got.InstanceID != "srv-a" {
		t.Fatalf("id = %q, want srv-a", got.InstanceID)
	}
	if got.InstanceName != "" || got.Role != "" || got.Version != "" {
		t.Fatalf("absent keys should stay empty, got %+v", got)
	}
}

func TestSanitizeTXTValue(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "srv-a", "srv-a"},
		{"trimmed", "  srv-a  ", "srv-a"},
		{"empty", "   ", ""},
		{"control characters dropped", "srv\x00-\x07a", "srv-a"},
		{"newline dropped", "srv\na", "srva"},
		{"del dropped", "srv\x7fa", "srva"},
		{"invalid utf8 rejected", "srv-\xff\xfe", ""},
		{"unicode kept", "café", "café"},
		{"oversized truncated", strings.Repeat("x", txtValueMax+50), strings.Repeat("x", txtValueMax)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeTXTValue(tt.in); got != tt.want {
				t.Fatalf("sanitizeTXTValue(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestEncodeTXTSkipsEmptyValues(t *testing.T) {
	// An empty value would be published as a bare "key=" that decodes back to
	// nothing, so it is dropped rather than taking up room in the packet.
	got := encodeTXT(Peer{InstanceID: "srv-a"})
	if len(got) != 1 || got[0] != "id=srv-a" {
		t.Fatalf("got %q, want exactly [id=srv-a]", got)
	}
}

func TestPeerBaseURL(t *testing.T) {
	tests := []struct {
		name string
		peer Peer
		want string
	}{
		{"hostname and port", Peer{Hostname: "srv-a.local", Port: 7842}, "http://srv-a.local:7842"},
		{"trailing dot stripped", Peer{Hostname: "srv-a.local.", Port: 7842}, "http://srv-a.local:7842"},
		{"https honoured", Peer{Hostname: "srv-a.local", Port: 443, Scheme: "https"}, "https://srv-a.local:443"},
		{"unknown scheme falls back to http", Peer{Hostname: "srv-a.local", Port: 7842, Scheme: "gopher"}, "http://srv-a.local:7842"},
		{"no hostname", Peer{Port: 7842}, ""},
		{"no port", Peer{Hostname: "srv-a.local"}, ""},
		{"negative port", Peer{Hostname: "srv-a.local", Port: -1}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.peer.BaseURL(); got != tt.want {
				t.Fatalf("BaseURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEscapeLabelProtectsTheServiceName(t *testing.T) {
	// An unescaped dot would split the DNS label and silently reparent the
	// record under a different service.
	got := instanceFQDN("my.laptop")
	want := `my\.laptop._heimdallm._tcp.local.`
	if got != want {
		t.Fatalf("instanceFQDN = %q, want %q", got, want)
	}
}

func TestEscapeLabelFallsBackWhenNameIsUnusable(t *testing.T) {
	if got := escapeLabel("  \x00 "); got != "heimdallm" {
		t.Fatalf("escapeLabel = %q, want heimdallm", got)
	}
}

func TestValidatePort(t *testing.T) {
	for _, port := range []int{0, -1, 65536} {
		if err := validatePort(port); err == nil {
			t.Fatalf("validatePort(%d) = nil, want an error", port)
		}
	}
	if err := validatePort(7842); err != nil {
		t.Fatalf("validatePort(7842) = %v, want nil", err)
	}
}

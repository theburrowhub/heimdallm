package cli

import "testing"

func TestIsLocalhost(t *testing.T) {
	tests := []struct {
		host string
		want bool
	}{
		{"http://localhost:7842", true},
		{"http://127.0.0.1:7842", true},
		{"http://[::1]:7842", true},
		{"http://localhost", true},
		{"https://localhost:443", true},
		{"http://example.com:7842", false},
		{"http://192.168.1.10:7842", false},
		{"not-a-url", false},
	}

	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			if got := isLocalhost(tt.host); got != tt.want {
				t.Errorf("isLocalhost(%q) = %v, want %v", tt.host, got, tt.want)
			}
		})
	}
}

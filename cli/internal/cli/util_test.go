package cli

import "testing"

func TestTruncate(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{"short", "hello", 10, "hello"},
		{"exact", "hello", 5, "hello"},
		{"truncated", "hello world", 8, "hello w…"},
		{"unicode_input", "héllo wörld", 8, "héllo w…"},
		{"empty", "", 5, ""},
		{"single_char", "a", 1, "a"},
		{"truncate_to_one", "abc", 1, "…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncate(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestColorSeverity(t *testing.T) {
	for _, sev := range []string{"high", "medium", "low", "info", "---", ""} {
		got := colorSeverity(sev, 8)
		if got == "" {
			t.Errorf("colorSeverity(%q, 8) returned empty string", sev)
		}
	}
}

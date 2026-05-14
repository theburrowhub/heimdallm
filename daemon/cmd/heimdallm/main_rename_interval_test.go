package main

import (
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/rename"
)

// TestParseRenameProbeInterval_DisabledByZero pins the operator
// escape hatch — "0" returns 0 so the wiring in main.go knows to
// skip starting the probe goroutine entirely.
func TestParseRenameProbeInterval_DisabledByZero(t *testing.T) {
	if got := parseRenameProbeInterval("0"); got != 0 {
		t.Errorf(`parseRenameProbeInterval("0") = %v, want 0 (disabled)`, got)
	}
}

// TestParseRenameProbeInterval_EmptyUsesDefault pins the
// applyDefaults contract: an empty string after config-load means
// the operator left the knob untouched, so use the default cadence.
func TestParseRenameProbeInterval_EmptyUsesDefault(t *testing.T) {
	if got := parseRenameProbeInterval(""); got != rename.DefaultProbeInterval {
		t.Errorf(`parseRenameProbeInterval("") = %v, want %v`, got, rename.DefaultProbeInterval)
	}
}

// TestParseRenameProbeInterval_UnparseableUsesDefault keeps a typo
// (e.g. "1hour" instead of "1h") from disabling the probe silently.
func TestParseRenameProbeInterval_UnparseableUsesDefault(t *testing.T) {
	if got := parseRenameProbeInterval("not a duration"); got != rename.DefaultProbeInterval {
		t.Errorf(`unparseable input = %v, want %v`, got, rename.DefaultProbeInterval)
	}
}

// TestParseRenameProbeInterval_ClampsBelowFloor protects GitHub
// rate-limit budget against an operator setting an aggressively
// low interval (e.g. "1s" while debugging). The probe fires one
// GET per monitored repo per tick; <1m is never the right answer.
func TestParseRenameProbeInterval_ClampsBelowFloor(t *testing.T) {
	for _, raw := range []string{"1s", "30s", "59s"} {
		got := parseRenameProbeInterval(raw)
		if got != minRenameProbeInterval {
			t.Errorf(`parseRenameProbeInterval(%q) = %v, want %v (clamped to floor)`,
				raw, got, minRenameProbeInterval)
		}
	}
}

// TestParseRenameProbeInterval_HonoursValueAtOrAboveFloor pins the
// non-clamp path so the floor only kicks in for values that need
// it — operator-specified 1m or above passes through unchanged.
func TestParseRenameProbeInterval_HonoursValueAtOrAboveFloor(t *testing.T) {
	cases := []struct {
		raw  string
		want time.Duration
	}{
		{"1m", time.Minute},
		{"5m", 5 * time.Minute},
		{"2h", 2 * time.Hour},
	}
	for _, c := range cases {
		got := parseRenameProbeInterval(c.raw)
		if got != c.want {
			t.Errorf(`parseRenameProbeInterval(%q) = %v, want %v`, c.raw, got, c.want)
		}
	}
}

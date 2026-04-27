package main

import (
	"testing"
	"time"
)

func TestParseDiscoveryIntervalFallsBackToPollInterval(t *testing.T) {
	got := parseDiscoveryInterval("", "1m")
	if got != time.Minute {
		t.Fatalf("parseDiscoveryInterval(empty, 1m) = %v, want 1m", got)
	}
}

func TestParseDiscoveryIntervalPreservesExplicitInterval(t *testing.T) {
	got := parseDiscoveryInterval("30m", "1m")
	if got != 30*time.Minute {
		t.Fatalf("parseDiscoveryInterval(30m, 1m) = %v, want 30m", got)
	}
}

func TestParseDiscoveryIntervalNegativeFallsBackToPollInterval(t *testing.T) {
	got := parseDiscoveryInterval("-5m", "1m")
	if got != time.Minute {
		t.Fatalf("parseDiscoveryInterval(-5m, 1m) = %v, want 1m", got)
	}
}

func TestParseDiscoveryIntervalUsesPollDefaultWhenBothInvalid(t *testing.T) {
	want := parsePollInterval("nope")
	got := parseDiscoveryInterval("", "nope")
	if got != want {
		t.Fatalf("parseDiscoveryInterval(empty, invalid) = %v, want %v", got, want)
	}
}

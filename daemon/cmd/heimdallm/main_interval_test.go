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

func TestResolveRefinementTimeoutPrecedence(t *testing.T) {
	got := resolveRefinementTimeout("30m", "20m", "5m")
	if got != 30*time.Minute {
		t.Fatalf("resolveRefinementTimeout(30m, 20m, 5m) = %v, want 30m", got)
	}
}

func TestResolveRefinementTimeoutFallsBackToAgentThenGlobal(t *testing.T) {
	got := resolveRefinementTimeout("", "20m", "5m")
	if got != 5*time.Minute {
		t.Fatalf("resolveRefinementTimeout(empty, 20m, 5m) = %v, want 5m", got)
	}

	got = resolveRefinementTimeout("", "20m", "")
	if got != 20*time.Minute {
		t.Fatalf("resolveRefinementTimeout(empty, 20m, empty) = %v, want 20m", got)
	}
}

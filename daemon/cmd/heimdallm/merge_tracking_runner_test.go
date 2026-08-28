package main

import (
	"sync"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
)

func TestMergeTrackInterval_PrefersTheSectionAndFallsBack(t *testing.T) {
	fallback := 5 * time.Minute
	cases := map[string]struct {
		raw  string
		want time.Duration
	}{
		"explicit":     {"90s", 90 * time.Second},
		"unset":        {"", fallback},
		"unparseable":  {"soon", fallback},
		"non-positive": {"0s", fallback},
		"negative":     {"-1m", fallback},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := &config.Config{MergeTracking: config.MergeTrackingConfig{PollInterval: tc.raw}}
			if got := mergeTrackInterval(cfg, fallback); got != tc.want {
				t.Errorf("interval = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFirstNonEmptyString(t *testing.T) {
	if got := firstNonEmptyString("", "", "third"); got != "third" {
		t.Errorf("got %q, want third", got)
	}
	if got := firstNonEmptyString("first", "second"); got != "first" {
		t.Errorf("got %q, want first", got)
	}
	if got := firstNonEmptyString("", ""); got != "" {
		t.Errorf("got %q, want empty", got)
	}
	if got := firstNonEmptyString(); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestMergeTrackAgentSpec_ResolvesPerRepoThenGlobal(t *testing.T) {
	cfg := &config.Config{
		AI: config.AIConfig{
			Primary:  "claude",
			Fallback: "codex",
			Repos: map[string]config.RepoAI{
				"acme/widgets": {Primary: "gemini"},
			},
		},
		MergeTracking: config.MergeTrackingConfig{
			ResolveTimeout: "12m",
			ResolveEffort:  "medium",
			Repos: map[string]config.MergeTrackingOverride{
				"acme/widgets": {ResolveTimeout: "3m", ResolveEffort: "low"},
			},
		},
	}
	var mu sync.Mutex
	specFor := mergeTrackAgentSpec(&cfg, &mu)

	got := specFor("acme/widgets")
	if got.Primary != "gemini" {
		t.Errorf("primary = %q, want the per-repo agent", got.Primary)
	}
	// The repo set no fallback, so the global one applies.
	if got.Fallback != "codex" {
		t.Errorf("fallback = %q, want the global codex", got.Fallback)
	}
	if got.Timeout != 3*time.Minute {
		t.Errorf("timeout = %v, want the per-repo 3m", got.Timeout)
	}
	if got.Effort != "low" {
		t.Errorf("effort = %q, want the per-repo low", got.Effort)
	}

	other := specFor("acme/other")
	if other.Primary != "claude" || other.Timeout != 12*time.Minute || other.Effort != "medium" {
		t.Errorf("unoverridden repo = %+v, want the global values", other)
	}
}

// An unparseable timeout must leave the executor's own default in place rather
// than pinning the agent to a zero deadline.
func TestMergeTrackAgentSpec_IgnoresAnUnparseableTimeout(t *testing.T) {
	cfg := &config.Config{
		AI:            config.AIConfig{Primary: "claude"},
		MergeTracking: config.MergeTrackingConfig{ResolveTimeout: "half an hour"},
	}
	var mu sync.Mutex
	if got := mergeTrackAgentSpec(&cfg, &mu)("acme/widgets"); got.Timeout != 0 {
		t.Errorf("timeout = %v, want zero so the executor default applies", got.Timeout)
	}
}

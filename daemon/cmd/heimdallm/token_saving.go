package main

import (
	"slices"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/pipeline"
)

func pipelineTokenSaving(ts config.ResolvedTokenSaving) pipeline.TokenSaving {
	return pipeline.TokenSaving{
		IncrementalDiff: ts.IncrementalDiff,
		FilterNoise:     ts.FilterNoise,
		NoiseGlobs:      ts.NoiseGlobs,
		CompactPrompt:   ts.CompactPrompt,
	}
}

// limitedExploration applies the limit_exploration caps to an agent that has
// no explicit setting of its own. Only Claude exposes turn and effort knobs
// through the executor today; other agents pass through unchanged.
func limitedExploration(cli string, maxTurns int, effort string, limit bool) (int, string) {
	if !limit || cli != "claude" {
		return maxTurns, effort
	}
	if maxTurns == 0 {
		maxTurns = config.DefaultLimitedMaxTurns
	}
	if effort == "" {
		effort = config.DefaultLimitedEffort
	}
	return maxTurns, effort
}

// resolvedTokenSavingMap projects the effective global measures for GET
// /config. noise_globs_default tells the UI whether the list is the built-in
// one (so "reset to defaults" can be offered).
func resolvedTokenSavingMap(ts config.ResolvedTokenSaving) map[string]any {
	return map[string]any{
		"incremental_diff":    ts.IncrementalDiff,
		"filter_noise":        ts.FilterNoise,
		"compact_prompt":      ts.CompactPrompt,
		"limit_exploration":   ts.LimitExploration,
		"noise_globs":         ts.NoiseGlobs,
		"noise_globs_default": slices.Equal(ts.NoiseGlobs, config.DefaultNoiseGlobs),
	}
}

// tokenSavingOverrideMap projects an org/repo override: only the measures it
// sets, so the UI can tell "inherit" from an explicit on/off. nil when the
// scope overrides nothing.
func tokenSavingOverrideMap(t *config.TokenSavingConfig) map[string]any {
	if t == nil {
		return nil
	}
	out := map[string]any{}
	for key, v := range map[string]*bool{
		"incremental_diff":  t.IncrementalDiff,
		"filter_noise":      t.FilterNoise,
		"compact_prompt":    t.CompactPrompt,
		"limit_exploration": t.LimitExploration,
	} {
		if v != nil {
			out[key] = *v
		}
	}
	if t.NoiseGlobs != nil {
		out["noise_globs"] = t.NoiseGlobs
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

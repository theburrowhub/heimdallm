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

// limitedExploration applies the limit_exploration turn cap to an agent with
// no max_turns of its own. Only Claude exposes a turn knob through the
// executor today; other agents pass through unchanged. soft is true when the
// cap is this default rather than the operator's: the executor then retries a
// review that ran out of turns without the cap, so the measure saves tokens on
// most reviews without failing the long ones. Effort is never lowered: that
// would change review quality, not just how much a review explores.
func limitedExploration(cli string, maxTurns int, limit bool) (turns int, soft bool) {
	if !limit || cli != "claude" || maxTurns != 0 {
		return maxTurns, false
	}
	return config.DefaultLimitedMaxTurns, true
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

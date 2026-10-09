package config

import (
	"fmt"
	"strings"
)

// TokenSavingConfig switches the measures that cut the tokens a review costs.
// Every field is optional so an org or repo section can override one measure
// and inherit the rest; nil means "inherit" (and, at the global level, the
// default: on).
type TokenSavingConfig struct {
	// IncrementalDiff sends a re-review only the changes since the last
	// reviewed commit, when that commit is still an ancestor of HEAD and the
	// range has no merge commit; otherwise the full diff.
	IncrementalDiff *bool `toml:"incremental_diff,omitempty"`
	// FilterNoise drops lockfiles, vendored/built output, binaries and
	// generated files (NoiseGlobs plus generated-file markers) from the diff.
	FilterNoise *bool `toml:"filter_noise,omitempty"`
	// NoiseGlobs replaces the default list of paths FilterNoise drops.
	NoiseGlobs []string `toml:"noise_globs,omitempty"`
	// CompactPrompt uses the terse built-in template, trims long comment
	// bodies, skips bot chatter and caps the re-review context.
	CompactPrompt *bool `toml:"compact_prompt,omitempty"`
	// LimitExploration caps how long an agent with no explicit turn limit
	// explores: Claude gets --max-turns DefaultLimitedMaxTurns, and a review
	// that needs more turns is retried once without the cap. Effort is left
	// as configured.
	LimitExploration *bool `toml:"limit_exploration,omitempty"`
}

// DefaultNoiseGlobs mirrors pipeline.DefaultNoiseGlobs (config cannot import
// pipeline); TestDefaultNoiseGlobsMatchPipeline keeps them identical.
var DefaultNoiseGlobs = []string{
	"**/*.lock",
	"**/package-lock.json",
	"**/pnpm-lock.yaml",
	"**/go.sum",
	"**/vendor/**",
	"**/node_modules/**",
	"**/dist/**",
	"**/*.min.js",
	"**/*.min.css",
	"**/*.map",
	"**/*.pb.go",
	"**/*_generated.*",
	"**/*.g.dart",
	"**/*.snap",
}

// DefaultLimitedMaxTurns is the turn cap LimitExploration gives an agent
// with no max_turns of its own.
const DefaultLimitedMaxTurns = 20

// maxNoiseGlobs and maxNoiseGlobLen bound the operator-supplied glob list.
const (
	maxNoiseGlobs   = 200
	maxNoiseGlobLen = 256
)

// ResolvedTokenSaving is the effective set of measures for one repo.
type ResolvedTokenSaving struct {
	IncrementalDiff  bool
	FilterNoise      bool
	NoiseGlobs       []string
	CompactPrompt    bool
	LimitExploration bool
}

func overlayTokenSaving(base ResolvedTokenSaving, o *TokenSavingConfig) ResolvedTokenSaving {
	if o == nil {
		return base
	}
	if o.IncrementalDiff != nil {
		base.IncrementalDiff = *o.IncrementalDiff
	}
	if o.FilterNoise != nil {
		base.FilterNoise = *o.FilterNoise
	}
	if o.NoiseGlobs != nil {
		// Non-nil even when empty: noise_globs = [] means "match no paths",
		// which the pipeline must not mistake for "unset, use the defaults".
		base.NoiseGlobs = append([]string{}, o.NoiseGlobs...)
	}
	if o.CompactPrompt != nil {
		base.CompactPrompt = *o.CompactPrompt
	}
	if o.LimitExploration != nil {
		base.LimitExploration = *o.LimitExploration
	}
	return base
}

// TokenSavingForRepo resolves the measures for repo through repo > org >
// global. Every measure is on unless a level turns it off.
func (c *Config) TokenSavingForRepo(repo string) ResolvedTokenSaving {
	out := ResolvedTokenSaving{
		IncrementalDiff:  true,
		FilterNoise:      true,
		NoiseGlobs:       append([]string(nil), DefaultNoiseGlobs...),
		CompactPrompt:    true,
		LimitExploration: true,
	}
	out = overlayTokenSaving(out, &c.AI.TokenSaving)
	if org, _, ok := strings.Cut(repo, "/"); ok {
		if o, found := c.AI.Orgs[org]; found {
			out = overlayTokenSaving(out, o.TokenSaving)
		}
	}
	if r, found := c.AI.Repos[repo]; found {
		out = overlayTokenSaving(out, r.TokenSaving)
	}
	return out
}

func validateTokenSaving(t *TokenSavingConfig, path string) error {
	if t == nil {
		return nil
	}
	if len(t.NoiseGlobs) > maxNoiseGlobs {
		return fmt.Errorf("config: %s.noise_globs has %d entries, at most %d allowed", path, len(t.NoiseGlobs), maxNoiseGlobs)
	}
	for _, g := range t.NoiseGlobs {
		if strings.TrimSpace(g) == "" || len(g) > maxNoiseGlobLen {
			return fmt.Errorf("config: %s.noise_globs entries must be non-empty and at most %d characters", path, maxNoiseGlobLen)
		}
	}
	return nil
}

func (c *Config) validateTokenSaving() error {
	if err := validateTokenSaving(&c.AI.TokenSaving, "ai.token_saving"); err != nil {
		return err
	}
	for org, o := range c.AI.Orgs {
		if err := validateTokenSaving(o.TokenSaving, "ai.orgs."+org+".token_saving"); err != nil {
			return err
		}
	}
	for repo, r := range c.AI.Repos {
		if err := validateTokenSaving(r.TokenSaving, "ai.repos."+repo+".token_saving"); err != nil {
			return err
		}
	}
	return nil
}

package config

import (
	"strings"
	"testing"
)

func TestTokenSaving_DefaultsAllOn(t *testing.T) {
	cfg, err := loadReviewLimitsTOML(t, "")
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.TokenSavingForRepo("acme/api")
	if !got.IncrementalDiff || !got.FilterNoise || !got.CompactPrompt || !got.LimitExploration {
		t.Errorf("defaults = %+v, want every measure on", got)
	}
	if strings.Join(got.NoiseGlobs, ",") != strings.Join(DefaultNoiseGlobs, ",") {
		t.Errorf("noise globs = %v", got.NoiseGlobs)
	}
	got.NoiseGlobs[0] = "mutated"
	if DefaultNoiseGlobs[0] == "mutated" {
		t.Fatal("resolution must copy the default list")
	}
}

func TestTokenSaving_RepoOverOrgOverGlobal(t *testing.T) {
	cfg, err := loadReviewLimitsTOML(t, `
[ai.token_saving]
compact_prompt = false
noise_globs = ["**/*.lock"]

[ai.orgs.acme.token_saving]
incremental_diff = false
compact_prompt = true

[ai.repos."acme/api".token_saving]
filter_noise = false
noise_globs = ["gen/**"]
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	api := cfg.TokenSavingForRepo("acme/api")
	if api.IncrementalDiff || api.FilterNoise || !api.CompactPrompt || !api.LimitExploration {
		t.Errorf("acme/api = %+v", api)
	}
	if strings.Join(api.NoiseGlobs, ",") != "gen/**" {
		t.Errorf("repo glob list must replace the inherited one: %v", api.NoiseGlobs)
	}
	web := cfg.TokenSavingForRepo("acme/web")
	if web.IncrementalDiff || !web.CompactPrompt || strings.Join(web.NoiseGlobs, ",") != "**/*.lock" {
		t.Errorf("acme/web = %+v", web)
	}
	other := cfg.TokenSavingForRepo("other/repo")
	if !other.IncrementalDiff || other.CompactPrompt {
		t.Errorf("other/repo = %+v", other)
	}
}

func TestTokenSaving_RejectsBadGlobs(t *testing.T) {
	long := strings.Repeat("a", maxNoiseGlobLen+1)
	for name, body := range map[string]string{
		"blank global": "[ai.token_saving]\nnoise_globs = [\" \"]\n",
		"too long org": "[ai.orgs.acme.token_saving]\nnoise_globs = [\"" + long + "\"]\n",
		"blank repo":   "[ai.repos.\"acme/api\".token_saving]\nnoise_globs = [\"\"]\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := loadReviewLimitsTOML(t, body); err == nil || !strings.Contains(err.Error(), "noise_globs") {
				t.Fatalf("err = %v", err)
			}
		})
	}
	var many strings.Builder
	many.WriteString("[ai.token_saving]\nnoise_globs = [")
	for i := 0; i <= maxNoiseGlobs; i++ {
		if i > 0 {
			many.WriteString(",")
		}
		many.WriteString(`"x"`)
	}
	many.WriteString("]\n")
	if _, err := loadReviewLimitsTOML(t, many.String()); err == nil {
		t.Fatal("too many globs must be rejected")
	}
}

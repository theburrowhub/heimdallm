package config_test

import (
	"testing"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/pipeline"
)

// TestResolvedReviewGuards_AlignedWithGateConfig ensures config.ResolvedReviewGuards
// stays field-for-field compatible with pipeline.GateConfig. The two structs are
// kept aligned so callers can do pipeline.GateConfig(cfg.ReviewGuards(login))
// without importing pipeline into config (which would cycle via github).
//
// The cast below fails to compile if a field is added, removed, or reordered
// in either struct — which is exactly the drift we want to catch.
func TestResolvedReviewGuards_AlignedWithGateConfig(t *testing.T) {
	src := config.ResolvedReviewGuards{
		SkipDrafts:     true,
		SkipSelfAuthor: true,
		BotLogin:       "bot",
	}
	gc := pipeline.GateConfig(src)
	if gc.SkipDrafts != src.SkipDrafts ||
		gc.SkipSelfAuthor != src.SkipSelfAuthor ||
		gc.BotLogin != src.BotLogin {
		t.Errorf("field mismatch after cast: src=%+v gc=%+v", src, gc)
	}
}

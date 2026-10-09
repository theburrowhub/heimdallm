package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/theburrowhub/heimdallm/cli/internal/api"
)

func TestPrintTokenStats(t *testing.T) {
	var buf bytes.Buffer
	printTokenStats(&buf, api.TokenStats{
		Reviews: 4, EstimatedReviews: 1, InputTokens: 4000, OutputTokens: 400,
		CacheReadTokens: 900, CostUSD: 0.42, AvgPromptBytes: 3072, TurnCapRetries: 2,
	})
	out := buf.String()
	for _, want := range []string{
		"Tokens (last 7 days):", "Reviews:      4 (1 estimated)", "Input:        4000",
		"Output:       400", "Cache reads:  900", "Per review:   1100 (includes estimates)", "Avg prompt:   3.0 KB", "Cost:         $0.42 (reported reviews only)",
		"Turn-cap retries: 2",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	buf.Reset()
	printTokenStats(&buf, api.TokenStats{Reviews: 1, InputTokens: 10})
	if strings.Contains(buf.String(), "Cost") || strings.Contains(buf.String(), "Cache") || strings.Contains(buf.String(), "estimate") || strings.Contains(buf.String(), "Turn-cap") {
		t.Errorf("zero fields must be omitted:\n%s", buf.String())
	}
	buf.Reset()
	printTokenStats(&buf, api.TokenStats{})
	if buf.Len() != 0 {
		t.Errorf("no reviews must print nothing, got %q", buf.String())
	}
}

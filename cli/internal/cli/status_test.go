package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/theburrowhub/heimdallm/cli/internal/api"
)

// GetHealth treats 503 as reachable so the dashboard can still read the version.
// The status command must not turn that into a flat "online": a degraded daemon
// is exactly the case an operator runs `status` to find.
func TestStatusLine(t *testing.T) {
	cases := []struct {
		name string
		h    *api.Health
		want string
	}{
		{"healthy", &api.Health{Status: "ok"}, "online"},
		{"degraded", &api.Health{Status: "degraded"}, "degraded"},
		{"unknown word passes through", &api.Health{Status: "starting"}, "starting"},
		{"no status reported", &api.Health{}, "online (status unreported)"},
		// The guard must evaluate the SANITISED value: a status made only of
		// non-printable bytes is non-empty, so a raw `h.Status == ""` check let it
		// through and then printed the sanitised "" — a blank Status line.
		{"only non-printable bytes", &api.Health{Status: "\n"}, "online (status unreported)"},
		{"control bytes around ok", &api.Health{Status: "\nok\r"}, "online"},
		{"nil payload", nil, "online (status unreported)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := statusLine(tc.h); got != tc.want {
				t.Errorf("statusLine() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPrintReviewLimits(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	var buf bytes.Buffer
	printReviewLimits(&buf, []api.ReviewLimitStatus{
		{Kind: "global", Windows: []api.ReviewLimitWindow{
			{Window: "minute", Used: 1, Limit: 1, ResetAt: now.Add(40 * time.Second)},
			{Window: "day", Used: 3, Limit: 50},
		}},
		{Kind: "repo", Key: "acme/api\x1b[31m", Windows: []api.ReviewLimitWindow{
			{Window: "hour", Used: 2, Limit: 2},
		}},
		{Kind: "agent", Key: "codex"}, // no limited window: skipped
	}, now)
	out := buf.String()
	for _, want := range []string{
		"Review limits:",
		"all reviews: 1/1 per minute (full, next slot in 40s), 3/50 per day",
		"acme/api",
		"2/2 per hour (full)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("control bytes reached the terminal: %q", out)
	}
	if strings.Contains(out, "agent codex") {
		t.Errorf("a budget with no windows must not print: %s", out)
	}

	buf.Reset()
	printReviewLimits(&buf, nil, now)
	if buf.Len() != 0 {
		t.Errorf("no budgets must print nothing, got %q", buf.String())
	}
}

package cli

import (
	"testing"

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

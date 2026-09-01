package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/theburrowhub/heimdallm/cli/internal/api"
)

func joined(lines []string) string { return strings.Join(lines, "\n") }

// A plain single-daemon install is not an error state; the tab has to say so.
func TestInstanceLinesWithoutAHub(t *testing.T) {
	d := &Dashboard{}
	out := joined(d.buildInstanceLines())

	if !strings.Contains(out, "not a cluster hub") {
		t.Errorf("lines = %q, want an explanation rather than an error", out)
	}
	if !strings.Contains(out, "config.toml") {
		t.Errorf("lines = %q, want it to say how to enable one", out)
	}
}

func TestInstanceLinesEmptyRegistry(t *testing.T) {
	d := &Dashboard{registry: &api.ClusterRegistry{SelfID: "hub-1"}}
	if got := joined(d.buildInstanceLines()); !strings.Contains(got, "No instances registered") {
		t.Errorf("lines = %q", got)
	}
}

func TestInstanceLinesRenderFleet(t *testing.T) {
	d := &Dashboard{registry: &api.ClusterRegistry{
		SelfID:   "hub-1",
		SelfName: "Local hub",
		Instances: []api.Instance{
			{
				ID: "hub-1", Name: "Local hub", BaseURL: "http://127.0.0.1:7842",
				Enabled: true, Self: true, IsFallback: true, InPool: true,
				AssignedRepos: 2,
				State:         &api.InstanceState{Reachable: true, Version: "0.9.0"},
			},
			{
				ID: "srv-a", Name: "Server A", BaseURL: "http://10.0.0.11:7842",
				Enabled: true, AssignedRepos: 1,
				State: &api.InstanceState{
					Reachable:           false,
					LastError:           "connection refused",
					ConsecutiveFailures: 3,
				},
			},
		},
	}}

	out := joined(d.buildInstanceLines())
	for _, want := range []string{
		"Local hub", "hub-1", "reachable", "0.9.0", "2 repos",
		"[hub,default,pool]",
		"srv-a", "UNREACHABLE", "connection refused", "(3 failed probes)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("lines are missing %q:\n%s", want, out)
		}
	}
}

func TestInstanceStatusLabel(t *testing.T) {
	tests := map[string]struct {
		inst api.Instance
		want string
	}{
		"token error": {
			api.Instance{Enabled: true, TokenError: "unset"},
			"token error",
		},
		"disabled": {api.Instance{Enabled: false}, "disabled"},
		// A never-probed instance is not "down"; saying so right after a hub
		// restart would be misleading.
		"never probed": {api.Instance{Enabled: true}, "not probed"},
		"reachable": {
			api.Instance{Enabled: true, State: &api.InstanceState{Reachable: true}},
			"reachable",
		},
		"unreachable": {
			api.Instance{Enabled: true, State: &api.InstanceState{}},
			"UNREACHABLE",
		},
	}
	for name, tt := range tests {
		if got := instanceStatusLabel(tt.inst); got != tt.want {
			t.Errorf("%s: instanceStatusLabel = %q, want %q", name, got, tt.want)
		}
	}
}

func TestInstanceProblemOnlyWhenBroken(t *testing.T) {
	healthy := api.Instance{Enabled: true, State: &api.InstanceState{Reachable: true}}
	if got := instanceProblem(healthy); got != "" {
		t.Errorf("instanceProblem(healthy) = %q, want empty", got)
	}
	if got := instanceProblem(api.Instance{Enabled: true}); got != "" {
		t.Errorf("instanceProblem(unprobed) = %q, want empty", got)
	}
	tokenBroken := api.Instance{Enabled: true, TokenError: "env var unset"}
	if got := instanceProblem(tokenBroken); !strings.Contains(got, "env var unset") {
		t.Errorf("instanceProblem(token) = %q", got)
	}
}

// Strings from a remote daemon are semi-trusted: an ANSI escape rendered in a
// terminal is a real injection vector.
func TestInstanceLinesSanitizeRemoteStrings(t *testing.T) {
	d := &Dashboard{registry: &api.ClusterRegistry{
		SelfID: "hub-1",
		Instances: []api.Instance{{
			ID: "srv-a", Name: "evil\x1b[31mred", Enabled: true,
			State: &api.InstanceState{LastError: "boom\nsecond line"},
		}},
	}}
	out := joined(d.buildInstanceLines())
	if strings.Contains(out, "\x1b") {
		t.Errorf("an escape sequence survived into the rendered output:\n%q", out)
	}
	if strings.Count(out, "second line") > 0 && strings.Contains(out, "boom\nsecond line") {
		t.Errorf("an embedded newline survived:\n%q", out)
	}
}

// Every tab needs a numeric jump: the previous 1-6 mapping skipped Merges
// outright, so that tab was unreachable by number while the help advertised it.
func TestEveryTabHasANumericJump(t *testing.T) {
	if len(tabNames) > 9 {
		t.Fatalf("there are %d tabs, more than the single-digit jump keys can address", len(tabNames))
	}
	for i, name := range tabNames {
		key := string(rune('1' + i))
		d := &Dashboard{activeTab: tabActivity, cursor: 7}

		d.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})

		if d.activeTab != tab(i) {
			t.Errorf("key %q selected tab %d, want %d (%s)", key, d.activeTab, i, name)
		}
		if d.cursor != 0 {
			t.Errorf("key %q left the cursor at %d, want it reset", key, d.cursor)
		}
	}
}

// A digit past the last tab must be ignored rather than selecting a tab that
// does not exist.
func TestNumericJumpIgnoresOutOfRangeDigits(t *testing.T) {
	d := &Dashboard{activeTab: tabPRs}
	d.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("9")})
	if d.activeTab != tabPRs {
		t.Errorf("key 9 changed the tab to %d, want it ignored", d.activeTab)
	}
}

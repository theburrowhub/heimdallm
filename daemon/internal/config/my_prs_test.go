package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
)

func loadTOML(t *testing.T, body string) (*config.Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[ai]\nprimary = \"claude\"\n"+body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return config.Load(path)
}

func TestParseHumanDuration(t *testing.T) {
	cases := map[string]time.Duration{
		"0":     0,
		"90m":   90 * time.Minute,
		"12h":   12 * time.Hour,
		"1h30m": 90 * time.Minute,
		"3d":    72 * time.Hour,
		"1.5d":  36 * time.Hour,
		" 2d ":  48 * time.Hour,
		"45s":   45 * time.Second,
		"0.5d":  12 * time.Hour,
	}
	for in, want := range cases {
		got, err := config.ParseHumanDuration(in)
		if err != nil {
			t.Errorf("ParseHumanDuration(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseHumanDuration(%q) = %v, want %v", in, got, want)
		}
	}
	for _, bad := range []string{"", "3", "d", "3days", "3w", "-d", "999999999d"} {
		if _, err := config.ParseHumanDuration(bad); err == nil {
			t.Errorf("ParseHumanDuration(%q) should fail", bad)
		}
	}
}

// A config that never mentions [my_prs] watches the operator's PRs with the
// documented defaults.
func TestMyPRs_AbsentSectionDefaults(t *testing.T) {
	cfg, err := loadTOML(t, "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.MyPRsEnabled() || !cfg.MyPRsIncludeAssigned() || !cfg.MyPRsDigestEnabled() {
		t.Errorf("enabled/include_assigned/digest_enabled default to true: %+v", cfg.MyPRs)
	}
	if cfg.MyPRs.NotifyTransitions {
		t.Error("notify_transitions defaults to false")
	}
	if got := cfg.MyPRsStaleAfter(); got != 72*time.Hour {
		t.Errorf("stale_after = %v, want 3d", got)
	}
	if cfg.MyPRs.DigestTime != "10:00" {
		t.Errorf("digest_time = %q, want 10:00", cfg.MyPRs.DigestTime)
	}
	eff := cfg.EffectiveMergeTrackingForRepo("acme/widgets")
	if !eff.Enabled || !eff.WatchOnly {
		t.Errorf("an untouched repo is watched: %+v", eff)
	}
}

func TestMyPRs_ExplicitValues(t *testing.T) {
	cfg, err := loadTOML(t, `
[my_prs]
enabled = false
include_assigned = false
stale_after = "0"
notify_transitions = true
digest_enabled = false
digest_time = "08:30"
`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.MyPRsEnabled() || cfg.MyPRsIncludeAssigned() || cfg.MyPRsDigestEnabled() {
		t.Errorf("explicit false must stick: %+v", cfg.MyPRs)
	}
	if !cfg.MyPRs.NotifyTransitions || cfg.MyPRs.DigestTime != "08:30" {
		t.Errorf("unexpected values: %+v", cfg.MyPRs)
	}
	if got := cfg.MyPRsStaleAfter(); got != 0 {
		t.Errorf(`stale_after "0" disables detection, got %v`, got)
	}
	if eff := cfg.EffectiveMergeTrackingForRepo("acme/widgets"); eff.Enabled || eff.WatchOnly {
		t.Errorf("with both features off the repo is not tracked: %+v", eff)
	}
	if eff := cfg.EffectiveMergeTrackingGlobal(); eff.Enabled {
		t.Errorf("global effective config must be off too: %+v", eff)
	}
}

// Merge tracking wins where it is enabled: the repo keeps its own automation
// and is not downgraded to watch-only.
func TestMyPRs_MergeTrackingWinsWhereEnabled(t *testing.T) {
	cfg, err := loadTOML(t, `
[merge_tracking]
merge = true

[merge_tracking.repos."acme/auto"]
enabled = true

[my_prs]
include_assigned = false
`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	auto := cfg.EffectiveMergeTrackingForRepo("acme/auto")
	if auto.WatchOnly || !auto.Merge {
		t.Errorf("a merge-tracking repo keeps its automation: %+v", auto)
	}
	watched := cfg.EffectiveMergeTrackingForRepo("acme/other")
	if !watched.WatchOnly || watched.Merge || watched.IncludeAssigned {
		t.Errorf("other repos are watch-only with my_prs.include_assigned: %+v", watched)
	}
	global := cfg.EffectiveMergeTrackingGlobal()
	if !global.WatchOnly || global.Merge {
		t.Errorf("global effective config is watch-only when merge tracking is off globally: %+v", global)
	}
}

func TestMyPRs_ValidationRejectsBadValues(t *testing.T) {
	for name, body := range map[string]string{
		"stale_after":          "[my_prs]\nstale_after = \"three days\"\n",
		"stale_after_overflow": "[my_prs]\nstale_after = \"999999999d\"\n",
		"digest_time":          "[my_prs]\ndigest_time = \"25:00\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadTOML(t, body)
			if err == nil {
				t.Fatal("invalid value must fail at load")
			}
			if field := strings.TrimSuffix(name, "_overflow"); !strings.Contains(err.Error(), "my_prs."+field) {
				t.Errorf("error should name the field: %v", err)
			}
		})
	}
}

func TestParseDigestTime(t *testing.T) {
	h, m, err := config.ParseDigestTime("07:05")
	if err != nil || h != 7 || m != 5 {
		t.Errorf("ParseDigestTime(07:05) = %d, %d, %v", h, m, err)
	}
	if _, _, err := config.ParseDigestTime("7pm"); err == nil {
		t.Error("7pm is not HH:MM")
	}
}

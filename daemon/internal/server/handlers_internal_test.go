package server

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Regression coverage for #75: the /logs SSE stream reads from
// daemonLogPath(), which must resolve to wherever setupLogging() in
// cmd/heimdallm actually wrote the file. Priorities:
//
//  1. $HEIMDALLM_DATA_DIR/heimdallm.log
//  2. /data/heimdallm.log (Docker convention)
//  3. Native fallback (macOS LaunchAgent path or XDG)

func withEnv(t *testing.T, key, value string) {
	t.Helper()
	prev, had := os.LookupEnv(key)
	if value == "" {
		_ = os.Unsetenv(key)
	} else {
		_ = os.Setenv(key, value)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, prev)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

func TestDaemonLogPath_HeimdallmDataDirWins(t *testing.T) {
	dir := t.TempDir()
	withEnv(t, "HEIMDALLM_DATA_DIR", dir)
	withEnv(t, "XDG_STATE_HOME", "") // ensure we are not falling through to it

	got := daemonLogPath()
	want := filepath.Join(dir, DaemonLogFileName)
	if got != want {
		t.Fatalf("daemonLogPath() = %q, want %q", got, want)
	}
}

func TestDaemonLogPath_FallsBackToNativeWhenDataDirUnset(t *testing.T) {
	withEnv(t, "HEIMDALLM_DATA_DIR", "")
	withEnv(t, "XDG_STATE_HOME", "")

	got := daemonLogPath()

	// The native fallback depends on GOOS. We assert on the *shape* of
	// the path rather than a hard-coded string so the test stays
	// portable when run on both macOS and Linux CI.
	if runtime.GOOS == "darwin" {
		// macOS: ~/Library/Logs/heimdallm/heimdallm-daemon-error.log when the
		// LaunchAgent stderr file exists, otherwise the data-dir log the
		// daemon itself writes. /data may also exist on the host (unusual on
		// macOS dev machines but possible if something else mounted it).
		dockerPath := filepath.Join("/data", DaemonLogFileName)
		if _, err := os.Stat("/data"); err == nil {
			if got != dockerPath {
				t.Fatalf("with /data present, daemonLogPath() = %q, want %q", got, dockerPath)
			}
			return
		}
		// Mirror the recency rule against the real host state: the fresher
		// of the two candidate files wins; only-existing wins; neither means
		// the data-dir path is reported. The hermetic stub tests below carry
		// the behavioral coverage — this test just keeps daemonLogPath()'s
		// real probe wired to that rule.
		launchd, dataLog := darwinLogCandidates(t)
		want := dataLog
		if lInfo, lErr := os.Stat(launchd); lErr == nil {
			dInfo, dErr := os.Stat(dataLog)
			if dErr != nil || !dInfo.ModTime().After(lInfo.ModTime()) {
				want = launchd
			}
		}
		if got != want {
			t.Fatalf("daemonLogPath() = %q, want %q", got, want)
		}
		return
	}

	// Linux: inside the test-docker sandbox there is no /data mount,
	// and HOME points at /tmp/home. Assert we got the XDG/HOME path.
	dockerPath := filepath.Join("/data", DaemonLogFileName)
	if _, err := os.Stat("/data"); err == nil {
		if got != dockerPath {
			t.Fatalf("with /data present, daemonLogPath() = %q, want %q", got, dockerPath)
		}
		return
	}
	if !strings.HasSuffix(got, filepath.Join("heimdallm", DaemonLogFileName)) {
		t.Fatalf("daemonLogPath() = %q, want to end in heimdallm/%s", got, DaemonLogFileName)
	}
}

func TestValidateCanonicalConfigPatchKeys(t *testing.T) {
	tests := []struct {
		name    string
		patch   map[string]any
		wantErr bool
	}{
		{
			name: "canonical global agent tree",
			patch: map[string]any{
				"ai": map[string]any{
					"agents": map[string]any{
						"claude": map[string]any{"extra_flags": "--verbose"},
					},
				},
			},
		},
		{
			name: "repo and org agent trees are unsupported",
			patch: map[string]any{
				"ai": map[string]any{
					"repos": map[string]any{
						"org/repo": map[string]any{
							"agents": map[string]any{
								"codex": map[string]any{"approval_mode": "on-request"},
							},
						},
					},
					"orgs": map[string]any{
						"org": map[string]any{
							"agents": map[string]any{
								"gemini": map[string]any{"model": "gemini-2.5-pro"},
							},
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "dangerous false reduces privilege",
			patch: map[string]any{
				"ai": map[string]any{
					"agents": map[string]any{
						"claude": map[string]any{"dangerously_skip_perms": false},
					},
				},
			},
		},
		{
			name: "dangerous true cannot enable privilege",
			patch: map[string]any{
				"ai": map[string]any{
					"agents": map[string]any{
						"claude": map[string]any{"dangerously_skip_perms": true},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "dangerous leaf alias rejected",
			patch: map[string]any{
				"ai": map[string]any{
					"agents": map[string]any{
						"claude": map[string]any{"DANGEROUSLY_SKIP_PERMS": true},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "unsafe extra flags rejected in otherwise schema-ignored repo tree",
			patch: map[string]any{
				"ai": map[string]any{
					"repos": map[string]any{
						"org/repo": map[string]any{
							"agents": map[string]any{
								"codex": map[string]any{"extra_flags": "--sandbox danger-full-access"},
							},
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "unsafe approval rejected in otherwise schema-ignored org tree",
			patch: map[string]any{
				"ai": map[string]any{
					"orgs": map[string]any{
						"org": map[string]any{
							"agents": map[string]any{
								"gemini": map[string]any{"approval_mode": "yolo"},
							},
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "unknown CLI rejected",
			patch: map[string]any{
				"ai": map[string]any{
					"agents": map[string]any{
						"future-cli": map[string]any{"extra_flags": "--model safe"},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "non-object agent rejected",
			patch: map[string]any{
				"ai": map[string]any{
					"agents": map[string]any{
						"claude": "not-an-object",
					},
				},
			},
			wantErr: true,
		},
		{
			name: "non-object structural node rejected",
			patch: map[string]any{
				"ai": map[string]any{
					"agents": "not-an-object",
				},
			},
			wantErr: true,
		},
		{
			name: "top-level AI alias rejected",
			patch: map[string]any{
				"AI": map[string]any{},
			},
			wantErr: true,
		},
		{
			name: "Agents alias rejected",
			patch: map[string]any{
				"ai": map[string]any{
					"Agents": map[string]any{},
				},
			},
			wantErr: true,
		},
		{
			name: "Repos alias rejected",
			patch: map[string]any{
				"ai": map[string]any{
					"Repos": map[string]any{},
				},
			},
			wantErr: true,
		},
		{
			name: "nested Agents alias rejected",
			patch: map[string]any{
				"ai": map[string]any{
					"orgs": map[string]any{
						"org": map[string]any{
							"Agents": map[string]any{},
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "CLI alias rejected",
			patch: map[string]any{
				"ai": map[string]any{
					"agents": map[string]any{
						"Codex": map[string]any{},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "agent field alias rejected",
			patch: map[string]any{
				"ai": map[string]any{
					"agents": map[string]any{
						"codex": map[string]any{"Extra_Flags": "--quiet"},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "autonomous repo agents rejected",
			patch: map[string]any{
				"autonomous": map[string]any{
					"repos": map[string]any{
						"org/repo": map[string]any{
							"agents": map[string]any{
								"codex": map[string]any{
									"extra_flags": "--sandbox danger-full-access",
								},
							},
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "autonomous agents alias rejected",
			patch: map[string]any{
				"autonomous": map[string]any{
					"orgs": map[string]any{
						"org": map[string]any{
							"Agents": map[string]any{},
						},
					},
				},
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCanonicalConfigPatchKeys(tc.patch)
			if tc.wantErr && err == nil {
				t.Fatal("expected casing validation error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected casing validation error: %v", err)
			}
		})
	}
}

func TestRejectUnsupportedScopedAgentPatchKeys(t *testing.T) {
	tests := []struct {
		name    string
		patch   map[string]any
		wantErr bool
	}{
		{
			name: "no agent tree",
			patch: map[string]any{
				"primary": "codex",
			},
		},
		{
			name: "canonical but unsupported",
			patch: map[string]any{
				"agents": map[string]any{
					"claude": map[string]any{"permission_mode": "acceptEdits"},
				},
			},
			wantErr: true,
		},
		{
			name: "case variant",
			patch: map[string]any{
				"Agents": map[string]any{
					"claude": map[string]any{},
				},
			},
			wantErr: true,
		},
		{
			name: "unsafe extra flags",
			patch: map[string]any{
				"agents": map[string]any{
					"opencode": map[string]any{"extra_flags": "--auto"},
				},
			},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := rejectUnsupportedScopedAgentPatchKeys(tc.patch, "ai.repos.org/repo")
			if tc.wantErr && err == nil {
				t.Fatal("expected scoped agent rejection")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected scoped rejection: %v", err)
			}
		})
	}
}

func darwinLogCandidates(t *testing.T) (launchd, dataLog string) {
	t.Helper()
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Logs", "heimdallm", "heimdallm-daemon-error.log"),
		filepath.Join(home, ".local", "share", "heimdallm", DaemonLogFileName)
}

func TestDaemonLogPath_DarwinPrefersLaunchAgentLogWhenActive(t *testing.T) {
	withEnv(t, "HEIMDALLM_DATA_DIR", "")
	if _, err := os.Stat("/data"); err == nil {
		t.Skip("/data exists on this host — Docker path wins, which is correct")
	}
	launchd, dataLog := darwinLogCandidates(t)

	// LaunchAgent log is the only file, or is at least as fresh as the
	// data-dir log (both are written while running under the LaunchAgent).
	mtimes := map[string]int64{launchd: 2000}
	probe := func(p string) (time.Time, bool) {
		sec, ok := mtimes[p]
		return time.Unix(sec, 0), ok
	}
	if got := daemonLogPathFor("darwin", probe); got != launchd {
		t.Fatalf("launchd-only: daemonLogPathFor(darwin) = %q, want %q", got, launchd)
	}
	mtimes[dataLog] = 1000
	if got := daemonLogPathFor("darwin", probe); got != launchd {
		t.Fatalf("launchd fresher: daemonLogPathFor(darwin) = %q, want %q", got, launchd)
	}
}

func TestDaemonLogPath_DarwinFallsBackToDataDirLogWithoutLaunchAgent(t *testing.T) {
	// When the daemon is launched directly by the app (no LaunchAgent), the
	// launchd stderr file never exists — but setupLogging always mirrors slog
	// into <dataDir>/heimdallm.log, so the /logs stream must read that instead
	// of reporting "log file not found".
	withEnv(t, "HEIMDALLM_DATA_DIR", "")
	if _, err := os.Stat("/data"); err == nil {
		t.Skip("/data exists on this host — Docker path wins, which is correct")
	}
	_, dataLog := darwinLogCandidates(t)

	got := daemonLogPathFor("darwin", func(string) (time.Time, bool) { return time.Time{}, false })
	if got != dataLog {
		t.Fatalf("daemonLogPathFor(darwin) = %q, want data-dir fallback %q", got, dataLog)
	}
}

func TestDaemonLogPath_DarwinPrefersFresherDataDirLogOverStaleLaunchAgent(t *testing.T) {
	// The LaunchAgent stderr file survives after the agent is uninstalled or
	// bypassed (daemon later launched directly by the app). Existence alone
	// does not make it the active sink: the file the daemon is writing NOW is
	// the fresher one, so /logs must serve by recency, not presence.
	withEnv(t, "HEIMDALLM_DATA_DIR", "")
	if _, err := os.Stat("/data"); err == nil {
		t.Skip("/data exists on this host — Docker path wins, which is correct")
	}
	launchd, dataLog := darwinLogCandidates(t)

	mtimes := map[string]int64{launchd: 1000, dataLog: 2000}
	probe := func(p string) (time.Time, bool) {
		sec, ok := mtimes[p]
		return time.Unix(sec, 0), ok
	}
	if got := daemonLogPathFor("darwin", probe); got != dataLog {
		t.Fatalf("stale launchd log: daemonLogPathFor(darwin) = %q, want fresher data-dir log %q", got, dataLog)
	}
}

func TestDaemonLogPath_XDGStateHomeUsedWhenSet(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("XDG path only used on non-darwin when HEIMDALLM_DATA_DIR is unset")
	}
	withEnv(t, "HEIMDALLM_DATA_DIR", "")
	xdg := t.TempDir()
	withEnv(t, "XDG_STATE_HOME", xdg)

	// When /data exists (Docker mode), it takes precedence over XDG —
	// matches the real setupLogging behaviour because dataDir() returns
	// "/data" in that situation. Skip the strict assertion in that case.
	if _, err := os.Stat("/data"); err == nil {
		t.Skip("/data exists on this host — Docker path wins over XDG, which is correct")
	}

	got := daemonLogPath()
	want := filepath.Join(xdg, "heimdallm", DaemonLogFileName)
	if got != want {
		t.Fatalf("daemonLogPath() = %q, want %q", got, want)
	}
}

package server

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
		// macOS: ~/Library/Logs/heimdallm/heimdallm-daemon-error.log,
		// unless /data happens to exist on the host (unusual on macOS
		// dev machines but possible if something else mounted it).
		dockerPath := filepath.Join("/data", DaemonLogFileName)
		if _, err := os.Stat("/data"); err == nil {
			if got != dockerPath {
				t.Fatalf("with /data present, daemonLogPath() = %q, want %q", got, dockerPath)
			}
			return
		}
		if !strings.Contains(got, filepath.Join("Library", "Logs", "heimdallm")) {
			t.Fatalf("daemonLogPath() = %q, want macOS LaunchAgent path", got)
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

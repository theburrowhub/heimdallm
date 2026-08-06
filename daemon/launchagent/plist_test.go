package launchagent_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/heimdallm/daemon/launchagent"
)

const plistName = "com.heimdallm.daemon.plist"

func installFakeLaunchctl(t *testing.T, exitCode int, output string) string {
	t.Helper()

	binDir := t.TempDir()
	capturePath := filepath.Join(t.TempDir(), "launchctl-args")
	const script = `#!/bin/sh
printf '%s\n' "$@" > "$HEIMDALLM_LAUNCHCTL_CAPTURE"
printf '%s' "$HEIMDALLM_LAUNCHCTL_OUTPUT" >&2
exit "$HEIMDALLM_LAUNCHCTL_EXIT"
`
	if err := os.WriteFile(filepath.Join(binDir, "launchctl"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake launchctl: %v", err)
	}

	// Use an exclusive PATH so this test can never reach the real launchctl on
	// the macOS CI runner. The fake uses only /bin/sh built-ins.
	t.Setenv("PATH", binDir)
	t.Setenv("HEIMDALLM_LAUNCHCTL_CAPTURE", capturePath)
	t.Setenv("HEIMDALLM_LAUNCHCTL_OUTPUT", output)
	t.Setenv("HEIMDALLM_LAUNCHCTL_EXIT", strconv.Itoa(exitCode))
	return capturePath
}

func setupHome(t *testing.T) (home, plistPath, logDir string) {
	t.Helper()

	home = t.TempDir()
	t.Setenv("HOME", home)
	plistPath = filepath.Join(home, "Library", "LaunchAgents", plistName)
	logDir = filepath.Join(home, "Library", "Logs", "heimdallm")
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o700); err != nil {
		t.Fatalf("create LaunchAgents directory: %v", err)
	}
	return home, plistPath, logDir
}

func requireCapturedArgs(t *testing.T, capturePath string, want ...string) {
	t.Helper()

	data, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read captured launchctl arguments: %v", err)
	}
	if got, expected := string(data), strings.Join(want, "\n")+"\n"; got != expected {
		t.Fatalf("launchctl arguments = %q, want %q", got, expected)
	}
}

func requireMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode for %s = %04o, want %04o", path, got, want)
	}
}

func TestInstallWritesPlistAndBootstraps(t *testing.T) {
	capturePath := installFakeLaunchctl(t, 0, "")
	home, plistPath, logDir := setupHome(t)
	binaryPath := filepath.Join(home, "Applications", "Heimdallm.app", "Contents", "MacOS", "heimdalld")

	if err := launchagent.Install(binaryPath); err != nil {
		t.Fatalf("Install: %v", err)
	}

	plist, err := os.ReadFile(plistPath)
	if err != nil {
		t.Fatalf("read plist: %v", err)
	}
	for _, want := range []string{
		"<string>com.heimdallm.daemon</string>",
		"<string>" + binaryPath + "</string>",
		"<string>" + filepath.Join(logDir, "heimdallm-daemon.log") + "</string>",
		"<string>" + filepath.Join(logDir, "heimdallm-daemon-error.log") + "</string>",
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
	} {
		if !strings.Contains(string(plist), want) {
			t.Errorf("plist does not contain %q:\n%s", want, plist)
		}
	}
	requireMode(t, plistPath, 0o600)
	requireMode(t, logDir, 0o700)
	requireCapturedArgs(t, capturePath,
		"bootstrap",
		fmt.Sprintf("gui/%d", os.Getuid()),
		plistPath,
	)
}

func TestInstallReportsContextualFailures(t *testing.T) {
	t.Run("logs directory", func(t *testing.T) {
		installFakeLaunchctl(t, 0, "")
		home := filepath.Join(t.TempDir(), "home-file")
		if err := os.WriteFile(home, []byte("not a directory"), 0o600); err != nil {
			t.Fatalf("write fake home: %v", err)
		}
		t.Setenv("HOME", home)

		err := launchagent.Install("/Applications/Heimdallm.app/heimdalld")
		if err == nil || !strings.Contains(err.Error(), "launchagent: mkdir logs:") {
			t.Fatalf("Install error = %v, want contextual logs-directory error", err)
		}
	})

	t.Run("plist creation", func(t *testing.T) {
		installFakeLaunchctl(t, 0, "")
		_, plistPath, _ := setupHome(t)
		if err := os.Mkdir(plistPath, 0o700); err != nil {
			t.Fatalf("create directory at plist path: %v", err)
		}

		err := launchagent.Install("/Applications/Heimdallm.app/heimdalld")
		if err == nil || !strings.Contains(err.Error(), "launchagent: create plist:") {
			t.Fatalf("Install error = %v, want contextual plist-creation error", err)
		}
	})

	t.Run("launchctl bootstrap", func(t *testing.T) {
		installFakeLaunchctl(t, 23, "bootstrap denied")
		setupHome(t)

		err := launchagent.Install("/Applications/Heimdallm.app/heimdalld")
		if err == nil {
			t.Fatal("Install returned nil error, want launchctl failure")
		}
		for _, want := range []string{"launchagent: launchctl bootstrap:", "bootstrap denied"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("Install error = %q, want it to contain %q", err, want)
			}
		}
	})
}

func TestUninstallRemovesPlistWhenBootoutFails(t *testing.T) {
	capturePath := installFakeLaunchctl(t, 17, "not loaded")
	_, plistPath, _ := setupHome(t)
	if err := os.WriteFile(plistPath, []byte("plist"), 0o600); err != nil {
		t.Fatalf("write plist: %v", err)
	}

	if err := launchagent.Uninstall(); err != nil {
		t.Fatalf("Uninstall with failed bootout: %v", err)
	}
	if _, err := os.Stat(plistPath); !os.IsNotExist(err) {
		t.Fatalf("plist still exists after Uninstall (stat error = %v)", err)
	}
	requireCapturedArgs(t, capturePath,
		"bootout",
		fmt.Sprintf("gui/%d", os.Getuid()),
		plistPath,
	)

	if err := launchagent.Uninstall(); err != nil {
		t.Fatalf("second Uninstall should be idempotent: %v", err)
	}
}

func TestUninstallReportsRemoveFailure(t *testing.T) {
	installFakeLaunchctl(t, 0, "")
	_, plistPath, _ := setupHome(t)
	if err := os.Mkdir(plistPath, 0o700); err != nil {
		t.Fatalf("create directory at plist path: %v", err)
	}
	if err := os.WriteFile(filepath.Join(plistPath, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatalf("make plist directory non-empty: %v", err)
	}

	err := launchagent.Uninstall()
	if err == nil || !strings.Contains(err.Error(), "launchagent: remove plist:") {
		t.Fatalf("Uninstall error = %v, want contextual remove error", err)
	}
}

func TestOperationsRejectMissingHome(t *testing.T) {
	installFakeLaunchctl(t, 0, "")
	t.Setenv("HOME", "")

	if err := launchagent.Install("/Applications/Heimdallm.app/heimdalld"); err == nil {
		t.Fatal("Install returned nil error without HOME")
	}
	if err := launchagent.Uninstall(); err == nil {
		t.Fatal("Uninstall returned nil error without HOME")
	}
}

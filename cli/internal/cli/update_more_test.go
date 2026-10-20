package cli

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theburrowhub/heimdallm/cli/internal/update"
	"time"

	"github.com/spf13/cobra"
)

func failingServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestVersionCheckStates(t *testing.T) {
	isolateConfig(t)
	srv := releaseServer(t, tarGz(t, "x"))
	stubUpdater(t, srv, "")

	out, err := runVersioned(t, "0.11.0", "version", "--check")
	if err != nil || !strings.Contains(out, "Up to date") {
		t.Fatalf("out=%q err=%v", out, err)
	}
	out, err = runVersioned(t, "dev", "version", "--check")
	if err != nil || !strings.Contains(out, "Latest release: v0.11.0") {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if _, err := runVersioned(t, "0.10.4", "version", "--check", "--channel", "bogus"); err == nil {
		t.Fatal("bad channel should fail")
	}
	if _, err := runVersioned(t, "0.10.4", "version", "extra"); err == nil {
		t.Fatal("positional args should be rejected")
	}
}

func TestUpdateErrorPaths(t *testing.T) {
	isolateConfig(t)
	bad := failingServer(t)
	stubUpdater(t, bad, "")
	if _, err := runVersioned(t, "0.10.4", "version", "--check"); err == nil || !strings.Contains(err.Error(), "checking for updates") {
		t.Fatalf("version err = %v", err)
	}
	if _, err := runVersioned(t, "0.10.4", "update"); err == nil || !strings.Contains(err.Error(), "checking for updates") {
		t.Fatalf("update err = %v", err)
	}

	// Corrupt archive: checksum in the listing does not match what is served.
	srv := releaseServer(t, tarGz(t, "bin"))
	exe := filepath.Join(t.TempDir(), "heimdallm-cli")
	os.WriteFile(exe, []byte("old"), 0o755)
	stubUpdater(t, srv, exe)
	oldExe := executablePath
	executablePath = func() (string, error) { return "", fmt.Errorf("no exe") }
	if _, err := runVersioned(t, "0.10.4", "update"); err == nil || !strings.Contains(err.Error(), "locating") {
		t.Fatalf("exe err = %v", err)
	}
	executablePath = oldExe

	missing := filepath.Join(t.TempDir(), "gone", "heimdallm-cli")
	executablePath = func() (string, error) { return missing, nil }
	if _, err := runVersioned(t, "0.10.4", "update"); err == nil {
		t.Fatal("unwritable location should fail")
	}
}

func TestUpdateUsesConfiguredChannel(t *testing.T) {
	isolateConfig(t)
	if err := saveCLIConfig(&cliConfig{AutoUpdate: &autoUpdateConfig{Channel: "bogus"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := runVersioned(t, "0.10.4", "update", "--check"); err == nil || !strings.Contains(err.Error(), "unknown channel") {
		t.Fatalf("err = %v", err)
	}
	if err := saveCLIConfig(&cliConfig{AutoUpdate: &autoUpdateConfig{Channel: "latest"}}); err != nil {
		t.Fatal(err)
	}
	ch, err := configuredChannel("")
	if err != nil || ch != "latest" {
		t.Fatalf("ch=%q err=%v", ch, err)
	}
	ch, err = configuredChannel("stable")
	if err != nil || ch != "stable" {
		t.Fatalf("flag should win: ch=%q err=%v", ch, err)
	}
}

func TestUpdateStateRoundTrip(t *testing.T) {
	isolateConfig(t)
	if st := loadUpdateState(); !st.CheckedAt.IsZero() {
		t.Fatal("fresh state should be empty")
	}
	now := time.Now().UTC().Truncate(time.Second)
	saveUpdateState(updateState{CheckedAt: now, Latest: "v1.2.3"})
	st := loadUpdateState()
	if !st.CheckedAt.Equal(now) || st.Latest != "v1.2.3" {
		t.Fatalf("state = %+v", st)
	}
}

func TestHintRechecksWhenClockWentBackwards(t *testing.T) {
	isolateConfig(t)
	srv := releaseServer(t, tarGz(t, "x"))
	stubUpdater(t, srv, "")
	now := time.Now()
	saveUpdateState(updateState{CheckedAt: now.Add(time.Hour)})
	var out bytes.Buffer
	hintUpdate(&out, context.Background(), nil, "0.10.4", now)
	if !strings.Contains(out.String(), "v0.11.0") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestHintSurvivesNetworkFailure(t *testing.T) {
	isolateConfig(t)
	stubUpdater(t, failingServer(t), "")
	var out bytes.Buffer
	hintUpdate(&out, context.Background(), nil, "0.10.4", time.Now())
	if out.Len() != 0 {
		t.Fatalf("unexpected output %q", out.String())
	}
	if st := loadUpdateState(); st.CheckedAt.IsZero() {
		t.Fatal("failed check should still be throttled")
	}
}

func TestMaybeHintUpdateGuards(t *testing.T) {
	isolateConfig(t)
	calls := 0
	srv := releaseServer(t, tarGz(t, "x"))
	stubUpdater(t, srv, "")
	inner := newUpdater
	newUpdater = func() *update.Updater { calls++; return inner() }

	root := &cobra.Command{Use: "root"}
	status := &cobra.Command{Use: "status"}
	skipped := &cobra.Command{Use: "dashboard"}
	root.AddCommand(status, skipped)

	maybeHint(skipped, "0.10.4", true)
	maybeHint(status, "dev", true)
	maybeHint(status, "0.10.4", false)
	if calls != 0 {
		t.Fatalf("guards failed, calls=%d", calls)
	}

	off := false
	if err := saveCLIConfig(&cliConfig{AutoUpdate: &autoUpdateConfig{Enabled: &off}}); err != nil {
		t.Fatal(err)
	}
	maybeHint(status, "0.10.4", true)
	if calls != 0 {
		t.Fatalf("disabled config should skip, calls=%d", calls)
	}

	if err := saveCLIConfig(&cliConfig{}); err != nil {
		t.Fatal(err)
	}
	status.SetContext(context.Background())
	status.SetErr(&bytes.Buffer{})
	maybeHint(status, "0.10.4", true)
	if calls != 1 {
		t.Fatalf("enabled config should check once, calls=%d", calls)
	}
	_ = stderrIsTerminal()
}

func TestManagedByPackageManager(t *testing.T) {
	for p, want := range map[string]bool{
		"/opt/homebrew/Cellar/heimdallm-cli/1/bin/heimdallm-cli": true,
		"/home/linuxbrew/.linuxbrew/Homebrew/bin/x":              true,
		"/usr/local/bin/heimdallm-cli":                           false,
	} {
		if got := managedByPackageManager(p); got != want {
			t.Errorf("%s = %v", p, got)
		}
	}
}

func TestDefaultExecutablePath(t *testing.T) {
	p, err := executablePath()
	if err != nil || p == "" {
		t.Fatalf("p=%q err=%v", p, err)
	}
}

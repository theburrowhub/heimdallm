package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/theburrowhub/heimdallm/cli/internal/update"
)

func isolateConfig(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

func tarGz(t *testing.T, bin string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "heimdallm-cli", Mode: 0o755, Size: int64(len(bin)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	tw.Write([]byte(bin))
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// releaseServer serves a fake GitHub API with one stable release v0.11.0.
func releaseServer(t *testing.T, archive []byte) *httptest.Server {
	t.Helper()
	sum := sha256.Sum256(archive)
	name := fmt.Sprintf("heimdallm-cli_0.11.0_%s_%s.tar.gz", "linux", "amd64")
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"tag_name":"v0.11.0","body":"- feat: shiny thing","assets":[{"name":%q,"browser_download_url":%q},{"name":"cli-checksums.txt","browser_download_url":%q}]}`,
			name, srv.URL+"/archive", srv.URL+"/sums")
	})
	mux.HandleFunc("/archive", func(w http.ResponseWriter, _ *http.Request) { w.Write(archive) })
	mux.HandleFunc("/sums", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func stubUpdater(t *testing.T, srv *httptest.Server, exe string) {
	t.Helper()
	oldNew, oldExe := newUpdater, executablePath
	t.Cleanup(func() { newUpdater, executablePath = oldNew, oldExe })
	newUpdater = func() *update.Updater {
		return &update.Updater{
			Client: srv.Client(), APIBase: srv.URL, GOOS: "linux", GOARCH: "amd64",
			AllowURL: func(*url.URL) bool { return true },
			Verify:   func(context.Context, string, string) error { return nil },
		}
	}
	executablePath = func() (string, error) { return exe, nil }
}

func runVersioned(t *testing.T, version string, args ...string) (string, error) {
	t.Helper()
	cmd := NewRootCmd(version)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestVersionCheckReportsUpdate(t *testing.T) {
	isolateConfig(t)
	srv := releaseServer(t, tarGz(t, "bin"))
	stubUpdater(t, srv, "")

	out, err := runVersioned(t, "0.10.4", "version", "--check")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"heimdallm-cli version 0.10.4", "Update available: 0.10.4 -> v0.11.0"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q missing %q", out, want)
		}
	}
}

func TestVersionWithoutCheckDoesNotUseNetwork(t *testing.T) {
	isolateConfig(t)
	oldNew := newUpdater
	t.Cleanup(func() { newUpdater = oldNew })
	newUpdater = func() *update.Updater { t.Fatal("network used"); return nil }

	out, err := runVersioned(t, "0.10.4", "version")
	if err != nil || !strings.Contains(out, "0.10.4") {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestUpdateAppliesAndPrintsChangelog(t *testing.T) {
	isolateConfig(t)
	srv := releaseServer(t, tarGz(t, "new-binary"))
	exe := filepath.Join(t.TempDir(), "heimdallm-cli")
	os.WriteFile(exe, []byte("old-binary"), 0o755)
	stubUpdater(t, srv, exe)

	out, err := runVersioned(t, "0.10.4", "update")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got, _ := os.ReadFile(exe); string(got) != "new-binary" {
		t.Fatalf("binary = %q", got)
	}
	if got, _ := os.ReadFile(exe + ".bak"); string(got) != "old-binary" {
		t.Fatalf("backup = %q", got)
	}
	if !strings.Contains(out, "shiny thing") {
		t.Errorf("changelog missing from %q", out)
	}
}

func TestUpdateCheckDoesNotModify(t *testing.T) {
	isolateConfig(t)
	srv := releaseServer(t, tarGz(t, "new-binary"))
	exe := filepath.Join(t.TempDir(), "heimdallm-cli")
	os.WriteFile(exe, []byte("old-binary"), 0o755)
	stubUpdater(t, srv, exe)

	if _, err := runVersioned(t, "0.10.4", "update", "--check"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "old-binary" {
		t.Fatal("--check modified the binary")
	}
}

func TestUpdateAlreadyCurrent(t *testing.T) {
	isolateConfig(t)
	srv := releaseServer(t, tarGz(t, "x"))
	stubUpdater(t, srv, "")
	out, err := runVersioned(t, "0.11.0", "update")
	if err != nil || !strings.Contains(out, "Already up to date") {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestUpdateRefusesDevBuildAndHomebrew(t *testing.T) {
	isolateConfig(t)
	srv := releaseServer(t, tarGz(t, "x"))
	stubUpdater(t, srv, "/opt/homebrew/Cellar/heimdallm-cli/0.10.4/bin/heimdallm-cli")

	if _, err := runVersioned(t, "dev", "update"); err == nil || !strings.Contains(err.Error(), "development build") {
		t.Fatalf("dev err = %v", err)
	}
	if _, err := runVersioned(t, "0.10.4", "update"); err == nil || !strings.Contains(err.Error(), "brew upgrade") {
		t.Fatalf("brew err = %v", err)
	}
}

func TestUpdateRejectsUnknownChannel(t *testing.T) {
	isolateConfig(t)
	if _, err := runVersioned(t, "0.10.4", "update", "--channel", "nightly"); err == nil {
		t.Fatal("expected error")
	}
}

func TestAutoUpdateConfigDefaultsAndParsing(t *testing.T) {
	var none *cliConfig
	if !none.autoUpdateEnabled() || none.updateInterval() != 24*time.Hour || none.updateChannel() != "stable" {
		t.Fatal("defaults wrong for nil config")
	}
	var cfg cliConfig
	src := "[auto_update]\nenabled = false\ncheck_interval = \"6h\"\nchannel = \"latest\"\n"
	if _, err := toml.Decode(src, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.autoUpdateEnabled() || cfg.updateInterval() != 6*time.Hour || cfg.updateChannel() != "latest" {
		t.Fatalf("parsed wrong: %+v", cfg.AutoUpdate)
	}
	cfg.AutoUpdate.CheckInterval = "banana"
	if cfg.updateInterval() != 24*time.Hour {
		t.Fatal("invalid interval should fall back to default")
	}
	cfg.AutoUpdate.CheckInterval = "1s"
	if cfg.updateInterval() != 24*time.Hour {
		t.Fatal("too-small interval should fall back to default")
	}
}

func TestAutoUpdateConfigSurvivesSave(t *testing.T) {
	isolateConfig(t)
	off := false
	cfg := &cliConfig{Host: "http://localhost:7842", AutoUpdate: &autoUpdateConfig{Enabled: &off, Channel: "latest"}}
	if err := saveCLIConfig(cfg); err != nil {
		t.Fatal(err)
	}
	got, err := loadCLIConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got.autoUpdateEnabled() || got.updateChannel() != "latest" {
		t.Fatalf("round trip lost auto_update: %+v", got.AutoUpdate)
	}
}

func TestHintUpdateThrottlesNetworkChecks(t *testing.T) {
	isolateConfig(t)
	calls := 0
	srv := releaseServer(t, tarGz(t, "x"))
	stubUpdater(t, srv, "")
	inner := newUpdater
	newUpdater = func() *update.Updater { calls++; return inner() }

	now := time.Now()
	var out bytes.Buffer
	hintUpdate(&out, context.Background(), nil, "0.10.4", now)
	if calls != 1 || !strings.Contains(out.String(), "v0.11.0 is available") {
		t.Fatalf("calls=%d out=%q", calls, out.String())
	}
	out.Reset()
	hintUpdate(&out, context.Background(), nil, "0.10.4", now.Add(time.Hour))
	if calls != 1 {
		t.Fatalf("expected cached result within interval, calls=%d", calls)
	}
	if !strings.Contains(out.String(), "v0.11.0 is available") {
		t.Fatalf("cached hint missing: %q", out.String())
	}
	out.Reset()
	hintUpdate(&out, context.Background(), nil, "0.10.4", now.Add(25*time.Hour))
	if calls != 2 {
		t.Fatalf("expected re-check after interval, calls=%d", calls)
	}
}

func TestHintUpdateSilentWhenCurrent(t *testing.T) {
	isolateConfig(t)
	srv := releaseServer(t, tarGz(t, "x"))
	stubUpdater(t, srv, "")
	var out bytes.Buffer
	hintUpdate(&out, context.Background(), nil, "0.11.0", time.Now())
	if out.Len() != 0 {
		t.Fatalf("unexpected hint %q", out.String())
	}
}

package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// releaseServerWithVersions is releaseServer plus a versions.json asset.
func releaseServerWithVersions(t *testing.T, archive []byte, versionsJSON string) *httptest.Server {
	t.Helper()
	sum := sha256.Sum256(archive)
	name := "heimdallm-cli_0.11.0_linux_amd64.tar.gz"
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"tag_name":"v0.11.0","assets":[{"name":%q,"browser_download_url":%q},{"name":"cli-checksums.txt","browser_download_url":%q},{"name":"versions.json","browser_download_url":%q}]}`,
			name, srv.URL+"/archive", srv.URL+"/sums", srv.URL+"/versions.json")
	})
	mux.HandleFunc("/archive", func(w http.ResponseWriter, _ *http.Request) { w.Write(archive) })
	mux.HandleFunc("/sums", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	})
	mux.HandleFunc("/versions.json", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, versionsJSON) })
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func writeExecutable(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "heimdallm-cli")
	if err := os.WriteFile(exe, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

// compatReleaseServer serves a v0.11.0 release whose versions.json (when
// versionsJSON is not empty) declares the CLI's minimum daemon API.
func compatReleaseServer(t *testing.T, versionsJSON string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		assets := ""
		if versionsJSON != "" {
			assets = fmt.Sprintf(`{"name":"versions.json","browser_download_url":%q}`, srv.URL+"/versions.json")
		}
		fmt.Fprintf(w, `{"tag_name":"v0.11.0","assets":[%s]}`, assets)
	})
	mux.HandleFunc("/versions.json", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, versionsJSON)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// daemonServer answers /health with the given body.
func daemonServer(t *testing.T, health string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, health)
	}))
	t.Cleanup(srv.Close)
	return srv
}

const needsAPI2 = `{"daemon":"0.11.0","gui":"0.11.0","cli":"0.11.0","daemon_api":2,"min_daemon_api":{"gui":2,"cli":2}}`

func TestVersionCheckWarnsWhenAheadOfDaemon(t *testing.T) {
	for name, tc := range map[string]struct {
		health   string
		versions string
		warn     bool
	}{
		"older daemon":                       {`{"status":"ok","version":"0.10.4","api_version":1}`, needsAPI2, true},
		"daemon predating api_version":       {`{"status":"ok","version":"0.9.0"}`, needsAPI2, true},
		"daemon new enough":                  {`{"status":"ok","version":"0.11.0","api_version":2}`, needsAPI2, false},
		"release without versions.json":      {`{"status":"ok","api_version":1}`, "", false},
		"versions.json without compat data":  {`{"status":"ok","api_version":1}`, `{"daemon":"0.11.0","gui":"0.11.0","cli":"0.11.0"}`, false},
		"degraded daemon still reports":      {`{"status":"degraded","api_version":1}`, needsAPI2, true},
		"undecodable daemon health is quiet": {`<html>`, needsAPI2, false},
	} {
		t.Run(name, func(t *testing.T) {
			isolateConfig(t)
			stubUpdater(t, compatReleaseServer(t, tc.versions), "")
			daemon := daemonServer(t, tc.health)

			out, err := runVersioned(t, "0.10.4", "version", "--check", "--host", daemon.URL)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, "Update available") {
				t.Fatalf("output %q missing the update notice", out)
			}
			warned := strings.Contains(out, "needs daemon API 2 or newer")
			if warned != tc.warn {
				t.Fatalf("warned = %v, want %v; output:\n%s", warned, tc.warn, out)
			}
			if tc.warn && !strings.Contains(out, daemon.URL) {
				t.Fatalf("warning does not name the daemon: %q", out)
			}
		})
	}
}

func TestUpdateCheckWarnsWhenAheadOfDaemon(t *testing.T) {
	isolateConfig(t)
	stubUpdater(t, compatReleaseServer(t, needsAPI2), "")
	daemon := daemonServer(t, `{"status":"ok","api_version":1}`)

	out, err := runVersioned(t, "0.10.4", "update", "--check", "--host", daemon.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "needs daemon API 2 or newer") {
		t.Fatalf("missing warning: %q", out)
	}
}

func TestUpdateWarnsButStillInstalls(t *testing.T) {
	isolateConfig(t)
	srv := releaseServerWithVersions(t, tarGz(t, "new"), needsAPI2)
	exe := writeExecutable(t)
	stubUpdater(t, srv, exe)
	daemon := daemonServer(t, `{"status":"ok","api_version":1}`)

	out, err := runVersioned(t, "0.10.4", "update", "--host", daemon.URL)
	if err != nil {
		t.Fatalf("update must not be blocked by the warning: %v\n%s", err, out)
	}
	if !strings.Contains(out, "needs daemon API 2 or newer") || !strings.Contains(out, "Updated to v0.11.0") {
		t.Fatalf("output %q", out)
	}
}

func TestNoWarningWhenAlreadyUpToDate(t *testing.T) {
	isolateConfig(t)
	stubUpdater(t, compatReleaseServer(t, needsAPI2), "")
	daemon := daemonServer(t, `{"status":"ok","api_version":1}`)

	out, err := runVersioned(t, "0.11.0", "version", "--check", "--host", daemon.URL)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "needs daemon API") {
		t.Fatalf("no update, no warning: %q", out)
	}
}

func TestNoWarningWhenDaemonUnreachable(t *testing.T) {
	isolateConfig(t)
	stubUpdater(t, compatReleaseServer(t, needsAPI2), "")
	daemon := daemonServer(t, "")
	daemon.Close()

	out, err := runVersioned(t, "0.10.4", "version", "--check", "--host", daemon.URL)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "needs daemon API") {
		t.Fatalf("an unreachable daemon is no evidence: %q", out)
	}
}

// update and version skip the root's client resolution, so the probe resolves
// the daemon the same way the other commands do: env, then cli.toml.
func TestCompatProbeResolvesDaemonFromEnvAndConfig(t *testing.T) {
	t.Run("HEIMDALLM_HOST", func(t *testing.T) {
		isolateConfig(t)
		stubUpdater(t, compatReleaseServer(t, needsAPI2), "")
		daemon := daemonServer(t, `{"status":"ok","api_version":1}`)
		t.Setenv("HEIMDALLM_HOST", daemon.URL)

		out, err := runVersioned(t, "0.10.4", "version", "--check")
		if err != nil || !strings.Contains(out, daemon.URL) {
			t.Fatalf("out=%q err=%v", out, err)
		}
	})
	t.Run("cli.toml", func(t *testing.T) {
		isolateConfig(t)
		t.Setenv("HEIMDALLM_HOST", "")
		stubUpdater(t, compatReleaseServer(t, needsAPI2), "")
		daemon := daemonServer(t, `{"status":"ok","api_version":1}`)
		if err := os.MkdirAll(filepath.Dir(configPath()), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(configPath(), []byte(fmt.Sprintf("host = %q\n", daemon.URL)), 0o600); err != nil {
			t.Fatal(err)
		}

		out, err := runVersioned(t, "0.10.4", "version", "--check")
		if err != nil || !strings.Contains(out, daemon.URL) {
			t.Fatalf("out=%q err=%v", out, err)
		}
	})
}

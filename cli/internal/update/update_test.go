package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.10.3", "0.10.4", -1},
		{"v0.10.4", "0.10.4", 0},
		{"1.0.0", "0.99.9", 1},
		{"0.10.0", "0.9.9", 1},
		{"1.0.0-rc.1", "1.0.0", -1},
		{"1.0.0-rc.2", "1.0.0-rc.10", -1},
		{"1.0.0-alpha", "1.0.0-alpha.1", -1},
		{"1.0.0-1", "1.0.0-alpha", -1},
	}
	for _, c := range cases {
		got, err := Compare(c.a, c.b)
		if err != nil || got != c.want {
			t.Errorf("Compare(%q,%q) = %d, %v; want %d", c.a, c.b, got, err, c.want)
		}
	}
	for _, bad := range []string{"dev", "1.2", "a.b.c", ""} {
		if _, err := Compare(bad, "1.0.0"); err == nil {
			t.Errorf("Compare(%q) should fail", bad)
		}
	}
}

func makeArchive(t *testing.T, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct{ name, body string }{{"README.md", "hi"}, {"heimdallm-cli", content}} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(f.body)); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

type fixture struct {
	srv *httptest.Server
	u   *Updater
	rel *Release
}

func newFixture(t *testing.T, archive []byte, sum string) *fixture {
	t.Helper()
	name := fmt.Sprintf("heimdallm-cli_0.11.0_%s_%s.tar.gz", "linux", "amd64")
	mux := http.NewServeMux()
	mux.HandleFunc("/dl/archive", func(w http.ResponseWriter, _ *http.Request) { w.Write(archive) })
	mux.HandleFunc("/dl/sums", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", sum, name)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &fixture{
		srv: srv,
		u: &Updater{
			Client: srv.Client(), APIBase: srv.URL, GOOS: "linux", GOARCH: "amd64",
			AllowURL: func(*url.URL) bool { return true },
			Verify:   func(context.Context, string, string) error { return nil },
		},
		rel: &Release{Tag: "v0.11.0", Assets: []Asset{
			{Name: name, URL: srv.URL + "/dl/archive"},
			{Name: "cli-checksums.txt", URL: srv.URL + "/dl/sums"},
		}},
	}
}

func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func writeExe(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "heimdallm-cli")
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestApplyReplacesBinaryAndKeepsBackup(t *testing.T) {
	archive := makeArchive(t, "new-binary")
	f := newFixture(t, archive, sha(archive))
	exe := writeExe(t, "old-binary")

	if err := f.u.Apply(context.Background(), f.rel, exe); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "new-binary" {
		t.Fatalf("binary = %q", got)
	}
	if got, _ := os.ReadFile(BackupPath(exe)); string(got) != "old-binary" {
		t.Fatalf("backup = %q", got)
	}
	if st, _ := os.Stat(exe); st.Mode().Perm()&0o100 == 0 {
		t.Fatal("new binary is not executable")
	}
	entries, _ := os.ReadDir(filepath.Dir(exe))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".heimdallm-cli-update-") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

func TestApplyRejectsChecksumMismatch(t *testing.T) {
	archive := makeArchive(t, "new-binary")
	f := newFixture(t, archive, sha([]byte("something else")))
	exe := writeExe(t, "old-binary")

	err := f.u.Apply(context.Background(), f.rel, exe)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "old-binary" {
		t.Fatal("binary was modified despite checksum failure")
	}
	if _, err := os.Stat(BackupPath(exe)); err == nil {
		t.Fatal("backup should not exist when nothing was replaced")
	}
}

func TestApplyRollsBackWhenHealthCheckFails(t *testing.T) {
	archive := makeArchive(t, "new-binary")
	f := newFixture(t, archive, sha(archive))
	f.u.Verify = func(context.Context, string, string) error { return errors.New("boom") }
	exe := writeExe(t, "old-binary")

	err := f.u.Apply(context.Background(), f.rel, exe)
	if err == nil || !strings.Contains(err.Error(), "previous version restored") {
		t.Fatalf("err = %v", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "old-binary" {
		t.Fatalf("binary = %q, want restored", got)
	}
}

func TestApplyRequiresChecksumsAsset(t *testing.T) {
	archive := makeArchive(t, "x")
	f := newFixture(t, archive, sha(archive))
	f.rel.Assets = f.rel.Assets[:1]
	if err := f.u.Apply(context.Background(), f.rel, writeExe(t, "old")); err == nil {
		t.Fatal("expected error without checksums asset")
	}
}

func TestApplyRefusesUntrustedHost(t *testing.T) {
	archive := makeArchive(t, "x")
	f := newFixture(t, archive, sha(archive))
	f.u.AllowURL = nil
	err := f.u.Apply(context.Background(), f.rel, writeExe(t, "old"))
	if err == nil || !strings.Contains(err.Error(), "untrusted") {
		t.Fatalf("err = %v", err)
	}
}

func TestDefaultAllowURL(t *testing.T) {
	ok := []string{"https://github.com/a/b", "https://objects.githubusercontent.com/x", "https://api.github.com/x"}
	bad := []string{"http://github.com/x", "https://evil.com/x", "https://github.com.evil.com/x", "https://notgithub.com/x"}
	for _, s := range ok {
		u, _ := url.Parse(s)
		if !defaultAllowURL(u) {
			t.Errorf("%s should be allowed", s)
		}
	}
	for _, s := range bad {
		u, _ := url.Parse(s)
		if defaultAllowURL(u) {
			t.Errorf("%s should be rejected", s)
		}
	}
}

func TestLatestChannels(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"tag_name":"v0.10.4","body":"notes"}`)
	})
	mux.HandleFunc("/releases", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `[{"tag_name":"v0.11.0-rc.1","prerelease":true},{"tag_name":"v0.12.0","draft":true},{"tag_name":"v0.10.4"},{"tag_name":"nightly"}]`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	u := &Updater{Client: srv.Client(), APIBase: srv.URL}

	rel, err := u.Latest(context.Background(), ChannelStable)
	if err != nil || rel.Version() != "0.10.4" {
		t.Fatalf("stable = %+v, %v", rel, err)
	}
	rel, err = u.Latest(context.Background(), ChannelLatest)
	if err != nil || rel.Version() != "0.11.0-rc.1" {
		t.Fatalf("latest = %+v, %v", rel, err)
	}
	if _, err := u.Latest(context.Background(), "nope"); err == nil {
		t.Fatal("unknown channel should fail")
	}
}

// Per-component releases (theburrowhub/heimdallm#437) share the repository's
// release list. Their tags are not plain semver, so the latest channel must
// keep resolving to unified vX.Y.Z releases even when they are newer.
func TestLatestChannelSkipsComponentReleases(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/releases", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `[{"tag_name":"cli/v9.0.0"},{"tag_name":"daemon/v9.0.0"},{"tag_name":"gui/v9.0.0"},{"tag_name":"v0.11.0"}]`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	u := &Updater{Client: srv.Client(), APIBase: srv.URL}

	rel, err := u.Latest(context.Background(), ChannelLatest)
	if err != nil || rel.Tag != "v0.11.0" {
		t.Fatalf("latest = %+v, %v", rel, err)
	}
	for _, tag := range []string{"cli/v1.2.3", "daemon/v1.2.3", "gui/v1.2.3"} {
		if _, err := parseSemver(tag); err == nil {
			t.Errorf("%s should not parse as a unified release", tag)
		}
	}
}

func TestLatestStableSkipsPrerelease(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"tag_name":"v1.0.0-rc.1","prerelease":true}`)
	}))
	defer srv.Close()
	u := &Updater{Client: srv.Client(), APIBase: srv.URL}
	if _, err := u.Latest(context.Background(), ChannelStable); !errors.Is(err, ErrNoRelease) {
		t.Fatalf("err = %v", err)
	}
}

func TestChecksumFor(t *testing.T) {
	sum := strings.Repeat("a", 64)
	got, err := checksumFor([]byte(sum+"  file.tar.gz\n"+sum+" *other.zip\n"), "other.zip")
	if err != nil || got != sum {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := checksumFor([]byte("short  file\n"), "file"); err == nil {
		t.Fatal("malformed checksum should fail")
	}
	if _, err := checksumFor([]byte(sum+"  a\n"), "b"); err == nil {
		t.Fatal("missing entry should fail")
	}
}

func TestChangelogSummary(t *testing.T) {
	got := ChangelogSummary("a\r\nb\nc\nd", 2)
	if got != "a\nb\n…" {
		t.Fatalf("got %q", got)
	}
}

func TestCompat(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"daemon":"1.0.0","daemon_api":3,"min_daemon_api":{"gui":2,"cli":3}}`)
	})
	mux.HandleFunc("/nocompat", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"daemon":"1.0.0","gui":"1.0.0","cli":"1.0.0"}`)
	})
	mux.HandleFunc("/bad", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{nope`) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	u := &Updater{Client: srv.Client(), APIBase: srv.URL, AllowURL: func(*url.URL) bool { return true }}
	rel := func(path string) *Release {
		return &Release{Tag: "v1.0.0", Assets: []Asset{{Name: "versions.json", URL: srv.URL + path}}}
	}

	c, err := u.Compat(context.Background(), rel("/ok"))
	if err != nil || c.DaemonAPI != 3 || c.MinDaemonAPI["cli"] != 3 || c.MinDaemonAPI["gui"] != 2 {
		t.Fatalf("compat = %+v, %v", c, err)
	}
	if _, err := u.Compat(context.Background(), rel("/nocompat")); !errors.Is(err, ErrNoCompat) {
		t.Fatalf("manifest without compat data: %v", err)
	}
	if _, err := u.Compat(context.Background(), &Release{Tag: "v1.0.0"}); !errors.Is(err, ErrNoCompat) {
		t.Fatalf("release without versions.json: %v", err)
	}
	if _, err := u.Compat(context.Background(), rel("/bad")); err == nil {
		t.Fatal("undecodable versions.json should fail")
	}
	strict := &Updater{Client: srv.Client(), APIBase: srv.URL}
	if _, err := strict.Compat(context.Background(), rel("/ok")); err == nil || !strings.Contains(err.Error(), "untrusted") {
		t.Fatalf("non-GitHub URL must be refused: %v", err)
	}
}

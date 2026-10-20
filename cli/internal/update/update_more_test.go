package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestExtractBinaryZip(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range []struct{ name, body string }{{"dir/", ""}, {"LICENSE", "l"}, {"heimdallm-cli.exe", "win-bin"}} {
		w, err := zw.Create(f.name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(f.body))
	}
	zw.Close()

	got, err := extractBinary(buf.Bytes(), "a.zip", true)
	if err != nil || string(got) != "win-bin" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := extractBinary([]byte("not a zip"), "a.zip", true); err == nil {
		t.Fatal("invalid zip should fail")
	}

	var empty bytes.Buffer
	zw = zip.NewWriter(&empty)
	zw.Close()
	if _, err := extractBinary(empty.Bytes(), "a.zip", true); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestExtractBinaryTarErrors(t *testing.T) {
	if _, err := extractBinary([]byte("garbage"), "a.tar.gz", false); err == nil {
		t.Fatal("non-gzip should fail")
	}

	var noBin bytes.Buffer
	gz := gzip.NewWriter(&noBin)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "README", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
	tw.Write([]byte("x"))
	tw.Close()
	gz.Close()
	if _, err := extractBinary(noBin.Bytes(), "a.tar.gz", false); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v", err)
	}

	// Truncated tar stream after a valid gzip wrapper.
	var bad bytes.Buffer
	gz = gzip.NewWriter(&bad)
	gz.Write(bytes.Repeat([]byte{1}, 600))
	gz.Close()
	if _, err := extractBinary(bad.Bytes(), "a.tar.gz", false); err == nil {
		t.Fatal("corrupt tar should fail")
	}
}

func TestArchiveNameWindows(t *testing.T) {
	u := &Updater{GOOS: "windows", GOARCH: "arm64"}
	if got := u.ArchiveName("1.2.3"); got != "heimdallm-cli_1.2.3_windows_arm64.zip" {
		t.Fatalf("got %s", got)
	}
}

func TestNewDefaults(t *testing.T) {
	u := New()
	if u.GOOS != runtime.GOOS || u.GOARCH != runtime.GOARCH || u.APIBase != DefaultAPIBase || u.Client == nil {
		t.Fatalf("unexpected defaults: %+v", u)
	}
}

func TestLatestErrors(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "{not json") })
	mux.HandleFunc("/releases", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "{not json") })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	u := &Updater{Client: srv.Client(), APIBase: srv.URL}
	for _, ch := range []string{ChannelStable, ChannelLatest} {
		if _, err := u.Latest(context.Background(), ch); err == nil {
			t.Errorf("%s: bad JSON should fail", ch)
		}
	}

	down := &Updater{Client: srv.Client(), APIBase: srv.URL + "/missing"}
	if _, err := down.Latest(context.Background(), ChannelStable); err == nil {
		t.Fatal("404 should fail")
	}
	if _, err := down.Latest(context.Background(), ChannelLatest); err == nil {
		t.Fatal("404 should fail")
	}

	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "[]") }))
	defer empty.Close()
	u = &Updater{Client: empty.Client(), APIBase: empty.URL}
	if _, err := u.Latest(context.Background(), ChannelLatest); err == nil {
		t.Fatal("no releases should fail")
	}
}

func TestGetRejectsOversizedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(bytes.Repeat([]byte("a"), 100))
	}))
	defer srv.Close()
	u := &Updater{Client: srv.Client()}
	if _, err := u.get(context.Background(), srv.URL, 10); err == nil {
		t.Fatal("oversized body should fail")
	}
	if _, err := u.get(context.Background(), "://bad", 10); err == nil {
		t.Fatal("bad URL should fail")
	}
	if _, err := u.get(context.Background(), "http://127.0.0.1:1", 10); err == nil {
		t.Fatal("connection error should fail")
	}
}

func TestApplyMissingArchiveAndBadURLs(t *testing.T) {
	archive := makeArchive(t, "x")
	f := newFixture(t, archive, sha(archive))
	exe := writeExe(t, "old")

	noArchive := &Release{Tag: "v0.11.0", Assets: f.rel.Assets[1:]}
	if err := f.u.Apply(context.Background(), noArchive, exe); err == nil || !strings.Contains(err.Error(), "no asset") {
		t.Fatalf("err = %v", err)
	}

	badURL := &Release{Tag: "v0.11.0", Assets: []Asset{
		{Name: f.rel.Assets[0].Name, URL: "://bad"},
		f.rel.Assets[1],
	}}
	if err := f.u.Apply(context.Background(), badURL, exe); err == nil {
		t.Fatal("invalid URL should fail")
	}

	f.u.APIBase = f.srv.URL
	missingSums := &Release{Tag: "v0.11.0", Assets: []Asset{
		f.rel.Assets[0],
		{Name: "cli-checksums.txt", URL: f.srv.URL + "/nope"},
	}}
	if err := f.u.Apply(context.Background(), missingSums, exe); err == nil {
		t.Fatal("missing checksums download should fail")
	}

	missingArchive := &Release{Tag: "v0.11.0", Assets: []Asset{
		{Name: f.rel.Assets[0].Name, URL: f.srv.URL + "/nope"},
		f.rel.Assets[1],
	}}
	if err := f.u.Apply(context.Background(), missingArchive, exe); err == nil {
		t.Fatal("missing archive download should fail")
	}

	wrongName := newFixture(t, archive, sha(archive))
	wrongName.u.GOARCH = "arm64"
	wrongName.rel.Assets[0].Name = "heimdallm-cli_0.11.0_linux_arm64.tar.gz"
	if err := wrongName.u.Apply(context.Background(), wrongName.rel, exe); err == nil || !strings.Contains(err.Error(), "no checksum") {
		t.Fatalf("err = %v", err)
	}

	if got, _ := os.ReadFile(exe); string(got) != "old" {
		t.Fatal("binary changed on failed update")
	}
}

func TestApplyArchiveWithoutBinary(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "README", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
	tw.Write([]byte("x"))
	tw.Close()
	gz.Close()
	f := newFixture(t, buf.Bytes(), sha(buf.Bytes()))
	if err := f.u.Apply(context.Background(), f.rel, writeExe(t, "old")); err == nil {
		t.Fatal("archive without binary should fail")
	}
}

func TestInstallFailures(t *testing.T) {
	u := &Updater{Verify: func(context.Context, string, string) error { return nil }}
	if err := u.install(context.Background(), []byte("x"), filepath.Join(t.TempDir(), "missing-dir", "bin"), "1.0.0"); err == nil {
		t.Fatal("unwritable dir should fail")
	}
	// Target does not exist, so the backup rename fails and nothing is left behind.
	dir := t.TempDir()
	if err := u.install(context.Background(), []byte("x"), filepath.Join(dir, "bin"), "1.0.0"); err == nil {
		t.Fatal("missing target should fail")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("leftovers: %v", entries)
	}
}

func TestDefaultVerify(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fixture")
	}
	dir := t.TempDir()
	good := filepath.Join(dir, "good")
	os.WriteFile(good, []byte("#!/bin/sh\necho 'heimdallm-cli version 1.2.3'\n"), 0o755)
	if err := defaultVerify(context.Background(), good, "1.2.3"); err != nil {
		t.Fatal(err)
	}
	if err := defaultVerify(context.Background(), good, "9.9.9"); err == nil {
		t.Fatal("version mismatch should fail")
	}
	bad := filepath.Join(dir, "bad")
	os.WriteFile(bad, []byte("#!/bin/sh\nexit 3\n"), 0o755)
	if err := defaultVerify(context.Background(), bad, "1.2.3"); err == nil {
		t.Fatal("non-zero exit should fail")
	}
	if err := defaultVerify(context.Background(), filepath.Join(dir, "none"), "1.2.3"); err == nil {
		t.Fatal("missing binary should fail")
	}
}

func TestApplyDefaultVerifyRollsBackOnVersionMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fixture")
	}
	archive := makeArchive(t, "#!/bin/sh\necho 'heimdallm-cli version 0.0.1'\n")
	f := newFixture(t, archive, sha(archive))
	f.u.Verify = nil
	exe := writeExe(t, "old-binary")
	if err := f.u.Apply(context.Background(), f.rel, exe); err == nil || !strings.Contains(err.Error(), "previous version restored") {
		t.Fatalf("err = %v", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "old-binary" {
		t.Fatalf("binary = %q", got)
	}
}

func TestAllowRejectsInvalidURL(t *testing.T) {
	u := &Updater{}
	if err := u.allow("https://github.com/x"); err != nil {
		t.Fatal(err)
	}
	if err := u.allow("http://[::1"); err == nil {
		t.Fatal("unparsable URL should fail")
	}
}

func TestCompareEdgeCases(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0+build1", "1.0.0+build2", 0},
		{"1.0.0-alpha", "1.0.0-beta", -1},
		{"1.0.0-beta", "1.0.0-alpha", 1},
		{"1.0.0-alpha.1", "1.0.0-alpha", 1},
		{"1.0.0-rc.1", "1.0.0-rc.1", 0},
		{"1.0.0-rc.alpha", "1.0.0-rc.1", 1},
		{"1.0.0-rc.10", "1.0.0-rc.2", 1},
		{"1.0.0", "1.0.0-rc.1", 1},
		{"1.0.0-rc.1", "1.0.0-rc.1.1", -1},
	}
	for _, c := range cases {
		got, err := Compare(c.a, c.b)
		if err != nil || got != c.want {
			t.Errorf("Compare(%q,%q) = %d, %v; want %d", c.a, c.b, got, err, c.want)
		}
	}
	if _, err := Compare("1.0.0", "bad"); err == nil {
		t.Fatal("bad right operand should fail")
	}
	if _, err := Compare("1.-1.0", "1.0.0"); err == nil {
		t.Fatal("negative component should fail")
	}
}

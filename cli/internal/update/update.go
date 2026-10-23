// Package update implements the CLI self-update: find the newest GitHub
// release, verify the downloaded archive against the release's
// cli-checksums.txt, and swap the running binary atomically, keeping the old
// one as a backup that is restored if the new one does not start.
package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// ChannelStable only considers non-prerelease releases.
	ChannelStable = "stable"
	// ChannelLatest also considers pre-releases.
	ChannelLatest = "latest"

	DefaultAPIBase = "https://api.github.com/repos/theburrowhub/heimdallm"
	checksumsAsset = "cli-checksums.txt"
	versionsAsset  = "versions.json"
	binaryName     = "heimdallm-cli"

	maxMetadataBytes = 4 << 20
	maxArchiveBytes  = 200 << 20
)

// ErrNoRelease is returned when the channel has no usable release.
var ErrNoRelease = errors.New("no release found")

// Asset is one downloadable file of a release.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// Release is the subset of a GitHub release the updater needs.
type Release struct {
	Tag        string  `json:"tag_name"`
	Body       string  `json:"body"`
	HTMLURL    string  `json:"html_url"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []Asset `json:"assets"`
}

// Compat is the compatibility part of a unified release's versions.json: the
// daemon API revision the release ships and the minimum each client needs.
type Compat struct {
	DaemonAPI    int            `json:"daemon_api"`
	MinDaemonAPI map[string]int `json:"min_daemon_api"`
}

// ErrNoCompat is returned for releases that publish no compatibility data
// (every release cut before it existed).
var ErrNoCompat = errors.New("release has no compatibility data")

// Version is the release tag without the leading "v".
func (r *Release) Version() string { return strings.TrimPrefix(r.Tag, "v") }

// Updater talks to GitHub Releases and replaces the binary on disk.
type Updater struct {
	Client  *http.Client
	APIBase string
	GOOS    string
	GOARCH  string

	// AllowURL decides which asset URLs may be downloaded. The default only
	// accepts https on GitHub hosts, so a tampered API response cannot point
	// the updater at an arbitrary server.
	AllowURL func(*url.URL) bool
	// Verify runs the freshly installed binary; a non-nil error triggers the
	// rollback. The default runs `<binary> --version` and expects the new
	// version in its output.
	Verify func(ctx context.Context, path, version string) error
}

// New returns an Updater for the current platform.
func New() *Updater {
	return &Updater{
		Client:  &http.Client{Timeout: 5 * time.Minute},
		APIBase: DefaultAPIBase,
		GOOS:    runtime.GOOS,
		GOARCH:  runtime.GOARCH,
	}
}

func defaultAllowURL(u *url.URL) bool {
	if u.Scheme != "https" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "github.com" || strings.HasSuffix(h, ".github.com") ||
		strings.HasSuffix(h, ".githubusercontent.com")
}

func (u *Updater) allow(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid download URL %q: %w", raw, err)
	}
	allow := u.AllowURL
	if allow == nil {
		allow = defaultAllowURL
	}
	if !allow(parsed) {
		return fmt.Errorf("refusing to download from untrusted URL %q", raw)
	}
	return nil
}

func (u *Updater) get(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "heimdallm-cli")
	resp, err := u.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", rawURL, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("GET %s: response larger than %d bytes", rawURL, limit)
	}
	return data, nil
}

// Latest returns the newest release for the channel.
func (u *Updater) Latest(ctx context.Context, channel string) (*Release, error) {
	switch channel {
	case "", ChannelStable:
		data, err := u.get(ctx, u.APIBase+"/releases/latest", maxMetadataBytes)
		if err != nil {
			return nil, err
		}
		var rel Release
		if err := json.Unmarshal(data, &rel); err != nil {
			return nil, fmt.Errorf("decoding release: %w", err)
		}
		if rel.Tag == "" || rel.Draft || rel.Prerelease {
			return nil, ErrNoRelease
		}
		return &rel, nil
	case ChannelLatest:
		data, err := u.get(ctx, u.APIBase+"/releases?per_page=20", maxMetadataBytes)
		if err != nil {
			return nil, err
		}
		var rels []Release
		if err := json.Unmarshal(data, &rels); err != nil {
			return nil, fmt.Errorf("decoding releases: %w", err)
		}
		var best *Release
		for i := range rels {
			r := &rels[i]
			if r.Draft || r.Tag == "" {
				continue
			}
			if _, err := parseSemver(r.Tag); err != nil {
				continue
			}
			if best == nil {
				best = r
				continue
			}
			if c, _ := Compare(r.Tag, best.Tag); c > 0 {
				best = r
			}
		}
		if best == nil {
			return nil, ErrNoRelease
		}
		return best, nil
	default:
		return nil, fmt.Errorf("unknown channel %q (want %q or %q)", channel, ChannelStable, ChannelLatest)
	}
}

// Compat downloads the release's versions.json and returns its compatibility
// data. A release without that asset, or whose manifest carries no
// min_daemon_api, yields ErrNoCompat.
func (u *Updater) Compat(ctx context.Context, rel *Release) (*Compat, error) {
	var assetURL string
	for _, a := range rel.Assets {
		if a.Name == versionsAsset {
			assetURL = a.URL
			break
		}
	}
	if assetURL == "" {
		return nil, ErrNoCompat
	}
	if err := u.allow(assetURL); err != nil {
		return nil, err
	}
	data, err := u.get(ctx, assetURL, maxMetadataBytes)
	if err != nil {
		return nil, err
	}
	var c Compat
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("decoding %s: %w", versionsAsset, err)
	}
	if len(c.MinDaemonAPI) == 0 {
		return nil, ErrNoCompat
	}
	return &c, nil
}

// ArchiveName is the release asset for this OS/arch.
func (u *Updater) ArchiveName(version string) string {
	ext := "tar.gz"
	if u.GOOS == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("%s_%s_%s_%s.%s", binaryName, version, u.GOOS, u.GOARCH, ext)
}

func findAsset(rel *Release, name string) (Asset, bool) {
	for _, a := range rel.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

// checksumFor looks name up in a `sha256sum`-style listing.
func checksumFor(listing []byte, name string) (string, error) {
	for _, line := range strings.Split(string(listing), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			sum := strings.ToLower(fields[0])
			if len(sum) != sha256.Size*2 {
				return "", fmt.Errorf("malformed checksum for %s", name)
			}
			return sum, nil
		}
	}
	return "", fmt.Errorf("no checksum for %s in %s", name, checksumsAsset)
}

// extractBinary returns the CLI executable stored in the archive.
func extractBinary(archive []byte, name string, isZip bool) ([]byte, error) {
	want := binaryName
	if isZip {
		want += ".exe"
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if f.FileInfo().IsDir() || filepath.Base(f.Name) != want {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return readCapped(rc)
		}
	} else {
		gz, err := gzip.NewReader(bytes.NewReader(archive))
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		tr := tar.NewReader(gz)
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != want {
				continue
			}
			return readCapped(tr)
		}
	}
	return nil, fmt.Errorf("%s not found in %s", want, name)
}

func readCapped(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxArchiveBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxArchiveBytes {
		return nil, errors.New("binary in archive is too large")
	}
	return data, nil
}

// BackupPath is where the previous binary is kept after an update.
func BackupPath(exe string) string { return exe + ".bak" }

// Apply downloads rel, verifies it, and replaces the binary at exePath. On any
// failure after the swap the previous binary is restored.
func (u *Updater) Apply(ctx context.Context, rel *Release, exePath string) error {
	version := rel.Version()
	name := u.ArchiveName(version)
	archiveAsset, ok := findAsset(rel, name)
	if !ok {
		return fmt.Errorf("release %s has no asset %s for %s/%s", rel.Tag, name, u.GOOS, u.GOARCH)
	}
	sumsAsset, ok := findAsset(rel, checksumsAsset)
	if !ok {
		return fmt.Errorf("release %s has no %s; refusing to install an unverified binary", rel.Tag, checksumsAsset)
	}
	for _, a := range []Asset{archiveAsset, sumsAsset} {
		if err := u.allow(a.URL); err != nil {
			return err
		}
	}

	sums, err := u.get(ctx, sumsAsset.URL, maxMetadataBytes)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", checksumsAsset, err)
	}
	want, err := checksumFor(sums, name)
	if err != nil {
		return err
	}
	archive, err := u.get(ctx, archiveAsset.URL, maxArchiveBytes)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", name, err)
	}
	got := sha256.Sum256(archive)
	if hex.EncodeToString(got[:]) != want {
		return fmt.Errorf("checksum mismatch for %s: refusing to install", name)
	}
	bin, err := extractBinary(archive, name, u.GOOS == "windows")
	if err != nil {
		return err
	}
	return u.install(ctx, bin, exePath, version)
}

func (u *Updater) install(ctx context.Context, bin []byte, exePath, version string) error {
	dir := filepath.Dir(exePath)
	tmp, err := os.CreateTemp(dir, ".heimdallm-cli-update-*")
	if err != nil {
		return fmt.Errorf("cannot write next to %s (re-run with the permissions that installed it): %w", exePath, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return err
	}

	bak := BackupPath(exePath)
	_ = os.Remove(bak)
	if err := os.Rename(exePath, bak); err != nil {
		return fmt.Errorf("backing up current binary: %w", err)
	}
	restore := func() { _ = os.Rename(bak, exePath) }
	if err := os.Rename(tmpPath, exePath); err != nil {
		restore()
		return fmt.Errorf("installing new binary: %w", err)
	}
	verify := u.Verify
	if verify == nil {
		verify = defaultVerify
	}
	if err := verify(ctx, exePath, version); err != nil {
		_ = os.Remove(exePath)
		restore()
		return fmt.Errorf("new binary failed its health check, previous version restored: %w", err)
	}
	return nil
}

func defaultVerify(ctx context.Context, path, version string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s --version: %w", path, err)
	}
	if !strings.Contains(string(out), version) {
		return fmt.Errorf("%s --version reported %q, expected %s", path, strings.TrimSpace(string(out)), version)
	}
	return nil
}

// ChangelogSummary trims release notes to at most maxLines lines.
func ChangelogSummary(body string, maxLines int) string {
	body = strings.TrimSpace(strings.ReplaceAll(body, "\r\n", "\n"))
	lines := strings.Split(body, "\n")
	if len(lines) > maxLines {
		lines = append(lines[:maxLines], "…")
	}
	return strings.Join(lines, "\n")
}

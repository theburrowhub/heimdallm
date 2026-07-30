package executor

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestLoginProbeEnvironmentExcludesCredentialBearingNetworkSettings(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("HOME", "/tmp/login-probe-home")
	t.Setenv("LANG", "C.UTF-8")
	t.Setenv("HTTPS_PROXY", "https://user:secret@example.invalid")
	t.Setenv("ALL_PROXY", "socks5://user:secret@example.invalid")
	t.Setenv("SSL_CERT_FILE", "/private/daemon/ca.pem")
	t.Setenv("NODE_EXTRA_CA_CERTS", "/private/daemon/node-ca.pem")
	t.Setenv("GITHUB_TOKEN", "daemon-secret")

	env := captureEnvironment().loginProbeEnvironment()
	joined := strings.Join(env, "\n")
	for _, name := range []string{
		"HTTPS_PROXY=",
		"ALL_PROXY=",
		"SSL_CERT_FILE=",
		"NODE_EXTRA_CA_CERTS=",
		"GITHUB_TOKEN=",
	} {
		if strings.Contains(joined, name) {
			t.Errorf("login-shell probe received %s", name)
		}
	}
	for _, name := range []string{"PATH=", "HOME=", "LANG="} {
		if !strings.Contains(joined, name) {
			t.Errorf("login-shell probe lost required %s", name)
		}
	}
}

func TestCodexShellEnvironmentPolicyRoundTripsTOMLControls(t *testing.T) {
	const value = "\x00\a\v\x1b\x7f quote=\" slash=\\ newline=\n emoji=😀"
	policy, err := codexShellEnvironmentPolicy(map[string]string{"CONTROL": value})
	if err != nil {
		t.Fatalf("codexShellEnvironmentPolicy: %v", err)
	}

	var decoded struct {
		Policy struct {
			Inherit     string            `toml:"inherit"`
			IncludeOnly []string          `toml:"include_only"`
			Set         map[string]string `toml:"set"`
		} `toml:"policy"`
	}
	if _, err := toml.Decode("policy = "+policy+"\n", &decoded); err != nil {
		t.Fatalf("decode generated policy %q: %v", policy, err)
	}
	if decoded.Policy.Inherit != "none" {
		t.Fatalf("inherit = %q, want none", decoded.Policy.Inherit)
	}
	if len(decoded.Policy.IncludeOnly) != 1 || decoded.Policy.IncludeOnly[0] != "CONTROL" {
		t.Fatalf("include_only = %v, want CONTROL", decoded.Policy.IncludeOnly)
	}
	if got := decoded.Policy.Set["CONTROL"]; got != value {
		t.Fatalf("round-trip value = %q, want %q", got, value)
	}
}

func TestCodexShellEnvironmentPolicyRejectsInvalidUTF8(t *testing.T) {
	_, err := codexShellEnvironmentPolicy(map[string]string{
		"INVALID": string([]byte{0xff}),
	})
	if err == nil || !strings.Contains(err.Error(), "invalid UTF-8") {
		t.Fatalf("codexShellEnvironmentPolicy error = %v, want invalid UTF-8", err)
	}
}

func TestMutableJSONStateRejectsConcurrentRotation(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.json")
	if err := os.WriteFile(source, []byte(`{"token":"old"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	firstDest := filepath.Join(root, "first", "state.json")
	secondDest := filepath.Join(root, "second", "state.json")
	firstSync, err := bridgeMutableJSONState(source, firstDest)
	if err != nil {
		t.Fatal(err)
	}
	secondSync, err := bridgeMutableJSONState(source, secondDest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(firstDest, []byte(`{"token":"first"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondDest, []byte(`{"token":"second"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := firstSync(); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if err := secondSync(); err == nil || !strings.Contains(err.Error(), "changed concurrently") {
		t.Fatalf("second sync error = %v, want concurrent-change rejection", err)
	}
	got, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"token":"first"}` {
		t.Fatalf("source = %q, want first rotation preserved", got)
	}
}

func TestMutableJSONStateRejectsSourceFIFOReplacementWithoutBlocking(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.json")
	if err := os.WriteFile(source, []byte(`{"token":"old"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "isolated", "state.json")
	syncState, err := bridgeMutableJSONState(source, dest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte(`{"token":"rotated"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(source, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := syncState(); err == nil || !strings.Contains(err.Error(), "changed file type") {
		t.Fatalf("sync after FIFO replacement error = %v, want file-type rejection", err)
	}
	info, err := os.Lstat(source)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("source mode = %v, FIFO was unexpectedly replaced", info.Mode())
	}
}

func TestMutableJSONStateRejectsAbsentParentSymlinkRetarget(t *testing.T) {
	root := t.TempDir()
	firstTarget := filepath.Join(root, "first")
	secondTarget := filepath.Join(root, "second")
	if err := os.Mkdir(firstTarget, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(secondTarget, 0o700); err != nil {
		t.Fatal(err)
	}
	parentLink := filepath.Join(root, "provider-home")
	if err := os.Symlink(firstTarget, parentLink); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(parentLink, "state.json")
	dest := filepath.Join(root, "isolated", "state.json")
	syncState, err := bridgeMutableJSONState(source, dest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte(`{"token":"new"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(parentLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secondTarget, parentLink); err != nil {
		t.Fatal(err)
	}

	if err := syncState(); err == nil || !strings.Contains(err.Error(), "existing parent changed target") {
		t.Fatalf("sync after parent symlink retarget error = %v", err)
	}
	for _, target := range []string{firstTarget, secondTarget} {
		if _, err := os.Lstat(filepath.Join(target, "state.json")); !os.IsNotExist(err) {
			t.Fatalf("state was unexpectedly created under %s: %v", target, err)
		}
	}
}

func TestMutableJSONStateAcceptsEmptyFirstRunFile(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.json")
	if err := os.WriteFile(source, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "isolated", "state.json")
	syncState, err := bridgeMutableJSONState(source, dest)
	if err != nil {
		t.Fatalf("bridge empty state: %v", err)
	}
	if got := string(mustReadInternalFile(t, dest)); got != "{}\n" {
		t.Fatalf("isolated seed = %q, want empty JSON object", got)
	}
	if err := syncState(); err != nil {
		t.Fatalf("sync unchanged empty state: %v", err)
	}
	if got := mustReadInternalFile(t, source); len(got) != 0 {
		t.Fatalf("unchanged first-run source = %q, want original empty bytes", got)
	}
}

func TestMutableJSONStateLetsProviderRepairMalformedState(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.json")
	if err := os.WriteFile(source, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "isolated", "state.json")
	syncState, err := bridgeMutableJSONState(source, dest)
	if err != nil {
		t.Fatalf("bridge malformed state: %v", err)
	}
	if err := os.WriteFile(dest, []byte(`{"repaired":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := syncState(); err != nil {
		t.Fatalf("sync repaired state: %v", err)
	}
	if got := string(mustReadInternalFile(t, source)); got != `{"repaired":true}` {
		t.Fatalf("repaired source = %q", got)
	}
}

func TestAtomicWritePrivateStateFallsBackForBusyBindMount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mounted.json")
	if err := os.WriteFile(path, []byte(`{"token":"old"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	renameBusy := func(oldPath, newPath string) error {
		return &os.LinkError{
			Op:  "rename",
			Old: oldPath,
			New: newPath,
			Err: syscall.EBUSY,
		}
	}
	const updated = `{"token":"rotated"}`
	if err := atomicWritePrivateStateWithRename(path, []byte(updated), renameBusy); err != nil {
		t.Fatalf("write busy bind-mounted state: %v", err)
	}
	if got := string(mustReadInternalFile(t, path)); got != updated {
		t.Fatalf("busy mounted state = %q, want %q", got, updated)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("busy mounted state mode = %o, want 600", got)
	}
}

func TestMutableJSONStateReportsReadOnlyPersistence(t *testing.T) {
	root := t.TempDir()
	sourceDir := filepath.Join(root, "source")
	if err := os.Mkdir(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(sourceDir, "state.json")
	if err := os.WriteFile(source, []byte(`{"token":"old"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "isolated", "state.json")
	syncState, err := bridgeMutableJSONState(source, dest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte(`{"token":"rotated"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sourceDir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(sourceDir, 0o700) }()
	if err := syncState(); err == nil {
		t.Fatal("read-only generic state persistence unexpectedly succeeded")
	}
	if got := string(mustReadInternalFile(t, source)); got != `{"token":"old"}` {
		t.Fatalf("read-only source changed to %q", got)
	}
}

func mustReadInternalFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

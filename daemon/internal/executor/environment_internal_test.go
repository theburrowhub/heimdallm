package executor

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
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

func TestAdditionalEnvironmentValueCanonicalizesOnlySSHAuthSock(t *testing.T) {
	captured := capturedEnvironment{values: map[string]string{
		"SSH_AUTH_SOCK": "/tmp/canonical.sock",
		"ssh_auth_sock": "/tmp/lower.sock",
		"Mixed_Case":    "exact",
	}}

	if got, ok := captured.additionalEnvironmentValue("SSH_AUTH_SOCK"); !ok || got != "/tmp/canonical.sock" {
		t.Fatalf("SSH_AUTH_SOCK = %q, %t, want canonical value", got, ok)
	}
	if got, ok := captured.additionalEnvironmentValue("Mixed_Case"); !ok || got != "exact" {
		t.Fatalf("Mixed_Case = %q, %t, want exact value", got, ok)
	}
	if got, ok := captured.additionalEnvironmentValue("MIXED_CASE"); ok {
		t.Fatalf("MIXED_CASE = %q, %t, want case-sensitive miss", got, ok)
	}

	delete(captured.values, "SSH_AUTH_SOCK")
	if got, ok := captured.additionalEnvironmentValue("SSH_AUTH_SOCK"); !ok || got != "/tmp/lower.sock" {
		t.Fatalf("lowercase SSH socket = %q, %t, want canonicalized fallback", got, ok)
	}
}

func TestEnvironmentFlagEnabledMatchesClaudeBooleanValues(t *testing.T) {
	for _, value := range []string{"1", "true", "TRUE", " yes ", "ON"} {
		if !environmentFlagEnabled(value) {
			t.Errorf("environmentFlagEnabled(%q) = false, want true", value)
		}
	}
	for _, value := range []string{"", "0", "false", "no", "off", "unexpected"} {
		if environmentFlagEnabled(value) {
			t.Errorf("environmentFlagEnabled(%q) = true, want false", value)
		}
	}
}

func TestPrepareRejectsConflictingClaudeBackendsBeforeCreatingHome(t *testing.T) {
	tmpRoot := t.TempDir()
	t.Setenv("TMPDIR", tmpRoot)
	captured := capturedEnvironment{values: map[string]string{
		"CLAUDE_CODE_USE_BEDROCK": "yes",
		"CLAUDE_CODE_USE_VERTEX":  "ON",
	}}

	prepared, err := captured.prepare("claude")
	if prepared != nil {
		_ = prepared.cleanup()
		t.Fatal("prepare returned an environment for conflicting Claude backends")
	}
	if err == nil ||
		!strings.Contains(err.Error(), "CLAUDE_CODE_USE_BEDROCK") ||
		!strings.Contains(err.Error(), "CLAUDE_CODE_USE_VERTEX") {
		t.Fatalf("prepare error = %v, want both conflicting selectors", err)
	}
	entries, readErr := os.ReadDir(tmpRoot)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("prepare created isolated state before rejecting conflict: %v", entries)
	}
}

func TestKnownProviderPolicyEnvironmentNamesRemainBlocked(t *testing.T) {
	names := []string{
		"CLAUDE_CODE_SAFE_MODE",
		"CLAUDE_CODE_SUBPROCESS_ENV_SCRUB",
		"CLAUDE_CONFIG_DIR",
		"CODEX_HOME",
		"GEMINI_CLI_HOME",
		"GEMINI_CLI_IDE_SERVER_STDIO_COMMAND",
		"GEMINI_CLI_SYSTEM_DEFAULTS_PATH",
		"GEMINI_CLI_SYSTEM_SETTINGS_PATH",
		"GEMINI_CLI_TRUSTED_FOLDERS_PATH",
		"GEMINI_CLI_TRUST_WORKSPACE",
		"GEMINI_SANDBOX",
		"GEMINI_SANDBOX_PROXY_COMMAND",
		"GEMINI_SYSTEM_MD",
		"GEMINI_WRITE_SYSTEM_MD",
		"OPENCODE_CONFIG",
		"OPENCODE_CONFIG_CONTENT",
		"OPENCODE_CONFIG_DIR",
		"OPENCODE_DISABLE_PROJECT_CONFIG",
		"OPENCODE_PURE",
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			if err := validateAdditionalEnvironmentName("codex", strings.ToLower(name)); err == nil {
				t.Fatalf("%s is no longer blocked case-insensitively", name)
			}
		})
	}
}

func TestUnwritableStateSourceErrorIsNarrow(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "read-only filesystem", err: syscall.EROFS, want: true},
		{name: "permission denied", err: syscall.EACCES, want: true},
		{name: "operation not permitted", err: syscall.EPERM, want: true},
		{name: "wrapped permission error", err: fmt.Errorf("persist: %w", syscall.EACCES), want: true},
		{name: "disk full", err: syscall.ENOSPC},
		{name: "I/O error", err: syscall.EIO},
		{name: "invalid argument", err: syscall.EINVAL},
		{name: "unresolved busy mount", err: syscall.EBUSY},
		{name: "unrelated wrapped error", err: fmt.Errorf("persist: %w", errors.New("boom"))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := unwritableStateSourceError(tc.err); got != tc.want {
				t.Fatalf("unwritableStateSourceError(%v) = %t, want %t", tc.err, got, tc.want)
			}
		})
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

func TestClaudeMutableJSONStateKeepsUnwritableSourceEphemeral(t *testing.T) {
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
	syncState, err := bridgeClaudeMutableJSONState(source, dest)
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

	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(
		&logs,
		&slog.HandlerOptions{Level: slog.LevelWarn},
	)))
	defer slog.SetDefault(previousLogger)

	if err := syncState(); err != nil {
		t.Fatalf("unwritable provider state should remain ephemeral: %v", err)
	}
	if got := string(mustReadInternalFile(t, source)); got != `{"token":"old"}` {
		t.Fatalf("unwritable source changed to %q", got)
	}
	if got := logs.String(); !strings.Contains(got, "refreshed state is ephemeral") {
		t.Fatalf("operator warning missing from logs:\n%s", got)
	}
}

func TestClaudeMutableJSONStateKeepsUnwritableFirstRunEphemeral(t *testing.T) {
	root := t.TempDir()
	sourceDir := filepath.Join(root, "source")
	if err := os.Mkdir(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(sourceDir, "state.json")
	dest := filepath.Join(root, "isolated", "state.json")
	syncState, err := bridgeClaudeMutableJSONState(source, dest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte(`{"token":"first-login"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sourceDir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(sourceDir, 0o700) }()
	if err := syncState(); err != nil {
		t.Fatalf("unwritable first-run state should remain ephemeral: %v", err)
	}
	if _, err := os.Lstat(source); !os.IsNotExist(err) {
		t.Fatalf("first-run source was unexpectedly created: %v", err)
	}
}

func TestMutableJSONStateReportsUnwritableSourceByDefault(t *testing.T) {
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
		t.Fatal("generic state bridge unexpectedly hid an unwritable source")
	}
	if got := string(mustReadInternalFile(t, source)); got != `{"token":"old"}` {
		t.Fatalf("unwritable source changed to %q", got)
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

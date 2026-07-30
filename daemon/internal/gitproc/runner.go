package gitproc

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	defaultTimeout = 3 * time.Minute
	maxStderrBytes = 16 * 1024
)

// Runner is the single subprocess boundary for daemon-owned Git commands.
// It builds the child environment from scratch, forwarding only an explicit
// proxy/CA compatibility allowlist. Each invocation gets a fresh, owner-only
// HOME, XDG config root, temporary directory and empty hooks directory.
type Runner struct {
	gitPath string
	initErr error
	timeout time.Duration
}

// New resolves the Git executable once. Resolution happens before any
// repository-controlled working directory is selected and the resulting
// absolute path is used for every child process.
func New() *Runner {
	path, err := exec.LookPath("git")
	if err == nil {
		path, err = filepath.Abs(path)
	}
	if err == nil {
		path, err = filepath.EvalSymlinks(path)
	}
	return &Runner{gitPath: path, initErr: err, timeout: defaultTimeout}
}

// NewWithPath is intended for hermetic tests and packaged deployments that
// already know the trusted Git binary path.
func NewWithPath(path string) *Runner {
	abs, err := filepath.Abs(path)
	if err == nil {
		abs, err = filepath.EvalSymlinks(abs)
	}
	return &Runner{gitPath: abs, initErr: err, timeout: defaultTimeout}
}

// Run executes a Git command in an existing repository. Local and worktree
// configuration is audited before the command starts.
func (r *Runner) Run(ctx context.Context, dir string, args ...string) error {
	_, err := r.execute(ctx, request{
		dir:   dir,
		args:  args,
		audit: true,
	})
	return err
}

// Capture is Run with stdout returned to the caller.
func (r *Runner) Capture(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return r.execute(ctx, request{
		dir:   dir,
		args:  args,
		audit: true,
	})
}

func (r *Runner) runAudited(ctx context.Context, dir string, allowFile bool, args ...string) error {
	_, err := r.execute(ctx, request{
		dir:       dir,
		args:      args,
		audit:     true,
		allowFile: allowFile,
	})
	return err
}

type request struct {
	dir       string
	args      []string
	audit     bool
	allowFile bool
	token     string
}

func (r *Runner) runTrusted(ctx context.Context, dir string, allowFile bool, args ...string) error {
	_, err := r.execute(ctx, request{
		dir:       dir,
		args:      args,
		allowFile: allowFile,
	})
	return err
}

func (r *Runner) captureTrusted(ctx context.Context, dir string, allowFile bool, args ...string) ([]byte, error) {
	return r.execute(ctx, request{
		dir:       dir,
		args:      args,
		allowFile: allowFile,
	})
}

func (r *Runner) runTrustedWithToken(
	ctx context.Context,
	dir string,
	token string,
	args ...string,
) error {
	_, err := r.execute(ctx, request{
		dir:   dir,
		args:  args,
		token: token,
	})
	return err
}

func (r *Runner) execute(ctx context.Context, req request) ([]byte, error) {
	if r == nil {
		return nil, errors.New("gitproc: nil runner")
	}
	if r.initErr != nil {
		return nil, fmt.Errorf("gitproc: resolve git executable: %w", r.initErr)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	dir := req.dir
	safeDirectory := ""
	if req.audit {
		if err := validateAuditedArgs(req.args); err != nil {
			return nil, err
		}
		var err error
		dir, err = r.AuditRepository(ctx, dir)
		if err != nil {
			return nil, err
		}
		safeDirectory = dir
	}

	box, err := newSandbox()
	if err != nil {
		return nil, err
	}
	defer box.cleanup()

	env := box.environment(r.gitPath)
	if req.token != "" {
		askpass, err := box.writeAskPass()
		if err != nil {
			return nil, err
		}
		env = append(env,
			"GIT_ASKPASS="+askpass,
			"GIT_ASKPASS_REQUIRE=force",
			"HEIMDALLM_GIT_TOKEN="+req.token,
		)
	}
	sort.Strings(env)

	args := hardenedArgs(box.hooks, req.allowFile, safeDirectory, req.args)
	runCtx, cancel := context.WithTimeout(ctx, r.commandTimeout())
	defer cancel()

	cmd := exec.CommandContext(runCtx, r.gitPath, args...)
	if dir == "" {
		cmd.Dir = box.root
	} else {
		cmd.Dir = dir
	}
	cmd.Env = env

	var stdout bytes.Buffer
	stderr := &boundedBuffer{limit: maxStderrBytes}
	cmd.Stdout = &stdout
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		errText := redact(stderr.String(), req.token)
		for _, secret := range forwardedEnvironmentSecrets(env) {
			errText = redact(errText, secret)
		}
		if stderr.truncated {
			errText += "\n... (stderr truncated)"
		}
		return nil, fmt.Errorf("%w (stderr: %s)", err, strings.TrimSpace(errText))
	}
	return stdout.Bytes(), nil
}

func validateAuditedArgs(args []string) error {
	if len(args) == 0 {
		return errors.New("gitproc: Git command is required")
	}
	for index := 0; index < len(args); {
		arg := args[index]
		if arg == "-c" {
			if index+1 >= len(args) {
				return errors.New("gitproc: missing value for Git -c")
			}
			config := args[index+1]
			key, _, hasValue := strings.Cut(config, "=")
			key = strings.ToLower(strings.TrimSpace(key))
			if !hasValue || !allowedAuditedConfigKey(key) {
				return fmt.Errorf("gitproc: Git config override %q is not allowed", config)
			}
			index += 2
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return fmt.Errorf("gitproc: Git global option %q is not allowed", arg)
		}
		// The first non-option is the subcommand. Later arguments are parsed
		// by that fixed subcommand and cannot override Git's global config.
		if !allowedAuditedSubcommand(arg) {
			return fmt.Errorf("gitproc: Git subcommand %q is not allowed", arg)
		}
		return nil
	}
	return errors.New("gitproc: Git subcommand is required")
}

func allowedAuditedConfigKey(key string) bool {
	switch key {
	case "core.quotepath", "user.name", "user.email":
		return true
	default:
		return false
	}
}

func allowedAuditedSubcommand(command string) bool {
	switch command {
	case "add",
		"checkout",
		"clean",
		"commit",
		"diff",
		"fetch",
		"log",
		"remote",
		"reset",
		"rev-parse",
		"status",
		"worktree":
		return true
	default:
		return false
	}
}

func (r *Runner) commandTimeout() time.Duration {
	if r.timeout <= 0 {
		return defaultTimeout
	}
	return r.timeout
}

func hardenedArgs(hooksDir string, allowFile bool, safeDirectory string, args []string) []string {
	safe := []string{
		"--no-pager",
		"-c", "core.hooksPath=" + hooksDir,
		"-c", "core.fsmonitor=false",
		"-c", "core.attributesFile=" + os.DevNull,
		"-c", "core.excludesFile=" + os.DevNull,
		"-c", "core.askPass=",
		"-c", "core.editor=false",
		"-c", "sequence.editor=false",
		"-c", "credential.helper=",
		"-c", "credential.interactive=never",
		"-c", "credential.useHttpPath=false",
		"-c", "commit.gpgSign=false",
		"-c", "push.gpgSign=false",
		"-c", "tag.gpgSign=false",
		"-c", "log.showSignature=false",
		"-c", "submodule.recurse=false",
		"-c", "fetch.recurseSubmodules=false",
		"-c", "push.recurseSubmodules=no",
		"-c", "gc.auto=0",
		"-c", "maintenance.auto=false",
		"-c", "protocol.allow=never",
		"-c", "protocol.https.allow=always",
		"-c", "protocol.ssh.allow=never",
		"-c", "protocol.ext.allow=never",
		"-c", "core.sshCommand=false",
	}
	if safeDirectory != "" {
		// Linux bind mounts can retain a host UID different from the daemon's
		// UID. Trust only the canonical worktree that AuditRepository just
		// accepted; never use safe.directory=* or an unresolved caller path.
		safe = append(safe, "-c", "safe.directory="+safeDirectory)
	}
	if allowFile {
		safe = append(safe, "-c", "protocol.file.allow=always")
	} else {
		safe = append(safe, "-c", "protocol.file.allow=never")
	}
	return append(safe, args...)
}

type sandbox struct {
	root  string
	home  string
	xdg   string
	tmp   string
	hooks string
}

func newSandbox() (*sandbox, error) {
	root, err := os.MkdirTemp("", "heimdallm-git-*")
	if err != nil {
		return nil, fmt.Errorf("gitproc: create sandbox: %w", err)
	}
	cleanupOnError := func(cause error) (*sandbox, error) {
		_ = os.RemoveAll(root)
		return nil, cause
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return cleanupOnError(fmt.Errorf("gitproc: chmod sandbox: %w", err))
	}
	box := &sandbox{
		root:  root,
		home:  filepath.Join(root, "home"),
		xdg:   filepath.Join(root, "xdg"),
		tmp:   filepath.Join(root, "tmp"),
		hooks: filepath.Join(root, "hooks"),
	}
	for _, dir := range []string{box.home, box.xdg, box.tmp, box.hooks} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			return cleanupOnError(fmt.Errorf("gitproc: create sandbox directory: %w", err))
		}
	}
	return box, nil
}

func (s *sandbox) cleanup() {
	if s != nil && s.root != "" {
		_ = os.RemoveAll(s.root)
	}
}

func (s *sandbox) environment(gitPath string) []string {
	var pathEntries []string
	seen := make(map[string]struct{})
	for _, entry := range []string{filepath.Dir(gitPath), "/usr/local/bin", "/usr/bin", "/bin"} {
		if !filepath.IsAbs(entry) {
			continue
		}
		entry = filepath.Clean(entry)
		if _, exists := seen[entry]; exists {
			continue
		}
		seen[entry] = struct{}{}
		pathEntries = append(pathEntries, entry)
	}
	env := []string{
		"GCM_INTERACTIVE=never",
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_SYSTEM=" + os.DevNull,
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_PAGER=cat",
		"GIT_TERMINAL_PROMPT=0",
		"HOME=" + s.home,
		"LANG=C",
		"LC_ALL=C",
		"PAGER=cat",
		"PATH=" + strings.Join(pathEntries, string(os.PathListSeparator)),
		"TMPDIR=" + s.tmp,
		"XDG_CONFIG_HOME=" + s.xdg,
	}
	// Network and CA settings are an explicit compatibility allowlist for
	// corporate proxies. No Git-, shell- or language-runtime control
	// variables are inherited.
	for _, name := range []string{
		"ALL_PROXY", "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY",
		"all_proxy", "https_proxy", "http_proxy", "no_proxy",
		"CURL_CA_BUNDLE", "SSL_CERT_DIR", "SSL_CERT_FILE",
	} {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	return env
}

func (s *sandbox) writeAskPass() (string, error) {
	path := filepath.Join(s.root, "askpass.sh")
	const script = `#!/bin/sh
prompt=$1
while [ "${prompt% }" != "$prompt" ]; do
  prompt=${prompt% }
done
case "$prompt" in
  "Password for 'https://x-access-token@github.com':")
    printf '%s' "$HEIMDALLM_GIT_TOKEN"
    ;;
  *)
    exit 1
    ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		return "", fmt.Errorf("gitproc: write askpass helper: %w", err)
	}
	return path, nil
}

type boundedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	original := len(p)
	remaining := b.limit - b.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.buf.Write(p)
	}
	if original > remaining {
		b.truncated = true
	}
	return original, nil
}

func (b *boundedBuffer) String() string { return b.buf.String() }

func redact(value, token string) string {
	if token == "" {
		return value
	}
	forms := []string{
		token,
		url.QueryEscape(token),
		url.PathEscape(token),
		base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token)),
	}
	for _, form := range forms {
		if form != "" {
			value = strings.ReplaceAll(value, form, "[REDACTED]")
		}
	}
	return value
}

func forwardedEnvironmentSecrets(env []string) []string {
	var secrets []string
	for _, entry := range env {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || value == "" {
			continue
		}
		switch strings.ToUpper(name) {
		case "ALL_PROXY", "HTTPS_PROXY", "HTTP_PROXY":
			secrets = append(secrets, value)
			parsed, err := url.Parse(value)
			if err != nil || parsed.User == nil {
				continue
			}
			if password, ok := parsed.User.Password(); ok && password != "" {
				secrets = append(secrets, password)
			}
			if userInfo := parsed.User.String(); userInfo != "" {
				secrets = append(secrets, userInfo)
			}
		}
	}
	return secrets
}

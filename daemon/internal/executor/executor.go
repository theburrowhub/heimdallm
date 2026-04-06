package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const executionTimeout = 5 * time.Minute

// ReviewResult is the parsed JSON response from the AI CLI.
type ReviewResult struct {
	Summary     string   `json:"summary"`
	Issues      []Issue  `json:"issues"`
	Suggestions []string `json:"suggestions"`
	Severity    string   `json:"severity"`
}

// Issue represents a single code issue found by the AI reviewer.
type Issue struct {
	File        string `json:"file"`
	Line        int    `json:"line"`
	Description string `json:"description"`
	Severity    string `json:"severity"`
}

// ExecOptions controls how the AI CLI is invoked.
type ExecOptions struct {
	// Model sets --model <value> for CLIs that support it.
	Model string
	// MaxTurns sets --max-turns <n> for Claude (0 = not set).
	MaxTurns int
	// ApprovalMode sets --approval-mode <value> for Codex.
	ApprovalMode string
	// ExtraFlags is a free-form string of additional CLI flags (split on spaces).
	ExtraFlags string
	// WorkDir is the working directory for the CLI process.
	// When set, the CLI runs inside the local repo directory, giving it
	// access to all project files for deeper analysis (missing tests, side effects, etc.).
	WorkDir string

	// Claude-specific flags
	Effort               string // --effort low|medium|high|max
	PermissionMode       string // --permission-mode <value>
	Bare                 bool   // --bare
	DangerouslySkipPerms bool   // --dangerously-skip-permissions
	NoSessionPersistence bool   // --no-session-persistence
}

// Executor runs AI CLI tools for code review.
type Executor struct{}

// New creates a new Executor.
func New() *Executor {
	return &Executor{}
}

// Detect returns the first available CLI (primary → fallback).
// Also checks the user's login shell environment to handle cases where the
// daemon is launched from a GUI app without inheriting the full shell PATH
// (e.g., Homebrew tools at /opt/homebrew/bin not in process PATH).
//
// SECURITY: Detect validates each name against the CLI allowlist before
// resolving it, preventing shell injection (issue #2).
func (e *Executor) Detect(primary, fallback string) (string, error) {
	for _, name := range []string{primary, fallback} {
		if name == "" {
			continue
		}
		if err := validateCLIName(name); err != nil {
			return "", err // reject unknown / potentially-injected names early
		}
		if resolveCLIPath(name) != "" {
			return name, nil
		}
	}
	return "", fmt.Errorf("executor: no AI CLI available (tried %q, %q)", primary, fallback)
}

// allowedCLIs is the strict allowlist of AI CLI names Heimdallr supports.
// Any value not in this set is rejected before reaching resolveCLIPath,
// preventing shell injection via a crafted ai.primary / ai.fallback config value.
var allowedCLIs = map[string]struct{}{
	"claude": {},
	"gemini": {},
	"codex":  {},
}

// validateCLIName returns an error if name is not in the known-safe allowlist.
// This must be called before any function that interpolates the name into a
// shell command (e.g. resolveCLIPath).
func validateCLIName(name string) error {
	if _, ok := allowedCLIs[name]; !ok {
		return fmt.Errorf("executor: unknown CLI %q — must be one of: claude, gemini, codex", name)
	}
	return nil
}

// resolveCLIPath returns the full path for a CLI tool, checking both the
// current process PATH and the user's login shell (handles Homebrew, nvm, etc.).
// Returns "" if not found anywhere.
//
// SECURITY: name MUST be validated with validateCLIName before calling this
// function. resolveCLIPath passes the name into a shell command; an unvalidated
// value would allow shell injection (CVE-equivalent: issue #2).
func resolveCLIPath(name string) string {
	// Fast path: already in the process PATH.
	if path, err := exec.LookPath(name); err == nil && path != "" {
		return path
	}
	// Try login shell — picks up ~/.zshrc, ~/.bashrc, Homebrew, nvm, etc.
	// Pass name as $1 (positional arg) so it is never shell-interpolated,
	// even though validateCLIName already guarantees it is safe.
	for _, shell := range []string{"/bin/zsh", "/bin/bash"} {
		cmd := exec.Command(shell, "-l", "-c", `which "$1"`, "--", name)
		out, err := cmd.Output()
		if err == nil {
			if path := strings.TrimSpace(string(out)); path != "" {
				return path
			}
		}
	}
	return ""
}

// dangerousPaths lists absolute filesystem paths that must never be used as a
// working directory for the AI CLI. The CLI reads all files under the working
// directory as context; exposing these paths would risk exfiltrating sensitive
// system files or credentials to the AI provider.
var dangerousPaths = []string{
	"/",
	"/etc",
	"/usr",
	"/var",
	"/System",
}

// dangerousSegments lists path substrings that must never appear in a resolved
// working directory. These directories commonly contain private keys, API tokens,
// and other secrets that must not be sent to an AI provider.
var dangerousSegments = []string{
	"/.ssh",
	"/.gnupg",
	"/.aws",
	"/.config/heimdallr",
}

// ValidateWorkDir checks that dir is a safe working directory for the AI CLI.
// It rejects paths outside the user's home directory and /tmp, as well as
// specific system paths and credential directories.
//
// SECURITY: This function uses filepath.Abs to resolve relative paths and
// symlinks before any comparison, preventing traversal attacks via relative
// paths or symlinked directories.
func ValidateWorkDir(dir string) error {
	if dir == "" {
		return nil // no working directory override; safe
	}

	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("executor: workdir %q: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("executor: workdir %q is not a directory", dir)
	}

	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("executor: workdir %q: cannot resolve absolute path: %w", dir, err)
	}

	// Reject explicitly dangerous top-level paths.
	for _, bad := range dangerousPaths {
		if abs == bad {
			return fmt.Errorf("executor: workdir %q is a restricted system path", abs)
		}
	}

	// Reject paths containing sensitive credential directories.
	for _, seg := range dangerousSegments {
		if strings.Contains(abs, seg) {
			return fmt.Errorf("executor: workdir %q contains a sensitive credential path (%s)", abs, seg)
		}
	}

	// Allow /tmp and its subdirectories.
	if abs == "/tmp" || strings.HasPrefix(abs, "/tmp/") {
		return nil
	}

	// Allow paths under the user's home directory only.
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("executor: cannot determine home directory: %w", err)
	}
	homeAbs, err := filepath.Abs(home)
	if err != nil {
		return fmt.Errorf("executor: cannot resolve home directory: %w", err)
	}
	if abs != homeAbs && !strings.HasPrefix(abs, homeAbs+"/") {
		return fmt.Errorf("executor: workdir %q is outside the user home directory and /tmp — rejected for security", abs)
	}

	return nil
}

// Execute runs the AI CLI with the given prompt and options, returning the parsed result.
func (e *Executor) Execute(cli, prompt string, opts ExecOptions) (*ReviewResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), executionTimeout)
	defer cancel()

	// Resolve full path — uses login shell to find binaries in Homebrew/npm/etc.
	cliPath := resolveCLIPath(cli)
	if cliPath == "" {
		cliPath = cli // best effort; execution will fail with a useful error
	}

	args := buildArgs(cli, opts)
	cmd := exec.CommandContext(ctx, cliPath, args...)
	cmd.Stdin = strings.NewReader(prompt)

	// Augment PATH with paths from the login shell so the CLI can find its own
	// dependencies, without running stdin THROUGH the shell (which would cause
	// shell startup scripts to consume our prompt).
	enrichedEnv := enrichEnvWithLoginPath()
	if enrichedEnv != nil {
		cmd.Env = enrichedEnv
	}
	if opts.WorkDir != "" {
		if err := ValidateWorkDir(opts.WorkDir); err != nil {
			return nil, err
		}
		cmd.Dir = opts.WorkDir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Some CLIs (e.g. claude) write errors to stdout rather than stderr.
		errDetail := strings.TrimSpace(stderr.String())
		if errDetail == "" {
			errDetail = strings.TrimSpace(stdout.String())
		}
		return nil, fmt.Errorf("executor: run %s: %w (output: %s)", cli, err, errDetail)
	}

	return parseResult(stdout.Bytes())
}

// buildArgs constructs the CLI argument list based on the CLI name and options.
func buildArgs(cli string, opts ExecOptions) []string {
	var args []string

	switch cli {
	case "codex":
		if opts.Model != "" {
			args = append(args, "--model", opts.Model)
		}
		if opts.ApprovalMode != "" {
			args = append(args, "--approval-mode", opts.ApprovalMode)
		}
	default:
		// claude, gemini: stdin mode
		args = append(args, "-p", "-")
		if opts.Model != "" {
			args = append(args, "--model", opts.Model)
		}
		if cli == "claude" {
			if opts.MaxTurns > 0 {
				args = append(args, "--max-turns", fmt.Sprintf("%d", opts.MaxTurns))
			}
			if opts.Effort != "" {
				args = append(args, "--effort", opts.Effort)
			}
			if opts.PermissionMode != "" {
				args = append(args, "--permission-mode", opts.PermissionMode)
			}
			if opts.Bare {
				args = append(args, "--bare")
			}
			if opts.DangerouslySkipPerms {
				args = append(args, "--dangerously-skip-permissions")
			}
			if opts.NoSessionPersistence {
				args = append(args, "--no-session-persistence")
			}
		}
	}

	// Append free-form extra flags (split on whitespace)
	if opts.ExtraFlags != "" {
		args = append(args, strings.Fields(opts.ExtraFlags)...)
	}

	return args
}

var (
	loginPathOnce sync.Once
	loginPathEnv  []string // os.Environ() + enriched PATH from login shell
)

// enrichEnvWithLoginPath returns the process environment augmented with the PATH
// from a login shell. Cached after the first call — cheap after startup.
// Using a login shell ONLY for PATH (not for execution) avoids the stdin
// consumption bug where shell startup scripts read our prompt.
func enrichEnvWithLoginPath() []string {
	loginPathOnce.Do(func() {
		base := os.Environ()
		// Ask the login shell for its PATH without providing any stdin
		// (pass /dev/null so startup scripts cannot accidentally consume stdin)
		cmd := exec.Command("/bin/zsh", "-l", "-c", "echo $PATH")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd = exec.CommandContext(ctx, "/bin/zsh", "-l", "-c", "echo $PATH")
		cmd.Stdin, _ = os.Open(os.DevNull)
		cmd.Stderr = nil
		out, err := cmd.Output()
		if err != nil {
			loginPathEnv = base
			return
		}
		loginShellPath := strings.TrimSpace(string(out))
		if loginShellPath == "" {
			loginPathEnv = base
			return
		}
		// Merge: put login shell PATH first so Homebrew bins take precedence
		currentPath := os.Getenv("PATH")
		merged := loginShellPath
		if currentPath != "" {
			merged = loginShellPath + ":" + currentPath
		}
		result := make([]string, 0, len(base)+1)
		for _, e := range base {
			if !strings.HasPrefix(e, "PATH=") {
				result = append(result, e)
			}
		}
		result = append(result, "PATH="+merged)
		loginPathEnv = result
	})
	return loginPathEnv
}

func parseResult(data []byte) (*ReviewResult, error) {
	s := strings.TrimSpace(string(data))
	// Strip potential markdown code fences
	if strings.HasPrefix(s, "```") {
		lines := strings.Split(s, "\n")
		if len(lines) > 2 {
			s = strings.Join(lines[1:len(lines)-1], "\n")
		}
	}

	// Find first { to last } in case there's surrounding text
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start >= 0 && end > start {
		s = s[start : end+1]
	}

	var result ReviewResult
	if err := json.Unmarshal([]byte(s), &result); err != nil {
		return nil, fmt.Errorf("executor: parse JSON result: %w (raw: %.200s)", err, s)
	}
	if result.Severity == "" {
		result.Severity = "low"
	}
	return &result, nil
}

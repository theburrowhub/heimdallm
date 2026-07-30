package executor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"
)

const environmentAllowlistPrefix = "HEIMDALLM_AI_"

var providerEnvironmentNames = map[string][]string{
	"claude": {
		"ANTHROPIC_API_KEY",
		"CLAUDE_CODE_OAUTH_TOKEN",
	},
	"codex": {
		"OPENAI_API_KEY",
		"CODEX_API_KEY",
	},
	"gemini": {
		"GEMINI_API_KEY",
		"GOOGLE_API_KEY",
		"GOOGLE_APPLICATION_CREDENTIALS",
		"GOOGLE_CLOUD_PROJECT",
		"GOOGLE_CLOUD_LOCATION",
		"GOOGLE_GENAI_USE_VERTEXAI",
	},
	"opencode": {
		"OPENROUTER_API_KEY",
	},
}

var providerRuntimeNames = map[string][]string{
	"opencode": {
		"OPENCODE_DISABLE_AUTOUPDATE",
	},
}

var providerSecretNames = map[string]struct{}{
	"ANTHROPIC_API_KEY":       {},
	"CLAUDE_CODE_OAUTH_TOKEN": {},
	"CODEX_API_KEY":           {},
	"GEMINI_API_KEY":          {},
	"GOOGLE_API_KEY":          {},
	"OPENAI_API_KEY":          {},
	"OPENROUTER_API_KEY":      {},
}

var commonEnvironmentNames = map[string]struct{}{
	"ALL_PROXY":           {},
	"CI":                  {},
	"COLORTERM":           {},
	"COMSPEC":             {},
	"CURL_CA_BUNDLE":      {},
	"HTTPS_PROXY":         {},
	"HTTP_PROXY":          {},
	"LANG":                {},
	"LANGUAGE":            {},
	"LC_ADDRESS":          {},
	"LC_ALL":              {},
	"LC_COLLATE":          {},
	"LC_CTYPE":            {},
	"LC_IDENTIFICATION":   {},
	"LC_MEASUREMENT":      {},
	"LC_MESSAGES":         {},
	"LC_MONETARY":         {},
	"LC_NAME":             {},
	"LC_NUMERIC":          {},
	"LC_PAPER":            {},
	"LC_TELEPHONE":        {},
	"LC_TIME":             {},
	"LOGNAME":             {},
	"NODE_EXTRA_CA_CERTS": {},
	"NO_COLOR":            {},
	"NO_PROXY":            {},
	"PATHEXT":             {},
	"REQUESTS_CA_BUNDLE":  {},
	"SHELL":               {},
	"SSL_CERT_DIR":        {},
	"SSL_CERT_FILE":       {},
	"SYSTEMROOT":          {},
	"TERM":                {},
	"TZ":                  {},
	"USER":                {},
	"WINDIR":              {},
}

var managedEnvironmentNames = map[string]struct{}{
	"HOME":            {},
	"PATH":            {},
	"PWD":             {},
	"TEMP":            {},
	"TMP":             {},
	"TMPDIR":          {},
	"XDG_CACHE_HOME":  {},
	"XDG_CONFIG_HOME": {},
	"XDG_DATA_HOME":   {},
	"XDG_STATE_HOME":  {},
}

var dangerousEnvironmentNames = map[string]struct{}{
	"_JAVA_OPTIONS":                       {},
	"BASH_ENV":                            {},
	"CLAUDE_CODE_SAFE_MODE":               {},
	"CLAUDE_CODE_SUBPROCESS_ENV_SCRUB":    {},
	"CDPATH":                              {},
	"CLAUDE_CONFIG_DIR":                   {},
	"CODEX_HOME":                          {},
	"DOTNET_STARTUP_HOOKS":                {},
	"ENV":                                 {},
	"GIO_EXTRA_MODULES":                   {},
	"GIO_MODULE_DIR":                      {},
	"GEMINI_CLI_HOME":                     {},
	"GEMINI_CLI_IDE_SERVER_STDIO_COMMAND": {},
	"GEMINI_CLI_SYSTEM_DEFAULTS_PATH":     {},
	"GH_TOKEN":                            {},
	"GITHUB_TOKEN":                        {},
	"GIT_ASKPASS":                         {},
	"GTK3_MODULES":                        {},
	"GTK_MODULES":                         {},
	"GTK_PATH":                            {},
	"GEMINI_CLI_SYSTEM_SETTINGS_PATH":     {},
	"GEMINI_CLI_TRUSTED_FOLDERS_PATH":     {},
	"GEMINI_CLI_TRUST_WORKSPACE":          {},
	"GEMINI_SANDBOX":                      {},
	"GEMINI_SANDBOX_PROXY_COMMAND":        {},
	"GEMINI_SYSTEM_MD":                    {},
	"GEMINI_WRITE_SYSTEM_MD":              {},
	"IFS":                                 {},
	"JAVA_TOOL_OPTIONS":                   {},
	"JDK_JAVA_OPTIONS":                    {},
	"NODE_OPTIONS":                        {},
	"NODE_PATH":                           {},
	"NPM_CONFIG_USERCONFIG":               {},
	"OPENCODE_CONFIG":                     {},
	"OPENCODE_CONFIG_CONTENT":             {},
	"OPENCODE_CONFIG_DIR":                 {},
	"OPENCODE_DISABLE_PROJECT_CONFIG":     {},
	"OPENCODE_PURE":                       {},
	"PERL5LIB":                            {},
	"PERL5OPT":                            {},
	"PROMPT_COMMAND":                      {},
	"PYTHONHOME":                          {},
	"PYTHONPATH":                          {},
	"PYTHONSTARTUP":                       {},
	"PYTHONWARNINGS":                      {},
	"RUBYOPT":                             {},
	"SSH_ASKPASS":                         {},
	"ZDOTDIR":                             {},
}

type capturedEnvironment struct {
	values map[string]string
}

type preparedEnvironment struct {
	env          []string
	codexToolEnv map[string]string
	runDir       string
	redact       func(string) string
	cleanup      func() error
}

type providerStateMount struct {
	source   string
	dest     string
	copyBack bool
}

var providerMutableStateMu sync.Mutex
var providerStateAbsenceLogged sync.Map

func captureEnvironment() capturedEnvironment {
	values := make(map[string]string)
	for _, entry := range os.Environ() {
		name, value, ok := strings.Cut(entry, "=")
		if ok {
			values[name] = value
		}
	}
	return capturedEnvironment{values: values}
}

func (c capturedEnvironment) loginProbeEnvironment() []string {
	env := make(map[string]string)
	for name, value := range c.values {
		if probeEnvironmentName(name) {
			env[name] = value
		}
	}
	if home := c.values["HOME"]; home != "" {
		env["HOME"] = home
	}
	env["PATH"] = sanitizePath(c.values["PATH"])
	return environmentSlice(env)
}

func (c capturedEnvironment) prepare(cli string) (*preparedEnvironment, error) {
	extraNames, err := c.additionalNames(cli)
	if err != nil {
		return nil, err
	}

	root, err := os.MkdirTemp("", "heimdallm-"+cli+"-")
	if err != nil {
		return nil, fmt.Errorf("executor: create isolated home for %s: %w", cli, err)
	}
	cleanupRoot := func() { _ = os.RemoveAll(root) }
	if err := os.Chmod(root, 0o700); err != nil {
		cleanupRoot()
		return nil, fmt.Errorf("executor: secure isolated home for %s: %w", cli, err)
	}

	base, err := c.base(root)
	if err != nil {
		cleanupRoot()
		return nil, err
	}

	stateCleanup, err := c.bridgeProviderState(cli, root)
	if err != nil {
		cleanupRoot()
		return nil, err
	}
	cleanup := func() error {
		return errors.Join(stateCleanup(), os.RemoveAll(root))
	}

	env := cloneEnvironment(base)
	redactions := make([]string, 0, len(providerEnvironmentNames[cli])+len(extraNames))
	for _, name := range providerEnvironmentNames[cli] {
		if value := c.values[name]; value != "" {
			env[name] = value
			if _, secret := providerSecretNames[name]; secret {
				redactions = append(redactions, value)
			}
		}
	}
	for _, name := range providerRuntimeNames[cli] {
		if value, ok := c.values[name]; ok {
			env[name] = value
		}
	}
	for _, name := range extraNames {
		if value, ok := c.values[name]; ok {
			env[name] = value
			if value != "" {
				redactions = append(
					redactions,
					additionalEnvironmentRedactionValues(name, value)...,
				)
			}
		} else {
			slog.Warn(
				"executor: allowlisted environment variable is not set",
				"cli", cli,
				"name", name,
			)
		}
	}
	if cli == "claude" {
		// Claude Code additionally scrubs provider credentials from Bash,
		// hooks and MCP children. This managed value is assigned after all
		// operator extras so an allowlist can never weaken the boundary.
		env["CLAUDE_CODE_SUBPROCESS_ENV_SCRUB"] = "1"
	}
	if cli == "gemini" {
		settingsPath := filepath.Join(root, "gemini-system-settings.json")
		const settings = "{\n  \"advanced\": {\n    \"ignoreLocalEnv\": true\n  }\n}\n"
		if err := os.WriteFile(settingsPath, []byte(settings), 0o600); err != nil {
			_ = cleanup()
			return nil, fmt.Errorf("executor: write Gemini system settings: %w", err)
		}
		// System settings override user and project settings. This managed
		// path cannot be replaced through the operator extra-env allowlist.
		env["GEMINI_CLI_SYSTEM_SETTINGS_PATH"] = settingsPath
	}
	if cli == "opencode" {
		// OpenCode otherwise discovers project opencode.json/.opencode
		// configuration after applying --dir. Those files can declare MCP
		// servers and trigger dependency installation. Assign these managed
		// values after operator extras so an allowlist cannot weaken them.
		env["OPENCODE_DISABLE_PROJECT_CONFIG"] = "1"
		env["OPENCODE_PURE"] = "1"
	}
	for name, value := range env {
		if proxyEnvironmentName(name) {
			redactions = append(redactions, proxyRedactionValues(value)...)
		}
	}

	codexToolEnv := codexNestedEnvironment(base)
	if value, ok := env["SSH_AUTH_SOCK"]; ok {
		// The agent socket is only forwarded when the operator explicitly
		// opted this provider in. It is a path, not the private key material.
		codexToolEnv["SSH_AUTH_SOCK"] = value
	}

	return &preparedEnvironment{
		env:          environmentSlice(env),
		codexToolEnv: codexToolEnv,
		runDir:       filepath.Join(root, "tmp"),
		redact:       environmentRedactor(redactions),
		cleanup:      cleanup,
	}, nil
}

func (c capturedEnvironment) prepareProbe() (*preparedEnvironment, error) {
	root, err := os.MkdirTemp("", "heimdallm-cli-probe-")
	if err != nil {
		return nil, fmt.Errorf("executor: create isolated CLI probe home: %w", err)
	}
	cleanupRoot := func() { _ = os.RemoveAll(root) }
	if err := os.Chmod(root, 0o700); err != nil {
		cleanupRoot()
		return nil, fmt.Errorf("executor: secure isolated CLI probe home: %w", err)
	}
	base, err := c.base(root)
	if err != nil {
		cleanupRoot()
		return nil, err
	}
	for name := range base {
		if !probeEnvironmentName(name) && !managedProbeEnvironmentName(name) {
			delete(base, name)
		}
	}
	return &preparedEnvironment{
		env:          environmentSlice(base),
		codexToolEnv: cloneEnvironment(base),
		runDir:       filepath.Join(root, "tmp"),
		redact:       func(value string) string { return value },
		cleanup:      func() error { return os.RemoveAll(root) },
	}, nil
}

func (c capturedEnvironment) base(root string) (map[string]string, error) {
	dirs := []string{
		filepath.Join(root, ".cache"),
		filepath.Join(root, ".config"),
		filepath.Join(root, ".local", "share"),
		filepath.Join(root, ".local", "state"),
		filepath.Join(root, "tmp"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("executor: create isolated environment directory: %w", err)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return nil, fmt.Errorf("executor: secure isolated environment directory: %w", err)
		}
	}

	env := make(map[string]string, len(commonEnvironmentNames)+12)
	for name, value := range c.values {
		// Environment names are matched case-insensitively while preserving
		// the spelling supplied by the parent process.
		if _, allowed := commonEnvironmentNames[strings.ToUpper(name)]; allowed {
			env[name] = value
		}
	}
	env["CI"] = "true"
	env["HOME"] = root
	env["PATH"] = enrichedPath(c.values)
	env["TEMP"] = filepath.Join(root, "tmp")
	env["TMP"] = filepath.Join(root, "tmp")
	env["TMPDIR"] = filepath.Join(root, "tmp")
	env["XDG_CACHE_HOME"] = filepath.Join(root, ".cache")
	env["XDG_CONFIG_HOME"] = filepath.Join(root, ".config")
	env["XDG_DATA_HOME"] = filepath.Join(root, ".local", "share")
	env["XDG_STATE_HOME"] = filepath.Join(root, ".local", "state")
	return env, nil
}

func (c capturedEnvironment) additionalNames(cli string) ([]string, error) {
	controlName := environmentAllowlistPrefix + strings.ToUpper(cli) + "_ENV_ALLOWLIST"
	raw := strings.TrimSpace(c.values[controlName])
	if raw == "" {
		return nil, nil
	}

	seen := make(map[string]struct{})
	var names []string
	for _, item := range strings.Split(raw, ",") {
		name := strings.TrimSpace(item)
		if name == "" {
			continue
		}
		if !validEnvironmentName(name) {
			return nil, fmt.Errorf("executor: %s contains invalid environment name %q", controlName, name)
		}
		if err := validateAdditionalEnvironmentName(cli, name); err != nil {
			return nil, fmt.Errorf("executor: %s: %w", controlName, err)
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func validateAdditionalEnvironmentName(cli, name string) error {
	upper := strings.ToUpper(name)
	if upper == "SSH_AUTH_SOCK" {
		return nil
	}
	if _, managed := managedEnvironmentNames[upper]; managed {
		return fmt.Errorf("%s is managed by Heimdallm and cannot be overridden", name)
	}
	if _, dangerous := dangerousEnvironmentNames[upper]; dangerous {
		return fmt.Errorf("%s is permanently blocked", name)
	}
	for _, prefix := range []string{"DYLD_", "GIT_", "HEIMDALLM_", "LD_"} {
		if strings.HasPrefix(upper, prefix) {
			return fmt.Errorf("%s is permanently blocked", name)
		}
	}
	for provider, providerNames := range providerEnvironmentNames {
		if provider == cli {
			continue
		}
		for _, providerName := range providerNames {
			if upper == providerName {
				if cli == "opencode" {
					// OpenCode is a multi-provider client. Naming a backend
					// credential in its exact allowlist is the operator's
					// explicit authorization for that configured backend.
					return nil
				}
				return fmt.Errorf("%s belongs to another provider and is permanently blocked", name)
			}
		}
	}
	return nil
}

func validEnvironmentName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' || (i > 0 && r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

func (c capturedEnvironment) bridgeProviderState(cli, root string) (func() error, error) {
	var stateSyncs []func() error
	for _, mount := range c.providerStateMounts(cli, root) {
		if mount.source == "" {
			continue
		}
		if !filepath.IsAbs(mount.source) {
			return nil, fmt.Errorf("executor: %s credential state path %q must be absolute", cli, mount.source)
		}
		if cli == "gemini" {
			syncState, err := bridgeGeminiState(mount.source, mount.dest)
			if err != nil {
				return nil, err
			}
			stateSyncs = append(stateSyncs, syncState)
			continue
		}
		if mount.copyBack {
			if _, err := os.Lstat(mount.source); os.IsNotExist(err) {
				logProviderStateAbsent(cli, mount.source)
			}
			syncState, err := bridgeMutableJSONState(mount.source, mount.dest)
			if err != nil {
				return nil, fmt.Errorf("executor: bridge %s mutable credential state: %w", cli, err)
			}
			stateSyncs = append(stateSyncs, syncState)
			continue
		}
		if _, err := os.Lstat(mount.source); err != nil {
			if os.IsNotExist(err) {
				logProviderStateAbsent(cli, mount.source)
				continue
			}
			return nil, fmt.Errorf("executor: inspect %s credential state: %w", cli, err)
		}
		if err := os.MkdirAll(filepath.Dir(mount.dest), 0o700); err != nil {
			return nil, fmt.Errorf("executor: prepare %s credential state bridge: %w", cli, err)
		}
		if err := os.Symlink(mount.source, mount.dest); err != nil {
			return nil, fmt.Errorf("executor: bridge %s credential state: %w", cli, err)
		}
	}
	return func() error {
		var syncErr error
		for _, syncState := range stateSyncs {
			syncErr = errors.Join(syncErr, syncState())
		}
		return syncErr
	}, nil
}

func logProviderStateAbsent(cli, path string) {
	key := cli + "\x00" + path
	if _, loaded := providerStateAbsenceLogged.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	slog.Info(
		"executor: provider state path is absent; starting without persisted state",
		"cli", cli,
		"path", path,
	)
}

type mutableStatePolicy struct {
	seedWhenMissing          []byte
	projectInitial           func([]byte) ([]byte, error)
	persistUpdated           func(initial, updated []byte) ([]byte, error)
	readOnly                 bool
	allowEphemeralOnReadOnly bool
}

type statePathAnchor struct {
	lexicalPath  string
	resolvedPath string
	info         os.FileInfo
}

// bridgeMutableJSONState copies a provider-owned JSON state file into the
// isolated home and safely copies a changed version back after the CLI exits,
// normally with an atomic replace. A file symlink is insufficient: CLIs
// commonly rotate OAuth state with write-temp-then-rename, which replaces the
// isolated symlink and silently loses the refreshed token when that home is
// removed.
func bridgeMutableJSONState(source, dest string) (func() error, error) {
	return bridgeMutableState(source, dest, mutableStatePolicy{
		projectInitial: projectMutableJSONState,
		persistUpdated: persistMutableJSONState,
	})
}

func bridgeGeminiMutableJSONState(source, dest string) (func() error, error) {
	return bridgeMutableState(source, dest, mutableStatePolicy{
		projectInitial:           projectMutableJSONState,
		persistUpdated:           persistMutableJSONState,
		allowEphemeralOnReadOnly: true,
	})
}

func bridgeGeminiMutableOpaqueState(source, dest string) (func() error, error) {
	return bridgeMutableState(source, dest, mutableStatePolicy{
		projectInitial: func(initial []byte) ([]byte, error) {
			return bytes.Clone(initial), nil
		},
		persistUpdated: func(_ []byte, updated []byte) ([]byte, error) {
			if len(updated) > 4*1024 {
				return nil, fmt.Errorf("opaque provider state exceeds 4 KiB")
			}
			if bytes.IndexByte(updated, 0) >= 0 {
				return nil, fmt.Errorf("opaque provider state contains a NUL byte")
			}
			return bytes.Clone(updated), nil
		},
		allowEphemeralOnReadOnly: true,
	})
}

func bridgeGeminiSettingsState(source, dest string) (func() error, error) {
	return bridgeMutableState(source, dest, mutableStatePolicy{
		seedWhenMissing: []byte("{}\n"),
		projectInitial:  projectGeminiAuthSettings,
		readOnly:        true,
	})
}

func bridgeMutableState(source, dest string, policy mutableStatePolicy) (func() error, error) {
	providerMutableStateMu.Lock()
	defer providerMutableStateMu.Unlock()

	resolvedSource := source
	initialExists := false
	var initial, initialProjection []byte
	var initialFileInfo os.FileInfo
	initialSourceMode := os.FileMode(0)
	var absentSourceAnchor statePathAnchor
	sourceInfo, err := os.Lstat(source)
	switch {
	case err == nil:
		initialSourceMode = sourceInfo.Mode()
		resolvedSource, err = filepath.EvalSymlinks(source)
		if err != nil {
			return nil, fmt.Errorf("resolve source %q: %w", source, err)
		}
		initial, initialFileInfo, err = readRegularStateFile(resolvedSource)
		if err != nil {
			return nil, fmt.Errorf("read source %q: %w", source, err)
		}
		initialProjection, err = policy.projectInitial(initial)
		if err != nil {
			return nil, fmt.Errorf("project source %q into isolated home: %w", source, err)
		}
		initialExists = true
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return nil, fmt.Errorf("prepare isolated state directory: %w", err)
		}
		if err := os.WriteFile(dest, initialProjection, 0o600); err != nil {
			return nil, fmt.Errorf("copy source %q into isolated home: %w", source, err)
		}
	case os.IsNotExist(err):
		// Register copy-back even when no state exists yet. Provider CLIs can
		// create their first login/session file during this execution.
		resolvedSource, absentSourceAnchor, err = resolveAbsentStatePath(source)
		if err != nil {
			return nil, fmt.Errorf("pin absent source path %q: %w", source, err)
		}
		if policy.seedWhenMissing != nil {
			initialProjection = bytes.Clone(policy.seedWhenMissing)
			if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
				return nil, fmt.Errorf("prepare isolated state directory: %w", err)
			}
			if err := os.WriteFile(dest, initialProjection, 0o600); err != nil {
				return nil, fmt.Errorf("seed isolated state %q: %w", dest, err)
			}
		}
	case err != nil:
		return nil, fmt.Errorf("inspect source %q: %w", source, err)
	}
	if policy.readOnly {
		// Gemini settings are input-only. The auth selector is needed to start
		// a headless OAuth session, but persisting any settings written by the
		// subprocess would reopen a configuration channel for future runs.
		return func() error { return nil }, nil
	}

	return func() error {
		providerMutableStateMu.Lock()
		defer providerMutableStateMu.Unlock()

		destInfo, err := os.Lstat(dest)
		if os.IsNotExist(err) {
			if initialExists {
				return fmt.Errorf("isolated state %q disappeared before synchronization", dest)
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect isolated state %q: %w", dest, err)
		}
		if !destInfo.Mode().IsRegular() {
			return fmt.Errorf("isolated state %q is not a regular file", dest)
		}
		updated, openedDestInfo, err := readRegularStateFile(dest)
		if err != nil {
			return fmt.Errorf("read isolated state %q: %w", dest, err)
		}
		if !os.SameFile(destInfo, openedDestInfo) {
			return fmt.Errorf("isolated state %q changed during synchronization", dest)
		}
		// Unchanged provider-owned bytes are accepted even when an older CLI
		// left an empty or malformed state file. This preserves the CLI's own
		// recovery path while validating every changed value before copy-out.
		if bytes.Equal(updated, initialProjection) {
			return nil
		}
		persisted, err := policy.persistUpdated(initial, updated)
		if err != nil {
			return fmt.Errorf("validate changed isolated state %q: %w", dest, err)
		}
		if bytes.Equal(persisted, initial) {
			return nil
		}

		currentExists := false
		var current []byte
		currentInfo, err := os.Lstat(source)
		switch {
		case err == nil:
			currentExists = true
			if initialExists {
				if currentInfo.Mode().Type() != initialSourceMode.Type() {
					return fmt.Errorf("source %q changed file type during execution", source)
				}
				currentResolved, resolveErr := filepath.EvalSymlinks(source)
				if resolveErr != nil {
					return fmt.Errorf("resolve current source %q: %w", source, resolveErr)
				}
				if currentResolved != resolvedSource {
					return fmt.Errorf("source %q changed target during execution", source)
				}
			} else {
				if !currentInfo.Mode().IsRegular() {
					return fmt.Errorf("source %q was created as a non-regular file", source)
				}
				currentResolved, resolveErr := filepath.EvalSymlinks(source)
				if resolveErr != nil {
					return fmt.Errorf("resolve newly created source %q: %w", source, resolveErr)
				}
				if currentResolved != resolvedSource {
					return fmt.Errorf("source %q changed target during execution", source)
				}
			}
			currentPath := resolvedSource
			var currentFileInfo os.FileInfo
			current, currentFileInfo, err = readRegularStateFile(currentPath)
			if err != nil {
				return fmt.Errorf("read current source %q: %w", source, err)
			}
			if initialExists && !os.SameFile(initialFileInfo, currentFileInfo) {
				return fmt.Errorf(
					"source %q changed concurrently (file was replaced); refusing to overwrite provider state",
					source,
				)
			}
		case os.IsNotExist(err):
		case err != nil:
			return fmt.Errorf("inspect current source %q: %w", source, err)
		}

		if currentExists && bytes.Equal(current, persisted) {
			return nil
		}
		if currentExists != initialExists || (initialExists && !bytes.Equal(current, initial)) {
			return fmt.Errorf("source %q changed concurrently; refusing to overwrite provider state", source)
		}
		if !initialExists {
			currentResolved, err := resolveAndValidateAbsentStatePath(source, absentSourceAnchor)
			if err != nil {
				return fmt.Errorf("validate absent source path %q before creation: %w", source, err)
			}
			if currentResolved != resolvedSource {
				return fmt.Errorf("source %q changed target during execution", source)
			}
		}
		if err := atomicWritePrivateState(resolvedSource, persisted); err != nil {
			if policy.allowEphemeralOnReadOnly &&
				(errors.Is(err, syscall.EROFS) || errors.Is(err, syscall.EACCES) ||
					errors.Is(err, syscall.EPERM)) {
				// Docker examples deliberately mount provider OAuth state
				// read-only. Preserve the successful review and make the
				// non-persistent refresh explicit instead of discarding output.
				slog.Warn(
					"executor: provider state source is read-only; refreshed state is ephemeral",
					"path", source,
					"err", err,
				)
				return nil
			}
			return err
		}
		return nil
	}, nil
}

func resolveAbsentStatePath(path string) (string, statePathAnchor, error) {
	candidate := filepath.Dir(path)
	for {
		_, err := os.Lstat(candidate)
		switch {
		case err == nil:
			resolved, err := filepath.EvalSymlinks(candidate)
			if err != nil {
				return "", statePathAnchor{}, err
			}
			info, err := readDirectoryIdentity(resolved)
			if err != nil {
				return "", statePathAnchor{}, err
			}
			relative, err := filepath.Rel(candidate, path)
			if err != nil {
				return "", statePathAnchor{}, err
			}
			return filepath.Join(resolved, relative), statePathAnchor{
				lexicalPath:  candidate,
				resolvedPath: resolved,
				info:         info,
			}, nil
		case os.IsNotExist(err):
			parent := filepath.Dir(candidate)
			if parent == candidate {
				return "", statePathAnchor{}, fmt.Errorf("no existing parent directory")
			}
			candidate = parent
		default:
			return "", statePathAnchor{}, err
		}
	}
}

func resolveAndValidateAbsentStatePath(path string, anchor statePathAnchor) (string, error) {
	resolvedAnchor, err := filepath.EvalSymlinks(anchor.lexicalPath)
	if err != nil {
		return "", err
	}
	if resolvedAnchor != anchor.resolvedPath {
		return "", fmt.Errorf("existing parent changed target")
	}
	currentAnchorInfo, err := readDirectoryIdentity(resolvedAnchor)
	if err != nil {
		return "", err
	}
	if !os.SameFile(anchor.info, currentAnchorInfo) {
		return "", fmt.Errorf("existing parent was replaced")
	}
	currentResolved, _, err := resolveAbsentStatePath(path)
	return currentResolved, err
}

func readDirectoryIdentity(path string) (_ os.FileInfo, returnErr error) {
	directory, err := os.OpenFile(
		path,
		os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return nil, err
	}
	defer func() {
		returnErr = errors.Join(returnErr, directory.Close())
	}()
	info, err := directory.Stat()
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%q is not a directory", path)
	}
	return info, nil
}

func readRegularStateFile(path string) (_ []byte, _ os.FileInfo, returnErr error) {
	file, err := os.OpenFile(
		path,
		os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		returnErr = errors.Join(returnErr, file.Close())
	}()
	info, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("%q is not a regular file", path)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, nil, err
	}
	return data, info, nil
}

func projectMutableJSONState(initial []byte) ([]byte, error) {
	if len(bytes.TrimSpace(initial)) == 0 {
		return []byte("{}\n"), nil
	}
	// Do not reject a pre-existing malformed file before the provider gets a
	// chance to repair it. Any changed copy is validated before persistence.
	return bytes.Clone(initial), nil
}

func persistMutableJSONState(_ []byte, updated []byte) ([]byte, error) {
	if !json.Valid(updated) {
		return nil, fmt.Errorf("provider state is not valid JSON")
	}
	return bytes.Clone(updated), nil
}

func projectGeminiAuthSettings(initial []byte) ([]byte, error) {
	settings, err := decodeJSONObject(initial)
	if err != nil {
		// A malformed user settings file must not block isolated authentication
		// or be copied into the execution environment. Settings are input-only,
		// so changes made in the isolated home are always discarded.
		slog.Debug("executor: ignoring malformed Gemini settings during auth projection", "err", err)
		settings = make(map[string]any)
	}
	return marshalJSONObject(geminiAuthProjection(settings))
}

func decodeJSONObject(data []byte) (map[string]any, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return make(map[string]any), nil
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, fmt.Errorf("expected JSON object")
	}
	return object, nil
}

func marshalJSONObject(object map[string]any) ([]byte, error) {
	encoded, err := json.MarshalIndent(object, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode JSON object: %w", err)
	}
	return append(encoded, '\n'), nil
}

func geminiAuthProjection(settings map[string]any) map[string]any {
	projected := make(map[string]any)
	if selected, ok := settings["selectedAuthType"].(string); ok {
		projected["selectedAuthType"] = selected
	}
	security, ok := settings["security"].(map[string]any)
	if !ok {
		return projected
	}
	auth, ok := security["auth"].(map[string]any)
	if !ok {
		return projected
	}
	projectedAuth := make(map[string]any)
	if selected, ok := auth["selectedType"].(string); ok {
		projectedAuth["selectedType"] = selected
	}
	if len(projectedAuth) != 0 {
		projected["security"] = map[string]any{"auth": projectedAuth}
	}
	return projected
}

func atomicWritePrivateState(path string, data []byte) error {
	return atomicWritePrivateStateWithRename(path, data, os.Rename)
}

func atomicWritePrivateStateWithRename(
	path string,
	data []byte,
	rename func(string, string) error,
) (returnErr error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("prepare state directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".heimdallm-*")
	if err != nil {
		return fmt.Errorf("create temporary state file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = os.Remove(tmpPath)
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secure temporary state file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temporary state file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temporary state file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary state file: %w", err)
	}
	if err := rename(tmpPath, path); err != nil {
		if errors.Is(err, syscall.EBUSY) {
			// Linux rejects rename over an individually bind-mounted file.
			// Fall back only for EBUSY, and write through an already verified
			// descriptor so the host mount keeps its identity.
			if fallbackErr := overwriteBusyMountedState(path, data); fallbackErr != nil {
				return fmt.Errorf("replace busy mounted provider state: %w", fallbackErr)
			}
			return nil
		}
		return fmt.Errorf("replace provider state atomically: %w", err)
	}
	return nil
}

func overwriteBusyMountedState(path string, data []byte) (returnErr error) {
	before, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect busy mounted state: %w", err)
	}
	if !before.Mode().IsRegular() {
		return fmt.Errorf("busy mounted state is not a regular file")
	}
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open busy mounted state: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, file.Close())
	}()
	after, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect opened busy mounted state: %w", err)
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return fmt.Errorf("busy mounted state changed before overwrite")
	}
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("secure busy mounted state: %w", err)
	}
	if err := file.Truncate(0); err != nil {
		return fmt.Errorf("truncate busy mounted state: %w", err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		return fmt.Errorf("seek busy mounted state: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write busy mounted state: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync busy mounted state: %w", err)
	}
	return nil
}

// bridgeGeminiState projects only Gemini's mutable authentication identifiers
// and a sanitized authentication selection. User settings, .env files, MCP
// tokens, extensions, commands, skills and policies never enter the isolated
// home. Mutable tokens and identifiers are copied back safely so atomic
// rotations and first-run creation survive removal of the temporary home;
// settings are an input-only projection and are never persisted.
func bridgeGeminiState(source, dest string) (func() error, error) {
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return nil, fmt.Errorf("executor: prepare Gemini credential state bridge: %w", err)
	}
	if err := os.Chmod(dest, 0o700); err != nil {
		return nil, fmt.Errorf("executor: secure Gemini credential state bridge: %w", err)
	}

	var syncs []func() error
	for _, state := range []struct {
		name   string
		bridge func(string, string) (func() error, error)
	}{
		{name: "oauth_creds.json", bridge: bridgeGeminiMutableJSONState},
		{name: "google_accounts.json", bridge: bridgeGeminiMutableJSONState},
		{name: "installation_id", bridge: bridgeGeminiMutableOpaqueState},
		{name: "user_id", bridge: bridgeGeminiMutableOpaqueState},
		{name: "settings.json", bridge: bridgeGeminiSettingsState},
	} {
		sourcePath := filepath.Join(source, state.name)
		destPath := filepath.Join(dest, state.name)
		if _, err := os.Lstat(sourcePath); os.IsNotExist(err) {
			logProviderStateAbsent("gemini", sourcePath)
		}
		syncState, err := state.bridge(sourcePath, destPath)
		if err != nil {
			return nil, fmt.Errorf("executor: bridge Gemini state %q: %w", state.name, err)
		}
		syncs = append(syncs, syncState)
	}
	return func() error {
		var syncErr error
		for _, syncState := range syncs {
			syncErr = errors.Join(syncErr, syncState())
		}
		return syncErr
	}, nil
}

func (c capturedEnvironment) providerStateMounts(cli, root string) []providerStateMount {
	home := c.values["HOME"]
	if home == "" {
		if resolved, err := os.UserHomeDir(); err == nil {
			home = resolved
		}
	}
	homePath := func(parts ...string) string {
		if home == "" {
			return ""
		}
		return filepath.Join(append([]string{home}, parts...)...)
	}

	switch cli {
	case "claude":
		claudeDir := c.values["CLAUDE_CONFIG_DIR"]
		if claudeDir == "" {
			claudeDir = homePath(".claude")
		}
		credentialsPath := ""
		if claudeDir != "" {
			credentialsPath = filepath.Join(claudeDir, ".credentials.json")
		}
		return []providerStateMount{
			{
				source:   credentialsPath,
				dest:     filepath.Join(root, ".claude", ".credentials.json"),
				copyBack: true,
			},
			{
				source:   homePath(".claude.json"),
				dest:     filepath.Join(root, ".claude.json"),
				copyBack: true,
			},
		}
	case "codex":
		codexHome := c.values["CODEX_HOME"]
		if codexHome == "" {
			codexHome = homePath(".codex")
		}
		return []providerStateMount{{source: codexHome, dest: filepath.Join(root, ".codex")}}
	case "gemini":
		return []providerStateMount{{source: homePath(".gemini"), dest: filepath.Join(root, ".gemini")}}
	case "opencode":
		configHome := c.values["XDG_CONFIG_HOME"]
		if configHome == "" {
			configHome = homePath(".config")
		}
		dataHome := c.values["XDG_DATA_HOME"]
		if dataHome == "" {
			dataHome = homePath(".local", "share")
		}
		var mounts []providerStateMount
		if configHome != "" {
			mounts = append(mounts, providerStateMount{
				source: filepath.Join(configHome, "opencode"),
				dest:   filepath.Join(root, ".config", "opencode"),
			})
		}
		if dataHome != "" {
			mounts = append(mounts, providerStateMount{
				source: filepath.Join(dataHome, "opencode"),
				dest:   filepath.Join(root, ".local", "share", "opencode"),
			})
		}
		return mounts
	default:
		return nil
	}
}

func cloneEnvironment(source map[string]string) map[string]string {
	cloned := make(map[string]string, len(source))
	for name, value := range source {
		cloned[name] = value
	}
	return cloned
}

func codexNestedEnvironment(source map[string]string) map[string]string {
	env := make(map[string]string, len(source))
	for name, value := range source {
		switch strings.ToUpper(name) {
		case "ALL_PROXY", "HTTP_PROXY", "HTTPS_PROXY":
			// Proxy URLs often embed credentials. Codex itself receives the
			// proxy through its allowlisted parent environment, but values
			// must never be serialized into command-line config overrides.
			continue
		default:
			env[name] = value
		}
	}
	return env
}

func probeEnvironmentName(name string) bool {
	switch strings.ToUpper(name) {
	case "CI",
		"COLORTERM",
		"COMSPEC",
		"LANG",
		"LANGUAGE",
		"LC_ADDRESS",
		"LC_ALL",
		"LC_COLLATE",
		"LC_CTYPE",
		"LC_IDENTIFICATION",
		"LC_MEASUREMENT",
		"LC_MESSAGES",
		"LC_MONETARY",
		"LC_NAME",
		"LC_NUMERIC",
		"LC_PAPER",
		"LC_TELEPHONE",
		"LC_TIME",
		"LOGNAME",
		"NO_COLOR",
		"PATHEXT",
		"SHELL",
		"SYSTEMROOT",
		"TERM",
		"TZ",
		"USER",
		"WINDIR":
		return true
	default:
		return false
	}
}

func managedProbeEnvironmentName(name string) bool {
	_, ok := managedEnvironmentNames[strings.ToUpper(name)]
	return ok
}

func environmentSlice(values map[string]string) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	env := make([]string, 0, len(names))
	for _, name := range names {
		env = append(env, name+"="+values[name])
	}
	return env
}

func sanitizePath(paths ...string) string {
	seen := make(map[string]struct{})
	entries := make([]string, 0, 16)
	add := func(path string) {
		path = filepath.Clean(strings.TrimSpace(path))
		if path == "." || !filepath.IsAbs(path) {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		entries = append(entries, path)
	}
	for _, value := range paths {
		for _, path := range filepath.SplitList(value) {
			add(path)
		}
	}
	for _, path := range []string{"/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"} {
		add(path)
	}
	return strings.Join(entries, string(os.PathListSeparator))
}

func proxyEnvironmentName(name string) bool {
	switch strings.ToUpper(name) {
	case "ALL_PROXY", "HTTP_PROXY", "HTTPS_PROXY":
		return true
	default:
		return false
	}
}

func proxyRedactionValues(value string) []string {
	if value == "" {
		return nil
	}
	values := []string{value}
	parsed, err := url.Parse(value)
	if err != nil || parsed.User == nil {
		return values
	}
	if password, ok := parsed.User.Password(); ok && password != "" {
		values = append(values, password)
	}
	return values
}

func secretEnvironmentName(name string) bool {
	upper := strings.ToUpper(name)
	for _, providerNames := range providerEnvironmentNames {
		for _, providerName := range providerNames {
			if upper == providerName {
				_, secret := providerSecretNames[upper]
				return secret
			}
		}
	}
	switch upper {
	case "AUTH", "CONNECTION_STRING", "COOKIE", "CREDENTIAL", "DATABASE_URL",
		"DB_URI", "DSN", "PASSWORD", "PASSWD", "PRIVATE_KEY", "SECRET", "TOKEN":
		return true
	}
	for _, suffix := range []string{
		"_API_KEY",
		"_AUTH",
		"_AUTH_SECRET",
		"_AUTH_TOKEN",
		"_CONNECTION_STRING",
		"_COOKIE",
		"_CREDENTIAL",
		"_CREDENTIALS",
		"_DATABASE_URL",
		"_DB_URI",
		"_DSN",
		"_PASSWORD",
		"_PASSWD",
		"_PRIVATE_KEY",
		"_SECRET",
		"_TOKEN",
	} {
		if strings.HasSuffix(upper, suffix) {
			return true
		}
	}
	return false
}

func additionalEnvironmentRedactionValues(name, value string) []string {
	var values []string
	if secretEnvironmentName(name) {
		values = append(values, value)
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.User == nil {
		return values
	}
	if len(values) == 0 {
		values = append(values, value)
	}
	if password, ok := parsed.User.Password(); ok && password != "" {
		values = append(values, password)
	}
	return values
}

func environmentRedactor(values []string) func(string) string {
	unique := make(map[string]struct{}, len(values)*2)
	for _, value := range values {
		if len(value) < 4 {
			continue
		}
		unique[value] = struct{}{}
		if escaped := url.QueryEscape(value); escaped != value {
			unique[escaped] = struct{}{}
		}
	}
	ordered := make([]string, 0, len(unique))
	for value := range unique {
		ordered = append(ordered, value)
	}
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })

	return func(message string) string {
		for _, value := range ordered {
			message = strings.ReplaceAll(message, value, "[REDACTED]")
		}
		return message
	}
}

func codexShellEnvironmentPolicy(values map[string]string) (string, error) {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)

	includes := make([]string, 0, len(names))
	settings := make([]string, 0, len(names))
	for _, name := range names {
		quotedName, err := tomlBasicString(name)
		if err != nil {
			return "", fmt.Errorf("environment name cannot be represented in TOML: %w", err)
		}
		quotedValue, err := tomlBasicString(values[name])
		if err != nil {
			return "", fmt.Errorf("environment value for %s cannot be represented in TOML: %w", name, err)
		}
		includes = append(includes, quotedName)
		settings = append(settings, quotedName+"="+quotedValue)
	}
	return "{inherit=\"none\",experimental_use_profile=false,ignore_default_excludes=false,include_only=[" +
		strings.Join(includes, ",") + "],set={" + strings.Join(settings, ",") + "}}", nil
}

func tomlBasicString(value string) (string, error) {
	if !utf8.ValidString(value) {
		return "", fmt.Errorf("invalid UTF-8")
	}

	var quoted strings.Builder
	quoted.Grow(len(value) + 2)
	quoted.WriteByte('"')
	for _, r := range value {
		switch r {
		case '\b':
			quoted.WriteString(`\b`)
		case '\t':
			quoted.WriteString(`\t`)
		case '\n':
			quoted.WriteString(`\n`)
		case '\f':
			quoted.WriteString(`\f`)
		case '\r':
			quoted.WriteString(`\r`)
		case '"':
			quoted.WriteString(`\"`)
		case '\\':
			quoted.WriteString(`\\`)
		default:
			if r < 0x20 || r == 0x7f {
				_, _ = fmt.Fprintf(&quoted, `\u%04X`, r)
				continue
			}
			quoted.WriteRune(r)
		}
	}
	quoted.WriteByte('"')
	return quoted.String(), nil
}

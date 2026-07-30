package executor

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const environmentAllowlistPrefix = "HEIMDALLM_AI_"

var providerCredentialNames = map[string][]string{
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
	"all_proxy":           {},
	"http_proxy":          {},
	"https_proxy":         {},
	"no_proxy":            {},
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
	"BASH_ENV":                         {},
	"CLAUDE_CODE_SUBPROCESS_ENV_SCRUB": {},
	"CDPATH":                           {},
	"CLAUDE_CONFIG_DIR":                {},
	"CODEX_HOME":                       {},
	"ENV":                              {},
	"GH_TOKEN":                         {},
	"GITHUB_TOKEN":                     {},
	"GIT_ASKPASS":                      {},
	"GEMINI_CLI_SYSTEM_SETTINGS_PATH":  {},
	"IFS":                              {},
	"NODE_OPTIONS":                     {},
	"NODE_PATH":                        {},
	"NPM_CONFIG_USERCONFIG":            {},
	"OPENCODE_DISABLE_PROJECT_CONFIG":  {},
	"OPENCODE_PURE":                    {},
	"PERL5LIB":                         {},
	"PERL5OPT":                         {},
	"PROMPT_COMMAND":                   {},
	"PYTHONHOME":                       {},
	"PYTHONPATH":                       {},
	"PYTHONSTARTUP":                    {},
	"RUBYOPT":                          {},
	"SSH_ASKPASS":                      {},
	"ZDOTDIR":                          {},
}

type capturedEnvironment struct {
	values map[string]string
}

type preparedEnvironment struct {
	env          []string
	codexToolEnv map[string]string
	runDir       string
	redact       func(string) string
	cleanup      func()
}

type providerStateMount struct {
	source string
	dest   string
}

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
	cleanup := func() { _ = os.RemoveAll(root) }
	if err := os.Chmod(root, 0o700); err != nil {
		cleanup()
		return nil, fmt.Errorf("executor: secure isolated home for %s: %w", cli, err)
	}

	base, err := c.base(root)
	if err != nil {
		cleanup()
		return nil, err
	}
	if err := c.bridgeProviderState(cli, root); err != nil {
		cleanup()
		return nil, err
	}

	env := cloneEnvironment(base)
	redactions := make([]string, 0, len(providerCredentialNames[cli])+len(extraNames))
	for _, name := range providerCredentialNames[cli] {
		if value := c.values[name]; value != "" {
			env[name] = value
			redactions = append(redactions, value)
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
				redactions = append(redactions, value)
			}
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
			cleanup()
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
	cleanup := func() { _ = os.RemoveAll(root) }
	if err := os.Chmod(root, 0o700); err != nil {
		cleanup()
		return nil, fmt.Errorf("executor: secure isolated CLI probe home: %w", err)
	}
	base, err := c.base(root)
	if err != nil {
		cleanup()
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
		cleanup:      cleanup,
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
	for provider, credentialNames := range providerCredentialNames {
		if provider == cli {
			continue
		}
		for _, credentialName := range credentialNames {
			if upper == credentialName {
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

func (c capturedEnvironment) bridgeProviderState(cli, root string) error {
	for _, mount := range c.providerStateMounts(cli, root) {
		if mount.source == "" {
			continue
		}
		if !filepath.IsAbs(mount.source) {
			return fmt.Errorf("executor: %s credential state path %q must be absolute", cli, mount.source)
		}
		if _, err := os.Lstat(mount.source); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("executor: inspect %s credential state: %w", cli, err)
		}
		if cli == "gemini" {
			if err := bridgeGeminiState(mount.source, mount.dest); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(mount.dest), 0o700); err != nil {
			return fmt.Errorf("executor: prepare %s credential state bridge: %w", cli, err)
		}
		if err := os.Symlink(mount.source, mount.dest); err != nil {
			return fmt.Errorf("executor: bridge %s credential state: %w", cli, err)
		}
	}
	return nil
}

// bridgeGeminiState projects Gemini's top-level state entries individually so
// its special ~/.gemini/.env file is absent. Gemini intentionally loads that
// file even when --ignore-env or advanced.ignoreLocalEnv is set. Other state,
// including browser OAuth credentials, remains available through symlinks.
func bridgeGeminiState(source, dest string) error {
	entries, err := os.ReadDir(source)
	if err != nil {
		return fmt.Errorf("executor: read Gemini credential state: %w", err)
	}
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return fmt.Errorf("executor: prepare Gemini credential state bridge: %w", err)
	}
	if err := os.Chmod(dest, 0o700); err != nil {
		return fmt.Errorf("executor: secure Gemini credential state bridge: %w", err)
	}
	for _, entry := range entries {
		if strings.EqualFold(entry.Name(), ".env") {
			continue
		}
		if err := os.Symlink(
			filepath.Join(source, entry.Name()),
			filepath.Join(dest, entry.Name()),
		); err != nil {
			return fmt.Errorf("executor: bridge Gemini credential state entry %q: %w", entry.Name(), err)
		}
	}
	return nil
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
		return []providerStateMount{
			{source: claudeDir, dest: filepath.Join(root, ".claude")},
			{source: homePath(".claude.json"), dest: filepath.Join(root, ".claude.json")},
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
		case "ALL_PROXY", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY":
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

func codexShellEnvironmentPolicy(values map[string]string) string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)

	includes := make([]string, 0, len(names))
	settings := make([]string, 0, len(names))
	for _, name := range names {
		includes = append(includes, strconv.Quote(name))
		settings = append(settings, strconv.Quote(name)+"="+strconv.Quote(values[name]))
	}
	return "{inherit=\"none\",experimental_use_profile=false,ignore_default_excludes=false,include_only=[" +
		strings.Join(includes, ",") + "],set={" + strings.Join(settings, ",") + "}}"
}

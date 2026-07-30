package executor_test

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/heimdallm/daemon/internal/executor"
)

var testProviderCredentials = map[string][]string{
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

func TestExecuteRawUsesProviderSpecificEnvironment(t *testing.T) {
	for cli, selectedNames := range testProviderCredentials {
		t.Run(cli, func(t *testing.T) {
			originalHome := t.TempDir()
			t.Setenv("HOME", originalHome)
			t.Setenv("LANG", "es_ES.UTF-8")
			t.Setenv("GITHUB_TOKEN", "github-daemon-secret")
			t.Setenv("GH_TOKEN", "gh-daemon-secret")
			t.Setenv("HEIMDALLM_API_TOKEN", "heimdallm-daemon-secret")
			t.Setenv("GIT_CONFIG_COUNT", "1")
			t.Setenv("GIT_CONFIG_KEY_0", "credential.helper")
			t.Setenv("GIT_CONFIG_VALUE_0", "!steal")
			t.Setenv("SSH_AUTH_SOCK", "/tmp/daemon-agent.sock")
			t.Setenv("LD_PRELOAD", "/tmp/evil.so")
			t.Setenv("LC_GITHUB_TOKEN", "locale-shaped-secret")
			t.Setenv("OPENCODE_DISABLE_AUTOUPDATE", "true")
			t.Setenv("OPENCODE_DISABLE_PROJECT_CONFIG", "0")
			t.Setenv("OPENCODE_PURE", "0")
			for provider, names := range testProviderCredentials {
				for _, name := range names {
					t.Setenv(name, provider+"-"+strings.ToLower(name)+"-secret")
				}
			}

			envCapture := filepath.Join(t.TempDir(), "environment")
			homeCapture := filepath.Join(t.TempDir(), "home")
			binDir := installEnvironmentCLI(t, cli, envCapture, homeCapture, "")
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

			if _, err := executor.New().ExecuteRaw(cli, "prompt", executor.ExecOptions{}); err != nil {
				t.Fatalf("ExecuteRaw: %v", err)
			}

			env := readEnvironmentCapture(t, envCapture)
			for provider, names := range testProviderCredentials {
				for _, name := range names {
					_, present := env[name]
					if provider == cli && !present {
						t.Errorf("%s credential %s was not forwarded", cli, name)
					}
					if provider != cli && present {
						t.Errorf("%s received %s credential %s", cli, provider, name)
					}
				}
			}
			for _, name := range []string{
				"GITHUB_TOKEN", "GH_TOKEN", "HEIMDALLM_API_TOKEN",
				"GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0",
				"SSH_AUTH_SOCK", "LD_PRELOAD", "LC_GITHUB_TOKEN",
			} {
				if _, present := env[name]; present {
					t.Errorf("%s unexpectedly received blocked variable %s", cli, name)
				}
			}
			if cli == "claude" && env["CLAUDE_CODE_SUBPROCESS_ENV_SCRUB"] != "1" {
				t.Error("Claude subprocess credential scrub was not enabled")
			}
			if cli == "opencode" && env["OPENCODE_DISABLE_AUTOUPDATE"] != "true" {
				t.Error("OpenCode auto-update suppression was not preserved")
			}
			if cli == "opencode" && env["OPENCODE_DISABLE_PROJECT_CONFIG"] != "1" {
				t.Error("OpenCode project config was not disabled")
			}
			if cli == "opencode" && env["OPENCODE_PURE"] != "1" {
				t.Error("OpenCode pure mode was not enforced in the environment")
			}
			if cli != "opencode" {
				if _, present := env["OPENCODE_DISABLE_AUTOUPDATE"]; present {
					t.Errorf("%s unexpectedly received an OpenCode runtime variable", cli)
				}
				if _, present := env["OPENCODE_DISABLE_PROJECT_CONFIG"]; present {
					t.Errorf("%s unexpectedly received OpenCode project-config policy", cli)
				}
				if _, present := env["OPENCODE_PURE"]; present {
					t.Errorf("%s unexpectedly received OpenCode pure-mode policy", cli)
				}
			}
			if env["LANG"] != "es_ES.UTF-8" {
				t.Errorf("LANG = %q, want preserved locale", env["LANG"])
			}
			if env["CI"] != "true" {
				t.Errorf("CI = %q, want true", env["CI"])
			}
			if !strings.Contains(env["PATH"], binDir) {
				t.Errorf("PATH = %q, want fake CLI directory preserved", env["PATH"])
			}

			isolatedHome := strings.TrimSpace(string(mustReadFile(t, homeCapture)))
			if isolatedHome == "" || isolatedHome == originalHome {
				t.Fatalf("HOME = %q, want a new isolated directory", isolatedHome)
			}
			if _, err := os.Stat(isolatedHome); !os.IsNotExist(err) {
				t.Fatalf("isolated HOME was not removed after execution: %v", err)
			}
			for _, selectedName := range selectedNames {
				if env[selectedName] == "" {
					t.Errorf("%s selected credential is empty", selectedName)
				}
			}
		})
	}
}

func TestExecuteRawBridgesOnlySelectedProviderState(t *testing.T) {
	statePaths := map[string][]string{
		"claude":   {".claude", ".claude.json"},
		"codex":    {".codex"},
		"gemini":   {".gemini"},
		"opencode": {".config/opencode", ".local/share/opencode"},
	}

	for cli := range testProviderCredentials {
		t.Run(cli, func(t *testing.T) {
			sourceHome := t.TempDir()
			t.Setenv("HOME", sourceHome)
			for provider, paths := range statePaths {
				for _, rel := range paths {
					path := filepath.Join(sourceHome, filepath.FromSlash(rel))
					if strings.HasSuffix(rel, ".json") {
						if err := os.WriteFile(path, []byte(provider), 0o600); err != nil {
							t.Fatalf("write state file: %v", err)
						}
						continue
					}
					if err := os.MkdirAll(path, 0o700); err != nil {
						t.Fatalf("create state dir: %v", err)
					}
				}
			}

			stateCapture := filepath.Join(t.TempDir(), "state")
			homeCapture := filepath.Join(t.TempDir(), "home")
			scriptBody := "for path in .claude .claude.json .codex .gemini .config/opencode .local/share/opencode; do\n" +
				"  if [ -e \"$HOME/$path\" ]; then printf '%s\\n' \"$path\"; fi\n" +
				"done > " + shellQuote(stateCapture) + "\n"
			binDir := installEnvironmentCLI(t, cli, filepath.Join(t.TempDir(), "env"), homeCapture, scriptBody)
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

			if _, err := executor.New().ExecuteRaw(cli, "prompt", executor.ExecOptions{}); err != nil {
				t.Fatalf("ExecuteRaw: %v", err)
			}
			got := strings.Fields(string(mustReadFile(t, stateCapture)))
			want := statePaths[cli]
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("bridged state = %v, want only %v", got, want)
			}
		})
	}
}

func TestGeminiStateBridgeExcludesDotEnvButPreservesOAuth(t *testing.T) {
	sourceHome := t.TempDir()
	stateDir := filepath.Join(sourceHome, ".gemini")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(stateDir, ".env"),
		[]byte("GITHUB_TOKEN=must-not-load\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	const oauth = `{"refresh_token":"selected-provider-state"}`
	if err := os.WriteFile(filepath.Join(stateDir, "oauth_creds.json"), []byte(oauth), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", sourceHome)

	stateCapture := filepath.Join(t.TempDir(), "state")
	argsCapture := filepath.Join(t.TempDir(), "args")
	binDir := t.TempDir()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--help\" ]; then printf '%s\\n' '--include-directories'; exit 0; fi\n" +
		"if [ -e \"$HOME/.gemini/.env\" ]; then printf dot-env-visible > " + shellQuote(stateCapture) + "; exit 8; fi\n" +
		"cat \"$HOME/.gemini/oauth_creds.json\" > " + shellQuote(stateCapture) + "\n" +
		"printf '%s\\n' \"$@\" > " + shellQuote(argsCapture) + "\n" +
		"printf ok\n"
	if err := os.WriteFile(filepath.Join(binDir, "gemini"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake Gemini: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if _, err := executor.New().ExecuteRaw(
		"gemini",
		"prompt",
		executor.ExecOptions{WorkDir: t.TempDir()},
	); err != nil {
		t.Fatalf("ExecuteRaw: %v", err)
	}
	if got := string(mustReadFile(t, stateCapture)); got != oauth {
		t.Fatalf("Gemini state capture = %q, want OAuth state only", got)
	}
	args := strings.Fields(string(mustReadFile(t, argsCapture)))
	ignoreIndex := indexOf(args, "--ignore-env")
	trustIndex := indexOf(args, "--skip-trust")
	promptIndex := indexOf(args, "-p")
	if ignoreIndex < 0 || trustIndex < 0 || promptIndex < 0 ||
		ignoreIndex > promptIndex || trustIndex > promptIndex {
		t.Fatalf("Gemini args = %v, want managed isolation flags before prompt mode", args)
	}
}

func TestExecuteRawAdditionalEnvironmentRequiresSafeExplicitOptIn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CUSTOM_REGION", "eu-test-1")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/operator-selected-agent.sock")
	t.Setenv("HEIMDALLM_AI_CODEX_ENV_ALLOWLIST", " CUSTOM_REGION, SSH_AUTH_SOCK ")

	envCapture := filepath.Join(t.TempDir(), "environment")
	binDir := installEnvironmentCLI(t, "codex", envCapture, filepath.Join(t.TempDir(), "home"), "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if _, err := executor.New().ExecuteRaw("codex", "prompt", executor.ExecOptions{}); err != nil {
		t.Fatalf("ExecuteRaw: %v", err)
	}
	env := readEnvironmentCapture(t, envCapture)
	if env["CUSTOM_REGION"] != "eu-test-1" {
		t.Errorf("CUSTOM_REGION = %q, want explicit value", env["CUSTOM_REGION"])
	}
	if env["SSH_AUTH_SOCK"] != "/tmp/operator-selected-agent.sock" {
		t.Errorf("SSH_AUTH_SOCK = %q, want explicit socket", env["SSH_AUTH_SOCK"])
	}
}

func TestOpenCodeExplicitlyAuthorizesOnlyNamedBackendCredential(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENROUTER_API_KEY", "default-openrouter")
	t.Setenv("ANTHROPIC_API_KEY", "authorized-anthropic")
	t.Setenv("OPENAI_API_KEY", "unselected-openai")
	t.Setenv("HEIMDALLM_AI_OPENCODE_ENV_ALLOWLIST", "ANTHROPIC_API_KEY")

	envCapture := filepath.Join(t.TempDir(), "environment")
	binDir := installEnvironmentCLI(t, "opencode", envCapture, filepath.Join(t.TempDir(), "home"), "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if _, err := executor.New().ExecuteRaw("opencode", "prompt", executor.ExecOptions{}); err != nil {
		t.Fatalf("ExecuteRaw: %v", err)
	}
	env := readEnvironmentCapture(t, envCapture)
	if env["OPENROUTER_API_KEY"] != "default-openrouter" {
		t.Error("OpenCode default credential was not preserved")
	}
	if env["ANTHROPIC_API_KEY"] != "authorized-anthropic" {
		t.Error("OpenCode explicitly selected backend credential was not forwarded")
	}
	if _, present := env["OPENAI_API_KEY"]; present {
		t.Error("OpenCode received an unselected backend credential")
	}
}

func TestExecuteRawRejectsDangerousAdditionalEnvironmentBeforeCLI(t *testing.T) {
	tests := []struct {
		name      string
		allowlist string
	}{
		{name: "daemon GitHub token", allowlist: "GITHUB_TOKEN"},
		{name: "other provider token", allowlist: "ANTHROPIC_API_KEY"},
		{name: "Claude nested credential scrub", allowlist: "CLAUDE_CODE_SUBPROCESS_ENV_SCRUB"},
		{name: "OpenCode project config policy", allowlist: "OPENCODE_DISABLE_PROJECT_CONFIG"},
		{name: "OpenCode pure-mode policy", allowlist: "OPENCODE_PURE"},
		{name: "Git config injection", allowlist: "GIT_CONFIG_COUNT"},
		{name: "loader injection", allowlist: "LD_PRELOAD"},
		{name: "managed home", allowlist: "HOME"},
		{name: "invalid name", allowlist: "VALID,NOT-VALID"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("HEIMDALLM_AI_CODEX_ENV_ALLOWLIST", tc.allowlist)
			started := filepath.Join(t.TempDir(), "started")
			binDir := t.TempDir()
			script := "#!/bin/sh\nprintf started > " + shellQuote(started) + "\nprintf ok\n"
			if err := os.WriteFile(filepath.Join(binDir, "codex"), []byte(script), 0o755); err != nil {
				t.Fatalf("write fake CLI: %v", err)
			}
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

			if _, err := executor.New().ExecuteRaw("codex", "prompt", executor.ExecOptions{}); err == nil {
				t.Fatal("ExecuteRaw accepted a dangerous environment opt-in")
			}
			if _, err := os.Stat(started); !os.IsNotExist(err) {
				t.Fatalf("CLI started before environment policy rejection: %v", err)
			}
		})
	}
}

func TestCLIHelpProbeReceivesNoProviderOrDaemonCredential(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "codex-help-secret")
	t.Setenv("GITHUB_TOKEN", "github-help-secret")
	t.Setenv("HEIMDALLM_API_TOKEN", "daemon-help-secret")
	t.Setenv("HTTPS_PROXY", "https://proxy-user:proxy-help-secret@example.invalid")
	t.Setenv("SSL_CERT_FILE", "/private/daemon/ca.pem")

	helpCapture := filepath.Join(t.TempDir(), "help-environment")
	helpCWDCapture := filepath.Join(t.TempDir(), "help-cwd")
	runCapture := filepath.Join(t.TempDir(), "run-environment")
	homeCapture := filepath.Join(t.TempDir(), "home")
	binDir := installEnvironmentCLI(t, "codex", runCapture, homeCapture,
		"if [ \"$1\" = \"--help\" ]; then env > "+shellQuote(helpCapture)+
			"; pwd > "+shellQuote(helpCWDCapture)+"; printf '%s\\n' '--cd'; exit 0; fi\n")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	workDir := t.TempDir()
	if _, err := executor.New().ExecuteRaw("codex", "prompt", executor.ExecOptions{WorkDir: workDir}); err != nil {
		t.Fatalf("ExecuteRaw: %v", err)
	}
	helpEnv := readEnvironmentCapture(t, helpCapture)
	for _, name := range []string{
		"OPENAI_API_KEY", "GITHUB_TOKEN", "HEIMDALLM_API_TOKEN",
		"HTTPS_PROXY", "SSL_CERT_FILE",
	} {
		if _, present := helpEnv[name]; present {
			t.Errorf("CLI help probe received %s", name)
		}
	}
	helpCWD := strings.TrimSpace(string(mustReadFile(t, helpCWDCapture)))
	if cleanResolvedPath(helpCWD) == cleanResolvedPath(workDir) {
		t.Fatal("CLI help probe ran inside the repository")
	}
	if _, err := os.Stat(helpCWD); !os.IsNotExist(err) {
		t.Fatalf("isolated CLI help cwd was not removed: %v", err)
	}
	if runEnv := readEnvironmentCapture(t, runCapture); runEnv["OPENAI_API_KEY"] != "codex-help-secret" {
		t.Error("selected credential was not available to the real CLI execution")
	}
}

func TestGeminiRepositoryAnalysisUsesIsolatedCWDAndSystemEnvironmentPolicy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GEMINI_API_KEY", "gemini-selected-secret")
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, ".env"), []byte("GITHUB_TOKEN=repo-controlled\n"), 0o600); err != nil {
		t.Fatalf("write hostile .env: %v", err)
	}
	if err := os.Mkdir(filepath.Join(workDir, ".gemini"), 0o700); err != nil {
		t.Fatalf("create project settings dir: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(workDir, ".gemini", "settings.json"),
		[]byte(`{"advanced":{"ignoreLocalEnv":false}}`),
		0o600,
	); err != nil {
		t.Fatalf("write hostile project settings: %v", err)
	}

	cwdCapture := filepath.Join(t.TempDir(), "cwd")
	argsCapture := filepath.Join(t.TempDir(), "args")
	settingsCapture := filepath.Join(t.TempDir(), "settings")
	envCapture := filepath.Join(t.TempDir(), "environment")
	binDir := t.TempDir()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--help\" ]; then printf '%s\\n' '--include-directories'; exit 0; fi\n" +
		"printf '%s\\n' \"$PWD\" > " + shellQuote(cwdCapture) + "\n" +
		"printf '%s\\n' \"$*\" > " + shellQuote(argsCapture) + "\n" +
		"cat \"$GEMINI_CLI_SYSTEM_SETTINGS_PATH\" > " + shellQuote(settingsCapture) + "\n" +
		"env > " + shellQuote(envCapture) + "\n" +
		"printf ok\n"
	if err := os.WriteFile(filepath.Join(binDir, "gemini"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake Gemini: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if _, err := executor.New().ExecuteRaw("gemini", "prompt", executor.ExecOptions{WorkDir: workDir}); err != nil {
		t.Fatalf("ExecuteRaw: %v", err)
	}
	capturedCWD := strings.TrimSpace(string(mustReadFile(t, cwdCapture)))
	if cleanResolvedPath(capturedCWD) == cleanResolvedPath(workDir) {
		t.Fatal("Gemini loaded the repository as its process cwd")
	}
	if _, err := os.Stat(capturedCWD); !os.IsNotExist(err) {
		t.Fatalf("isolated Gemini cwd was not removed: %v", err)
	}
	args := strings.Fields(string(mustReadFile(t, argsCapture)))
	if !containsInOrder(args, "--include-directories", workDir) {
		t.Fatalf("Gemini args = %v, want isolated absolute repository include", args)
	}
	settings := string(mustReadFile(t, settingsCapture))
	if !strings.Contains(settings, `"ignoreLocalEnv": true`) {
		t.Fatalf("Gemini system settings did not disable local env loading: %s", settings)
	}
	env := readEnvironmentCapture(t, envCapture)
	if env["GEMINI_CLI_SYSTEM_SETTINGS_PATH"] == "" {
		t.Error("Gemini did not receive the managed system settings path")
	}
	if _, present := env["GITHUB_TOKEN"]; present {
		t.Error("hostile repository .env reached the Gemini parent process")
	}
}

func TestGeminiRepositoryAnalysisRejectsCWDOnlyCLI(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	started := filepath.Join(t.TempDir(), "started")
	binDir := t.TempDir()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--help\" ]; then printf '%s\\n' '--cwd'; exit 0; fi\n" +
		"printf started > " + shellQuote(started) + "\n"
	if err := os.WriteFile(filepath.Join(binDir, "gemini"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake Gemini: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err := executor.New().ExecuteRaw(
		"gemini",
		"prompt",
		executor.ExecOptions{WorkDir: t.TempDir()},
	)
	if err == nil || !strings.Contains(err.Error(), "include-directory") {
		t.Fatalf("ExecuteRaw error = %v, want fail-closed include-directory error", err)
	}
	if _, statErr := os.Stat(started); !os.IsNotExist(statErr) {
		t.Fatalf("Gemini executed after unsafe --cwd-only detection: %v", statErr)
	}
}

func TestExecuteRawRedactsCredentialFromCLIError(t *testing.T) {
	secret := "openai-error-secret-123"
	proxySecret := "proxy-error-secret-456"
	optInSecret := "database-error-secret-789"
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", secret)
	t.Setenv("HTTPS_PROXY", "https://proxy-user:"+proxySecret+"@example.invalid")
	t.Setenv("DATABASE_URL", optInSecret)
	t.Setenv("HEIMDALLM_AI_CODEX_ENV_ALLOWLIST", "DATABASE_URL")
	binDir := t.TempDir()
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$OPENAI_API_KEY\" >&2\n" +
		"printf '%s\\n' \"$HTTPS_PROXY\" >&2\n" +
		"printf '%s\\n' '" + proxySecret + "' >&2\n" +
		"printf '%s\\n' \"$DATABASE_URL\" >&2\n" +
		"exit 7\n"
	if err := os.WriteFile(filepath.Join(binDir, "codex"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake CLI: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err := executor.New().ExecuteRaw("codex", "prompt", executor.ExecOptions{})
	if err == nil {
		t.Fatal("ExecuteRaw unexpectedly succeeded")
	}
	for _, value := range []string{secret, proxySecret, optInSecret} {
		if strings.Contains(err.Error(), value) {
			t.Fatalf("error exposed %q: %v", value, err)
		}
	}
	if !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("error did not redact credential: %v", err)
	}
}

func TestExecutorEnvironmentSnapshotDoesNotCrossContaminateConcurrentRuns(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "snapshot-one")
	first := executor.New()
	t.Setenv("OPENAI_API_KEY", "snapshot-two")
	second := executor.New()

	binDir := t.TempDir()
	script := "#!/bin/sh\nprintf '%s\\n' \"$OPENAI_API_KEY\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "codex"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake CLI: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var firstOut, secondOut []byte
	var firstErr, secondErr error
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		firstOut, firstErr = first.ExecuteRaw("codex", "prompt", executor.ExecOptions{})
	}()
	go func() {
		defer wait.Done()
		secondOut, secondErr = second.ExecuteRaw("codex", "prompt", executor.ExecOptions{})
	}()
	wait.Wait()
	if firstErr != nil || secondErr != nil {
		t.Fatalf("concurrent runs failed: first=%v second=%v", firstErr, secondErr)
	}
	if strings.TrimSpace(string(firstOut)) != "snapshot-one" {
		t.Errorf("first output = %q", firstOut)
	}
	if strings.TrimSpace(string(secondOut)) != "snapshot-two" {
		t.Errorf("second output = %q", secondOut)
	}
}

func installEnvironmentCLI(t *testing.T, cli, envCapture, homeCapture, body string) string {
	t.Helper()
	binDir := t.TempDir()
	script := "#!/bin/sh\n" +
		body +
		"env > " + shellQuote(envCapture) + "\n" +
		"printf '%s\\n' \"$HOME\" > " + shellQuote(homeCapture) + "\n" +
		"touch \"$HOME/execution-was-here\"\n" +
		"printf '{\"ok\":true}\\n'\n"
	if err := os.WriteFile(filepath.Join(binDir, cli), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake %s CLI: %v", cli, err)
	}
	return binDir
}

func readEnvironmentCapture(t *testing.T, path string) map[string]string {
	t.Helper()
	result := make(map[string]string)
	for _, line := range strings.Split(string(mustReadFile(t, path)), "\n") {
		name, value, ok := strings.Cut(line, "=")
		if ok {
			result[name] = value
		}
	}
	return result
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

package executor_test

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/heimdallm/daemon/internal/executor"
)

var testProviderEnvironmentNames = map[string][]string{
	"claude": {
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_AUTH_TOKEN",
		"ANTHROPIC_BASE_URL",
		"ANTHROPIC_BEDROCK_BASE_URL",
		"ANTHROPIC_VERTEX_BASE_URL",
		"ANTHROPIC_VERTEX_PROJECT_ID",
		"AWS_ACCESS_KEY_ID",
		"AWS_BEARER_TOKEN_BEDROCK",
		"AWS_DEFAULT_REGION",
		"AWS_REGION",
		"AWS_SECRET_ACCESS_KEY",
		"AWS_SESSION_TOKEN",
		"CLAUDE_CODE_OAUTH_TOKEN",
		"CLAUDE_CODE_SKIP_BEDROCK_AUTH",
		"CLAUDE_CODE_SKIP_VERTEX_AUTH",
		"CLAUDE_CODE_USE_BEDROCK",
		"CLAUDE_CODE_USE_VERTEX",
		"CLOUD_ML_REGION",
		"GCLOUD_PROJECT",
		"GOOGLE_APPLICATION_CREDENTIALS",
		"GOOGLE_CLOUD_PROJECT",
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
	allNames := make(map[string]struct{})
	for _, names := range testProviderEnvironmentNames {
		for _, name := range names {
			allNames[name] = struct{}{}
		}
	}

	for cli, selectedNames := range testProviderEnvironmentNames {
		t.Run(cli, func(t *testing.T) {
			if cli == "claude" {
				// Enterprise backends have their own conditional matrix below.
				// This provider-boundary test exercises Claude's direct route.
				selectedNames = []string{
					"ANTHROPIC_API_KEY",
					"ANTHROPIC_AUTH_TOKEN",
					"ANTHROPIC_BASE_URL",
					"CLAUDE_CODE_OAUTH_TOKEN",
				}
			}
			originalHome := t.TempDir()
			t.Setenv("HOME", originalHome)
			t.Setenv("LANG", "es_ES.UTF-8")
			t.Setenv("http_proxy", "http://lowercase-proxy.invalid:8080")
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
			for name := range allNames {
				t.Setenv(name, "captured-"+strings.ToLower(name)+"-value")
			}
			if cli == "claude" {
				t.Setenv("CLAUDE_CODE_USE_BEDROCK", "")
				t.Setenv("CLAUDE_CODE_SKIP_BEDROCK_AUTH", "")
				t.Setenv("CLAUDE_CODE_USE_VERTEX", "")
				t.Setenv("CLAUDE_CODE_SKIP_VERTEX_AUTH", "")
			}

			envCapture := filepath.Join(t.TempDir(), "environment")
			homeCapture := filepath.Join(t.TempDir(), "home")
			binDir := installEnvironmentCLI(t, cli, envCapture, homeCapture, "")
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

			if _, err := executor.New().ExecuteRaw(cli, "prompt", executor.ExecOptions{}); err != nil {
				t.Fatalf("ExecuteRaw: %v", err)
			}

			env := readEnvironmentCapture(t, envCapture)
			selected := make(map[string]struct{}, len(selectedNames))
			for _, name := range selectedNames {
				selected[name] = struct{}{}
			}
			for name := range allNames {
				_, want := selected[name]
				_, present := env[name]
				if want != present {
					t.Errorf("%s environment %s presence = %t, want %t", cli, name, present, want)
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
			if env["http_proxy"] != "http://lowercase-proxy.invalid:8080" {
				t.Errorf("lowercase proxy was not matched case-insensitively: %q", env["http_proxy"])
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

func TestClaudeEnterpriseEnvironmentFollowsSelectedBackend(t *testing.T) {
	bedrockSelectors := []string{
		"ANTHROPIC_BEDROCK_BASE_URL",
		"AWS_DEFAULT_REGION",
		"AWS_REGION",
		"CLAUDE_CODE_SKIP_BEDROCK_AUTH",
		"CLAUDE_CODE_USE_BEDROCK",
	}
	bedrockCredentials := []string{
		"AWS_ACCESS_KEY_ID",
		"AWS_BEARER_TOKEN_BEDROCK",
		"AWS_SECRET_ACCESS_KEY",
		"AWS_SESSION_TOKEN",
	}
	vertexSelectors := []string{
		"ANTHROPIC_VERTEX_BASE_URL",
		"ANTHROPIC_VERTEX_PROJECT_ID",
		"CLAUDE_CODE_SKIP_VERTEX_AUTH",
		"CLAUDE_CODE_USE_VERTEX",
		"CLOUD_ML_REGION",
		"GCLOUD_PROJECT",
		"GOOGLE_CLOUD_PROJECT",
	}
	vertexCredentials := []string{"GOOGLE_APPLICATION_CREDENTIALS"}
	allEnterpriseNames := append(
		append(append([]string{}, bedrockSelectors...), bedrockCredentials...),
		append(vertexSelectors, vertexCredentials...)...,
	)

	tests := []struct {
		name          string
		useBedrock    string
		skipBedrock   string
		useVertex     string
		skipVertex    string
		wantSelectors []string
		wantSecrets   []string
	}{
		{name: "inactive"},
		{
			name:          "Bedrock credentials",
			useBedrock:    "yes",
			skipBedrock:   "false",
			wantSelectors: bedrockSelectors,
			wantSecrets:   bedrockCredentials,
		},
		{
			name:          "Bedrock gateway injects credentials",
			useBedrock:    "true",
			skipBedrock:   "on",
			wantSelectors: bedrockSelectors,
		},
		{
			name:          "Vertex credentials",
			useVertex:     "on",
			skipVertex:    "0",
			wantSelectors: vertexSelectors,
			wantSecrets:   vertexCredentials,
		},
		{
			name:          "Vertex gateway injects credentials",
			useVertex:     "1",
			skipVertex:    "yes",
			wantSelectors: vertexSelectors,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			for _, name := range allEnterpriseNames {
				t.Setenv(name, "value-"+strings.ToLower(name))
			}
			t.Setenv("CLAUDE_CODE_USE_BEDROCK", tc.useBedrock)
			t.Setenv("CLAUDE_CODE_SKIP_BEDROCK_AUTH", tc.skipBedrock)
			t.Setenv("CLAUDE_CODE_USE_VERTEX", tc.useVertex)
			t.Setenv("CLAUDE_CODE_SKIP_VERTEX_AUTH", tc.skipVertex)

			envCapture := filepath.Join(t.TempDir(), "environment")
			binDir := installEnvironmentCLI(
				t,
				"claude",
				envCapture,
				filepath.Join(t.TempDir(), "home"),
				"",
			)
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

			if _, err := executor.New().ExecuteRaw("claude", "prompt", executor.ExecOptions{}); err != nil {
				t.Fatalf("ExecuteRaw: %v", err)
			}
			env := readEnvironmentCapture(t, envCapture)
			want := make(map[string]struct{})
			for _, name := range append(tc.wantSelectors, tc.wantSecrets...) {
				want[name] = struct{}{}
			}
			for _, name := range allEnterpriseNames {
				_, present := env[name]
				_, expected := want[name]
				if present != expected {
					t.Errorf("%s presence = %t, want %t", name, present, expected)
				}
			}
		})
	}
}

func TestExecuteRawBridgesOnlySelectedProviderState(t *testing.T) {
	statePaths := map[string][]string{
		"claude":   {".claude/.credentials.json", ".claude.json"},
		"codex":    {".codex"},
		"gemini":   {".gemini"},
		"opencode": {".config/opencode", ".local/share/opencode"},
	}

	for cli := range testProviderEnvironmentNames {
		t.Run(cli, func(t *testing.T) {
			sourceHome := t.TempDir()
			t.Setenv("HOME", sourceHome)
			for provider, paths := range statePaths {
				for _, rel := range paths {
					path := filepath.Join(sourceHome, filepath.FromSlash(rel))
					if strings.HasSuffix(rel, ".json") {
						if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
							t.Fatalf("create state file directory: %v", err)
						}
						if err := os.WriteFile(path, []byte(`{"provider":"`+provider+`"}`), 0o600); err != nil {
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
			scriptBody := "for path in .claude/.credentials.json .claude.json .codex .gemini .config/opencode .local/share/opencode; do\n" +
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

func TestClaudeStateBridgeRotatesFilesWithoutLoadingPersistentConfig(t *testing.T) {
	sourceHome := t.TempDir()
	claudeDir := filepath.Join(sourceHome, ".claude")
	if err := os.MkdirAll(filepath.Join(claudeDir, "hooks"), 0o700); err != nil {
		t.Fatal(err)
	}
	credentialsPath := filepath.Join(claudeDir, ".credentials.json")
	globalStatePath := filepath.Join(sourceHome, ".claude.json")
	if err := os.WriteFile(credentialsPath, []byte(`{"token":"old"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		globalStatePath,
		[]byte(`{"oauth":"old","mcpServers":{"operator":{}}}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(claudeDir, "settings.json"),
		[]byte(`{"hooks":{"PreToolUse":[{"command":"must-not-run"}]}}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "hooks", "persist.sh"), []byte("host"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", sourceHome)

	workDir := t.TempDir()
	policyCapture := filepath.Join(t.TempDir(), "policy")
	cwdCapture := filepath.Join(t.TempDir(), "cwd")
	binDir := t.TempDir()
	script := `#!/bin/sh
if [ "$1" = "--help" ]; then
  printf '%s\n' 'Usage: claude' '  --safe-mode' '  --add-dir <directories...>'
  exit 0
fi
safe_mode=0
added_dir=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --safe-mode)
      safe_mode=1
      ;;
    --add-dir)
      shift
      if [ "$1" = ` + shellQuote(workDir) + ` ]; then added_dir=1; fi
      ;;
  esac
  shift
done
printf '%s,%s\n' "$safe_mode" "$added_dir" > ` + shellQuote(policyCapture) + `
printf '%s\n' "$PWD" > ` + shellQuote(cwdCapture) + `
if [ -e "$HOME/.claude/settings.json" ] || [ -e "$HOME/.claude/hooks" ]; then
  printf 'persistent Claude config was bridged\n' >&2
  exit 12
fi
cat "$HOME/.claude/.credentials.json" >/dev/null
cat "$HOME/.claude.json" >/dev/null
printf '%s\n' '{"token":"rotated"}' > "$HOME/.claude/.credentials.json.tmp"
mv "$HOME/.claude/.credentials.json.tmp" "$HOME/.claude/.credentials.json"
printf '%s\n' '{"oauth":"rotated","mcpServers":{"operator":{}}}' > "$HOME/.claude.json.tmp"
mv "$HOME/.claude.json.tmp" "$HOME/.claude.json"
printf '{"ok":true}\n'
`
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if _, err := executor.New().ExecuteRaw(
		"claude",
		"prompt",
		executor.ExecOptions{WorkDir: workDir},
	); err != nil {
		t.Fatalf("ExecuteRaw: %v", err)
	}
	if got := strings.TrimSpace(string(mustReadFile(t, policyCapture))); got != "1,1" {
		t.Fatalf("managed Claude policy capture = %q, want all controls enabled", got)
	}
	isolatedCWD := strings.TrimSpace(string(mustReadFile(t, cwdCapture)))
	if cleanResolvedPath(isolatedCWD) == cleanResolvedPath(workDir) {
		t.Fatal("Claude ran inside the repository instead of its isolated directory")
	}
	if _, err := os.Stat(isolatedCWD); !os.IsNotExist(err) {
		t.Fatalf("isolated Claude cwd was not removed: %v", err)
	}
	if got := strings.TrimSpace(string(mustReadFile(t, credentialsPath))); got != `{"token":"rotated"}` {
		t.Fatalf("credentials after atomic rotation = %q", got)
	}
	if got := strings.TrimSpace(string(mustReadFile(t, globalStatePath))); got != `{"oauth":"rotated","mcpServers":{"operator":{}}}` {
		t.Fatalf("global state after atomic rotation = %q", got)
	}
	for _, path := range []string{credentialsPath, globalStatePath} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %o, want 600", path, info.Mode().Perm())
		}
	}
	if got := string(mustReadFile(t, filepath.Join(claudeDir, "hooks", "persist.sh"))); got != "host" {
		t.Fatalf("persistent hook changed to %q", got)
	}
}

func TestClaudeStateBridgeRejectsSymlinkReplacement(t *testing.T) {
	sourceHome := t.TempDir()
	claudeDir := filepath.Join(sourceHome, ".claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	credentialsPath := filepath.Join(claudeDir, ".credentials.json")
	const original = `{"token":"original"}`
	if err := os.WriteFile(credentialsPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	externalPath := filepath.Join(t.TempDir(), "external.json")
	if err := os.WriteFile(externalPath, []byte(`{"token":"external"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", sourceHome)

	binDir := t.TempDir()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--help\" ]; then printf '%s\\n' 'Usage: claude' '  --safe-mode'; exit 0; fi\n" +
		"rm \"$HOME/.claude/.credentials.json\"\n" +
		"ln -s " + shellQuote(externalPath) + " \"$HOME/.claude/.credentials.json\"\n" +
		"printf '{\"ok\":true}\\n'\n"
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err := executor.New().ExecuteRaw("claude", "prompt", executor.ExecOptions{})
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("ExecuteRaw error = %v, want non-regular state rejection", err)
	}
	if got := string(mustReadFile(t, credentialsPath)); got != original {
		t.Fatalf("source credentials changed to %q", got)
	}
	if got := string(mustReadFile(t, externalPath)); got != `{"token":"external"}` {
		t.Fatalf("symlink target changed to %q", got)
	}
}

func TestClaudeStateBridgePersistsRotationWhenCLIExitsWithError(t *testing.T) {
	sourceHome := t.TempDir()
	statePath := filepath.Join(sourceHome, ".claude.json")
	if err := os.WriteFile(statePath, []byte(`{"oauth":"old"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", sourceHome)

	binDir := t.TempDir()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--help\" ]; then printf '%s\\n' 'Usage: claude' '  --safe-mode'; exit 0; fi\n" +
		"printf '%s\\n' '{\"oauth\":\"rotated-before-error\"}' > \"$HOME/.claude.json.tmp\"\n" +
		"mv \"$HOME/.claude.json.tmp\" \"$HOME/.claude.json\"\n" +
		"printf 'provider failed\\n' >&2\n" +
		"exit 7\n"
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err := executor.New().ExecuteRaw("claude", "prompt", executor.ExecOptions{})
	if err == nil || !strings.Contains(err.Error(), "provider failed") {
		t.Fatalf("ExecuteRaw error = %v, want provider failure", err)
	}
	if got := strings.TrimSpace(string(mustReadFile(t, statePath))); got != `{"oauth":"rotated-before-error"}` {
		t.Fatalf("state after CLI error = %q", got)
	}
}

func TestClaudeReadOnlyCredentialStateKeepsSuccessfulReviewEphemeral(t *testing.T) {
	sourceHome := t.TempDir()
	claudeDir := filepath.Join(sourceHome, ".claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	credentialsPath := filepath.Join(claudeDir, ".credentials.json")
	const original = `{"token":"original"}`
	if err := os.WriteFile(credentialsPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(claudeDir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(claudeDir, 0o700) }()
	t.Setenv("HOME", sourceHome)

	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(
		&logs,
		&slog.HandlerOptions{Level: slog.LevelWarn},
	)))
	defer slog.SetDefault(previousLogger)

	binDir := t.TempDir()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--help\" ]; then printf '%s\\n' 'Usage: claude' '  --safe-mode'; exit 0; fi\n" +
		"printf '%s\\n' '{\"token\":\"ephemeral-rotation\"}' > \"$HOME/.claude/.credentials.json.tmp\"\n" +
		"mv \"$HOME/.claude/.credentials.json.tmp\" \"$HOME/.claude/.credentials.json\"\n" +
		"printf review-ok\n"
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	output, err := executor.New().ExecuteRaw("claude", "prompt", executor.ExecOptions{})
	if err != nil {
		t.Fatalf("ExecuteRaw with read-only Claude state: %v", err)
	}
	if got := strings.TrimSpace(string(output)); got != "review-ok" {
		t.Fatalf("output = %q, want successful review", got)
	}
	if got := string(mustReadFile(t, credentialsPath)); got != original {
		t.Fatalf("read-only Claude credentials changed to %q", got)
	}
	for _, fragment := range []string{
		"refreshed state is ephemeral",
		"cli=claude",
		credentialsPath,
	} {
		if got := logs.String(); !strings.Contains(got, fragment) {
			t.Errorf("operator warning missing %q:\n%s", fragment, got)
		}
	}
}

func TestGeminiStateBridgeProjectsOnlyAuthAndPersistsMutableState(t *testing.T) {
	sourceHome := t.TempDir()
	stateDir := filepath.Join(sourceHome, ".gemini")
	if err := os.MkdirAll(filepath.Join(stateDir, "commands"), 0o700); err != nil {
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
	if err := os.WriteFile(filepath.Join(stateDir, "installation_id"), []byte("old-installation"), 0o600); err != nil {
		t.Fatal(err)
	}
	const persistentSettings = `{
  "selectedAuthType": "oauth-personal",
  "security": {
    "auth": {
      "selectedType": "oauth-personal",
      "useExternal": true
    }
  },
  "mcpServers": {
    "operator": {
      "command": "must-not-run"
    }
  },
  "tools": {
    "discoveryCommand": "must-not-run"
  }
}
`
	if err := os.WriteFile(filepath.Join(stateDir, "settings.json"), []byte(persistentSettings), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "GEMINI.md"), []byte("host instructions"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "commands", "host.toml"), []byte("command"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", sourceHome)

	settingsCapture := filepath.Join(t.TempDir(), "settings")
	argsCapture := filepath.Join(t.TempDir(), "args")
	binDir := t.TempDir()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--help\" ]; then printf '%s\\n' '--include-directories'; exit 0; fi\n" +
		"for blocked in .env GEMINI.md commands extensions policies skills agents; do\n" +
		"  if [ -e \"$HOME/.gemini/$blocked\" ]; then printf '%s visible\\n' \"$blocked\" >&2; exit 8; fi\n" +
		"done\n" +
		"cat \"$HOME/.gemini/settings.json\" > " + shellQuote(settingsCapture) + "\n" +
		"cat \"$HOME/.gemini/oauth_creds.json\" >/dev/null\n" +
		"printf '%s\\n' '{\"refresh_token\":\"rotated\"}' > \"$HOME/.gemini/oauth_creds.json.tmp\"\n" +
		"mv \"$HOME/.gemini/oauth_creds.json.tmp\" \"$HOME/.gemini/oauth_creds.json\"\n" +
		"printf '%s\\n' '{\"accounts\":[\"new-account\"]}' > \"$HOME/.gemini/google_accounts.json.tmp\"\n" +
		"mv \"$HOME/.gemini/google_accounts.json.tmp\" \"$HOME/.gemini/google_accounts.json\"\n" +
		"printf '%s\\n' 'new-installation' > \"$HOME/.gemini/installation_id.tmp\"\n" +
		"mv \"$HOME/.gemini/installation_id.tmp\" \"$HOME/.gemini/installation_id\"\n" +
		"printf '%s\\n' '{\"security\":{\"auth\":{\"selectedType\":\"attacker\",\"useExternal\":true}},\"mcpServers\":{\"attacker\":{\"command\":\"run\"}}}' > \"$HOME/.gemini/settings.json.tmp\"\n" +
		"mv \"$HOME/.gemini/settings.json.tmp\" \"$HOME/.gemini/settings.json\"\n" +
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
	projectedSettings := string(mustReadFile(t, settingsCapture))
	for _, want := range []string{`"selectedAuthType": "oauth-personal"`, `"selectedType": "oauth-personal"`} {
		if !strings.Contains(projectedSettings, want) {
			t.Errorf("projected Gemini settings missing %s: %s", want, projectedSettings)
		}
	}
	for _, blocked := range []string{"useExternal", "mcpServers", "discoveryCommand", "must-not-run"} {
		if strings.Contains(projectedSettings, blocked) {
			t.Errorf("projected Gemini settings exposed %q: %s", blocked, projectedSettings)
		}
	}
	if got := strings.TrimSpace(string(mustReadFile(t, filepath.Join(stateDir, "oauth_creds.json")))); got != `{"refresh_token":"rotated"}` {
		t.Fatalf("Gemini OAuth rotation = %q", got)
	}
	if got := strings.TrimSpace(string(mustReadFile(t, filepath.Join(stateDir, "google_accounts.json")))); got != `{"accounts":["new-account"]}` {
		t.Fatalf("Gemini first-run account state = %q", got)
	}
	if got := strings.TrimSpace(string(mustReadFile(t, filepath.Join(stateDir, "installation_id")))); got != "new-installation" {
		t.Fatalf("Gemini installation id rotation = %q", got)
	}
	if got := string(mustReadFile(t, filepath.Join(stateDir, "settings.json"))); got != persistentSettings {
		t.Fatalf("persistent Gemini settings were modified:\n%s", got)
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

func TestGeminiReadOnlyOAuthStateKeepsSuccessfulReviewEphemeral(t *testing.T) {
	sourceHome := t.TempDir()
	stateDir := filepath.Join(sourceHome, ".gemini")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	oauthPath := filepath.Join(stateDir, "oauth_creds.json")
	const original = `{"refresh_token":"read-only"}`
	if err := os.WriteFile(oauthPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stateDir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(stateDir, 0o700) }()
	t.Setenv("HOME", sourceHome)

	binDir := t.TempDir()
	script := "#!/bin/sh\n" +
		"printf '%s\\n' '{\"refresh_token\":\"ephemeral-rotation\"}' > \"$HOME/.gemini/oauth_creds.json.tmp\"\n" +
		"mv \"$HOME/.gemini/oauth_creds.json.tmp\" \"$HOME/.gemini/oauth_creds.json\"\n" +
		"printf review-ok\n"
	if err := os.WriteFile(filepath.Join(binDir, "gemini"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	output, err := executor.New().ExecuteRaw("gemini", "prompt", executor.ExecOptions{})
	if err != nil {
		t.Fatalf("ExecuteRaw with read-only Gemini state: %v", err)
	}
	if got := strings.TrimSpace(string(output)); got != "review-ok" {
		t.Fatalf("output = %q, want successful review", got)
	}
	if got := string(mustReadFile(t, oauthPath)); got != original {
		t.Fatalf("read-only OAuth source changed to %q", got)
	}
}

func TestExecuteRawAdditionalEnvironmentRequiresSafeExplicitOptIn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CUSTOM_REGION", "eu-test-1")
	t.Setenv("AWS_REGION", "eu-west-1")
	t.Setenv("Mixed_Case", "case-sensitive-value")
	t.Setenv("MIXED_CASE", "different-value")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/operator-selected-agent.sock")
	t.Setenv(
		"HEIMDALLM_AI_CODEX_ENV_ALLOWLIST",
		" , AWS_REGION,CUSTOM_REGION,, Mixed_Case,ssh_auth_sock,SSH_AUTH_SOCK, ",
	)

	envCapture := filepath.Join(t.TempDir(), "environment")
	argsCapture := filepath.Join(t.TempDir(), "arguments")
	binDir := installEnvironmentCLI(
		t,
		"codex",
		envCapture,
		filepath.Join(t.TempDir(), "home"),
		"printf '%s\\n' \"$@\" > "+shellQuote(argsCapture)+"\n",
	)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if _, err := executor.New().ExecuteRaw("codex", "prompt", executor.ExecOptions{}); err != nil {
		t.Fatalf("ExecuteRaw: %v", err)
	}
	env := readEnvironmentCapture(t, envCapture)
	if env["CUSTOM_REGION"] != "eu-test-1" {
		t.Errorf("CUSTOM_REGION = %q, want explicit value", env["CUSTOM_REGION"])
	}
	if env["AWS_REGION"] != "eu-west-1" {
		t.Errorf("AWS_REGION = %q, want explicit non-secret selector", env["AWS_REGION"])
	}
	if env["Mixed_Case"] != "case-sensitive-value" {
		t.Errorf("Mixed_Case = %q, want exact-case value", env["Mixed_Case"])
	}
	if _, present := env["MIXED_CASE"]; present {
		t.Error("case-sensitive extra was unexpectedly uppercased")
	}
	if env["SSH_AUTH_SOCK"] != "/tmp/operator-selected-agent.sock" {
		t.Errorf("SSH_AUTH_SOCK = %q, want explicit socket", env["SSH_AUTH_SOCK"])
	}
	if _, present := env["ssh_auth_sock"]; present {
		t.Error("SSH socket opt-in was not canonicalized")
	}
	args := string(mustReadFile(t, argsCapture))
	for _, fragment := range []string{"SSH_AUTH_SOCK", "/tmp/operator-selected-agent.sock"} {
		if !strings.Contains(args, fragment) {
			t.Errorf("Codex nested-tool policy missing %q:\n%s", fragment, args)
		}
	}
}

func TestMissingAllowlistedVariableAndStatePathEmitOperatorVisibleDiagnostics(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HEIMDALLM_AI_CODEX_ENV_ALLOWLIST", "MISSING_SELECTOR")
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(
		&logs,
		&slog.HandlerOptions{Level: slog.LevelInfo},
	)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	binDir := installEnvironmentCLI(
		t,
		"codex",
		filepath.Join(t.TempDir(), "environment"),
		filepath.Join(t.TempDir(), "home"),
		"",
	)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := executor.New().ExecuteRaw("codex", "prompt", executor.ExecOptions{}); err != nil {
		t.Fatalf("ExecuteRaw: %v", err)
	}
	if _, err := executor.New().ExecuteRaw("codex", "prompt", executor.ExecOptions{}); err != nil {
		t.Fatalf("second ExecuteRaw: %v", err)
	}

	got := logs.String()
	for _, fragment := range []string{
		"allowlisted environment variable is not set",
		"name=MISSING_SELECTOR",
		"provider state path is absent",
		filepath.Join(home, ".codex"),
	} {
		if !strings.Contains(got, fragment) {
			t.Errorf("operator-visible logs missing %q:\n%s", fragment, got)
		}
	}
	if count := strings.Count(got, "provider state path is absent"); count != 1 {
		t.Errorf("state-absence diagnostic count = %d, want deduplicated once:\n%s", count, got)
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
		{name: "other provider credential file", allowlist: "GOOGLE_APPLICATION_CREDENTIALS"},
		{name: "Claude nested credential scrub", allowlist: "CLAUDE_CODE_SUBPROCESS_ENV_SCRUB"},
		{name: "OpenCode project config policy", allowlist: "OPENCODE_DISABLE_PROJECT_CONFIG"},
		{name: "OpenCode pure-mode policy", allowlist: "OPENCODE_PURE"},
		{name: "OpenCode custom config file", allowlist: "OPENCODE_CONFIG"},
		{name: "OpenCode inline config", allowlist: "OPENCODE_CONFIG_CONTENT"},
		{name: "OpenCode executable config directory", allowlist: "OPENCODE_CONFIG_DIR"},
		{name: "Gemini custom system prompt", allowlist: "GEMINI_SYSTEM_MD"},
		{name: "Gemini system prompt writer", allowlist: "GEMINI_WRITE_SYSTEM_MD"},
		{name: "Gemini home relocation", allowlist: "GEMINI_CLI_HOME"},
		{name: "Gemini IDE command", allowlist: "GEMINI_CLI_IDE_SERVER_STDIO_COMMAND"},
		{name: "Gemini sandbox command", allowlist: "GEMINI_SANDBOX"},
		{name: "Gemini sandbox proxy command", allowlist: "GEMINI_SANDBOX_PROXY_COMMAND"},
		{name: "Git config injection", allowlist: "GIT_CONFIG_COUNT"},
		{name: "loader injection", allowlist: "LD_PRELOAD"},
		{name: "JVM agent injection", allowlist: "JAVA_TOOL_OPTIONS"},
		{name: "legacy JVM option injection", allowlist: "_JAVA_OPTIONS"},
		{name: "JDK option injection", allowlist: "JDK_JAVA_OPTIONS"},
		{name: ".NET startup hook", allowlist: "DOTNET_STARTUP_HOOKS"},
		{name: "GTK module injection", allowlist: "GTK_MODULES"},
		{name: "GTK 3 module injection", allowlist: "GTK3_MODULES"},
		{name: "GTK path injection", allowlist: "GTK_PATH"},
		{name: "GIO module directory", allowlist: "GIO_MODULE_DIR"},
		{name: "GIO extra modules", allowlist: "GIO_EXTRA_MODULES"},
		{name: "Python warning import", allowlist: "PYTHONWARNINGS"},
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
	serviceSecret := "service-error-secret-321"
	const sentryDSN = "https://sentry-public-key@sentry.invalid/42"
	const publicEndpoint = "https://true@example.invalid"
	const nonSecret = "eu-error-visible"
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", secret)
	t.Setenv("HTTPS_PROXY", "https://proxy-user:"+proxySecret+"@example.invalid")
	t.Setenv("DATABASE_URL", optInSecret)
	t.Setenv("SERVICE_TOKEN", serviceSecret)
	t.Setenv("SENTRY_DSN", sentryDSN)
	t.Setenv("PUBLIC_ENDPOINT", publicEndpoint)
	t.Setenv("TOKENIZER_MODE", "true")
	t.Setenv("CUSTOM_REGION", nonSecret)
	t.Setenv(
		"HEIMDALLM_AI_CODEX_ENV_ALLOWLIST",
		"DATABASE_URL,SERVICE_TOKEN,SENTRY_DSN,PUBLIC_ENDPOINT,TOKENIZER_MODE,CUSTOM_REGION",
	)
	binDir := t.TempDir()
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$OPENAI_API_KEY\" >&2\n" +
		"printf '%s\\n' \"$HTTPS_PROXY\" >&2\n" +
		"printf '%s\\n' '" + proxySecret + "' >&2\n" +
		"printf '%s\\n' \"$DATABASE_URL\" >&2\n" +
		"printf '%s\\n' \"$SERVICE_TOKEN\" >&2\n" +
		"printf '%s\\n' \"$SENTRY_DSN\" >&2\n" +
		"printf '%s\\n' \"$PUBLIC_ENDPOINT\" >&2\n" +
		"printf '%s\\n' 'endpoint-user=true' >&2\n" +
		"printf 'tokenizer=%s\\n' \"$TOKENIZER_MODE\" >&2\n" +
		"printf 'region=%s\\n' \"$CUSTOM_REGION\" >&2\n" +
		"exit 7\n"
	if err := os.WriteFile(filepath.Join(binDir, "codex"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake CLI: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err := executor.New().ExecuteRaw("codex", "prompt", executor.ExecOptions{})
	if err == nil {
		t.Fatal("ExecuteRaw unexpectedly succeeded")
	}
	for _, value := range []string{
		secret,
		proxySecret,
		optInSecret,
		serviceSecret,
		sentryDSN,
		publicEndpoint,
	} {
		if strings.Contains(err.Error(), value) {
			t.Fatalf("error exposed %q: %v", value, err)
		}
	}
	if !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("error did not redact credential: %v", err)
	}
	for _, visible := range []string{"endpoint-user=true", "tokenizer=true", "region=" + nonSecret} {
		if !strings.Contains(err.Error(), visible) {
			t.Fatalf("error redacted non-secret allowlisted selector %q: %v", visible, err)
		}
	}
}

func TestGeminiErrorRedactsSecretsWithoutCorruptingRuntimeSelectors(t *testing.T) {
	const secret = "gemini-error-secret-123"
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GEMINI_API_KEY", secret)
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/private/google/credentials.json")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "operator-project")
	t.Setenv("GOOGLE_CLOUD_LOCATION", "us-central1")
	t.Setenv("GOOGLE_GENAI_USE_VERTEXAI", "true")
	// Repeating a built-in non-secret selector in the explicit allowlist must
	// not reclassify it as a secret merely because its name says credentials.
	t.Setenv("HEIMDALLM_AI_GEMINI_ENV_ALLOWLIST", "GOOGLE_APPLICATION_CREDENTIALS")

	binDir := t.TempDir()
	script := "#!/bin/sh\n" +
		"printf '%s|%s|%s|%s|%s\\n' \"$GEMINI_API_KEY\" \"$GOOGLE_APPLICATION_CREDENTIALS\" \"$GOOGLE_CLOUD_PROJECT\" \"$GOOGLE_CLOUD_LOCATION\" \"$GOOGLE_GENAI_USE_VERTEXAI\" >&2\n" +
		"exit 7\n"
	if err := os.WriteFile(filepath.Join(binDir, "gemini"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err := executor.New().ExecuteRaw("gemini", "prompt", executor.ExecOptions{})
	if err == nil {
		t.Fatal("ExecuteRaw unexpectedly succeeded")
	}
	if strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("Gemini API key was not redacted: %v", err)
	}
	for _, selector := range []string{
		"/private/google/credentials.json",
		"operator-project",
		"us-central1",
		"true",
	} {
		if !strings.Contains(err.Error(), selector) {
			t.Errorf("Gemini error lost non-secret selector %q: %v", selector, err)
		}
	}
}

func TestClaudeEnterpriseErrorRedactsSecretsWithoutCorruptingSelectors(t *testing.T) {
	directSecrets := map[string]string{
		"ANTHROPIC_API_KEY":       "anthropic-api-secret-123",
		"ANTHROPIC_AUTH_TOKEN":    "gateway-auth-secret-456",
		"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-secret-123",
	}
	const gatewayURL = "https://gateway-user:embedded-secret@gateway.invalid"
	tests := []struct {
		name      string
		secrets   map[string]string
		selectors map[string]string
		visible   []string
	}{
		{
			name: "Bedrock",
			secrets: map[string]string{
				"AWS_ACCESS_KEY_ID":        "AKIAREDACTME123456",
				"AWS_BEARER_TOKEN_BEDROCK": "bedrock-bearer-secret-123",
				"AWS_SECRET_ACCESS_KEY":    "aws-access-secret-456",
				"AWS_SESSION_TOKEN":        "aws-session-secret-789",
			},
			selectors: map[string]string{
				"ANTHROPIC_BEDROCK_BASE_URL":    "https://bedrock-gateway.invalid",
				"AWS_REGION":                    "eu-west-1",
				"CLAUDE_CODE_SKIP_BEDROCK_AUTH": "false",
				"CLAUDE_CODE_USE_BEDROCK":       "true",
			},
			visible: []string{
				"https://bedrock-gateway.invalid",
				"eu-west-1",
				"CLAUDE_CODE_USE_BEDROCK=true",
			},
		},
		{
			name: "Vertex",
			selectors: map[string]string{
				"ANTHROPIC_VERTEX_BASE_URL":      "https://vertex-gateway.invalid",
				"ANTHROPIC_VERTEX_PROJECT_ID":    "operator-project",
				"CLAUDE_CODE_SKIP_VERTEX_AUTH":   "false",
				"CLAUDE_CODE_USE_VERTEX":         "on",
				"CLOUD_ML_REGION":                "europe-west1",
				"GOOGLE_APPLICATION_CREDENTIALS": "/private/google/credentials.json",
			},
			visible: []string{
				"https://vertex-gateway.invalid",
				"operator-project",
				"europe-west1",
				"/private/google/credentials.json",
				"CLAUDE_CODE_USE_VERTEX=on",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			for name, value := range directSecrets {
				t.Setenv(name, value)
			}
			for name, value := range tc.secrets {
				t.Setenv(name, value)
			}
			t.Setenv("ANTHROPIC_BASE_URL", gatewayURL)
			for name, value := range tc.selectors {
				t.Setenv(name, value)
			}

			binDir := t.TempDir()
			script := "#!/bin/sh\n" +
				"if [ \"$1\" = \"--help\" ]; then printf '%s\\n' 'Usage: claude' '  --safe-mode'; exit 0; fi\n" +
				"env >&2\n" +
				"exit 7\n"
			if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

			_, err := executor.New().ExecuteRaw("claude", "prompt", executor.ExecOptions{})
			if err == nil {
				t.Fatal("ExecuteRaw unexpectedly succeeded")
			}
			for name, secret := range directSecrets {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("Claude error exposed %s", name)
				}
			}
			for name, secret := range tc.secrets {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("Claude error exposed %s", name)
				}
			}
			for _, secret := range []string{"embedded-secret", gatewayURL} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("Claude error exposed base-URL credential %q", secret)
				}
			}
			if !strings.Contains(err.Error(), "[REDACTED]") {
				t.Fatalf("Claude error did not contain a redaction marker: %v", err)
			}
			for _, visible := range tc.visible {
				if !strings.Contains(err.Error(), visible) {
					t.Errorf("Claude error lost non-secret selector %q: %v", visible, err)
				}
			}
		})
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
	script := "#!/bin/sh\n"
	if cli == "claude" {
		script += "if [ \"$1\" = \"--help\" ]; then printf '%s\\n' 'Usage: claude' '  --safe-mode'; exit 0; fi\n"
	}
	script +=
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

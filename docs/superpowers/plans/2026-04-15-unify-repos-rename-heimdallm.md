# Unify heimdallm + heimdallm-docker & Rename to heimdallm

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Merge all Docker infrastructure from `heimdallm-docker` into `heimdallm`, producing a single unified repository, then rename the project from `heimdallm` to `heimdallm`.

**Architecture:** The daemon code in `heimdallm` is more advanced (security hardening, comment injection, SSE subscriber limits) but lacks Docker support. The `heimdallm-docker` daemon has env var overrides, Docker-aware paths, opencode CLI support, and better config tests. We merge Docker-specific additions into the heimdallm daemon (additive — no removals from heimdallm), then bring over all Docker infrastructure files, merge CI/CD, and finally rename everything from heimdallm to heimdallm.

**Tech Stack:** Go 1.21, Flutter/Dart, Docker, GitHub Actions, release-please

---

## File Map

### Files to create (from heimdallm-docker)
- `Dockerfile` — multi-stage Docker build
- `docker-compose.yml` — production Docker deployment
- `docker-compose.test.yml` — test overlay
- `scripts/test-local.sh` — 3-level test runner
- `docs/e2e-test-guide.md` — E2E testing guide
- `config.example.toml` — configuration reference
- `.env.example` — environment variable reference
- `release-please-config.json` — automated versioning
- `.release-please-manifest.json` — version tracking
- `CHANGELOG.md` — release history
- `.github/CODEOWNERS` — code ownership
- `.github/workflows/docker-publish.yml` — Docker CI
- `.github/workflows/release.yml` — release-please + Docker push

### Files to modify (merge Docker features into heimdallm)
- `daemon/cmd/heimdallm/main.go` — add Docker-aware paths, bind addr, LoadOrCreate
- `daemon/internal/config/config.go` — add BindAddr, env var overrides, LoadOrCreate, writeConfigTOML
- `daemon/internal/config/config_test.go` — merge 21 tests from heimdallm-docker
- `daemon/internal/executor/executor.go` — add opencode to CLI allowlist
- `daemon/internal/keychain/keychain.go` — add env var + file-based token resolution (keep Keychain too)
- `daemon/internal/server/handlers.go` — add bindAddr param to Start()

### Files to rename (heimdallm -> heimdallm)
- Every file containing "heimdallm" in its content (Go imports, module path, Flutter config, CI, Docker, docs)
- `daemon/go.mod` module path
- `flutter_app/` references
- All `.github/workflows/*.yml`
- `Makefile` references
- `README.md`

---

## Phase 1: Merge Docker features into daemon

### Task 1: Add BindAddr to server config and Start()

**Files:**
- Modify: `daemon/internal/config/config.go`
- Modify: `daemon/internal/server/handlers.go`

- [ ] **Step 1: Add BindAddr field to ServerConfig in config.go**

In `daemon/internal/config/config.go`, add `BindAddr` to the `ServerConfig` struct:

```go
type ServerConfig struct {
	Port     int    `toml:"port"`
	BindAddr string `toml:"bind_addr"`
}
```

And in `applyDefaults()`, add:

```go
if c.Server.BindAddr == "" {
	c.Server.BindAddr = "127.0.0.1"
}
```

- [ ] **Step 2: Add bindAddr parameter to server Start()**

In `daemon/internal/server/handlers.go`, modify the `Start()` method signature to accept `bindAddr string` and use it in the listen address:

```go
func (s *Server) Start(bindAddr string) error {
	addr := fmt.Sprintf("%s:%d", bindAddr, s.port)
	// ...
}
```

- [ ] **Step 3: Update main.go to pass BindAddr to server**

In `daemon/cmd/heimdallm/main.go`, update the `srv.Start()` call:

```go
srv.Start(cfg.Server.BindAddr)
```

- [ ] **Step 4: Run tests**

Run: `cd daemon && go test ./... -timeout 60s`
Expected: All existing tests pass.

- [ ] **Step 5: Commit**

```bash
cd /Users/stejon/develop/heimdallm
git add daemon/internal/config/config.go daemon/internal/server/handlers.go daemon/cmd/heimdallm/main.go
git commit -m "feat(config): add BindAddr to server config and Start()"
```

---

### Task 2: Add environment variable overrides to config

**Files:**
- Modify: `daemon/internal/config/config.go`

- [ ] **Step 1: Add applyEnvOverrides() method**

Add after `applyDefaults()` in `daemon/internal/config/config.go`:

```go
func (c *Config) applyEnvOverrides() {
	if v := os.Getenv("HEIMDALLM_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			c.Server.Port = p
		}
	}
	if v := os.Getenv("HEIMDALLM_BIND_ADDR"); v != "" {
		c.Server.BindAddr = v
	}
	if v := os.Getenv("HEIMDALLM_POLL_INTERVAL"); v != "" {
		c.GitHub.PollInterval = v
	}
	if v := os.Getenv("HEIMDALLM_REPOSITORIES"); v != "" {
		repos := strings.Split(v, ",")
		cleaned := make([]string, 0, len(repos))
		for _, r := range repos {
			if s := strings.TrimSpace(r); s != "" {
				cleaned = append(cleaned, s)
			}
		}
		c.GitHub.Repositories = cleaned
	}
	if v := os.Getenv("HEIMDALLM_AI_PRIMARY"); v != "" {
		c.AI.Primary = v
	}
	if v := os.Getenv("HEIMDALLM_AI_FALLBACK"); v != "" {
		c.AI.Fallback = v
	}
	if v := os.Getenv("HEIMDALLM_REVIEW_MODE"); v != "" {
		c.AI.ReviewMode = v
	}
	if v := os.Getenv("HEIMDALLM_RETENTION_DAYS"); v != "" {
		if d, err := strconv.Atoi(v); err == nil {
			c.Retention.MaxDays = d
		}
	}
}
```

Add required imports: `"os"`, `"strconv"`, `"strings"`.

- [ ] **Step 2: Call applyEnvOverrides() in Load()**

In the `Load()` function, after `applyDefaults()`:

```go
cfg.applyDefaults()
cfg.applyEnvOverrides()
```

- [ ] **Step 3: Run tests**

Run: `cd daemon && go test ./... -timeout 60s`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add daemon/internal/config/config.go
git commit -m "feat(config): add environment variable overrides (12-factor app)"
```

---

### Task 3: Add LoadOrCreate() and writeConfigTOML()

**Files:**
- Modify: `daemon/internal/config/config.go`
- Modify: `daemon/cmd/heimdallm/main.go`

- [ ] **Step 1: Add writeConfigTOML() function**

```go
func writeConfigTOML(path string, cfg *Config) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create config file: %w", err)
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(cfg)
}
```

- [ ] **Step 2: Add LoadOrCreate() function**

```go
// LoadOrCreate loads config from path, or creates a minimal config from
// environment variables if the file does not exist. This is the preferred
// entry point for Docker / headless deployments.
func LoadOrCreate(path string) (*Config, error) {
	if _, err := os.Stat(path); err == nil {
		return Load(path)
	}
	// No config file — build from env vars.
	cfg := &Config{}
	cfg.applyDefaults()
	cfg.applyEnvOverrides()
	if cfg.AI.Primary == "" {
		return nil, fmt.Errorf("no config file and HEIMDALLM_AI_PRIMARY not set")
	}
	if err := writeConfigTOML(path, cfg); err != nil {
		log.Printf("warning: could not persist generated config: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}
```

- [ ] **Step 3: Add Docker-aware path helpers to main.go**

In `daemon/cmd/heimdallm/main.go`, add two helper functions:

```go
// configPath resolves the config file location.
// Priority: HEIMDALLM_CONFIG_PATH env > /config/config.toml (Docker) > ~/.config/heimdallm/config.toml
func configPath() string {
	if v := os.Getenv("HEIMDALLM_CONFIG_PATH"); v != "" {
		return v
	}
	if info, err := os.Stat("/config"); err == nil && info.IsDir() {
		return "/config/config.toml"
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "heimdallm", "config.toml")
}

// dataDir resolves the data directory.
// Priority: HEIMDALLM_DATA_DIR env > /data (Docker) > ~/.local/share/heimdallm
func dataDirPath() string {
	if v := os.Getenv("HEIMDALLM_DATA_DIR"); v != "" {
		return v
	}
	if info, err := os.Stat("/data"); err == nil && info.IsDir() {
		return "/data"
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "heimdallm")
}
```

- [ ] **Step 4: Wire LoadOrCreate in main.go**

Replace the `config.Load()` call with logic that uses `LoadOrCreate` when in Docker/CI mode:

```go
var cfg *config.Config
var err error
if os.Getenv("CI") == "true" || os.Getenv("HEIMDALLM_DATA_DIR") != "" {
	cfg, err = config.LoadOrCreate(configPath())
} else {
	cfg, err = config.Load(configPath())
}
```

Use `dataDirPath()` for the data directory resolution instead of the hardcoded path.

- [ ] **Step 5: Run tests**

Run: `cd daemon && go test ./... -timeout 60s`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add daemon/internal/config/config.go daemon/cmd/heimdallm/main.go
git commit -m "feat(config): add LoadOrCreate for Docker/headless deployments"
```

---

### Task 4: Add opencode to executor CLI allowlist

**Files:**
- Modify: `daemon/internal/executor/executor.go`

- [ ] **Step 1: Add opencode to the allowed CLIs**

In `daemon/internal/executor/executor.go`, find the CLI allowlist and add `"opencode"`:

```go
var allowedCLIs = map[string]bool{
	"claude":   true,
	"gemini":   true,
	"codex":    true,
	"opencode": true,
}
```

- [ ] **Step 2: Run tests**

Run: `cd daemon && go test ./... -timeout 60s`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add daemon/internal/executor/executor.go
git commit -m "feat(executor): add opencode as 4th supported AI CLI"
```

---

### Task 5: Add env var + file-based token resolution to keychain

**Files:**
- Modify: `daemon/internal/keychain/keychain.go`

- [ ] **Step 1: Add Docker-compatible token resolution**

Modify `Get()` to check env var first, then Keychain (macOS), then file paths:

```go
func Get() (string, error) {
	// 1. Environment variable (Docker / CI).
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		return tok, nil
	}

	// 2. macOS Keychain (desktop).
	if runtime.GOOS == "darwin" {
		if tok, err := getFromKeychain(); err == nil && tok != "" {
			return tok, nil
		}
	}

	// 3. Token files (Docker mount or manual).
	for _, p := range tokenFilePaths() {
		if data, err := os.ReadFile(p); err == nil {
			if tok := strings.TrimSpace(string(data)); tok != "" {
				return tok, nil
			}
		}
	}

	return "", fmt.Errorf("no GitHub token found: set GITHUB_TOKEN, use macOS Keychain, or mount a token file at /config/.token")
}

func tokenFilePaths() []string {
	paths := []string{"/config/.token"}
	home, err := os.UserHomeDir()
	if err == nil {
		paths = append(paths, filepath.Join(home, ".config", "heimdallm", ".token"))
	}
	return paths
}
```

Add imports: `"runtime"`, `"strings"`, `"path/filepath"`.

Keep the existing `getFromKeychain()` and `Set()` functions for macOS desktop use.

- [ ] **Step 2: Run tests**

Run: `cd daemon && go test ./... -timeout 60s`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add daemon/internal/keychain/keychain.go
git commit -m "feat(keychain): add env var and file-based token resolution for Docker"
```

---

### Task 6: Merge better config tests from heimdallm-docker

**Files:**
- Modify: `daemon/internal/config/config_test.go`

- [ ] **Step 1: Merge test cases from heimdallm-docker**

Read the full test file from `/Users/stejon/develop/heimdallm-docker/daemon/internal/config/config_test.go` and merge all test functions into the heimdallm version. Keep any existing heimdallm tests that cover validation logic not present in docker. Add all new tests:
- `TestApplyDefaults` / `TestApplyDefaults_PreservesExisting`
- `TestApplyEnvOverrides` (all variants)
- `TestLoadOrCreate_*` (3 scenarios)
- `TestAIForRepo_*` / `TestAgentConfigFor_*`

- [ ] **Step 2: Run tests**

Run: `cd daemon && go test ./internal/config/ -v -timeout 60s`
Expected: All tests PASS

- [ ] **Step 3: Commit**

```bash
git add daemon/internal/config/config_test.go
git commit -m "test(config): merge comprehensive tests from heimdallm-docker (21 cases)"
```

---

## Phase 2: Bring Docker infrastructure files

### Task 7: Copy Docker files from heimdallm-docker

**Files:**
- Create: `Dockerfile`
- Create: `docker-compose.yml`
- Create: `docker-compose.test.yml`
- Create: `config.example.toml`
- Create: `.env.example`
- Create: `scripts/test-local.sh`
- Create: `docs/e2e-test-guide.md`

- [ ] **Step 1: Copy Docker infrastructure files**

```bash
cp /Users/stejon/develop/heimdallm-docker/Dockerfile /Users/stejon/develop/heimdallm/
cp /Users/stejon/develop/heimdallm-docker/docker-compose.yml /Users/stejon/develop/heimdallm/
cp /Users/stejon/develop/heimdallm-docker/docker-compose.test.yml /Users/stejon/develop/heimdallm/
cp /Users/stejon/develop/heimdallm-docker/config.example.toml /Users/stejon/develop/heimdallm/
cp /Users/stejon/develop/heimdallm-docker/.env.example /Users/stejon/develop/heimdallm/
mkdir -p /Users/stejon/develop/heimdallm/scripts
cp /Users/stejon/develop/heimdallm-docker/scripts/test-local.sh /Users/stejon/develop/heimdallm/scripts/
cp /Users/stejon/develop/heimdallm-docker/docs/e2e-test-guide.md /Users/stejon/develop/heimdallm/docs/
```

- [ ] **Step 2: Update Dockerfile paths**

The Dockerfile copies from `daemon/` — this path is the same in both repos, so the Dockerfile should work as-is. Verify the `COPY daemon/ ./daemon/` line matches the directory structure.

- [ ] **Step 3: Verify Docker build**

Run: `cd /Users/stejon/develop/heimdallm && docker build -t heimdallm-test .`
Expected: Image builds successfully.

- [ ] **Step 4: Commit**

```bash
git add Dockerfile docker-compose.yml docker-compose.test.yml config.example.toml .env.example scripts/ docs/e2e-test-guide.md
git commit -m "feat(docker): add Docker infrastructure from heimdallm-docker"
```

---

### Task 8: Add release-please and CODEOWNERS

**Files:**
- Create: `release-please-config.json`
- Create: `.release-please-manifest.json`
- Create: `CHANGELOG.md`
- Create: `.github/CODEOWNERS`

- [ ] **Step 1: Copy release-please config**

```bash
cp /Users/stejon/develop/heimdallm-docker/release-please-config.json /Users/stejon/develop/heimdallm/
cp /Users/stejon/develop/heimdallm-docker/.release-please-manifest.json /Users/stejon/develop/heimdallm/
cp /Users/stejon/develop/heimdallm-docker/CHANGELOG.md /Users/stejon/develop/heimdallm/
```

Reset the manifest version to `0.1.0` since this is a new unified repo.

- [ ] **Step 2: Copy CODEOWNERS**

```bash
cp /Users/stejon/develop/heimdallm-docker/.github/CODEOWNERS /Users/stejon/develop/heimdallm/.github/
```

- [ ] **Step 3: Commit**

```bash
git add release-please-config.json .release-please-manifest.json CHANGELOG.md .github/CODEOWNERS
git commit -m "chore: add release-please config and CODEOWNERS"
```

---

### Task 9: Merge CI/CD workflows

**Files:**
- Keep: `.github/workflows/tests.yml` (desktop tests)
- Keep: `.github/workflows/build.yml` (desktop builds)
- Keep: `.github/workflows/tag.yml` (manual tagging)
- Create: `.github/workflows/docker-publish.yml` (Docker build validation on PRs)
- Create: `.github/workflows/release.yml` (release-please + Docker push)

- [ ] **Step 1: Copy Docker CI workflows**

```bash
cp /Users/stejon/develop/heimdallm-docker/.github/workflows/docker-publish.yml /Users/stejon/develop/heimdallm/.github/workflows/
cp /Users/stejon/develop/heimdallm-docker/.github/workflows/release.yml /Users/stejon/develop/heimdallm/.github/workflows/
```

- [ ] **Step 2: Review and adjust workflow triggers**

Ensure no workflow name conflicts. The existing `build.yml` triggers on version tags — check it doesn't collide with `release.yml`. Both may need adjustments to avoid double-triggering.

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/docker-publish.yml .github/workflows/release.yml
git commit -m "ci: add Docker publish and release-please workflows"
```

---

## Phase 3: Rename heimdallm -> heimdallm

### Task 10: Rename Go module and all Go references

**Files:**
- Modify: `daemon/go.mod`
- Modify: All `*.go` files with import paths containing "heimdallm"

- [ ] **Step 1: Update go.mod module path**

Change `module github.com/heimdallm/daemon` (or current module) to `module github.com/heimdallm/daemon`.

- [ ] **Step 2: Find and replace all Go import paths**

```bash
cd /Users/stejon/develop/heimdallm
grep -r "heimdallm" daemon/ --include="*.go" -l
```

Replace all occurrences of the old module path with the new one in every `.go` file.

- [ ] **Step 3: Replace string literals containing "heimdallm"**

Search all Go files for string literals like `"heimdallm"` (log messages, file paths, config keys, service names) and update to `"heimdallm"`. Be careful with:
- Config path: `~/.config/heimdallm` -> `~/.config/heimdallm`
- Data path: `~/.local/share/heimdallm` -> `~/.local/share/heimdallm`
- Keychain service name
- LaunchAgent label
- API token header name `X-Heimdallm-Token` -> `X-Heimdallm-Token`
- Docker user name in Dockerfile

- [ ] **Step 4: Run tests**

Run: `cd daemon && go test ./... -timeout 60s`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add daemon/
git commit -m "refactor: rename heimdallm -> heimdallm in Go daemon"
```

---

### Task 11: Rename Flutter app references

**Files:**
- Modify: `flutter_app/pubspec.yaml`
- Modify: `flutter_app/lib/**/*.dart` (all Dart files referencing "heimdallm")
- Modify: `flutter_app/macos/Runner/Info.plist`
- Modify: `flutter_app/linux/CMakeLists.txt`

- [ ] **Step 1: Find all heimdallm references in Flutter**

```bash
grep -r "heimdallm" flutter_app/ -l
```

- [ ] **Step 2: Replace in pubspec.yaml and Dart source**

Update app name, description, and any hardcoded references to "heimdallm" in all Dart files.

- [ ] **Step 3: Update platform configs**

- `Info.plist`: Bundle identifier, app name
- `CMakeLists.txt`: Binary name
- Any `.entitlements` files

- [ ] **Step 4: Run Flutter tests**

Run: `cd flutter_app && flutter test`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add flutter_app/
git commit -m "refactor: rename heimdallm -> heimdallm in Flutter app"
```

---

### Task 12: Rename Docker, CI, and docs references

**Files:**
- Modify: `Dockerfile`
- Modify: `docker-compose.yml`, `docker-compose.test.yml`
- Modify: `Dockerfile.linux-verify`
- Modify: `config.example.toml`, `.env.example`
- Modify: `scripts/test-local.sh`
- Modify: `Makefile`
- Modify: `README.md`, `docs/*.md`
- Modify: `.github/workflows/*.yml`
- Modify: `release-please-config.json`
- Modify: `.gitignore`, `.dockerignore`

- [ ] **Step 1: Find all remaining heimdallm references**

```bash
grep -r "heimdallm" /Users/stejon/develop/heimdallm/ --include="*.yml" --include="*.yaml" --include="*.md" --include="*.toml" --include="*.sh" --include="*.json" --include="Makefile" --include="Dockerfile*" --include=".env*" --include=".git*" -l
```

- [ ] **Step 2: Replace all occurrences**

For each file, replace:
- `heimdallm` -> `heimdallm` (lowercase)
- `Heimdallm` -> `Heimdallm` (capitalized)
- `HEIMDALLM_` -> `HEIMDALLM_` (env var prefix — **IMPORTANT**: this changes all env var names)

Note: the env var prefix rename (`HEIMDALLM_*` -> `HEIMDALLM_*`) means `heimdallm-deploy/.env` will also need updating.

- [ ] **Step 3: Rename Docker image reference**

In CI workflows, update `ghcr.io/theburrowhub/heimdallm-docker` to `ghcr.io/theburrowhub/heimdallm`.

- [ ] **Step 4: Verify Docker build with new name**

Run: `docker build -t heimdallm .`
Expected: Builds successfully.

- [ ] **Step 5: Run all tests**

```bash
cd daemon && go test ./... -timeout 60s
cd ../flutter_app && flutter test
```

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "refactor: rename heimdallm -> heimdallm across Docker, CI, and docs"
```

---

## Phase 4: GitHub repo rename and cleanup

### Task 13: Rename GitHub repository

**This requires manual action or gh CLI:**

- [ ] **Step 1: Rename repo on GitHub**

```bash
gh repo rename heimdallm --repo theburrowhub/heimdallm --yes
```

- [ ] **Step 2: Update local remote**

```bash
cd /Users/stejon/develop/heimdallm
git remote set-url origin git@github.com:theburrowhub/heimdallm.git
```

- [ ] **Step 3: Rename local directory**

```bash
mv /Users/stejon/develop/heimdallm /Users/stejon/develop/heimdallm
```

- [ ] **Step 4: Update heimdallm-deploy to use new image**

Update `/Users/stejon/develop/heimdallm-deploy/docker-compose.yml`:
- Image: `ghcr.io/theburrowhub/heimdallm:latest`
- Env var prefix: `HEIMDALLM_*`
- Container name: `heimdallm`

Update `/Users/stejon/develop/heimdallm-deploy/.env`:
- Rename all `HEIMDALLM_*` vars to `HEIMDALLM_*`

Update `/Users/stejon/develop/heimdallm-deploy/config/config.toml`:
- Any internal references

- [ ] **Step 5: Push all changes**

```bash
cd /Users/stejon/develop/heimdallm
git push origin main
```

---

### Task 14: Archive heimdallm-docker repo

- [ ] **Step 1: Archive the old Docker repo**

```bash
gh repo archive theburrowhub/heimdallm-docker --yes
```

- [ ] **Step 2: Add note to heimdallm-docker README**

Before archiving, push a final commit noting the repo has been unified into `theburrowhub/heimdallm`.

---

## Decision Points

1. **Env var prefix**: Renaming `HEIMDALLM_*` to `HEIMDALLM_*` is a breaking change for anyone using the Docker image. Consider supporting both prefixes temporarily with the old ones as deprecated fallbacks.

2. **Docker image name**: The new image will be at `ghcr.io/theburrowhub/heimdallm`. The old `heimdallm-docker` image stops getting updates once archived.

3. **Go module path**: Changing the module path is a breaking change for any external importers. Since this is a private project, this should be fine.

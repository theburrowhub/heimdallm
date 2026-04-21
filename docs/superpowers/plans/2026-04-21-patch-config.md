# Patch-Based Config Saves Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate silent config data loss by switching from full-config TOML writes to PATCH-based API calls where the daemon owns the file.

**Architecture:** Flutter sends JSON patches (only changed fields) to new PATCH endpoints. The daemon reads the current TOML into a generic map, deep-merges the patch, validates by round-tripping through the Config struct, and atomically writes the result. DELETE endpoint resets individual per-repo fields. The existing PUT /config (web UI → SQLite store) is unchanged.

**Tech Stack:** Go (chi router, BurntSushi/toml), Dart/Flutter (http, Riverpod)

---

## File Structure

| File | Action | Responsibility |
|------|--------|----------------|
| `daemon/internal/config/writer.go` | Create | TOML map I/O, deep merge, null check, delete-key, atomic write, validate-map |
| `daemon/internal/config/writer_test.go` | Create | Unit tests for all writer.go functions |
| `daemon/internal/server/handlers.go` | Modify | Add `configPath`, `tomlMu`, 3 new handlers, `patchTOML` pipeline, route registration |
| `daemon/internal/server/handlers_test.go` | Modify | Integration tests for PATCH/DELETE endpoints |
| `daemon/cmd/heimdallm/main.go` | Modify | Pass `cfgPath` to server via `SetConfigPath` |
| `flutter_app/lib/core/api/api_client.dart` | Modify | Add `patchConfig`, `patchRepoConfig`, `deleteRepoField` methods |
| `flutter_app/lib/features/config/config_providers.dart` | Modify | `save()` → patch-based; add `updateFromServer`; add `_computeGlobalDiff` |
| `flutter_app/lib/features/repositories/repo_detail_screen.dart` | Modify | `_autoSave` → diff-based; add `_computeDiff`; handle monitoring changes |
| `flutter_app/lib/shared/widgets/override_field.dart` | Modify | Reset button calls DELETE via new callback |

---

### Task 1: TOML Merge Engine

**Files:**
- Create: `daemon/internal/config/writer.go`
- Create: `daemon/internal/config/writer_test.go`

- [ ] **Step 1: Write tests for DeepMerge**

```go
// daemon/internal/config/writer_test.go
package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDeepMerge_ScalarReplace(t *testing.T) {
	base := map[string]any{"a": "old", "b": "keep"}
	patch := map[string]any{"a": "new"}
	result := DeepMerge(base, patch)
	assert.Equal(t, "new", result["a"])
	assert.Equal(t, "keep", result["b"])
}

func TestDeepMerge_NestedMerge(t *testing.T) {
	base := map[string]any{
		"ai": map[string]any{"primary": "claude", "fallback": "gemini"},
	}
	patch := map[string]any{
		"ai": map[string]any{"primary": "openai"},
	}
	result := DeepMerge(base, patch)
	ai := result["ai"].(map[string]any)
	assert.Equal(t, "openai", ai["primary"])
	assert.Equal(t, "gemini", ai["fallback"])
}

func TestDeepMerge_AddNewKey(t *testing.T) {
	base := map[string]any{"a": "1"}
	patch := map[string]any{"b": "2"}
	result := DeepMerge(base, patch)
	assert.Equal(t, "1", result["a"])
	assert.Equal(t, "2", result["b"])
}

func TestDeepMerge_EmptyPatch(t *testing.T) {
	base := map[string]any{"a": "1"}
	result := DeepMerge(base, map[string]any{})
	assert.Equal(t, "1", result["a"])
}

func TestDeepMerge_ReplaceArrayEntirely(t *testing.T) {
	base := map[string]any{"tags": []any{"a", "b"}}
	patch := map[string]any{"tags": []any{"c"}}
	result := DeepMerge(base, patch)
	assert.Equal(t, []any{"c"}, result["tags"])
}

func TestDeepMerge_PatchMapOverScalar(t *testing.T) {
	base := map[string]any{"x": "string"}
	patch := map[string]any{"x": map[string]any{"nested": true}}
	result := DeepMerge(base, patch)
	assert.Equal(t, map[string]any{"nested": true}, result["x"])
}

func TestDeepMerge_DoesNotMutateBase(t *testing.T) {
	base := map[string]any{
		"ai": map[string]any{"primary": "claude"},
	}
	patch := map[string]any{
		"ai": map[string]any{"primary": "openai"},
	}
	DeepMerge(base, patch)
	ai := base["ai"].(map[string]any)
	assert.Equal(t, "claude", ai["primary"], "base must not be mutated")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd daemon && go test ./internal/config/ -run TestDeepMerge -v`
Expected: compilation error — `DeepMerge` undefined

- [ ] **Step 3: Implement DeepMerge**

```go
// daemon/internal/config/writer.go
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// DeepMerge recursively merges patch into a copy of base. Only keys present
// in patch are updated; absent keys are left untouched. When both base and
// patch have a map for the same key, the merge recurses. Otherwise the patch
// value replaces the base value entirely (including arrays).
//
// base is never mutated — the result is a new map.
func DeepMerge(base, patch map[string]any) map[string]any {
	out := make(map[string]any, len(base))
	for k, v := range base {
		out[k] = v
	}
	for k, pv := range patch {
		bv, exists := out[k]
		bMap, bIsMap := bv.(map[string]any)
		pMap, pIsMap := pv.(map[string]any)
		if exists && bIsMap && pIsMap {
			out[k] = DeepMerge(bMap, pMap)
		} else {
			out[k] = pv
		}
	}
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd daemon && go test ./internal/config/ -run TestDeepMerge -v`
Expected: all PASS

- [ ] **Step 5: Write tests for ContainsNull**

```go
// append to daemon/internal/config/writer_test.go

func TestContainsNull_NoNulls(t *testing.T) {
	m := map[string]any{"a": "1", "b": map[string]any{"c": 2}}
	assert.NoError(t, ContainsNull(m))
}

func TestContainsNull_TopLevelNull(t *testing.T) {
	m := map[string]any{"a": nil}
	assert.Error(t, ContainsNull(m))
}

func TestContainsNull_NestedNull(t *testing.T) {
	m := map[string]any{"a": map[string]any{"b": nil}}
	assert.Error(t, ContainsNull(m))
}

func TestContainsNull_NullInArray(t *testing.T) {
	// Arrays with nil elements — we only reject top-level map nil values,
	// not array elements, since TOML arrays don't have null.
	m := map[string]any{"a": []any{"x", nil}}
	assert.NoError(t, ContainsNull(m))
}
```

- [ ] **Step 6: Run tests to verify they fail**

Run: `cd daemon && go test ./internal/config/ -run TestContainsNull -v`
Expected: compilation error — `ContainsNull` undefined

- [ ] **Step 7: Implement ContainsNull**

```go
// append to daemon/internal/config/writer.go

// ContainsNull walks a nested map and returns an error if any value is nil.
// This enforces the "null means don't touch, use DELETE to remove" principle.
func ContainsNull(m map[string]any) error {
	for k, v := range m {
		if v == nil {
			return fmt.Errorf("null value at key %q — use DELETE to remove fields", k)
		}
		if sub, ok := v.(map[string]any); ok {
			if err := ContainsNull(sub); err != nil {
				return fmt.Errorf("%s.%w", k, err)
			}
		}
	}
	return nil
}
```

- [ ] **Step 8: Run tests to verify they pass**

Run: `cd daemon && go test ./internal/config/ -run TestContainsNull -v`
Expected: all PASS

- [ ] **Step 9: Write tests for DeleteNestedKey**

```go
// append to daemon/internal/config/writer_test.go

func TestDeleteNestedKey_TopLevel(t *testing.T) {
	m := map[string]any{"a": "1", "b": "2"}
	deleted := DeleteNestedKey(m, []string{"a"})
	assert.True(t, deleted)
	assert.NotContains(t, m, "a")
	assert.Contains(t, m, "b")
}

func TestDeleteNestedKey_Nested(t *testing.T) {
	m := map[string]any{
		"ai": map[string]any{
			"repos": map[string]any{
				"org/repo": map[string]any{
					"primary":  "claude",
					"pr_draft": true,
				},
			},
		},
	}
	deleted := DeleteNestedKey(m, []string{"ai", "repos", "org/repo", "pr_draft"})
	assert.True(t, deleted)
	repo := m["ai"].(map[string]any)["repos"].(map[string]any)["org/repo"].(map[string]any)
	assert.NotContains(t, repo, "pr_draft")
	assert.Contains(t, repo, "primary")
}

func TestDeleteNestedKey_CleansEmptyParents(t *testing.T) {
	m := map[string]any{
		"ai": map[string]any{
			"repos": map[string]any{
				"org/repo": map[string]any{
					"pr_draft": true,
				},
			},
		},
	}
	deleted := DeleteNestedKey(m, []string{"ai", "repos", "org/repo", "pr_draft"})
	assert.True(t, deleted)
	// org/repo map is now empty → removed; repos map is now empty → removed
	assert.NotContains(t, m["ai"].(map[string]any), "repos")
}

func TestDeleteNestedKey_NonExistent(t *testing.T) {
	m := map[string]any{"a": "1"}
	deleted := DeleteNestedKey(m, []string{"b"})
	assert.False(t, deleted)
}

func TestDeleteNestedKey_PartialPath(t *testing.T) {
	m := map[string]any{"a": "string-not-map"}
	deleted := DeleteNestedKey(m, []string{"a", "b"})
	assert.False(t, deleted)
}
```

- [ ] **Step 10: Run tests to verify they fail**

Run: `cd daemon && go test ./internal/config/ -run TestDeleteNestedKey -v`
Expected: compilation error — `DeleteNestedKey` undefined

- [ ] **Step 11: Implement DeleteNestedKey**

```go
// append to daemon/internal/config/writer.go

// DeleteNestedKey deletes a key at the given path within a nested map.
// If deleting the key leaves parent maps empty, they are also removed.
// Returns true if the key was found and deleted.
func DeleteNestedKey(m map[string]any, path []string) bool {
	if len(path) == 0 {
		return false
	}
	if len(path) == 1 {
		if _, exists := m[path[0]]; !exists {
			return false
		}
		delete(m, path[0])
		return true
	}
	child, ok := m[path[0]].(map[string]any)
	if !ok {
		return false
	}
	deleted := DeleteNestedKey(child, path[1:])
	if deleted && len(child) == 0 {
		delete(m, path[0])
	}
	return deleted
}
```

- [ ] **Step 12: Run tests to verify they pass**

Run: `cd daemon && go test ./internal/config/ -run TestDeleteNestedKey -v`
Expected: all PASS

- [ ] **Step 13: Write tests for NormalizeNumbers**

```go
// append to daemon/internal/config/writer_test.go

func TestNormalizeNumbers_FloatToInt(t *testing.T) {
	m := map[string]any{"port": float64(7842)}
	NormalizeNumbers(m)
	assert.Equal(t, int64(7842), m["port"])
}

func TestNormalizeNumbers_KeepsFractional(t *testing.T) {
	m := map[string]any{"ratio": float64(3.14)}
	NormalizeNumbers(m)
	assert.Equal(t, float64(3.14), m["ratio"])
}

func TestNormalizeNumbers_Nested(t *testing.T) {
	m := map[string]any{
		"server": map[string]any{"port": float64(8080)},
	}
	NormalizeNumbers(m)
	assert.Equal(t, int64(8080), m["server"].(map[string]any)["port"])
}

func TestNormalizeNumbers_InArray(t *testing.T) {
	m := map[string]any{"vals": []any{float64(1), float64(2.5)}}
	NormalizeNumbers(m)
	arr := m["vals"].([]any)
	assert.Equal(t, int64(1), arr[0])
	assert.Equal(t, float64(2.5), arr[1])
}
```

- [ ] **Step 14: Run tests to verify they fail**

Run: `cd daemon && go test ./internal/config/ -run TestNormalizeNumbers -v`
Expected: compilation error — `NormalizeNumbers` undefined

- [ ] **Step 15: Implement NormalizeNumbers**

```go
// append to daemon/internal/config/writer.go

// NormalizeNumbers walks a map from JSON decoding and converts float64 values
// that represent whole numbers to int64. TOML distinguishes integer and float
// types; without this conversion, a JSON number like 7842 (decoded as
// float64(7842)) would serialize as 7842.0 in TOML and fail to unmarshal
// into an int struct field.
func NormalizeNumbers(m map[string]any) {
	for k, v := range m {
		switch val := v.(type) {
		case float64:
			if val == float64(int64(val)) {
				m[k] = int64(val)
			}
		case map[string]any:
			NormalizeNumbers(val)
		case []any:
			normalizeSlice(val)
		}
	}
}

func normalizeSlice(s []any) {
	for i, v := range s {
		switch val := v.(type) {
		case float64:
			if val == float64(int64(val)) {
				s[i] = int64(val)
			}
		case map[string]any:
			NormalizeNumbers(val)
		case []any:
			normalizeSlice(val)
		}
	}
}
```

- [ ] **Step 16: Run tests to verify they pass**

Run: `cd daemon && go test ./internal/config/ -run TestNormalizeNumbers -v`
Expected: all PASS

- [ ] **Step 17: Implement ReadTOMLMap, ValidateMap, AtomicWriteTOML**

These are I/O functions that are harder to unit-test in isolation; they're integration-tested via the handler tests in Task 2-4.

```go
// append to daemon/internal/config/writer.go

// ReadTOMLMap reads a TOML file and returns its contents as a generic map.
func ReadTOMLMap(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	var m map[string]any
	if err := toml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	return m, nil
}

// ValidateMap round-trips a generic map through the Config struct to check
// that the map represents a valid configuration. Returns nil on success.
func ValidateMap(m map[string]any) error {
	buf, err := toml.Marshal(m)
	if err != nil {
		return fmt.Errorf("config: marshal map: %w", err)
	}
	var cfg Config
	if err := toml.Unmarshal(buf, &cfg); err != nil {
		return fmt.Errorf("config: invalid config structure: %w", err)
	}
	return cfg.Validate()
}

// AtomicWriteTOML writes a generic map as TOML to the given path using a
// temp-file + rename strategy to prevent corruption on crash.
func AtomicWriteTOML(path string, m map[string]any) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".config-*.toml")
	if err != nil {
		return fmt.Errorf("config: create temp: %w", err)
	}
	tmpName := tmp.Name()
	if err := toml.NewEncoder(tmp).Encode(m); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("config: encode toml: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("config: close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("config: rename: %w", err)
	}
	return nil
}
```

- [ ] **Step 18: Run full test suite**

Run: `cd daemon && go test ./internal/config/ -v -count=1`
Expected: all PASS (existing + new tests)

- [ ] **Step 19: Commit**

```bash
git add daemon/internal/config/writer.go daemon/internal/config/writer_test.go
git commit -m "feat(config): add TOML merge engine — DeepMerge, ContainsNull, DeleteNestedKey, atomic write (#144)"
```

---

### Task 2: Server Infrastructure + handlePatchConfig

**Files:**
- Modify: `daemon/internal/server/handlers.go:29-48` (Server struct), `handlers.go:62-81` (constructors), `handlers.go:182-210` (buildRouter)
- Modify: `daemon/internal/server/handlers_test.go`

- [ ] **Step 1: Add configPath and tomlMu to Server struct**

In `daemon/internal/server/handlers.go`, add to the Server struct (after line 47):

```go
// configPath is the path to config.toml. Required for PATCH/DELETE
// endpoints that read-merge-write the TOML file.
configPath string
// tomlMu serialises TOML read-merge-write operations.
tomlMu sync.Mutex
```

Add `"sync"` to the imports.

Add the setter method (after the existing Set* methods):

```go
// SetConfigPath sets the path to config.toml for PATCH/DELETE handlers.
func (srv *Server) SetConfigPath(path string) { srv.configPath = path }
```

- [ ] **Step 2: Add patchTOML shared pipeline method**

Add to `daemon/internal/server/handlers.go` (after the Set* methods):

```go
// patchTOML is the shared read-merge-write pipeline for all TOML-mutating
// endpoints. mutateFn receives the current TOML as a map and must apply
// its changes in-place. On success, returns the full live config for the
// response body.
func (srv *Server) patchTOML(mutateFn func(m map[string]any) error) (map[string]any, error) {
	srv.tomlMu.Lock()
	defer srv.tomlMu.Unlock()

	m, err := config.ReadTOMLMap(srv.configPath)
	if err != nil {
		return nil, err
	}
	if err := mutateFn(m); err != nil {
		return nil, err
	}
	if err := config.ValidateMap(m); err != nil {
		return nil, err
	}
	if err := config.AtomicWriteTOML(srv.configPath, m); err != nil {
		return nil, err
	}
	if srv.reloadFn != nil {
		if err := srv.reloadFn(); err != nil {
			return nil, fmt.Errorf("config reload after write: %w", err)
		}
	}
	if srv.configFn != nil {
		return srv.configFn(), nil
	}
	return m, nil
}
```

- [ ] **Step 3: Implement handlePatchConfig and register route**

Add handler to `daemon/internal/server/handlers.go`:

```go
func (srv *Server) handlePatchConfig(w http.ResponseWriter, r *http.Request) {
	if srv.configPath == "" {
		http.Error(w, `{"error":"PATCH not available — configPath not set"}`, http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var patch map[string]any
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}
	if err := config.ContainsNull(patch); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "null values not allowed in PATCH — use DELETE to remove fields",
		})
		return
	}
	config.NormalizeNumbers(patch)

	result, err := srv.patchTOML(func(m map[string]any) error {
		merged := config.DeepMerge(m, patch)
		for k, v := range merged {
			m[k] = v
		}
		// Remove base keys not in merged (shouldn't happen with DeepMerge,
		// but defensive).
		for k := range m {
			if _, ok := merged[k]; !ok {
				delete(m, k)
			}
		}
		return nil
	})
	if err != nil {
		slog.Error("PATCH /config failed", "err", err)
		// Validation errors are user errors; I/O errors are server errors.
		if strings.Contains(err.Error(), "config:") {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
		} else {
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		}
		return
	}
	writeJSON(w, http.StatusOK, result)
}
```

Register in `buildRouter()` (after `r.Put("/config", srv.handlePutConfig)`):

```go
r.Patch("/config", srv.handlePatchConfig)
```

- [ ] **Step 4: Verify daemon compiles**

Run: `cd daemon && go build ./...`
Expected: clean build

- [ ] **Step 5: Write integration test for PATCH /config**

Add to `daemon/internal/server/handlers_test.go`:

```go
func TestHandlePatchConfig(t *testing.T) {
	// Create a temp TOML file with initial config
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	initial := `[ai]
primary = "claude"
fallback = "gemini"
review_mode = "single"

[github]
poll_interval = "5m"
repositories = ["org/repo1"]
`
	os.WriteFile(cfgPath, []byte(initial), 0o644)

	s := setupTestStore(t)
	srv := server.New(s, sse.NewBroker(), nil, "test-token")
	srv.SetConfigPath(cfgPath)
	srv.SetConfigFn(func() map[string]any {
		return map[string]any{"ai_primary": "patched"}
	})
	srv.SetReloadFn(func() error { return nil })

	body := `{"ai":{"primary":"openai","review_mode":"multi"}}`
	req := httptest.NewRequest(http.MethodPatch, "/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Heimdallm-Token", "test-token")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	// Verify TOML was updated
	data, _ := os.ReadFile(cfgPath)
	assert.Contains(t, string(data), `primary = "openai"`)
	assert.Contains(t, string(data), `review_mode = "multi"`)
	// Untouched fields preserved
	assert.Contains(t, string(data), `fallback = "gemini"`)
	assert.Contains(t, string(data), `poll_interval = "5m"`)
}

func TestHandlePatchConfig_RejectsNull(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	os.WriteFile(cfgPath, []byte(`[ai]\nprimary = "claude"\n`), 0o644)

	s := setupTestStore(t)
	srv := server.New(s, sse.NewBroker(), nil, "test-token")
	srv.SetConfigPath(cfgPath)

	body := `{"ai":{"primary":null}}`
	req := httptest.NewRequest(http.MethodPatch, "/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Heimdallm-Token", "test-token")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "null values not allowed")
}
```

- [ ] **Step 6: Run test to verify it passes**

Run: `cd daemon && go test ./internal/server/ -run TestHandlePatchConfig -v`
Expected: PASS

Note: if `setupTestStore` doesn't exist in the test file, adapt the test to match the existing test helper pattern in `handlers_test.go`. Read that file first.

- [ ] **Step 7: Commit**

```bash
git add daemon/internal/server/handlers.go daemon/internal/server/handlers_test.go
git commit -m "feat(server): add PATCH /config endpoint — deep merge into TOML (#144)"
```

---

### Task 3: handlePatchRepoConfig

**Files:**
- Modify: `daemon/internal/server/handlers.go:182-210` (buildRouter)
- Modify: `daemon/internal/server/handlers_test.go`

- [ ] **Step 1: Implement handlePatchRepoConfig**

Add to `daemon/internal/server/handlers.go`:

```go
func (srv *Server) handlePatchRepoConfig(w http.ResponseWriter, r *http.Request) {
	if srv.configPath == "" {
		http.Error(w, `{"error":"PATCH not available — configPath not set"}`, http.StatusServiceUnavailable)
		return
	}
	repo, err := url.PathUnescape(chi.URLParam(r, "repo"))
	if err != nil || repo == "" {
		http.Error(w, `{"error":"invalid repo parameter"}`, http.StatusBadRequest)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var patch map[string]any
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}
	if err := config.ContainsNull(patch); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "null values not allowed in PATCH — use DELETE to remove fields",
		})
		return
	}
	config.NormalizeNumbers(patch)

	// Wrap the repo patch as a global patch: {"ai": {"repos": {"<repo>": <patch>}}}
	globalPatch := map[string]any{
		"ai": map[string]any{
			"repos": map[string]any{
				repo: patch,
			},
		},
	}

	result, err := srv.patchTOML(func(m map[string]any) error {
		merged := config.DeepMerge(m, globalPatch)
		for k, v := range merged {
			m[k] = v
		}
		for k := range m {
			if _, ok := merged[k]; !ok {
				delete(m, k)
			}
		}
		return nil
	})
	if err != nil {
		slog.Error("PATCH /config/repos failed", "repo", repo, "err", err)
		if strings.Contains(err.Error(), "config:") {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
		} else {
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		}
		return
	}
	writeJSON(w, http.StatusOK, result)
}
```

Register in `buildRouter()`:

```go
r.Patch("/config/repos/{repo}", srv.handlePatchRepoConfig)
```

- [ ] **Step 2: Verify daemon compiles**

Run: `cd daemon && go build ./...`
Expected: clean build

- [ ] **Step 3: Write integration test**

Add to `daemon/internal/server/handlers_test.go`:

```go
func TestHandlePatchRepoConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	initial := `[ai]
primary = "claude"

[ai.repos."org/repo1"]
primary = "gemini"
fallback = "openai"
`
	os.WriteFile(cfgPath, []byte(initial), 0o644)

	s := setupTestStore(t)
	srv := server.New(s, sse.NewBroker(), nil, "test-token")
	srv.SetConfigPath(cfgPath)
	srv.SetConfigFn(func() map[string]any { return map[string]any{"ok": true} })
	srv.SetReloadFn(func() error { return nil })

	// Patch only primary, leave fallback untouched
	body := `{"primary":"claude-new"}`
	encoded := url.PathEscape("org/repo1")
	req := httptest.NewRequest(http.MethodPatch, "/config/repos/"+encoded, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Heimdallm-Token", "test-token")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	data, _ := os.ReadFile(cfgPath)
	assert.Contains(t, string(data), `primary = "claude-new"`)
	assert.Contains(t, string(data), `fallback = "openai"`)
}

func TestHandlePatchRepoConfig_CreatesNewSection(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	initial := `[ai]
primary = "claude"
`
	os.WriteFile(cfgPath, []byte(initial), 0o644)

	s := setupTestStore(t)
	srv := server.New(s, sse.NewBroker(), nil, "test-token")
	srv.SetConfigPath(cfgPath)
	srv.SetConfigFn(func() map[string]any { return map[string]any{"ok": true} })
	srv.SetReloadFn(func() error { return nil })

	body := `{"pr_draft":true}`
	encoded := url.PathEscape("org/new-repo")
	req := httptest.NewRequest(http.MethodPatch, "/config/repos/"+encoded, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Heimdallm-Token", "test-token")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	data, _ := os.ReadFile(cfgPath)
	content := string(data)
	assert.Contains(t, content, "org/new-repo")
	assert.Contains(t, content, "pr_draft")
}
```

- [ ] **Step 4: Run tests**

Run: `cd daemon && go test ./internal/server/ -run TestHandlePatchRepoConfig -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add daemon/internal/server/handlers.go daemon/internal/server/handlers_test.go
git commit -m "feat(server): add PATCH /config/repos/{repo} endpoint (#144)"
```

---

### Task 4: handleDeleteRepoField

**Files:**
- Modify: `daemon/internal/server/handlers.go`
- Modify: `daemon/internal/server/handlers_test.go`

- [ ] **Step 1: Implement handleDeleteRepoField**

Add to `daemon/internal/server/handlers.go`:

```go
func (srv *Server) handleDeleteRepoField(w http.ResponseWriter, r *http.Request) {
	if srv.configPath == "" {
		http.Error(w, `{"error":"DELETE not available — configPath not set"}`, http.StatusServiceUnavailable)
		return
	}
	repo, err := url.PathUnescape(chi.URLParam(r, "repo"))
	if err != nil || repo == "" {
		http.Error(w, `{"error":"invalid repo parameter"}`, http.StatusBadRequest)
		return
	}
	field := chi.URLParam(r, "*")
	if field == "" {
		http.Error(w, `{"error":"field path required"}`, http.StatusBadRequest)
		return
	}
	// Build the full path: ai → repos → <repo> → <field segments>
	segments := append([]string{"ai", "repos", repo}, strings.Split(field, "/")...)

	result, err := srv.patchTOML(func(m map[string]any) error {
		config.DeleteNestedKey(m, segments)
		// Idempotent: not finding the key is not an error
		return nil
	})
	if err != nil {
		slog.Error("DELETE /config/repos field failed", "repo", repo, "field", field, "err", err)
		if strings.Contains(err.Error(), "config:") {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
		} else {
			http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		}
		return
	}
	writeJSON(w, http.StatusOK, result)
}
```

Register in `buildRouter()`:

```go
r.Delete("/config/repos/{repo}/*", srv.handleDeleteRepoField)
```

- [ ] **Step 2: Verify daemon compiles**

Run: `cd daemon && go build ./...`
Expected: clean build

- [ ] **Step 3: Write integration tests**

Add to `daemon/internal/server/handlers_test.go`:

```go
func TestHandleDeleteRepoField_TopLevel(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	initial := `[ai]
primary = "claude"

[ai.repos."org/repo1"]
primary = "gemini"
pr_draft = true
`
	os.WriteFile(cfgPath, []byte(initial), 0o644)

	s := setupTestStore(t)
	srv := server.New(s, sse.NewBroker(), nil, "test-token")
	srv.SetConfigPath(cfgPath)
	srv.SetConfigFn(func() map[string]any { return map[string]any{"ok": true} })
	srv.SetReloadFn(func() error { return nil })

	encoded := url.PathEscape("org/repo1")
	req := httptest.NewRequest(http.MethodDelete, "/config/repos/"+encoded+"/pr_draft", nil)
	req.Header.Set("X-Heimdallm-Token", "test-token")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	data, _ := os.ReadFile(cfgPath)
	content := string(data)
	assert.NotContains(t, content, "pr_draft")
	assert.Contains(t, content, `primary = "gemini"`)
}

func TestHandleDeleteRepoField_NestedPath(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	initial := `[ai]
primary = "claude"

[ai.repos."org/repo1".issue_tracking]
develop_labels = ["ready"]
filter_mode = "exclusive"
`
	os.WriteFile(cfgPath, []byte(initial), 0o644)

	s := setupTestStore(t)
	srv := server.New(s, sse.NewBroker(), nil, "test-token")
	srv.SetConfigPath(cfgPath)
	srv.SetConfigFn(func() map[string]any { return map[string]any{"ok": true} })
	srv.SetReloadFn(func() error { return nil })

	encoded := url.PathEscape("org/repo1")
	req := httptest.NewRequest(http.MethodDelete, "/config/repos/"+encoded+"/issue_tracking/develop_labels", nil)
	req.Header.Set("X-Heimdallm-Token", "test-token")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	data, _ := os.ReadFile(cfgPath)
	content := string(data)
	assert.NotContains(t, content, "develop_labels")
	assert.Contains(t, content, "filter_mode")
}

func TestHandleDeleteRepoField_Idempotent(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	initial := `[ai]
primary = "claude"
`
	os.WriteFile(cfgPath, []byte(initial), 0o644)

	s := setupTestStore(t)
	srv := server.New(s, sse.NewBroker(), nil, "test-token")
	srv.SetConfigPath(cfgPath)
	srv.SetConfigFn(func() map[string]any { return map[string]any{"ok": true} })
	srv.SetReloadFn(func() error { return nil })

	encoded := url.PathEscape("org/nonexistent")
	req := httptest.NewRequest(http.MethodDelete, "/config/repos/"+encoded+"/whatever", nil)
	req.Header.Set("X-Heimdallm-Token", "test-token")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}
```

- [ ] **Step 4: Run tests**

Run: `cd daemon && go test ./internal/server/ -run TestHandleDeleteRepoField -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add daemon/internal/server/handlers.go daemon/internal/server/handlers_test.go
git commit -m "feat(server): add DELETE /config/repos/{repo}/{field} endpoint (#144)"
```

---

### Task 5: Wire configPath in main.go

**Files:**
- Modify: `daemon/cmd/heimdallm/main.go:180`

- [ ] **Step 1: Add SetConfigPath call after server creation**

In `daemon/cmd/heimdallm/main.go`, after line 180 (`srv := server.New(s, broker, p, apiToken)`), add:

```go
srv.SetConfigPath(cfgPath)
```

- [ ] **Step 2: Verify daemon compiles and starts**

Run: `cd daemon && go build -o bin/heimdallm ./cmd/heimdallm`
Expected: clean build

- [ ] **Step 3: Commit**

```bash
git add daemon/cmd/heimdallm/main.go
git commit -m "feat(main): wire configPath to server for PATCH/DELETE endpoints (#144)"
```

---

### Task 6: Flutter API Client Methods

**Files:**
- Modify: `flutter_app/lib/core/api/api_client.dart:170-180`

- [ ] **Step 1: Add patchConfig method**

Add after the existing `updateConfig` method (line 180) in `flutter_app/lib/core/api/api_client.dart`:

```dart
// ── Patch-based config (TOML merge) ─────────────────────────────────

/// Sends a partial config update. The daemon deep-merges the patch into
/// its TOML file. Only keys present in [patch] are updated; absent keys
/// are left untouched. Returns the full config after the merge.
Future<Map<String, dynamic>> patchConfig(Map<String, dynamic> patch) async {
  final resp = await _client.patch(
    _uri('/config'),
    headers: await _authHeaders(),
    body: jsonEncode(patch),
  );
  if (resp.statusCode != 200) {
    throw ApiException('PATCH /config failed: ${resp.statusCode} ${resp.body}');
  }
  return jsonDecode(resp.body) as Map<String, dynamic>;
}
```

- [ ] **Step 2: Add patchRepoConfig method**

```dart
/// Sends a partial per-repo override update. The daemon deep-merges the
/// patch into [ai.repos."<repo>"] in the TOML file. Returns the full
/// config after the merge.
Future<Map<String, dynamic>> patchRepoConfig(
    String repo, Map<String, dynamic> patch) async {
  final resp = await _client.patch(
    _uri('/config/repos/${Uri.encodeComponent(repo)}'),
    headers: await _authHeaders(),
    body: jsonEncode(patch),
  );
  if (resp.statusCode != 200) {
    throw ApiException(
        'PATCH /config/repos failed: ${resp.statusCode} ${resp.body}');
  }
  return jsonDecode(resp.body) as Map<String, dynamic>;
}
```

- [ ] **Step 3: Add deleteRepoField method**

```dart
/// Resets a per-repo override field back to the global default by
/// removing it from the TOML file. [fieldPath] uses "/" for nested
/// fields (e.g. "issue_tracking/develop_labels"). Returns the full
/// config after the deletion.
Future<Map<String, dynamic>> deleteRepoField(
    String repo, String fieldPath) async {
  final resp = await _client.delete(
    _uri('/config/repos/${Uri.encodeComponent(repo)}/$fieldPath'),
    headers: await _authHeaders(),
  );
  if (resp.statusCode != 200) {
    throw ApiException(
        'DELETE /config/repos field failed: ${resp.statusCode} ${resp.body}');
  }
  return jsonDecode(resp.body) as Map<String, dynamic>;
}
```

- [ ] **Step 4: Verify Flutter compiles**

Run: `cd flutter_app && flutter analyze --no-fatal-infos`
Expected: no errors

- [ ] **Step 5: Commit**

```bash
git add flutter_app/lib/core/api/api_client.dart
git commit -m "feat(flutter): add patchConfig, patchRepoConfig, deleteRepoField API methods (#144)"
```

---

### Task 7: Flutter ConfigNotifier Patch-Based Save

**Files:**
- Modify: `flutter_app/lib/features/config/config_providers.dart:21-79`

- [ ] **Step 1: Add updateFromServer method to ConfigNotifier**

Add to `ConfigNotifier` class in `flutter_app/lib/features/config/config_providers.dart`:

```dart
/// Replaces local state with fresh config from the daemon. Called after
/// PATCH/DELETE endpoints return the full config.
void updateFromServer(Map<String, dynamic> json) {
  state = AsyncValue.data(AppConfig.fromJson(json));
}
```

- [ ] **Step 2: Replace save() with patch-based save**

Replace the existing `save()` method (lines 36-41):

```dart
/// Save global config changes by computing the diff and sending only
/// changed fields to the daemon via PATCH.
Future<void> save(AppConfig updated) async {
  final current = state.valueOrNull;
  if (current == null) return;
  final api = ref.read(apiClientProvider);
  final diff = _computeGlobalDiff(current, updated);
  if (diff.isEmpty) {
    state = AsyncValue.data(updated);
    return;
  }
  final freshJson = await api.patchConfig(diff);
  state = AsyncValue.data(AppConfig.fromJson(freshJson));
}
```

- [ ] **Step 3: Add _computeGlobalDiff function**

Add as a top-level function in the same file:

```dart
/// Computes a nested diff between two AppConfig instances, returning only
/// the fields that changed in the structure expected by PATCH /config
/// (mirrors TOML layout).
Map<String, dynamic> _computeGlobalDiff(AppConfig old, AppConfig updated) {
  final diff = <String, dynamic>{};
  final aiDiff = <String, dynamic>{};
  final githubDiff = <String, dynamic>{};
  final retentionDiff = <String, dynamic>{};

  // AI section
  if (old.aiPrimary != updated.aiPrimary) aiDiff['primary'] = updated.aiPrimary;
  if (old.aiFallback != updated.aiFallback) aiDiff['fallback'] = updated.aiFallback;
  if (old.reviewMode != updated.reviewMode) aiDiff['review_mode'] = updated.reviewMode;

  // PR metadata
  final prMeta = <String, dynamic>{};
  if (_listsDiffer(old.globalPRReviewers, updated.globalPRReviewers)) {
    prMeta['reviewers'] = updated.globalPRReviewers;
  }
  if (_listsDiffer(old.globalPRLabels, updated.globalPRLabels)) {
    prMeta['labels'] = updated.globalPRLabels;
  }
  if (prMeta.isNotEmpty) aiDiff['pr_metadata'] = prMeta;

  if (aiDiff.isNotEmpty) diff['ai'] = aiDiff;

  // GitHub section
  if (old.pollInterval != updated.pollInterval) {
    githubDiff['poll_interval'] = updated.pollInterval;
  }
  // Repositories list (monitoring changes)
  if (_listsDiffer(old.repositories, updated.repositories)) {
    githubDiff['repositories'] = updated.repositories;
  }
  final oldNonMon = old.repoConfigs.entries
      .where((e) => !e.value.isMonitored).map((e) => e.key).toList()..sort();
  final newNonMon = updated.repoConfigs.entries
      .where((e) => !e.value.isMonitored).map((e) => e.key).toList()..sort();
  if (_listsDiffer(oldNonMon, newNonMon)) {
    githubDiff['non_monitored'] = newNonMon;
  }

  // Issue tracking (global)
  final itDiff = _computeIssueTrackingDiff(old.issueTracking, updated.issueTracking);
  if (itDiff.isNotEmpty) githubDiff['issue_tracking'] = itDiff;

  if (githubDiff.isNotEmpty) diff['github'] = githubDiff;

  // Retention
  if (old.retentionDays != updated.retentionDays) {
    retentionDiff['max_days'] = updated.retentionDays;
  }
  if (retentionDiff.isNotEmpty) diff['retention'] = retentionDiff;

  return diff;
}

Map<String, dynamic> _computeIssueTrackingDiff(
    IssueTrackingConfig old, IssueTrackingConfig updated) {
  final diff = <String, dynamic>{};
  if (old.enabled != updated.enabled) diff['enabled'] = updated.enabled;
  if (old.filterMode != updated.filterMode) diff['filter_mode'] = updated.filterMode;
  if (old.defaultAction != updated.defaultAction) diff['default_action'] = updated.defaultAction;
  if (_listsDiffer(old.developLabels, updated.developLabels)) diff['develop_labels'] = updated.developLabels;
  if (_listsDiffer(old.reviewOnlyLabels, updated.reviewOnlyLabels)) diff['review_only_labels'] = updated.reviewOnlyLabels;
  if (_listsDiffer(old.skipLabels, updated.skipLabels)) diff['skip_labels'] = updated.skipLabels;
  if (_listsDiffer(old.organizations, updated.organizations)) diff['organizations'] = updated.organizations;
  if (_listsDiffer(old.assignees, updated.assignees)) diff['assignees'] = updated.assignees;
  return diff;
}

bool _listsDiffer(List<String> a, List<String> b) {
  if (a.length != b.length) return true;
  for (var i = 0; i < a.length; i++) {
    if (a[i] != b[i]) return true;
  }
  return false;
}
```

- [ ] **Step 4: Verify Flutter compiles**

Run: `cd flutter_app && flutter analyze --no-fatal-infos`
Expected: no errors

- [ ] **Step 5: Commit**

```bash
git add flutter_app/lib/features/config/config_providers.dart
git commit -m "feat(flutter): ConfigNotifier save() sends patches instead of full TOML (#144)"
```

---

### Task 8: Flutter Repo Detail Screen — Diff-Based Save

**Files:**
- Modify: `flutter_app/lib/features/repositories/repo_detail_screen.dart:24-80`

- [ ] **Step 1: Add _previousConfig field and _computeRepoDiff function**

Add a `_previousConfig` field to `_RepoDetailScreenState` (after `_config`):

```dart
RepoConfig _previousConfig = const RepoConfig();
```

Update `_initFrom` to also set `_previousConfig`:

```dart
void _initFrom(AppConfig config) {
  if (_initialized) return;
  _initialized = true;
  _config = config.repoConfigs[widget.repoName] ?? const RepoConfig();
  _previousConfig = _config;
  _loadRepoMeta();
}
```

Add `_computeRepoDiff` as a method on the state class:

```dart
/// Computes the diff between the previous and current RepoConfig, returning
/// only the fields that changed using TOML key names.
Map<String, dynamic> _computeRepoDiff(RepoConfig old, RepoConfig updated) {
  final diff = <String, dynamic>{};
  if (old.aiPrimary != updated.aiPrimary) diff['primary'] = updated.aiPrimary ?? '';
  if (old.aiFallback != updated.aiFallback) diff['fallback'] = updated.aiFallback ?? '';
  if (old.reviewMode != updated.reviewMode) diff['review_mode'] = updated.reviewMode ?? '';
  if (old.promptId != updated.promptId) diff['prompt'] = updated.promptId ?? '';
  if (old.localDir != updated.localDir) diff['local_dir'] = updated.localDir ?? '';
  if (old.prAssignee != updated.prAssignee) diff['pr_assignee'] = updated.prAssignee ?? '';
  if (old.prDraft != updated.prDraft && updated.prDraft != null) diff['pr_draft'] = updated.prDraft!;
  if (old.developPromptId != updated.developPromptId) diff['implement_prompt'] = updated.developPromptId ?? '';

  // List fields
  if (!_listsEqual(old.prReviewers, updated.prReviewers)) {
    diff['pr_reviewers'] = updated.prReviewers ?? <String>[];
  }
  if (!_listsEqual(old.prLabels, updated.prLabels)) {
    diff['pr_labels'] = updated.prLabels ?? <String>[];
  }

  // Issue tracking sub-fields
  final itDiff = <String, dynamic>{};
  if (old.itEnabled != updated.itEnabled && updated.itEnabled != null) {
    itDiff['enabled'] = updated.itEnabled!;
  }
  if (old.devEnabled != updated.devEnabled && updated.devEnabled != null) {
    itDiff['develop_enabled'] = updated.devEnabled!;
  }
  if (old.issueFilterMode != updated.issueFilterMode) {
    itDiff['filter_mode'] = updated.issueFilterMode ?? '';
  }
  if (old.issueDefaultAction != updated.issueDefaultAction) {
    itDiff['default_action'] = updated.issueDefaultAction ?? '';
  }
  if (old.issuePromptId != updated.issuePromptId) {
    itDiff['issue_prompt'] = updated.issuePromptId ?? '';
  }
  if (!_listsEqual(old.reviewOnlyLabels, updated.reviewOnlyLabels)) {
    itDiff['review_only_labels'] = updated.reviewOnlyLabels ?? <String>[];
  }
  if (!_listsEqual(old.skipLabels, updated.skipLabels)) {
    itDiff['skip_labels'] = updated.skipLabels ?? <String>[];
  }
  if (!_listsEqual(old.developLabels, updated.developLabels)) {
    itDiff['develop_labels'] = updated.developLabels ?? <String>[];
  }
  if (!_listsEqual(old.issueOrganizations, updated.issueOrganizations)) {
    itDiff['organizations'] = updated.issueOrganizations ?? <String>[];
  }
  if (!_listsEqual(old.issueAssignees, updated.issueAssignees)) {
    itDiff['assignees'] = updated.issueAssignees ?? <String>[];
  }

  if (itDiff.isNotEmpty) diff['issue_tracking'] = itDiff;
  return diff;
}

bool _listsEqual(List<String>? a, List<String>? b) {
  if (a == null && b == null) return true;
  if (a == null || b == null) return false;
  if (a.length != b.length) return false;
  for (var i = 0; i < a.length; i++) {
    if (a[i] != b[i]) return false;
  }
  return true;
}
```

- [ ] **Step 2: Replace _update and _autoSave**

Replace the existing `_update` and `_autoSave` methods (lines 62-80):

```dart
void _update(RepoConfig updated) {
  final previous = _config;
  setState(() => _config = updated);
  _debounce?.cancel();
  _debounce = Timer(const Duration(milliseconds: 800), () => _autoSave(previous));
}

Future<void> _autoSave(RepoConfig previous) async {
  final api = ref.read(apiClientProvider);
  try {
    // 1. Patch repo override fields
    final repoDiff = _computeRepoDiff(previous, _config);
    Map<String, dynamic>? lastResponse;
    if (repoDiff.isNotEmpty) {
      lastResponse = await api.patchRepoConfig(widget.repoName, repoDiff);
    }

    // 2. If monitoring status changed, update the global repositories list
    final monitoringChanged = previous.isMonitored != _config.isMonitored;
    if (monitoringChanged) {
      final current = ref.read(configNotifierProvider).valueOrNull;
      if (current != null) {
        final updatedRepos = Map<String, RepoConfig>.from(current.repoConfigs);
        updatedRepos[widget.repoName] = _config;
        final monitored = updatedRepos.entries
            .where((e) => e.value.isMonitored)
            .map((e) => e.key)
            .toList()..sort();
        final nonMonitored = updatedRepos.entries
            .where((e) => !e.value.isMonitored)
            .map((e) => e.key)
            .toList()..sort();
        lastResponse = await api.patchConfig({
          'github': {
            'repositories': monitored,
            'non_monitored': nonMonitored,
          },
        });
      }
    }

    // 3. Update local state from daemon response
    if (lastResponse != null) {
      ref.read(configNotifierProvider.notifier).updateFromServer(lastResponse);
    }
    _previousConfig = _config;
    if (mounted) showToast(context, 'Saved');
  } catch (e) {
    if (mounted) showToast(context, 'Error: $e', isError: true);
  }
}
```

- [ ] **Step 3: Verify Flutter compiles**

Run: `cd flutter_app && flutter analyze --no-fatal-infos`
Expected: no errors

- [ ] **Step 4: Commit**

```bash
git add flutter_app/lib/features/repositories/repo_detail_screen.dart
git commit -m "feat(flutter): repo detail autoSave sends diffs via PATCH (#144)"
```

---

### Task 9: Flutter Override Field — DELETE on Reset

**Files:**
- Modify: `flutter_app/lib/shared/widgets/override_field.dart`
- Modify: `flutter_app/lib/features/repositories/repo_detail_screen.dart`

The override fields currently call `onChanged(null)` on reset, which sets the field to null locally and triggers the debounced `_autoSave`. With the new diff-based save, setting a field to null means "send empty string to PATCH" which is not the same as "delete the override from TOML".

The correct approach: reset needs to call the DELETE endpoint directly, then refresh state from the daemon response.

- [ ] **Step 1: Add onReset callback to OverrideTextField**

Modify `OverrideTextField` in `flutter_app/lib/shared/widgets/override_field.dart` to accept an optional `onReset` callback:

```dart
class OverrideTextField extends StatefulWidget {
  final String label;
  final String? helper;
  final String globalValue;
  final String? overrideValue;
  final ValueChanged<String?> onChanged;
  final VoidCallback? onReset; // ← new

  const OverrideTextField({
    super.key,
    required this.label,
    this.helper,
    required this.globalValue,
    required this.overrideValue,
    required this.onChanged,
    this.onReset, // ← new
  });
```

Update `_reset()` in `_OverrideTextFieldState`:

```dart
void _reset() {
  _ctrl.text = widget.globalValue;
  if (widget.onReset != null) {
    widget.onReset!();
  } else {
    widget.onChanged(null);
  }
}
```

- [ ] **Step 2: Add onReset callback to OverrideDropdown**

Modify `OverrideDropdown`:

```dart
class OverrideDropdown extends StatelessWidget {
  final String label;
  final String globalValue;
  final String? overrideValue;
  final List<String> options;
  final ValueChanged<String?> onChanged;
  final VoidCallback? onReset; // ← new

  const OverrideDropdown({
    super.key,
    required this.label,
    required this.globalValue,
    required this.overrideValue,
    required this.options,
    required this.onChanged,
    this.onReset, // ← new
  });
```

Update the reset `GestureDetector.onTap`:

```dart
GestureDetector(
  onTap: () {
    if (onReset != null) {
      onReset!();
    } else {
      onChanged(null);
    }
  },
```

- [ ] **Step 3: Wire onReset in repo_detail_screen.dart**

Add a `_resetField` method to `_RepoDetailScreenState`:

```dart
Future<void> _resetField(String fieldPath) async {
  final api = ref.read(apiClientProvider);
  try {
    final freshJson = await api.deleteRepoField(widget.repoName, fieldPath);
    ref.read(configNotifierProvider.notifier).updateFromServer(freshJson);
    // Update local state from refreshed config
    final freshConfig = AppConfig.fromJson(freshJson);
    setState(() {
      _config = freshConfig.repoConfigs[widget.repoName] ?? const RepoConfig();
      _previousConfig = _config;
    });
    if (mounted) showToast(context, 'Reset to global');
  } catch (e) {
    if (mounted) showToast(context, 'Error: $e', isError: true);
  }
}
```

Then, wherever `OverrideTextField` or `OverrideDropdown` is used in the build method, add the `onReset` parameter. For example, for the AI Primary dropdown:

```dart
OverrideDropdown(
  label: 'AI Primary',
  globalValue: config.aiPrimary,
  overrideValue: _config.aiPrimary,
  options: ['claude', 'gemini', 'openai'],
  onChanged: (v) => _update(_config.copyWith(aiPrimary: v)),
  onReset: () => _resetField('primary'), // ← new
),
```

Apply the same pattern for every override field, mapping each to its TOML key:
- AI Primary → `'primary'`
- AI Fallback → `'fallback'`
- Review Mode → `'review_mode'`
- Prompt → `'prompt'`
- PR Assignee → `'pr_assignee'`
- PR Draft → `'pr_draft'`
- PR Reviewers → `'pr_reviewers'`
- PR Labels → `'pr_labels'`
- Develop Prompt → `'implement_prompt'`
- Issue Filter Mode → `'issue_tracking/filter_mode'`
- Issue Default Action → `'issue_tracking/default_action'`
- Issue Prompt → `'issue_tracking/issue_prompt'`
- Develop Labels → `'issue_tracking/develop_labels'`
- Review Only Labels → `'issue_tracking/review_only_labels'`
- Skip Labels → `'issue_tracking/skip_labels'`
- Issue Organizations → `'issue_tracking/organizations'`
- Issue Assignees → `'issue_tracking/assignees'`

- [ ] **Step 4: Verify Flutter compiles**

Run: `cd flutter_app && flutter analyze --no-fatal-infos`
Expected: no errors

- [ ] **Step 5: Commit**

```bash
git add flutter_app/lib/shared/widgets/override_field.dart flutter_app/lib/features/repositories/repo_detail_screen.dart
git commit -m "feat(flutter): override field reset calls DELETE endpoint (#144)"
```

---

### Task 10: End-to-End Verification

**Files:** None (testing only)

- [ ] **Step 1: Run all daemon tests**

Run: `cd daemon && go test ./... -count=1`
Expected: all PASS

- [ ] **Step 2: Run Flutter analysis**

Run: `cd flutter_app && flutter analyze --no-fatal-infos`
Expected: no errors

- [ ] **Step 3: Run Flutter tests**

Run: `cd flutter_app && flutter test`
Expected: all PASS

- [ ] **Step 4: Build daemon binary**

Run: `cd daemon && go build -o bin/heimdallm ./cmd/heimdallm`
Expected: clean build, binary created

- [ ] **Step 5: Final commit with all remaining changes (if any)**

```bash
git status
# If there are uncommitted changes from test adjustments:
git add -A && git commit -m "test: end-to-end verification for patch-based config saves (#144)"
```

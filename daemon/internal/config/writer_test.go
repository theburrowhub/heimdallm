package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// ── DeepMerge ─────────────────────────────────────────────────────────────────

func TestDeepMerge_ScalarReplace(t *testing.T) {
	base := map[string]any{"a": "old", "b": 1}
	patch := map[string]any{"a": "new"}

	got := DeepMerge(base, patch)

	if got["a"] != "new" {
		t.Errorf("a = %v, want %q", got["a"], "new")
	}
	if got["b"] != 1 {
		t.Errorf("b = %v, want 1", got["b"])
	}
}

func TestDeepMerge_NestedMerge(t *testing.T) {
	base := map[string]any{
		"server": map[string]any{"port": 7842, "bind_addr": "127.0.0.1"},
	}
	patch := map[string]any{
		"server": map[string]any{"port": 8080},
	}

	got := DeepMerge(base, patch)

	server, ok := got["server"].(map[string]any)
	if !ok {
		t.Fatal("server is not map[string]any")
	}
	if server["port"] != 8080 {
		t.Errorf("server.port = %v, want 8080", server["port"])
	}
	if server["bind_addr"] != "127.0.0.1" {
		t.Errorf("server.bind_addr = %v, want %q", server["bind_addr"], "127.0.0.1")
	}
}

func TestDeepMerge_AddNewKey(t *testing.T) {
	base := map[string]any{"a": 1}
	patch := map[string]any{"b": 2}

	got := DeepMerge(base, patch)

	if got["a"] != 1 {
		t.Errorf("a = %v, want 1", got["a"])
	}
	if got["b"] != 2 {
		t.Errorf("b = %v, want 2", got["b"])
	}
}

func TestDeepMerge_EmptyPatch(t *testing.T) {
	base := map[string]any{"a": "hello", "b": 42}
	patch := map[string]any{}

	got := DeepMerge(base, patch)

	if !reflect.DeepEqual(got, base) {
		t.Errorf("got %v, want %v", got, base)
	}
}

func TestDeepMerge_ReplaceArrayEntirely(t *testing.T) {
	base := map[string]any{"repos": []any{"a", "b", "c"}}
	patch := map[string]any{"repos": []any{"x"}}

	got := DeepMerge(base, patch)

	repos, ok := got["repos"].([]any)
	if !ok {
		t.Fatal("repos is not []any")
	}
	if len(repos) != 1 || repos[0] != "x" {
		t.Errorf("repos = %v, want [x]", repos)
	}
}

func TestDeepMerge_PatchMapOverScalar(t *testing.T) {
	// If base has a scalar and patch has a map for the same key, patch wins.
	base := map[string]any{"key": "scalar"}
	patch := map[string]any{"key": map[string]any{"nested": true}}

	got := DeepMerge(base, patch)

	nested, ok := got["key"].(map[string]any)
	if !ok {
		t.Fatalf("key = %v (%T), want map[string]any", got["key"], got["key"])
	}
	if nested["nested"] != true {
		t.Errorf("key.nested = %v, want true", nested["nested"])
	}
}

func TestDeepMerge_DoesNotMutateBase(t *testing.T) {
	base := map[string]any{
		"a":      "original",
		"nested": map[string]any{"x": 1},
	}
	patch := map[string]any{
		"a":      "changed",
		"nested": map[string]any{"x": 99},
	}

	_ = DeepMerge(base, patch)

	if base["a"] != "original" {
		t.Errorf("base[a] mutated: %v", base["a"])
	}
	nested := base["nested"].(map[string]any)
	if nested["x"] != 1 {
		t.Errorf("base.nested.x mutated: %v", nested["x"])
	}
}

// ── ContainsNull ──────────────────────────────────────────────────────────────

func TestContainsNull_NoNulls(t *testing.T) {
	m := map[string]any{
		"a": "value",
		"b": map[string]any{"c": 42},
	}
	if err := ContainsNull(m); err != nil {
		t.Errorf("ContainsNull() = %v, want nil", err)
	}
}

func TestContainsNull_TopLevelNull(t *testing.T) {
	m := map[string]any{
		"a": "value",
		"b": nil,
	}
	if err := ContainsNull(m); err == nil {
		t.Error("ContainsNull() = nil, want error for top-level null")
	}
}

func TestContainsNull_NestedNull(t *testing.T) {
	m := map[string]any{
		"outer": map[string]any{
			"inner": nil,
		},
	}
	if err := ContainsNull(m); err == nil {
		t.Error("ContainsNull() = nil, want error for nested null")
	}
}

func TestContainsNull_NullInArrayIsOK(t *testing.T) {
	// Arrays are not checked — only map values.
	m := map[string]any{
		"items": []any{"a", nil, "c"},
	}
	if err := ContainsNull(m); err != nil {
		t.Errorf("ContainsNull() = %v, want nil (nulls in arrays are ignored)", err)
	}
}

// ── DeleteNestedKey ───────────────────────────────────────────────────────────

func TestDeleteNestedKey_TopLevel(t *testing.T) {
	m := map[string]any{"a": 1, "b": 2}
	found := DeleteNestedKey(m, []string{"a"})

	if !found {
		t.Error("DeleteNestedKey() = false, want true")
	}
	if _, exists := m["a"]; exists {
		t.Error("key 'a' still present after deletion")
	}
	if m["b"] != 2 {
		t.Error("key 'b' unexpectedly modified")
	}
}

func TestDeleteNestedKey_Nested(t *testing.T) {
	m := map[string]any{
		"server": map[string]any{"port": 7842, "bind_addr": "127.0.0.1"},
	}
	found := DeleteNestedKey(m, []string{"server", "port"})

	if !found {
		t.Error("DeleteNestedKey() = false, want true")
	}
	server := m["server"].(map[string]any)
	if _, exists := server["port"]; exists {
		t.Error("server.port still present after deletion")
	}
	if server["bind_addr"] != "127.0.0.1" {
		t.Error("server.bind_addr unexpectedly removed")
	}
	// Parent should NOT be cleaned up since it still has bind_addr.
	if _, exists := m["server"]; !exists {
		t.Error("server map unexpectedly deleted (it still had bind_addr)")
	}
}

func TestDeleteNestedKey_CleansEmptyParents(t *testing.T) {
	m := map[string]any{
		"parent": map[string]any{
			"child": "value",
		},
	}
	found := DeleteNestedKey(m, []string{"parent", "child"})

	if !found {
		t.Error("DeleteNestedKey() = false, want true")
	}
	// parent should have been cleaned up because it became empty.
	if _, exists := m["parent"]; exists {
		t.Error("empty parent map was not cleaned up")
	}
}

func TestDeleteNestedKey_NonExistent(t *testing.T) {
	m := map[string]any{"a": 1}
	found := DeleteNestedKey(m, []string{"z"})

	if found {
		t.Error("DeleteNestedKey() = true, want false for non-existent key")
	}
	if m["a"] != 1 {
		t.Error("existing key 'a' was modified")
	}
}

func TestDeleteNestedKey_PartialPath(t *testing.T) {
	m := map[string]any{
		"a": map[string]any{"b": 1},
	}
	// Path goes deeper than the map structure allows.
	found := DeleteNestedKey(m, []string{"a", "b", "c"})

	if found {
		t.Error("DeleteNestedKey() = true, want false for non-existent nested path")
	}
}

// ── NormalizeNumbers ──────────────────────────────────────────────────────────

func TestNormalizeNumbers_FloatToInt(t *testing.T) {
	m := map[string]any{"port": float64(7842)}
	NormalizeNumbers(m)

	if m["port"] != int64(7842) {
		t.Errorf("port = %v (%T), want int64(7842)", m["port"], m["port"])
	}
}

func TestNormalizeNumbers_KeepsFractional(t *testing.T) {
	m := map[string]any{"ratio": float64(3.14)}
	NormalizeNumbers(m)

	if m["ratio"] != float64(3.14) {
		t.Errorf("ratio = %v (%T), want float64(3.14)", m["ratio"], m["ratio"])
	}
}

func TestNormalizeNumbers_Nested(t *testing.T) {
	m := map[string]any{
		"server": map[string]any{"port": float64(8080)},
	}
	NormalizeNumbers(m)

	server := m["server"].(map[string]any)
	if server["port"] != int64(8080) {
		t.Errorf("server.port = %v (%T), want int64(8080)", server["port"], server["port"])
	}
}

func TestNormalizeNumbers_InArray(t *testing.T) {
	m := map[string]any{
		"values": []any{float64(1), float64(2.5), float64(3)},
	}
	NormalizeNumbers(m)

	values := m["values"].([]any)
	if values[0] != int64(1) {
		t.Errorf("values[0] = %v (%T), want int64(1)", values[0], values[0])
	}
	if values[1] != float64(2.5) {
		t.Errorf("values[1] = %v (%T), want float64(2.5)", values[1], values[1])
	}
	if values[2] != int64(3) {
		t.Errorf("values[2] = %v (%T), want int64(3)", values[2], values[2])
	}
}

// ── ReadTOMLMap ───────────────────────────────────────────────────────────────

func TestReadTOMLMap_ValidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := `[server]
port = 7842
bind_addr = "127.0.0.1"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	m, err := ReadTOMLMap(path)
	if err != nil {
		t.Fatalf("ReadTOMLMap() error: %v", err)
	}

	server, ok := m["server"].(map[string]any)
	if !ok {
		t.Fatal("server is not map[string]any")
	}
	if server["bind_addr"] != "127.0.0.1" {
		t.Errorf("server.bind_addr = %v, want %q", server["bind_addr"], "127.0.0.1")
	}
}

func TestReadTOMLMap_NonExistentFile(t *testing.T) {
	_, err := ReadTOMLMap("/nonexistent/path/config.toml")
	if err == nil {
		t.Error("ReadTOMLMap() = nil, want error for missing file")
	}
}

// ── ValidateMap ───────────────────────────────────────────────────────────────

func TestValidateMap_ValidConfig(t *testing.T) {
	m := map[string]any{
		"ai": map[string]any{
			"primary": "claude",
		},
	}
	if err := ValidateMap(m); err != nil {
		t.Errorf("ValidateMap() = %v, want nil", err)
	}
}

func TestValidateMap_MissingRequired(t *testing.T) {
	// Empty map — ai.primary is required.
	m := map[string]any{}
	if err := ValidateMap(m); err == nil {
		t.Error("ValidateMap() = nil, want error for missing ai.primary")
	}
}

// ── AtomicWriteTOML ───────────────────────────────────────────────────────────

func TestAtomicWriteTOML_WritesAndReads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.toml")

	m := map[string]any{
		"server": map[string]any{
			"port":      int64(9000),
			"bind_addr": "0.0.0.0",
		},
	}

	if err := AtomicWriteTOML(path, m); err != nil {
		t.Fatalf("AtomicWriteTOML() error: %v", err)
	}

	// Verify file exists and can be read back.
	got, err := ReadTOMLMap(path)
	if err != nil {
		t.Fatalf("ReadTOMLMap() error: %v", err)
	}

	server, ok := got["server"].(map[string]any)
	if !ok {
		t.Fatal("server is not map[string]any after round-trip")
	}
	if server["bind_addr"] != "0.0.0.0" {
		t.Errorf("server.bind_addr = %v, want %q", server["bind_addr"], "0.0.0.0")
	}
}

func TestAtomicWriteTOML_CreatesDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "nested", "config.toml")

	m := map[string]any{"key": "value"}
	if err := AtomicWriteTOML(path, m); err != nil {
		t.Fatalf("AtomicWriteTOML() error creating nested dirs: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Errorf("file does not exist after write: %v", err)
	}
}

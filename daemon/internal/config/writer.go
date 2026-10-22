package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// ValidationError wraps config validation failures. Handlers use errors.As
// to distinguish user errors (400) from infrastructure errors (500).
type ValidationError struct {
	Err error
}

func (e *ValidationError) Error() string { return e.Err.Error() }
func (e *ValidationError) Unwrap() error { return e.Err }

// DeepMerge recursively merges patch into a copy of base and returns the
// result. Only keys present in patch are updated. When both base and patch
// have a map[string]any for the same key, the merge recurses. Otherwise the
// patch value replaces the base value.
//
// The top-level map is a fresh allocation. Nested maps for keys that appear
// in patch are also fresh (via recursion). Nested maps for keys absent from
// patch share backing storage with base — callers must not mutate those.
func DeepMerge(base, patch map[string]any) map[string]any {
	out := make(map[string]any, len(base))
	for k, v := range base {
		out[k] = v
	}
	for k, pv := range patch {
		bv, exists := out[k]
		if exists {
			bMap, bIsMap := bv.(map[string]any)
			pMap, pIsMap := pv.(map[string]any)
			if bIsMap && pIsMap {
				out[k] = DeepMerge(bMap, pMap)
				continue
			}
		}
		out[k] = pv
	}
	return out
}

// ContainsNull walks m recursively and returns an error if any map value is
// nil. Array elements are not checked — only map values at any nesting depth.
func ContainsNull(m map[string]any) error {
	for k, v := range m {
		if v == nil {
			return fmt.Errorf("config: key %q has null value", k)
		}
		if nested, ok := v.(map[string]any); ok {
			if err := ContainsNull(nested); err != nil {
				return fmt.Errorf("%s.%w", k, err)
			}
		}
	}
	return nil
}

// DeleteNestedKey deletes the key at the given path in m. It cleans up parent
// maps that become empty as a result. Returns true if the key was found and
// deleted, false if the path does not exist.
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
	// Recurse into the next level.
	child, ok := m[path[0]].(map[string]any)
	if !ok {
		return false
	}
	found := DeleteNestedKey(child, path[1:])
	if found && len(child) == 0 {
		delete(m, path[0])
	}
	return found
}

// NormalizeNumbers converts float64 map values that represent whole numbers to
// int64. This is needed because JSON decodes all numbers as float64, but TOML
// distinguishes between integer and float types. The function also walks nested
// maps and slices.
func NormalizeNumbers(m map[string]any) {
	for k, v := range m {
		m[k] = normalizeValue(v)
	}
}

func normalizeValue(v any) any {
	switch val := v.(type) {
	case float64:
		if val == float64(int64(val)) {
			return int64(val)
		}
		return val
	case map[string]any:
		NormalizeNumbers(val)
		return val
	case []any:
		for i, elem := range val {
			val[i] = normalizeValue(elem)
		}
		return val
	default:
		return v
	}
}

// ReadTOMLMap reads a TOML file into a generic map[string]any.
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

// ValidateMap validates a generic config map by round-tripping it through the
// Config struct: it marshals the map to TOML bytes, unmarshals into a Config,
// applies defaults, then calls Validate(). Returns nil on success.
// Validation and structural errors are wrapped in *ValidationError so callers
// can use errors.As to distinguish 400 (bad input) from 500 (infra failure).
func ValidateMap(m map[string]any) error {
	return ValidateMapAt(m, "")
}

// ValidateMapAt is ValidateMap for a map that will be written to path: a
// custom agent's schema_path is resolved next to it, exactly as Load will
// resolve it after the write. The map is projected the way Load projects a
// file, so a custom agent's option values are validated against its schema.
func ValidateMapAt(m map[string]any, path string) error {
	known, err := projectKnownConfigMap(m)
	if err != nil {
		return &ValidationError{Err: fmt.Errorf("config: invalid config structure: %w", err)}
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(known); err != nil {
		return fmt.Errorf("config: encode map to TOML: %w", err)
	}
	var cfg Config
	if err := toml.Unmarshal(buf.Bytes(), &cfg); err != nil {
		return &ValidationError{Err: fmt.Errorf("config: invalid config structure: %w", err)}
	}
	configDir := ""
	if path != "" {
		configDir = filepath.Dir(path)
	}
	if err := cfg.resolveCustomAgentSchemas(configDir); err != nil {
		return &ValidationError{Err: err}
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return &ValidationError{Err: err}
	}
	return nil
}

// AtomicWriteTOML writes m as TOML to path using a temp file + os.Rename for
// crash safety. The directory is created if it does not exist.
func AtomicWriteTOML(path string, m map[string]any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("config: create dir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.toml.tmp")
	if err != nil {
		return fmt.Errorf("config: create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		// Best-effort cleanup if we did not successfully rename.
		_ = os.Remove(tmpPath)
	}()

	if err := toml.NewEncoder(tmp).Encode(m); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("config: encode TOML to temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config: close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("config: rename temp file to %s: %w", path, err)
	}
	return nil
}

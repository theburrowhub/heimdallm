package config_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/heimdallm/daemon/internal/config"
)

// writeConfigFile writes contents to a temp config path and returns it.
// Each test gets its own t.TempDir so the AtomicWriteTOML rename never
// clobbers another fixture.
func writeConfigFile(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func readBack(t *testing.T, path string) map[string]any {
	t.Helper()
	m, err := config.ReadTOMLMap(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	return m
}

func TestRenameRepoInTOML_RenamesAIRepoKey(t *testing.T) {
	path := writeConfigFile(t, `
[ai]

[ai.repos."acme/old"]
issue_prompt = "issue-deep"
implement_prompt = "impl-fast"
`)

	if err := config.RenameRepoInTOML(path, "acme/old", "acme/new"); err != nil {
		t.Fatalf("rename: %v", err)
	}

	m := readBack(t, path)
	ai := m["ai"].(map[string]any)
	repos := ai["repos"].(map[string]any)
	if _, has := repos["acme/old"]; has {
		t.Error("old key still present")
	}
	newBlock, ok := repos["acme/new"].(map[string]any)
	if !ok {
		t.Fatalf("new key missing or wrong type: %T", repos["acme/new"])
	}
	if newBlock["issue_prompt"] != "issue-deep" || newBlock["implement_prompt"] != "impl-fast" {
		t.Errorf("values not preserved across rename: %+v", newBlock)
	}
}

func TestRenameRepoInTOML_UpdatesRepositoriesList(t *testing.T) {
	path := writeConfigFile(t, `
[github]
repositories = ["foo/bar", "acme/old", "x/y"]
`)

	if err := config.RenameRepoInTOML(path, "acme/old", "acme/new"); err != nil {
		t.Fatalf("rename: %v", err)
	}

	m := readBack(t, path)
	gh := m["github"].(map[string]any)
	got := toStringSlice(gh["repositories"])
	want := []string{"foo/bar", "acme/new", "x/y"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("repositories = %v, want %v (order preserved)", got, want)
	}
}

func TestRenameRepoInTOML_UpdatesNonMonitoredList(t *testing.T) {
	path := writeConfigFile(t, `
[github]
non_monitored = ["a/b", "acme/old", "c/d"]
`)

	if err := config.RenameRepoInTOML(path, "acme/old", "acme/new"); err != nil {
		t.Fatalf("rename: %v", err)
	}

	got := toStringSlice(readBack(t, path)["github"].(map[string]any)["non_monitored"])
	want := []string{"a/b", "acme/new", "c/d"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("non_monitored = %v, want %v", got, want)
	}
}

func TestRenameRepoInTOML_PreservesUnrelatedTopLevelKeys(t *testing.T) {
	path := writeConfigFile(t, `
[github]
repositories = ["acme/old"]

[ai.repos."acme/old"]
issue_prompt = "p"

[operator_custom]
totally_not_modeled = "value-that-must-survive"
nested_count = 7
`)

	if err := config.RenameRepoInTOML(path, "acme/old", "acme/new"); err != nil {
		t.Fatalf("rename: %v", err)
	}

	m := readBack(t, path)
	custom, ok := m["operator_custom"].(map[string]any)
	if !ok {
		t.Fatalf("operator_custom dropped or wrong type: %T", m["operator_custom"])
	}
	if custom["totally_not_modeled"] != "value-that-must-survive" {
		t.Errorf("custom string lost: %+v", custom)
	}
	// TOML integers come back as int64 via the BurntSushi decoder.
	if n, _ := custom["nested_count"].(int64); n != 7 {
		t.Errorf("custom int lost: %+v", custom)
	}
}

func TestRenameRepoInTOML_NoOpWhenOldAbsent(t *testing.T) {
	path := writeConfigFile(t, `
[github]
repositories = ["foo/bar"]
`)

	if err := config.RenameRepoInTOML(path, "acme/old", "acme/new"); err != nil {
		t.Fatalf("rename: %v", err)
	}

	got := toStringSlice(readBack(t, path)["github"].(map[string]any)["repositories"])
	if !reflect.DeepEqual(got, []string{"foo/bar"}) {
		t.Errorf("repositories perturbed by no-op: %v", got)
	}
}

func TestRenameRepoInTOML_RenamesAIOrgKeyWhenOrgChanged(t *testing.T) {
	// Org rename: every repo under acme flips to widget. The
	// reconciler invokes RenameRepoInTOML once per repo; the org map
	// key must also move when the org component differs.
	path := writeConfigFile(t, `
[ai.orgs."acme"]
issue_prompt = "org-default"

[ai.repos."acme/api"]
implement_prompt = "fast"
`)

	if err := config.RenameRepoInTOML(path, "acme/api", "widget/api"); err != nil {
		t.Fatalf("rename: %v", err)
	}

	m := readBack(t, path)
	ai := m["ai"].(map[string]any)
	orgs, _ := ai["orgs"].(map[string]any)
	if orgs == nil {
		t.Fatal("ai.orgs section dropped")
	}
	if _, has := orgs["acme"]; has {
		t.Error("old org key still present after org rename")
	}
	widget, ok := orgs["widget"].(map[string]any)
	if !ok {
		t.Fatalf("new org key missing: %+v", orgs)
	}
	if widget["issue_prompt"] != "org-default" {
		t.Errorf("org values not preserved: %+v", widget)
	}
}

func toStringSlice(v any) []string {
	if v == nil {
		return nil
	}
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, e := range raw {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}


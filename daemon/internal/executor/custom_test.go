package executor_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/heimdallm/daemon/internal/agentschema"
	"github.com/heimdallm/daemon/internal/executor"
)

func aiderSchema(t *testing.T) *agentschema.Schema {
	t.Helper()
	s, err := agentschema.Parse([]byte(`{
	  "name": "aider", "binary": "aider",
	  "fields": [
	    {"key": "model", "type": "select", "label": "Model", "flag": "--model", "options": ["gpt-4o", "deepseek-chat"], "default": "gpt-4o"},
	    {"key": "auto_commits", "type": "bool", "label": "--auto-commits", "flag": "--auto-commits", "false_flag": "--no-auto-commits", "default": true},
	    {"key": "edit_format", "type": "select", "label": "--edit-format", "flag": "--edit-format", "options": ["whole", "diff"]}
	  ]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// registerAider registers the aider custom agent for one test.
func registerAider(t *testing.T) {
	t.Helper()
	if err := executor.SetCustomAgents([]executor.CustomAgent{{Name: "aider", Binary: "aider", Schema: aiderSchema(t)}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := executor.SetCustomAgents(nil); err != nil {
			t.Fatal(err)
		}
	})
}

func TestSetCustomAgentsRejects(t *testing.T) {
	t.Cleanup(func() { _ = executor.SetCustomAgents(nil) })
	schema := aiderSchema(t)
	for name, agents := range map[string][]executor.CustomAgent{
		"no schema":      {{Name: "aider", Binary: "aider"}},
		"builtin name":   {{Name: "claude", Binary: "claude", Schema: schema}},
		"path binary":    {{Name: "aider", Binary: "/tmp/aider", Schema: schema}},
		"shell binary":   {{Name: "aider", Binary: "sh", Schema: schema}},
		"declared twice": {{Name: "aider", Binary: "aider", Schema: schema}, {Name: "aider", Binary: "aider", Schema: schema}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := executor.SetCustomAgents(agents); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	if err := executor.ValidateCustomExtraFlags(executor.CustomAgent{Name: "x"}, ""); err == nil {
		t.Fatal("ValidateCustomExtraFlags needs a schema")
	}
}

func TestCustomAgentRegistry(t *testing.T) {
	if executor.IsCustomAgent("aider") || executor.ValidateCLIName("aider") == nil {
		t.Fatal("aider must be unknown before it is registered")
	}
	if len(executor.CustomAgents()) != 0 {
		t.Fatal("no custom agents are registered by default")
	}
	registerAider(t)
	if err := executor.ValidateCLIName("aider"); err != nil {
		t.Fatalf("ValidateCLIName(aider) = %v", err)
	}
	if !executor.IsBuiltinCLI("claude") || executor.IsBuiltinCLI("aider") {
		t.Fatal("IsBuiltinCLI")
	}
	if !strings.Contains(strings.Join(executor.SupportedCLIs(), ","), "aider") {
		t.Fatalf("SupportedCLIs must list custom agents: %v", executor.SupportedCLIs())
	}
	a, ok := executor.CustomAgentFor("aider")
	if !ok || a.Binary != "aider" {
		t.Fatalf("CustomAgentFor = %+v %v", a, ok)
	}
	a.Schema.Fields[0].Label = "changed"
	again, _ := executor.CustomAgentFor("aider")
	if again.Schema.Fields[0].Label == "changed" {
		t.Fatal("CustomAgentFor must return a copy")
	}
	if _, ok := executor.CustomAgentFor("nope"); ok {
		t.Fatal("unknown agent found")
	}
	if list := executor.CustomAgents(); len(list) != 1 || list[0].Name != "aider" {
		t.Fatalf("CustomAgents = %+v", list)
	}
	if executor.WriteCapable("aider") {
		t.Fatal("custom agents only review")
	}
	if executor.IsHTTPOnly("aider") {
		t.Fatal("custom agents are CLIs")
	}
}

func TestExecute_CustomAgentRunsFromItsSchemaOutsideTheCheckout(t *testing.T) {
	registerAider(t)
	args, cwd, stdin := fakeAgentBin(t, "aider")
	workDir := t.TempDir()
	opts := executor.ExecOptions{
		Model:        "deepseek-chat",
		WorkDir:      workDir,
		ExtraFlags:   "--verbose --map-refresh=auto",
		CustomValues: executor.NewCustomValues(map[string]any{"auto_commits": false, "edit_format": "whole"}),
	}
	res, err := executor.New().Execute("aider", "review this", opts)
	if err != nil || res.Summary != "ok" {
		t.Fatalf("Execute: %+v %v", res, err)
	}
	got := mustRead(t, args)
	want := "--model deepseek-chat --no-auto-commits --edit-format whole --verbose --map-refresh=auto"
	if got != want {
		t.Fatalf("args = %q, want %q", got, want)
	}
	if dir := mustRead(t, cwd); dir == workDir || strings.HasPrefix(dir, workDir) {
		t.Fatalf("a custom agent must not run in the checkout, ran in %q", dir)
	}
	if mustRead(t, stdin) != "review this" {
		t.Fatalf("prompt must arrive on stdin, got %q", mustRead(t, stdin))
	}
	if _, err := os.Stat(filepath.Join(workDir, "x")); err == nil {
		t.Fatal("unexpected write into the checkout")
	}
}

func TestExecute_CustomAgentRejectsInvalidValuesAtTheBoundary(t *testing.T) {
	registerAider(t)
	fakeAgentBin(t, "aider")
	_, err := executor.New().ExecuteRaw("aider", "x", executor.ExecOptions{
		CustomValues: executor.NewCustomValues(map[string]any{"edit_format": "--evil"}),
	})
	if err == nil {
		t.Fatal("an invalid custom value must be refused before the subprocess starts")
	}
	_, err = executor.New().ExecuteRaw("aider", "x", executor.ExecOptions{
		CustomValues: executor.NewCustomValues(map[string]any{"unknown": true}),
	})
	if err == nil {
		t.Fatal("an undeclared option must be refused")
	}
}

func TestCustomAgentExtraFlagPolicy(t *testing.T) {
	registerAider(t)
	for _, ok := range []string{"", "--verbose", "--map-refresh=auto -v"} {
		if err := executor.ValidateExtraFlagsForCLI("aider", ok); err != nil {
			t.Errorf("ValidateExtraFlagsForCLI(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{
		"positional",
		"--map-refresh auto",             // values must be attached
		"-",                              // a bare dash is not a flag
		"--dangerously-skip-permissions", // danger flags
		"--allow-dangerously-everything",
		"--config=/tmp/x",       // policy-changing for a built-in provider
		"--model=gpt-4o",        // set by the schema
		"--no-auto-commits",     // set by the schema
		"--x=bypassPermissions", // dangerous value
	} {
		if err := executor.ValidateExtraFlagsForCLI("aider", bad); err == nil {
			t.Errorf("ValidateExtraFlagsForCLI(%q) must fail", bad)
		}
	}
}

func TestCustomAgentOptionsAreDroppedOnFallback(t *testing.T) {
	opts := executor.ExecOptions{CustomValues: executor.NewCustomValues(map[string]any{"a": true})}
	if got := executor.OptionsForSelectedCLI("aider", "claude", opts); got.CustomValues != nil {
		t.Fatal("a fallback agent must not receive the primary's custom options")
	}
}

func TestNewCustomValues(t *testing.T) {
	if executor.NewCustomValues(nil) != nil {
		t.Fatal("no values → nil")
	}
	src := map[string]any{"a": 1}
	v := executor.NewCustomValues(src)
	src["a"] = 2
	if got := v.Map(); !reflect.DeepEqual(got, map[string]any{"a": 1}) {
		t.Fatalf("NewCustomValues must copy: %v", got)
	}
	var nilValues *executor.CustomValues
	if len(nilValues.Map()) != 0 {
		t.Fatal("nil values map to an empty map")
	}
}

func TestResolveCLIPathUsesTheCustomBinary(t *testing.T) {
	registerAider(t)
	fakeAgentBin(t, "aider")
	if p := executor.ResolveCLIPath("aider"); filepath.Base(p) != "aider" {
		t.Fatalf("ResolveCLIPath(aider) = %q", p)
	}
}

func TestMigrateLegacyExtraFlagsLeavesCustomAgentsAlone(t *testing.T) {
	registerAider(t)
	in := executor.ExecOptions{ExtraFlags: "--model=x"}
	out, migrated := executor.MigrateLegacyTypedExtraFlagsForCLI("aider", in)
	if len(migrated) != 0 || out.ExtraFlags != in.ExtraFlags {
		t.Fatalf("custom agents have no legacy flags to migrate: %+v %v", out, migrated)
	}
}

func TestValidateCustomAgentsDoesNotRegister(t *testing.T) {
	agents := []executor.CustomAgent{{Name: "aider", Binary: "aider", Schema: aiderSchema(t)}}
	if err := executor.ValidateCustomAgents(agents); err != nil {
		t.Fatal(err)
	}
	if executor.IsCustomAgent("aider") {
		t.Fatal("ValidateCustomAgents must not register anything")
	}
	if err := executor.ValidateCustomAgents([]executor.CustomAgent{{Name: "aider", Binary: "bash", Schema: aiderSchema(t)}}); err == nil {
		t.Fatal("a shell binary must be refused")
	}
}

func TestResolveCLIPathUsesABinaryNamedDifferently(t *testing.T) {
	schema, err := agentschema.Parse([]byte(`{"fields":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.SetCustomAgents([]executor.CustomAgent{{Name: "tool", Binary: "tool-cli", Schema: schema}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = executor.SetCustomAgents(nil) })
	defer executor.SetLoginShellLookPathForTest(func(string) string { return "" })()
	defer executor.SetWellKnownBinDirsForTest(func() []string { return nil })()
	fakeAgentBin(t, "tool-cli")
	if p := executor.ResolveCLIPath("tool"); filepath.Base(p) != "tool-cli" {
		t.Fatalf("ResolveCLIPath(tool) = %q, want the tool-cli binary", p)
	}
}

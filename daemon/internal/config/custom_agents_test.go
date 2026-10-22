package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/heimdallm/daemon/internal/agentschema"
	"github.com/heimdallm/daemon/internal/executor"
)

const aiderSchemaJSON = `{
  "name": "aider",
  "display_name": "Aider",
  "fields": [
    {"key": "model", "type": "select", "label": "Model", "flag": "--model", "options": ["gpt-4o", "deepseek-chat"], "default": "gpt-4o"},
    {"key": "auto_commits", "type": "bool", "label": "--auto-commits", "flag": "--auto-commits", "false_flag": "--no-auto-commits", "default": true},
    {"key": "edit_format", "type": "select", "label": "--edit-format", "flag": "--edit-format", "options": ["whole", "diff", "udiff"]},
    {"key": "effort", "type": "select", "label": "Effort", "flag": "--reasoning", "options": ["fast", "deep"]},
    {"key": "map_tokens", "type": "number", "label": "--map-tokens", "flag": "--map-tokens", "min": 0, "max": 8192}
  ]
}`

// writeConfigDir writes config.toml (and any extra files) into a fresh
// directory and returns the config path.
func writeConfigDir(t *testing.T, content string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad_CustomAgentFromSchemaPath(t *testing.T) {
	path := writeConfigDir(t, `
[ai]
primary = "aider"
fallback = "claude"

[ai.agents.aider]
binary = "aider"
schema_path = "schemas/aider.schema.json"
model = "deepseek-chat"
auto_commits = false
edit_format = "whole"
effort = "deep"
map_tokens = 1024
extra_flags = "--verbose"
prompt = "strict"
execution_timeout = "5m"

[ai.agents.claude]
effort = "high"
`, map[string]string{"schemas/aider.schema.json": aiderSchemaJSON})

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	a := cfg.AI.Agents["aider"]
	if !a.IsCustom() || a.Binary != "aider" || a.Model != "deepseek-chat" || a.ExtraFlags != "--verbose" || a.PromptID != "strict" {
		t.Fatalf("shared settings: %+v", a)
	}
	// effort is a typed claude setting, but here it is one of aider's options.
	if a.Effort != "" {
		t.Fatalf("a custom agent's options must not land in typed settings: effort=%q", a.Effort)
	}
	want := AgentOptions{"auto_commits": false, "edit_format": "whole", "effort": "deep", "map_tokens": int64(1024)}
	if !reflect.DeepEqual(a.Options, want) {
		t.Fatalf("Options = %#v, want %#v", a.Options, want)
	}
	if cfg.AI.Agents["claude"].Effort != "high" {
		t.Fatal("built-in agents keep their typed settings")
	}
}

func TestLoad_CustomAgentRejectsUndeclaredOption(t *testing.T) {
	// Option keys are the schema's keys, matched exactly.
	path := writeConfigDir(t, `
[ai]
primary = "claude"
[ai.agents.aider]
binary = "aider"
schema_path = "aider.schema.json"
nope = true
`, map[string]string{"aider.schema.json": aiderSchemaJSON})
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("Load must refuse an option the schema does not declare, got %v", err)
	}
}

func TestLoad_CustomAgentInlineSchema(t *testing.T) {
	path := writeConfigDir(t, `
[ai]
primary = "aider"

[ai.agents.aider]
binary = "aider"
edit_format = "diff"

[ai.agents.aider.schema]
display_name = "Aider"

[[ai.agents.aider.schema.fields]]
key = "edit_format"
type = "select"
label = "--edit-format"
flag = "--edit-format"
options = ["whole", "diff"]

[ai.agents.tool]
binary = "tool"
schema = '{"fields":[{"key":"fast","type":"bool","label":"Fast","flag":"--fast"}]}'

[ai.agents.tool.options]
fast = true
`, nil)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	agents := cfg.CustomAgents()
	if len(agents) != 2 || agents[0].Name != "aider" || agents[1].Name != "tool" {
		t.Fatalf("CustomAgents = %+v", agents)
	}
	if agents[0].Schema.Name != "aider" || agents[0].Schema.Binary != "aider" || agents[0].Schema.DisplayName != "Aider" {
		t.Fatalf("schema identity is filled from the table: %+v", agents[0].Schema)
	}
	if got := cfg.AI.Agents["tool"].Options; !reflect.DeepEqual(got, AgentOptions{"fast": true}) {
		t.Fatalf("explicit options table: %#v", got)
	}
}

func TestLoad_CustomAgentErrors(t *testing.T) {
	schemaFiles := map[string]string{"aider.schema.json": aiderSchemaJSON, "dir/x.json": aiderSchemaJSON}
	cases := map[string]string{
		"builtin name":        "[ai.agents.claude]\nbinary = \"claude\"\nschema_path = \"aider.schema.json\"\n",
		"catalog name":        "[ai.agents.cursor]\nbinary = \"cursor\"\nschema_path = \"aider.schema.json\"\n",
		"bad name":            "[ai.agents.\"My Agent\"]\nbinary = \"aider\"\nschema_path = \"aider.schema.json\"\n",
		"no schema":           "[ai.agents.aider]\nbinary = \"aider\"\n",
		"both schemas":        "[ai.agents.aider]\nbinary = \"aider\"\nschema_path = \"aider.schema.json\"\nschema = '{\"fields\":[]}'\n",
		"absolute path":       "[ai.agents.aider]\nbinary = \"aider\"\nschema_path = \"/etc/passwd\"\n",
		"traversal":           "[ai.agents.aider]\nbinary = \"aider\"\nschema_path = \"../outside.json\"\n",
		"missing file":        "[ai.agents.aider]\nbinary = \"aider\"\nschema_path = \"nope.json\"\n",
		"directory":           "[ai.agents.aider]\nbinary = \"aider\"\nschema_path = \"dir\"\n",
		"shell binary":        "[ai.agents.aider]\nbinary = \"bash\"\nschema_path = \"aider.schema.json\"\n",
		"name mismatch":       "[ai.agents.other]\nbinary = \"aider\"\nschema_path = \"aider.schema.json\"\n",
		"binary mismatch":     "[ai.agents.tool]\nbinary = \"tool\"\nschema = '{\"binary\":\"other\",\"fields\":[]}'\n",
		"bad option value":    "[ai.agents.aider]\nbinary = \"aider\"\nschema_path = \"aider.schema.json\"\nedit_format = \"patch\"\n",
		"model without field": "[ai.agents.tool]\nbinary = \"tool\"\nschema = '{\"fields\":[]}'\nmodel = \"x\"\n",
		"bad extra flags":     "[ai.agents.aider]\nbinary = \"aider\"\nschema_path = \"aider.schema.json\"\nextra_flags = \"positional\"\n",
		"bad timeout":         "[ai.agents.aider]\nbinary = \"aider\"\nschema_path = \"aider.schema.json\"\nexecution_timeout = \"soon\"\n",
		"table option":        "[ai.agents.aider]\nbinary = \"aider\"\nschema_path = \"aider.schema.json\"\n[ai.agents.aider.nested]\nx = 1\n",
		"duplicate option":    "[ai.agents.aider]\nbinary = \"aider\"\nschema_path = \"aider.schema.json\"\nedit_format = \"diff\"\n[ai.agents.aider.options]\nedit_format = \"whole\"\n",
		"options not table":   "[ai.agents.aider]\nbinary = \"aider\"\nschema_path = \"aider.schema.json\"\noptions = 3\n",
		"array option":        "[ai.agents.tool]\nbinary = \"tool\"\nschema = '{\"fields\":[]}'\n[ai.agents.tool.options]\nlist = [1, 2]\n",
		"bad inline schema":   "[ai.agents.tool]\nbinary = \"tool\"\nschema = '{'\n",
		"schema of numbers":   "[ai.agents.tool]\nbinary = \"tool\"\nschema = 3\n",
		"schema without bin":  "[ai.agents.claude]\nschema = '{\"fields\":[]}'\n",
		"options without bin": "[ai.agents.tool.options]\nfast = true\n",
	}
	for name, agents := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeConfigDir(t, "[ai]\nprimary = \"claude\"\n"+agents, schemaFiles)
			if _, err := Load(path); err == nil {
				t.Fatal("expected Load to fail")
			}
		})
	}
}

func TestLoad_SchemaPathSymlinkOutsideConfigDir(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret.json")
	if err := os.WriteFile(outside, []byte(aiderSchemaJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	path := writeConfigDir(t, "[ai]\nprimary = \"claude\"\n[ai.agents.aider]\nbinary = \"aider\"\nschema_path = \"link.json\"\n", nil)
	if err := os.Symlink(outside, filepath.Join(filepath.Dir(path), "link.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("a symlink leaving the config directory must be refused, got %v", err)
	}
}

func TestLoad_SchemaPathTooLarge(t *testing.T) {
	path := writeConfigDir(t, "[ai]\nprimary = \"claude\"\n[ai.agents.aider]\nbinary = \"aider\"\nschema_path = \"big.json\"\n",
		map[string]string{"big.json": strings.Repeat(" ", agentschema.MaxSchemaBytes+1)})
	if _, err := Load(path); err == nil {
		t.Fatal("an oversized schema file must be refused")
	}
}

func TestFlowsAcceptCustomAgentsDeclaredInTheSameConfig(t *testing.T) {
	path := writeConfigDir(t, `
[ai]
primary = "claude"
flow = "f"

[ai.agents.aider]
binary = "aider"
schema_path = "aider.schema.json"

[ai.flows.f]
name = "F"
start = "a"

[ai.flows.f.nodes.a]
type = "agent"
agent = "aider"
`, map[string]string{"aider.schema.json": aiderSchemaJSON})
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if executor.IsCustomAgent("aider") {
		t.Fatal("Load must not register agents itself")
	}
	if refs := cfg.AgentReferences("aider"); !reflect.DeepEqual(refs, []string{"ai.flows.f.nodes.a"}) {
		t.Fatalf("AgentReferences = %v", refs)
	}
	// The public CheckFlow uses the live registry, where aider is unknown.
	if errs, _ := CheckFlow("f", cfg.AI.Flows["f"]); len(errs) == 0 {
		t.Fatal("CheckFlow must use the executor registry")
	}
}

func TestAgentReferences(t *testing.T) {
	c := &Config{}
	c.AI.Primary, c.AI.Fallback = "aider", "aider"
	c.AI.Orgs = map[string]OrgAI{"acme": {Primary: "aider", Fallback: "claude"}}
	c.AI.Repos = map[string]RepoAI{"acme/x": {Fallback: "aider"}}
	c.AI.Flows = map[string]FlowConfig{"f": {Nodes: map[string]FlowNode{
		"q": {Type: NodeCondition, Quota: map[string]QuotaCondition{"c": {Agent: "aider"}}},
	}}}
	want := []string{"ai.fallback", "ai.flows.f.nodes.q.quota.c", "ai.orgs.acme.primary", "ai.primary", "ai.repos.acme/x.fallback"}
	if got := c.AgentReferences("aider"); !reflect.DeepEqual(got, want) {
		t.Fatalf("AgentReferences = %v, want %v", got, want)
	}
}

func TestValidateMapAtResolvesSchemaPathNextToTheConfig(t *testing.T) {
	path := writeConfigDir(t, "", map[string]string{"aider.schema.json": aiderSchemaJSON})
	m := map[string]any{"ai": map[string]any{
		"primary": "claude",
		"agents": map[string]any{"aider": map[string]any{
			"binary": "aider", "schema_path": "aider.schema.json", "edit_format": "udiff",
		}},
	}}
	if err := ValidateMapAt(m, path); err != nil {
		t.Fatalf("ValidateMapAt: %v", err)
	}
	if err := ValidateMap(m); err == nil {
		t.Fatal("without a config path a schema_path cannot be resolved")
	}
	m["ai"].(map[string]any)["agents"].(map[string]any)["aider"].(map[string]any)["edit_format"] = "patch"
	if err := ValidateMapAt(m, path); err == nil {
		t.Fatal("ValidateMapAt must validate option values against the schema")
	}
	bad := map[string]any{"ai": map[string]any{"agents": map[string]any{"x": map[string]any{"Model": "a", "model": "b", "MODEL": "c"}}}}
	if err := ValidateMapAt(bad, path); err == nil {
		t.Fatal("ambiguous aliases are a validation error")
	}
}

func TestCustomAgentsAPIEnabled(t *testing.T) {
	if CustomAgentsAPIEnabled(map[string]any{"ai": map[string]any{}}) {
		t.Fatal("off by default")
	}
	if !CustomAgentsAPIEnabled(map[string]any{"ai": map[string]any{"allow_custom_agents_api": true}}) {
		t.Fatal("opt-in not read")
	}
	if CustomAgentsAPIEnabled(map[string]any{"ai": map[string]any{"allow_custom_agents_api": "yes"}}) {
		t.Fatal("a non-boolean value must not enable the API")
	}
	refs, err := AgentReferencesInMap(map[string]any{"ai": map[string]any{"primary": "aider"}}, "aider")
	if err != nil || !reflect.DeepEqual(refs, []string{"ai.primary"}) {
		t.Fatalf("AgentReferencesInMap = %v %v", refs, err)
	}
	if _, err := AgentReferencesInMap(map[string]any{"ai": map[string]any{"primary": 3}}, "aider"); err == nil {
		t.Fatal("a structurally invalid map is an error")
	}
}

func TestAgentSchemaValueRoundTrip(t *testing.T) {
	var v AgentSchemaValue
	if err := v.UnmarshalTOML(aiderSchemaJSON); err != nil {
		t.Fatal(err)
	}
	text, err := v.MarshalText()
	if err != nil {
		t.Fatal(err)
	}
	var back AgentSchemaValue
	if err := back.UnmarshalTOML(string(text)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(v.Schema, back.Schema) {
		t.Fatal("schema does not round-trip through MarshalText")
	}
	if text, _ := (AgentSchemaValue{}).MarshalText(); string(text) != "{}" {
		t.Fatalf("empty schema text = %q", text)
	}
	// A whole config with an inline schema encodes and decodes again.
	cfg := Config{}
	cfg.AI.Agents = map[string]CLIAgentConfig{"aider": {Binary: "aider", Schema: &v, Options: AgentOptions{"auto_commits": false}}}
	var buf strings.Builder
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		t.Fatal(err)
	}
	var decoded Config
	if err := toml.Unmarshal([]byte(buf.String()), &decoded); err != nil {
		t.Fatalf("decode %s: %v", buf.String(), err)
	}
	if decoded.AI.Agents["aider"].Schema.Schema.Name != "aider" {
		t.Fatalf("inline schema lost: %+v", decoded.AI.Agents["aider"])
	}
	var opts AgentOptions
	if err := opts.UnmarshalTOML("x"); err == nil {
		t.Fatal("options must be a table")
	}
}

func TestSanitizeLegacyPolicyMapSkipsCustomAgents(t *testing.T) {
	m := map[string]any{"ai": map[string]any{"agents": map[string]any{
		"aider": map[string]any{"binary": "aider", "effort": "deep", "extra_flags": "--model x"},
	}}}
	if err := SanitizeLegacyAgentExecutionPolicyMap(m, "test"); err != nil {
		t.Fatal(err)
	}
	agent := m["ai"].(map[string]any)["agents"].(map[string]any)["aider"].(map[string]any)
	if agent["effort"] != "deep" || agent["extra_flags"] != "--model x" {
		t.Fatalf("a custom agent's table must be left alone: %v", agent)
	}
}

func TestIsCustomAgentSharedKey(t *testing.T) {
	for _, k := range []string{"binary", "MODEL", "prompt", "review_limits"} {
		if !IsCustomAgentSharedKey(k) {
			t.Errorf("%s is shared", k)
		}
	}
	if IsCustomAgentSharedKey("effort") {
		t.Error("effort is an option for a custom agent")
	}
	if !ReservedAgentName("claude") || !ReservedAgentName("cursor") || ReservedAgentName("aider") {
		t.Error("ReservedAgentName")
	}
}

// Built-in schemas describe the typed settings, so every field must be a
// CLIAgentConfig TOML key and every closed list must match what the executor
// accepts.
func TestBuiltinSchemasMatchTypedSettings(t *testing.T) {
	keys := map[string]bool{}
	for i := 0; i < cliAgentSchemaType.NumField(); i++ {
		name, include, err := canonicalTOMLFieldName(cliAgentSchemaType.Field(i))
		if err == nil && include {
			keys[name] = true
		}
	}
	for _, s := range agentschema.Builtins() {
		if err := executor.ValidateCLIName(s.Name); err != nil {
			t.Errorf("%s: %v", s.Name, err)
		}
		for _, f := range s.Fields {
			if !keys[f.Key] {
				t.Errorf("%s.%s is not a CLIAgentConfig setting", s.Name, f.Key)
			}
			for _, o := range f.Options {
				var err error
				switch f.Key {
				case "effort":
					err = executor.ValidateEffort(o)
				case "permission_mode":
					err = executor.ValidatePermissionMode(o)
				case "approval_mode":
					err = executor.ValidateApprovalModeForCLI(s.Name, o)
				case "model":
					err = executor.ValidateModel(o)
				}
				if err != nil {
					t.Errorf("%s.%s option %q is refused by the executor: %v", s.Name, f.Key, o, err)
				}
			}
		}
	}
	for _, name := range executor.SupportedCLIs() {
		if _, ok := agentschema.Builtin(name); !ok {
			t.Errorf("built-in agent %s has no schema", name)
		}
	}
}

func TestStoreNeverSetsACustomAgentsExtraFlags(t *testing.T) {
	base := CLIAgentConfig{Binary: "aider", ExtraFlags: "--verbose"}
	got, apply, err := mergeStoredAgentConfig("claude", base, []byte(`{"extra_flags":"-y","prompt":"p"}`))
	if err != nil || !apply {
		t.Fatalf("merge: %v %v", apply, err)
	}
	if got.ExtraFlags != "--verbose" || got.PromptID != "p" || got.Binary != "aider" {
		t.Fatalf("merged = %+v", got)
	}
}

// On reload the store layer is merged before the executor learns about the
// new config's custom agents: an override for one must not be dropped.
func TestStoreOverridesACustomAgentTheExecutorDoesNotKnowYet(t *testing.T) {
	if executor.IsCustomAgent("aider") {
		t.Fatal("precondition: aider is not registered")
	}
	base := CLIAgentConfig{Binary: "aider", PromptID: "old"}
	got, apply, err := mergeStoredAgentConfig("aider", base, []byte(`{"prompt":"new","model":"gpt-4o"}`))
	if err != nil || !apply {
		t.Fatalf("merge: apply=%v err=%v", apply, err)
	}
	if got.PromptID != "new" || got.Model != "gpt-4o" || got.Binary != "aider" {
		t.Fatalf("merged = %+v", got)
	}
	// An agent that is neither built in nor declared is still ignored.
	if _, apply, _ := mergeStoredAgentConfig("ghost", CLIAgentConfig{}, []byte(`{"prompt":"x"}`)); apply {
		t.Fatal("an unknown agent's override must be ignored")
	}
}

// Built-in agents keep their typed validation: their schema only describes
// them for the UIs, so a value outside it (OpenRouter allows 40 tool rounds
// in its form) still loads.
func TestBuiltinValuesAreNotCheckedAgainstTheirSchema(t *testing.T) {
	path := writeConfigDir(t, `
[ai]
primary = "claude"

[ai.agents.openrouter]
max_turns = 120

[ai.agents.claude]
model = "claude-legacy-1"
`, nil)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AI.Agents["openrouter"].MaxTurns != 120 {
		t.Fatal("value lost")
	}
}

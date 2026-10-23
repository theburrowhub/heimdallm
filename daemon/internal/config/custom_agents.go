package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/heimdallm/daemon/internal/agentcatalog"
	"github.com/heimdallm/daemon/internal/agentschema"
	"github.com/heimdallm/daemon/internal/executor"
)

// A custom agent is any CLI the operator declares in config.toml with a
// schema of its options:
//
//	[ai.agents.aider]
//	binary = "aider"
//	schema_path = "aider.schema.json"   # or an inline schema = { … }
//	model = "gpt-4o"
//	auto_commits = true
//
// The table's `binary` key is what makes it a custom agent. Its option values
// sit next to the shared settings, as in the example; Load gathers them into
// CLIAgentConfig.Options. The definition (binary and schema) belongs to the
// trusted layer — config.toml — like dangerously_skip_perms: the HTTP API may
// change the option values, and may change the definition only when
// [ai] allow_custom_agents_api is true.

// customAgentSharedKeys are the keys of a custom agent's table that are not
// option values: what defines it, and the settings every agent shares.
var customAgentSharedKeys = []string{
	"binary",
	"schema",
	"schema_path",
	"options",
	agentschema.ModelKey,
	"prompt",
	"extra_flags",
	"execution_timeout",
	"review_limits",
}

// IsCustomAgentSharedKey reports whether key is a shared (non-option) key of a
// custom agent's table.
func IsCustomAgentSharedKey(key string) bool {
	for _, k := range customAgentSharedKeys {
		if strings.EqualFold(k, key) {
			return true
		}
	}
	return false
}

// CustomAgentDefinitionKeys define a custom agent. Only config.toml, or the
// opt-in /agent-schemas endpoints, may set them.
var CustomAgentDefinitionKeys = []string{"binary", "schema", "schema_path", "options"}

// AgentOptions holds a custom agent's option values, keyed by its schema's
// field keys. Only scalars are kept: a schema field is a string, a bool or a
// number.
type AgentOptions map[string]any

// UnmarshalTOML implements toml.Unmarshaler.
func (o *AgentOptions) UnmarshalTOML(v any) error {
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("options must be a table, got %T", v)
	}
	out := make(AgentOptions, len(m))
	for k, raw := range m {
		switch raw.(type) {
		case string, bool, int64, float64:
			out[k] = raw
		default:
			return fmt.Errorf("options.%s must be a string, a boolean or a number, got %T", k, raw)
		}
	}
	*o = out
	return nil
}

// AgentSchemaValue is a custom agent's inline schema: a TOML table, or a
// string holding the schema's JSON.
type AgentSchemaValue struct {
	Schema *agentschema.Schema
}

// UnmarshalTOML implements toml.Unmarshaler.
func (s *AgentSchemaValue) UnmarshalTOML(v any) error {
	var (
		schema *agentschema.Schema
		err    error
	)
	switch raw := v.(type) {
	case string:
		schema, err = agentschema.Parse([]byte(raw))
	case map[string]any:
		schema, err = agentschema.FromMap(raw)
	default:
		return fmt.Errorf("schema must be a table or a JSON string, got %T", v)
	}
	if err != nil {
		return err
	}
	s.Schema = schema
	return nil
}

// MarshalText writes the schema back as JSON, which UnmarshalTOML reads.
func (s AgentSchemaValue) MarshalText() ([]byte, error) {
	if s.Schema == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(s.Schema)
}

// IsCustom reports whether the agent is a custom agent (declares a binary).
func (a CLIAgentConfig) IsCustom() bool { return strings.TrimSpace(a.Binary) != "" }

// projectCustomAgentTable projects a custom agent's TOML table: the shared
// keys keep their place and every other scalar becomes an option value. A
// built-in agent's typed keys (effort, bare, …) are option values here too:
// a custom agent's options are whatever its schema declares.
func projectCustomAgentTable(table map[string]any, path string) (map[string]any, error) {
	shared := make(map[string]any)
	options := make(map[string]any)
	for _, key := range sortedMapKeys(table) {
		if IsCustomAgentSharedKey(key) {
			shared[key] = table[key]
			continue
		}
		switch table[key].(type) {
		case string, bool, int64, float64:
			options[key] = table[key]
		default:
			return nil, fmt.Errorf("%s.%s: a custom agent's option must be a string, a boolean or a number", path, key)
		}
	}
	projected, err := walkKnownTOMLStructFields(shared, cliAgentSchemaType, path)
	if err != nil {
		return nil, err
	}
	if explicit, ok := projected["options"].(map[string]any); ok {
		for k, v := range explicit {
			if _, dup := options[k]; dup {
				return nil, fmt.Errorf("%s: option %q is set both in the table and in its options", path, k)
			}
			options[k] = v
		}
	} else if raw, present := projected["options"]; present {
		return nil, fmt.Errorf("%s.options must be a table, got %T", path, raw)
	}
	if len(options) > 0 {
		projected["options"] = options
	} else {
		delete(projected, "options")
	}
	return projected, nil
}

// tableIsCustomAgent reports whether a raw agent table declares a binary.
func tableIsCustomAgent(table map[string]any) bool {
	return len(caseFoldedMapKeys(table, "binary")) > 0
}

// resolveCustomAgentSchemas loads every schema_path, relative to the
// directory of config.toml. Paths that leave that directory are refused, so a
// schema reference cannot be used to read (and serve back through GET
// /agent-schemas) an arbitrary file.
func (c *Config) resolveCustomAgentSchemas(configDir string) error {
	for name, a := range c.AI.Agents {
		if !a.IsCustom() || a.SchemaPath == "" {
			continue
		}
		if a.Schema != nil && a.Schema.Schema != nil {
			return fmt.Errorf("config: agents[%s]: set schema or schema_path, not both", name)
		}
		if configDir == "" {
			return fmt.Errorf("config: agents[%s].schema_path needs the config file's location", name)
		}
		schema, err := readSchemaFile(configDir, a.SchemaPath)
		if err != nil {
			return fmt.Errorf("config: agents[%s].schema_path: %w", name, err)
		}
		a.ResolvedSchema = schema
		c.AI.Agents[name] = a
	}
	return nil
}

func readSchemaFile(configDir, rel string) (*agentschema.Schema, error) {
	if filepath.IsAbs(rel) || strings.Contains(rel, "\x00") {
		return nil, fmt.Errorf("%q must be relative to the config directory", rel)
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("%q must stay inside the config directory", rel)
	}
	base, err := filepath.EvalSymlinks(configDir)
	if err != nil {
		return nil, err
	}
	full, err := filepath.EvalSymlinks(filepath.Join(base, clean))
	if err != nil {
		return nil, err
	}
	if inside, err := filepath.Rel(base, full); err != nil || inside == ".." ||
		strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("%q resolves outside the config directory", rel)
	}
	info, err := os.Stat(full)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%q is not a regular file", rel)
	}
	if info.Size() > agentschema.MaxSchemaBytes {
		return nil, fmt.Errorf("%q is %d bytes, at most %d", rel, info.Size(), agentschema.MaxSchemaBytes)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, err
	}
	return agentschema.Parse(data)
}

// CustomAgentSchema returns the schema of a custom agent declared in c, with
// its name and binary filled in from the table.
func (a CLIAgentConfig) CustomAgentSchema(name string) (*agentschema.Schema, error) {
	var schema *agentschema.Schema
	switch {
	case a.ResolvedSchema != nil:
		schema = a.ResolvedSchema.Clone()
	case a.Schema != nil && a.Schema.Schema != nil:
		schema = a.Schema.Schema.Clone()
	default:
		return nil, fmt.Errorf("custom agent %q needs a schema or a schema_path", name)
	}
	if schema.Name != "" && schema.Name != name {
		return nil, fmt.Errorf("schema name %q does not match the agent %q", schema.Name, name)
	}
	if schema.Binary != "" && schema.Binary != a.Binary {
		return nil, fmt.Errorf("schema binary %q does not match the agent's binary %q", schema.Binary, a.Binary)
	}
	schema.Name, schema.Binary = name, strings.TrimSpace(a.Binary)
	return schema, nil
}

// reservedAgentName reports whether name belongs to Heimdallm's own agents:
// a built-in agent, or any entry of the agent catalog (the Cursor IDE, and
// whatever the catalog lists in the future), so a custom agent can never
// share an ID with one in GET /cli-agents.
func reservedAgentName(name string) bool {
	if executor.IsBuiltinCLI(name) {
		return true
	}
	for _, e := range agentcatalog.Catalog {
		if e.ID == name || e.ConfigAgent == name {
			return true
		}
	}
	return false
}

// customAgentValues returns a custom agent's option values plus its model,
// the same set the executor validates at the subprocess boundary.
func (a CLIAgentConfig) customAgentValues() map[string]any {
	values := make(map[string]any, len(a.Options)+1)
	for k, v := range a.Options {
		values[k] = v
	}
	if m := strings.TrimSpace(a.Model); m != "" {
		values[agentschema.ModelKey] = m
	}
	return values
}

// validateCustomAgent checks one custom agent declared in c.
func (c *Config) validateCustomAgent(name string, a CLIAgentConfig) error {
	if !agentschema.IsAgentName(name) {
		return fmt.Errorf("config: agents[%s]: a custom agent's name must be lowercase letters, digits, '-' or '_' (32 at most) and start with a letter", name)
	}
	if reservedAgentName(name) {
		return fmt.Errorf("config: agents[%s]: binary is only for custom agents; %q is a built-in agent", name, name)
	}
	schema, err := a.CustomAgentSchema(name)
	if err != nil {
		return fmt.Errorf("config: agents[%s]: %w", name, err)
	}
	if err := schema.ValidateCustom(reservedAgentName); err != nil {
		return fmt.Errorf("config: agents[%s]: %w", name, err)
	}
	if a.Model != "" {
		if _, ok := schema.Field(agentschema.ModelKey); !ok {
			return fmt.Errorf("config: agents[%s].model: the schema declares no model field", name)
		}
	}
	if _, err := schema.ValidateValues(a.customAgentValues()); err != nil {
		return fmt.Errorf("config: agents[%s]: %w", name, err)
	}
	agent := executor.CustomAgent{Name: name, Binary: schema.Binary, Schema: schema}
	if err := executor.ValidateCustomExtraFlags(agent, a.ExtraFlags); err != nil {
		return fmt.Errorf("config: agents[%s].extra_flags: %w", name, err)
	}
	if a.ExecutionTimeout != "" {
		if d, err := time.ParseDuration(a.ExecutionTimeout); err != nil || d <= 0 {
			return fmt.Errorf("config: agents[%s].execution_timeout must be a positive duration, e.g. 20m", name)
		}
	}
	return nil
}

// CustomAgents returns every custom agent declared in c, ready for
// executor.SetCustomAgents. c must have passed Validate.
func (c *Config) CustomAgents() []executor.CustomAgent {
	var out []executor.CustomAgent
	for name, a := range c.AI.Agents {
		if !a.IsCustom() {
			continue
		}
		schema, err := a.CustomAgentSchema(name)
		if err != nil {
			continue
		}
		out = append(out, executor.CustomAgent{Name: name, Binary: schema.Binary, Schema: schema})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// validateAgentName accepts a built-in agent, a custom agent registered with
// the executor, or a custom agent declared in c itself — the last so a config
// that adds an agent and a flow using it validates before the reload that
// registers the agent.
func (c *Config) validateAgentName(name string) error {
	if a, ok := c.AI.Agents[name]; ok && a.IsCustom() {
		return nil
	}
	return executor.ValidateCLIName(name)
}

// AgentReferences lists where c selects the agent name: primary/fallback at
// every level and flow steps. A custom agent that is still referenced cannot
// be deleted.
func (c *Config) AgentReferences(name string) []string {
	var refs []string
	add := func(path, value string) {
		if value == name {
			refs = append(refs, path)
		}
	}
	add("ai.primary", c.AI.Primary)
	add("ai.fallback", c.AI.Fallback)
	for org, o := range c.AI.Orgs {
		add("ai.orgs."+org+".primary", o.Primary)
		add("ai.orgs."+org+".fallback", o.Fallback)
	}
	for repo, r := range c.AI.Repos {
		add("ai.repos."+repo+".primary", r.Primary)
		add("ai.repos."+repo+".fallback", r.Fallback)
	}
	for id, f := range c.AI.Flows {
		for nid, n := range f.Nodes {
			if n.Agent == name {
				refs = append(refs, "ai.flows."+id+".nodes."+nid)
			}
			for ck, q := range n.Quota {
				if q.Agent == name {
					refs = append(refs, "ai.flows."+id+".nodes."+nid+".quota."+ck)
				}
			}
		}
	}
	sort.Strings(refs)
	return refs
}

// ReservedAgentName reports whether a custom agent may not use name: a
// built-in agent or another entry of the agent catalog.
func ReservedAgentName(name string) bool { return reservedAgentName(name) }

// decodeMap decodes a generic config map the way Load decodes config.toml,
// without validating it.
func decodeMap(m map[string]any) (*Config, error) {
	known, err := projectKnownConfigMap(m)
	if err != nil {
		return nil, err
	}
	var buf strings.Builder
	if err := toml.NewEncoder(&buf).Encode(known); err != nil {
		return nil, err
	}
	var cfg Config
	if err := toml.Unmarshal([]byte(buf.String()), &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// CustomAgentsAPIEnabled reports whether a config map (config.toml as read
// from disk) opts in to defining custom agents over the HTTP API.
func CustomAgentsAPIEnabled(m map[string]any) bool {
	cfg, err := decodeMap(m)
	return err == nil && cfg.AI.AllowCustomAgentsAPI
}

// AgentReferencesInMap is AgentReferences for a generic config map.
func AgentReferencesInMap(m map[string]any, name string) ([]string, error) {
	cfg, err := decodeMap(m)
	if err != nil {
		return nil, err
	}
	return cfg.AgentReferences(name), nil
}

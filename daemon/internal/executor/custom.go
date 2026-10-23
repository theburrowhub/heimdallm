package executor

import (
	"fmt"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/heimdallm/daemon/internal/agentschema"
)

// CustomAgent is a CLI the operator declared in config.toml with a schema
// ([ai.agents.<name>] binary = "…"). Heimdallm runs it from its schema alone:
// review-only, in an empty workspace, with the prompt on stdin.
type CustomAgent struct {
	Name   string
	Binary string
	Schema *agentschema.Schema
}

// customAgents is the live registry, swapped atomically on every config load
// and reload. Readers never take a lock.
var customAgents atomic.Pointer[map[string]CustomAgent]

// IsBuiltinCLI reports whether name is one of the agents Heimdallm ships.
func IsBuiltinCLI(name string) bool {
	_, ok := allowedCLIs[name]
	return ok
}

// SetCustomAgents replaces the registry of custom agents. Every agent is
// validated again here, at the executor's own boundary, so a caller that
// skipped config validation still cannot register a path, a shell or a name
// that collides with a built-in agent.
func SetCustomAgents(agents []CustomAgent) error {
	next, err := buildCustomRegistry(agents)
	if err != nil {
		return err
	}
	customAgents.Store(&next)
	return nil
}

// ValidateCustomAgents runs SetCustomAgents' checks without registering the
// agents, so a reload can refuse a config before it has any side effect.
func ValidateCustomAgents(agents []CustomAgent) error {
	_, err := buildCustomRegistry(agents)
	return err
}

func buildCustomRegistry(agents []CustomAgent) (map[string]CustomAgent, error) {
	next := make(map[string]CustomAgent, len(agents))
	for _, a := range agents {
		if a.Schema == nil {
			return nil, fmt.Errorf("executor: custom agent %q has no schema", a.Name)
		}
		schema := a.Schema.Clone()
		schema.Name, schema.Binary = a.Name, a.Binary
		if err := schema.ValidateCustom(IsBuiltinCLI); err != nil {
			return nil, fmt.Errorf("executor: custom agent %q: %w", a.Name, err)
		}
		if _, dup := next[a.Name]; dup {
			return nil, fmt.Errorf("executor: custom agent %q is declared twice", a.Name)
		}
		next[a.Name] = CustomAgent{Name: a.Name, Binary: a.Binary, Schema: schema}
	}
	return next, nil
}

// CustomAgentFor returns a registered custom agent.
func CustomAgentFor(name string) (CustomAgent, bool) {
	p := customAgents.Load()
	if p == nil {
		return CustomAgent{}, false
	}
	a, ok := (*p)[name]
	if !ok {
		return CustomAgent{}, false
	}
	a.Schema = a.Schema.Clone()
	return a, true
}

// IsCustomAgent reports whether name is a registered custom agent.
func IsCustomAgent(name string) bool {
	p := customAgents.Load()
	if p == nil {
		return false
	}
	_, ok := (*p)[name]
	return ok
}

// CustomAgents lists the registered custom agents, sorted by name.
func CustomAgents() []CustomAgent {
	p := customAgents.Load()
	if p == nil {
		return nil
	}
	out := make([]CustomAgent, 0, len(*p))
	for _, a := range *p {
		a.Schema = a.Schema.Clone()
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// CustomValues is a read-only copy of a custom agent's option values.
type CustomValues struct {
	values map[string]any
}

// NewCustomValues copies values, so the caller's map can change afterwards.
func NewCustomValues(values map[string]any) *CustomValues {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]any, len(values))
	for k, v := range values {
		out[k] = v
	}
	return &CustomValues{values: out}
}

// Map returns a copy of the values.
func (c *CustomValues) Map() map[string]any {
	out := map[string]any{}
	if c == nil {
		return out
	}
	for k, v := range c.values {
		out[k] = v
	}
	return out
}

// customValues merges the shared model setting into a custom agent's option
// values when its schema declares a model field.
func customValues(agent CustomAgent, opts ExecOptions) map[string]any {
	values := opts.CustomValues.Map()
	if _, ok := agent.Schema.Field(agentschema.ModelKey); ok && strings.TrimSpace(opts.Model) != "" {
		values[agentschema.ModelKey] = strings.TrimSpace(opts.Model)
	}
	return values
}

// customArgs validates a custom agent's values against its schema and builds
// its flags. It runs at the subprocess boundary, so a value that slipped past
// ingress validation still never reaches the CLI.
func customArgs(agent CustomAgent, opts ExecOptions) ([]string, error) {
	values, err := agent.Schema.ValidateValues(customValues(agent, opts))
	if err != nil {
		return nil, fmt.Errorf("executor: %w", err)
	}
	return agent.Schema.BuildArgs(values)
}

// validateCustomExtraFlags is the free-form flag policy of a custom agent.
// Heimdallm knows nothing about the CLI, so the rule is structural: only
// flags (values attached with '='), never one the schema already sets, never
// a --danger*/--allow-dangerously* flag, and never a long flag that any
// built-in provider treats as policy-changing.
func validateCustomExtraFlags(agent CustomAgent, flags string) error {
	schemaFlags := map[string]bool{}
	for _, f := range agent.Schema.Flags() {
		schemaFlags[strings.ToLower(f)] = true
	}
	for _, part := range strings.Fields(flags) {
		flag := canonicalFlagName(part)
		if flag == "" || flag == "-" || flag == "--" {
			return fmt.Errorf("executor: unexpected value %q in ExtraFlags for %s: attach values with '=' (--flag=value)", part, agent.Name)
		}
		if strings.HasPrefix(flag, "--danger") || strings.HasPrefix(flag, "--allow-dangerously") {
			return forbiddenExtraFlagError(agent.Name, part, "permission or sandbox policy")
		}
		if category, forbidden := forbiddenExtraFlagCategory("", flag); forbidden {
			return forbiddenExtraFlagError(agent.Name, part, category)
		}
		if schemaFlags[flag] {
			return fmt.Errorf("executor: flag %q is set by %s's schema; change that setting instead of adding it to ExtraFlags", part, agent.Name)
		}
		if err := validateExtraFlagValue(part); err != nil {
			return err
		}
	}
	return nil
}

// ValidateCustomExtraFlags applies a custom agent's free-form flag policy
// without registering the agent, so config validation can check an agent the
// reload has not registered yet.
func ValidateCustomExtraFlags(agent CustomAgent, flags string) error {
	if agent.Schema == nil {
		return fmt.Errorf("executor: custom agent %q has no schema", agent.Name)
	}
	return validateCustomExtraFlags(agent, flags)
}

package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/heimdallm/daemon/internal/agentschema"
	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/executor"
)

// agentSchemaView is one schema in GET /agent-schemas.
type agentSchemaView struct {
	*agentschema.Schema
	// Builtin is true for the agents Heimdallm ships, false for custom ones.
	Builtin bool `json:"builtin"`
}

// agentSchemasResponse is the GET /agent-schemas body.
type agentSchemasResponse struct {
	Schemas []agentSchemaView `json:"schemas"`
	// CustomAgentsAPIEnabled reports [ai] allow_custom_agents_api: whether
	// PUT and DELETE /agent-schemas/{name} are allowed. When false, the UIs
	// generate the schema for the operator to add to config.toml instead.
	CustomAgentsAPIEnabled bool `json:"custom_agents_api_enabled"`
}

var (
	errCustomAgentsAPIDisabled = errors.New("custom agents can only be defined in config.toml: set [ai] allow_custom_agents_api = true there to manage them from the app")
	errCustomAgentNotFound     = errors.New("custom agent not found")
)

// errAgentInUse is returned when a custom agent that is still selected
// somewhere would be deleted.
type errAgentInUse struct{ refs []string }

func (e errAgentInUse) Error() string {
	return "the agent is still selected by " + strings.Join(e.refs, ", ") + "; pick another agent there first"
}

// customAgentsAPIEnabled reads the opt-in from config.toml itself, the
// trusted layer, rather than from anything the API can write.
func (srv *Server) customAgentsAPIEnabled() bool {
	if srv.configPath == "" {
		return false
	}
	m, err := config.ReadTOMLMap(srv.configPath)
	if err != nil {
		return false
	}
	return config.CustomAgentsAPIEnabled(m)
}

func allAgentSchemas() []agentSchemaView {
	var out []agentSchemaView
	for _, s := range agentschema.Builtins() {
		out = append(out, agentSchemaView{Schema: s, Builtin: true})
	}
	for _, a := range executor.CustomAgents() {
		out = append(out, agentSchemaView{Schema: a.Schema})
	}
	return out
}

// handleListAgentSchemas serves every agent's schema: the built-in ones and
// the custom agents of the live config.
func (srv *Server) handleListAgentSchemas(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, agentSchemasResponse{
		Schemas:                allAgentSchemas(),
		CustomAgentsAPIEnabled: srv.customAgentsAPIEnabled(),
	})
}

func (srv *Server) handleGetAgentSchema(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	for _, s := range allAgentSchemas() {
		if s.Name == name {
			writeJSON(w, http.StatusOK, s)
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("no agent %q", name)})
}

// handlePutAgentSchema creates or replaces a custom agent: its binary and
// schema are written to [ai.agents.<name>] in config.toml. Only allowed when
// config.toml opts in with [ai] allow_custom_agents_api = true. Option values
// the new schema no longer declares, or no longer accepts, are dropped.
func (srv *Server) handlePutAgentSchema(w http.ResponseWriter, r *http.Request) {
	if srv.configPath == "" {
		http.Error(w, `{"error":"custom agents cannot be edited — configPath not set"}`, http.StatusServiceUnavailable)
		return
	}
	name := chi.URLParam(r, "name")
	if !agentschema.IsAgentName(name) || config.ReservedAgentName(name) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("%q is not a valid custom agent name (lowercase letters, digits, '-' or '_', starting with a letter, and not a built-in agent)", name),
		})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, agentschema.MaxSchemaBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid schema: " + err.Error()})
		return
	}
	schema, err := agentschema.Parse(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if schema.Name == "" {
		schema.Name = name
	}
	if schema.Name != name {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("schema name %q does not match %q", schema.Name, name),
		})
		return
	}
	if err := schema.ValidateCustom(config.ReservedAgentName); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	schemaMap, err := schemaToMap(schema)
	if err != nil {
		http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
		return
	}
	result, err := srv.patchTOML(func(m map[string]any) error {
		if !config.CustomAgentsAPIEnabled(m) {
			return errCustomAgentsAPIDisabled
		}
		agents := childMap(childMap(m, "ai"), "agents")
		entry, _ := agents[name].(map[string]any)
		if entry == nil {
			entry = map[string]any{}
		}
		for key := range entry {
			for _, def := range []string{"binary", "schema", "schema_path"} {
				if strings.EqualFold(key, def) {
					delete(entry, key)
				}
			}
		}
		pruneCustomAgentValues(entry, schema)
		entry["binary"] = schema.Binary
		entry["schema"] = schemaMap
		agents[name] = entry
		return nil
	})
	srv.writeCustomAgentResult(w, "PUT /agent-schemas", result, err)
}

// handleDeleteAgentSchema removes a custom agent from config.toml. An agent
// still selected as a primary/fallback agent or by a flow is not removed.
func (srv *Server) handleDeleteAgentSchema(w http.ResponseWriter, r *http.Request) {
	if srv.configPath == "" {
		http.Error(w, `{"error":"custom agents cannot be edited — configPath not set"}`, http.StatusServiceUnavailable)
		return
	}
	name := chi.URLParam(r, "name")
	result, err := srv.patchTOML(func(m map[string]any) error {
		if !config.CustomAgentsAPIEnabled(m) {
			return errCustomAgentsAPIDisabled
		}
		ai, _ := m["ai"].(map[string]any)
		agents, _ := ai["agents"].(map[string]any)
		entry, _ := agents[name].(map[string]any)
		if entry == nil || !tableHasKey(entry, "binary") {
			return errCustomAgentNotFound
		}
		refs, err := config.AgentReferencesInMap(m, name)
		if err != nil {
			return &config.ValidationError{Err: err}
		}
		if len(refs) > 0 {
			return errAgentInUse{refs: refs}
		}
		delete(agents, name)
		return nil
	})
	srv.writeCustomAgentResult(w, "DELETE /agent-schemas", result, err)
}

func (srv *Server) writeCustomAgentResult(w http.ResponseWriter, op string, result map[string]any, err error) {
	var inUse errAgentInUse
	switch {
	case errors.Is(err, errCustomAgentsAPIDisabled):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
	case errors.Is(err, errCustomAgentNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.As(err, &inUse):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	default:
		srv.writeConfigResult(w, op, result, err)
	}
}

func tableHasKey(m map[string]any, key string) bool {
	for k := range m {
		if strings.EqualFold(k, key) {
			return true
		}
	}
	return false
}

// schemaToMap turns a schema into the generic map written to config.toml.
func schemaToMap(s *agentschema.Schema) (map[string]any, error) {
	data, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	config.NormalizeNumbers(m)
	return m, nil
}

// pruneCustomAgentValues drops the option values of a custom agent's table
// that schema does not declare or no longer accepts, so replacing a schema
// never leaves the config invalid.
func pruneCustomAgentValues(entry map[string]any, schema *agentschema.Schema) {
	prune := func(table map[string]any, shared bool) {
		for key, value := range table {
			if shared && config.IsCustomAgentSharedKey(key) {
				if strings.EqualFold(key, agentschema.ModelKey) {
					if _, err := schema.ValidateValue(agentschema.ModelKey, value); err != nil {
						delete(table, key)
					}
				}
				continue
			}
			field, declared := schema.Field(key)
			if _, err := schema.ValidateValue(key, value); err != nil || (declared && field.Warning && value == true) {
				// A dangerous toggle turned on stays a config.toml decision,
				// even when a new schema is what made it dangerous.
				delete(table, key)
			}
		}
	}
	prune(entry, true)
	for key, raw := range entry {
		if strings.EqualFold(key, "options") {
			if options, ok := raw.(map[string]any); ok {
				prune(options, false)
			}
		}
	}
}

// validateCustomAgentPatchKeys checks a PATCH of a custom agent's settings:
// its option values against its schema, and the shared settings as for
// every agent. A warning option (a dangerous toggle) may only be turned off,
// like dangerously_skip_perms.
func validateCustomAgentPatchKeys(agent executor.CustomAgent, patch map[string]any, path string) error {
	for key, value := range patch {
		keyPath := path + "." + key
		if config.IsCustomAgentSharedKey(key) {
			if canonical := canonicalSharedKey(key); canonical != key {
				return fmt.Errorf("config PATCH key %q must use canonical casing %q", keyPath, path+"."+canonical)
			}
		}
		switch key {
		case agentschema.ModelKey:
			v, err := agent.Schema.ValidateValue(key, value)
			if err != nil {
				return fmt.Errorf("config PATCH key %q: %w", keyPath, err)
			}
			patch[key] = v
		case "extra_flags":
			flags, ok := value.(string)
			if !ok {
				return fmt.Errorf("config PATCH key %q must be a string", keyPath)
			}
			if err := executor.ValidateExtraFlagsForCLI(agent.Name, flags); err != nil {
				return fmt.Errorf("config PATCH key %q: %w", keyPath, err)
			}
		case "prompt":
			if _, ok := value.(string); !ok {
				return fmt.Errorf("config PATCH key %q must be a string", keyPath)
			}
		case "execution_timeout":
			s, ok := value.(string)
			if !ok {
				return fmt.Errorf("config PATCH key %q must be a string", keyPath)
			}
			if s != "" {
				if d, err := time.ParseDuration(s); err != nil || d <= 0 {
					return fmt.Errorf("config PATCH key %q must be a positive duration, e.g. 20m", keyPath)
				}
			}
		case "review_limits":
			// Validated with the rest of the config before the write.
		default:
			field, ok := agent.Schema.Field(key)
			if !ok {
				return fmt.Errorf("config PATCH key %q: %s's schema has no option %q", keyPath, agent.Name, key)
			}
			if field.Warning && value == true {
				return fmt.Errorf(
					"config PATCH key %q cannot be enabled via HTTP API; configure it directly in config.toml",
					keyPath)
			}
			v, err := agent.Schema.ValidateValue(key, value)
			if err != nil {
				return fmt.Errorf("config PATCH key %q: %w", keyPath, err)
			}
			if n, isNum := v.(float64); isNum && n == float64(int64(n)) {
				v = int64(n)
			}
			patch[key] = v
		}
	}
	return nil
}

// customAgentExtraFlagsInPatch lists the custom agents' extra_flags a PATCH
// /config sets. Heimdallm cannot know which flags of an arbitrary CLI are
// safe, so changing them over HTTP takes the same opt-in as defining the
// agent: otherwise a PATCH could turn off a wrapped CLI's sandbox or
// approvals, which dangerously_skip_perms (M-5) forbids for built-in agents.
func customAgentExtraFlagsInPatch(patch map[string]any) []string {
	ai, _ := patch["ai"].(map[string]any)
	agents, _ := ai["agents"].(map[string]any)
	var out []string
	for name, raw := range agents {
		agent, ok := raw.(map[string]any)
		if !ok || !executor.IsCustomAgent(name) {
			continue
		}
		if _, ok := agent["extra_flags"]; ok {
			out = append(out, "ai.agents."+name+".extra_flags")
		}
	}
	sort.Strings(out)
	return out
}

func canonicalSharedKey(key string) string {
	for _, k := range []string{agentschema.ModelKey, "prompt", "extra_flags", "execution_timeout", "review_limits"} {
		if strings.EqualFold(k, key) {
			return k
		}
	}
	return key
}

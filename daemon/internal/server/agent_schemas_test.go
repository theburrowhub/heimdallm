package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heimdallm/daemon/internal/agentschema"
	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/executor"
	"github.com/heimdallm/daemon/internal/server"
)

const aiderSchemaBody = `{
  "name": "aider",
  "display_name": "Aider",
  "binary": "aider",
  "fields": [
    {"key": "model", "type": "select", "label": "Model", "flag": "--model", "options": ["gpt-4o", "deepseek-chat"]},
    {"key": "auto_commits", "type": "bool", "label": "--auto-commits", "flag": "--auto-commits", "false_flag": "--no-auto-commits", "default": true},
    {"key": "edit_format", "type": "select", "label": "--edit-format", "flag": "--edit-format", "options": ["whole", "diff"]},
    {"key": "map_tokens", "type": "number", "label": "--map-tokens", "flag": "--map-tokens", "min": 0, "max": 8192},
    {"key": "yolo", "type": "bool", "label": "--yes-always", "flag": "--yes-always", "warning": true}
  ]
}`

// customAgentServer serves a config.toml that declares the aider custom agent
// (registered with the executor, as the daemon's reload would) and optionally
// opts in to the /agent-schemas write API.
func customAgentServer(t *testing.T, optIn bool, extra string) (*server.Server, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "aider.schema.json"), []byte(aiderSchemaBody), 0o644); err != nil {
		t.Fatal(err)
	}
	content := "[ai]\nprimary = \"claude\"\n"
	if optIn {
		content += "allow_custom_agents_api = true\n"
	}
	content += "\n[ai.agents.aider]\nbinary = \"aider\"\nschema_path = \"aider.schema.json\"\nedit_format = \"diff\"\n" + extra
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := executor.SetCustomAgents(cfg.CustomAgents()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = executor.SetCustomAgents(nil) })
	srv := setupServerWithToken(t, "tok")
	srv.SetConfigPath(path)
	return srv, path
}

func agentReq(t *testing.T, srv *server.Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Heimdallm-Token", "tok")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	return w
}

func readAgentTable(t *testing.T, path, name string) map[string]any {
	t.Helper()
	m, err := config.ReadTOMLMap(path)
	if err != nil {
		t.Fatal(err)
	}
	ai, _ := m["ai"].(map[string]any)
	agents, _ := ai["agents"].(map[string]any)
	table, _ := agents[name].(map[string]any)
	return table
}

func TestListAgentSchemas(t *testing.T) {
	srv, _ := customAgentServer(t, false, "")
	w := agentReq(t, srv, "GET", "/agent-schemas", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var body struct {
		Schemas []struct {
			Name    string `json:"name"`
			Binary  string `json:"binary"`
			Builtin bool   `json:"builtin"`
			Fields  []agentschema.Field
		} `json:"schemas"`
		Enabled bool `json:"custom_agents_api_enabled"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Enabled {
		t.Fatal("the write API is off unless config.toml opts in")
	}
	byName := map[string]bool{}
	for _, s := range body.Schemas {
		byName[s.Name] = s.Builtin
		if s.Name == "aider" && (s.Binary != "aider" || len(s.Fields) != 5) {
			t.Fatalf("aider schema: %+v", s)
		}
	}
	if !byName["claude"] || byName["aider"] || len(body.Schemas) != 8 {
		t.Fatalf("schemas: %v", byName)
	}

	if w := agentReq(t, srv, "GET", "/agent-schemas/aider", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"builtin":false`) {
		t.Fatalf("GET one: %d %s", w.Code, w.Body)
	}
	if w := agentReq(t, srv, "GET", "/agent-schemas/nope", ""); w.Code != http.StatusNotFound {
		t.Fatalf("GET unknown: %d", w.Code)
	}
}

func TestListAgentSchemasWithoutConfigPath(t *testing.T) {
	srv := setupServerWithToken(t, "tok")
	w := agentReq(t, srv, "GET", "/agent-schemas", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"custom_agents_api_enabled":false`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	for _, method := range []string{"PUT", "DELETE"} {
		if w := agentReq(t, srv, method, "/agent-schemas/aider", aiderSchemaBody); w.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s without config path: %d", method, w.Code)
		}
	}
}

func TestPutAgentSchemaRequiresTheOptIn(t *testing.T) {
	srv, path := customAgentServer(t, false, "")
	before, _ := os.ReadFile(path)
	w := agentReq(t, srv, "PUT", "/agent-schemas/tool", `{"binary":"tool","fields":[]}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if w := agentReq(t, srv, "DELETE", "/agent-schemas/aider", ""); w.Code != http.StatusForbidden {
		t.Fatalf("DELETE status %d", w.Code)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("a refused request must not touch config.toml")
	}
}

func TestPutAgentSchemaWritesTheDefinition(t *testing.T) {
	srv, path := customAgentServer(t, true, "map_tokens = 4096\nmodel = \"gpt-4o\"\n")
	// The new schema drops map_tokens and model, and narrows edit_format.
	body := `{"display_name":"Aider 2","binary":"aider","fields":[
	  {"key":"edit_format","type":"select","label":"Format","flag":"--edit-format","options":["whole"]},
	  {"key":"auto_commits","type":"bool","label":"Commits","flag":"--auto-commits"}]}`
	w := agentReq(t, srv, "PUT", "/agent-schemas/aider", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	table := readAgentTable(t, path, "aider")
	if table["binary"] != "aider" || table["schema_path"] != nil {
		t.Fatalf("definition not replaced: %v", table)
	}
	for _, gone := range []string{"edit_format", "map_tokens", "model"} {
		if _, ok := table[gone]; ok {
			t.Errorf("%s must be dropped: the new schema no longer accepts it (%v)", gone, table)
		}
	}
	schema, _ := table["schema"].(map[string]any)
	if schema["display_name"] != "Aider 2" {
		t.Fatalf("inline schema not written: %v", schema)
	}
	// The written file loads.
	if _, err := config.Load(path); err != nil {
		t.Fatalf("written config does not load: %v", err)
	}

	// A brand-new agent.
	w = agentReq(t, srv, "PUT", "/agent-schemas/tool", `{"name":"tool","binary":"tool-cli","fields":[{"key":"fast","type":"bool","label":"Fast","flag":"--fast"}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("new agent: %d %s", w.Code, w.Body)
	}
	if readAgentTable(t, path, "tool")["binary"] != "tool-cli" {
		t.Fatal("new agent not written")
	}
}

func TestPutAgentSchemaRejects(t *testing.T) {
	srv, _ := customAgentServer(t, true, "")
	cases := map[string]struct{ name, body string }{
		"builtin name":  {"claude", `{"binary":"claude","fields":[]}`},
		"catalog name":  {"cursor", `{"binary":"cursor","fields":[]}`},
		"invalid name":  {"Bad_Name", `{"binary":"x","fields":[]}`},
		"bad json":      {"tool", `{`},
		"name mismatch": {"tool", `{"name":"other","binary":"tool","fields":[]}`},
		"shell binary":  {"tool", `{"binary":"bash","fields":[]}`},
		"path binary":   {"tool", `{"binary":"../tool","fields":[]}`},
		"no flag":       {"tool", `{"binary":"tool","fields":[{"key":"a","type":"bool","label":"A"}]}`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if w := agentReq(t, srv, "PUT", "/agent-schemas/"+tc.name, tc.body); w.Code != http.StatusBadRequest {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
		})
	}
}

func TestDeleteAgentSchema(t *testing.T) {
	srv, path := customAgentServer(t, true, "")
	if w := agentReq(t, srv, "DELETE", "/agent-schemas/claude", ""); w.Code != http.StatusNotFound {
		t.Fatalf("a built-in agent is not a custom one: %d", w.Code)
	}
	// Selected as the fallback: refused.
	if w := agentReq(t, srv, "PATCH", "/config", `{"ai":{"fallback":"aider"}}`); w.Code != http.StatusOK {
		t.Fatalf("PATCH fallback: %d %s", w.Code, w.Body)
	}
	if w := agentReq(t, srv, "DELETE", "/agent-schemas/aider", ""); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "ai.fallback") {
		t.Fatalf("in use: %d %s", w.Code, w.Body)
	}
	if w := agentReq(t, srv, "PATCH", "/config", `{"ai":{"fallback":"gemini"}}`); w.Code != http.StatusOK {
		t.Fatalf("PATCH fallback: %d %s", w.Code, w.Body)
	}
	if w := agentReq(t, srv, "DELETE", "/agent-schemas/aider", ""); w.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if readAgentTable(t, path, "aider") != nil {
		t.Fatal("agent not removed")
	}
	if w := agentReq(t, srv, "DELETE", "/agent-schemas/aider", ""); w.Code != http.StatusNotFound {
		t.Fatalf("second delete: %d", w.Code)
	}
}

func TestPatchCustomAgentOptions(t *testing.T) {
	srv, path := customAgentServer(t, false, "")
	w := agentReq(t, srv, "PATCH", "/config", `{"ai":{"agents":{"aider":{
	  "model":" deepseek-chat ","auto_commits":false,"edit_format":"whole","map_tokens":2048,
	  "yolo":false,"prompt":"p1","execution_timeout":"5m"}}}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	table := readAgentTable(t, path, "aider")
	if table["model"] != "deepseek-chat" || table["auto_commits"] != false || table["edit_format"] != "whole" ||
		table["map_tokens"] != int64(2048) || table["prompt"] != "p1" {
		t.Fatalf("written values: %#v", table)
	}
}

func TestPatchCustomAgentExtraFlagsNeedTheOptIn(t *testing.T) {
	srv, path := customAgentServer(t, false, "")
	w := agentReq(t, srv, "PATCH", "/config", `{"ai":{"agents":{"aider":{"extra_flags":"--verbose"}}}}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "allow_custom_agents_api") {
		t.Fatalf("without the opt-in: %d %s", w.Code, w.Body)
	}
	if _, set := readAgentTable(t, path, "aider")["extra_flags"]; set {
		t.Fatal("a refused PATCH must not write extra_flags")
	}

	srv, path = customAgentServer(t, true, "")
	if w := agentReq(t, srv, "PATCH", "/config", `{"ai":{"agents":{"aider":{"extra_flags":"--verbose"}}}}`); w.Code != http.StatusOK {
		t.Fatalf("with the opt-in: %d %s", w.Code, w.Body)
	}
	if readAgentTable(t, path, "aider")["extra_flags"] != "--verbose" {
		t.Fatal("extra_flags not written")
	}
	// The generic policy still applies under the opt-in.
	if w := agentReq(t, srv, "PATCH", "/config", `{"ai":{"agents":{"aider":{"extra_flags":"positional"}}}}`); w.Code != http.StatusBadRequest {
		t.Fatalf("policy under the opt-in: %d %s", w.Code, w.Body)
	}
}

func TestPutAgentSchemaDropsAWarningToggleThatIsOn(t *testing.T) {
	// fast is an ordinary toggle, on; the new schema marks it dangerous.
	srv, path := customAgentServer(t, true, "")
	body := `{"binary":"aider","fields":[{"key":"fast","type":"bool","label":"Fast","flag":"--fast"}]}`
	if w := agentReq(t, srv, "PUT", "/agent-schemas/aider", body); w.Code != http.StatusOK {
		t.Fatalf("first PUT: %d %s", w.Code, w.Body)
	}
	// Turn it on in config.toml, as the operator would.
	m, err := config.ReadTOMLMap(path)
	if err != nil {
		t.Fatal(err)
	}
	m["ai"].(map[string]any)["agents"].(map[string]any)["aider"].(map[string]any)["fast"] = true
	if err := config.AtomicWriteTOML(path, m); err != nil {
		t.Fatal(err)
	}
	body = `{"binary":"aider","fields":[{"key":"fast","type":"bool","label":"Fast","flag":"--fast","warning":true}]}`
	if w := agentReq(t, srv, "PUT", "/agent-schemas/aider", body); w.Code != http.StatusOK {
		t.Fatalf("second PUT: %d %s", w.Code, w.Body)
	}
	if _, kept := readAgentTable(t, path, "aider")["fast"]; kept {
		t.Fatal("a toggle that became a warning toggle must not stay on")
	}
}

func TestPatchCustomAgentRejects(t *testing.T) {
	srv, path := customAgentServer(t, false, "")
	before, _ := os.ReadFile(path)
	cases := map[string]string{
		"binary":           `{"ai":{"agents":{"aider":{"binary":"bash"}}}}`,
		"binary casing":    `{"ai":{"agents":{"aider":{"Binary":"bash"}}}}`,
		"schema":           `{"ai":{"agents":{"aider":{"schema":"{}"}}}}`,
		"schema_path":      `{"ai":{"agents":{"aider":{"schema_path":"/etc/passwd"}}}}`,
		"options":          `{"ai":{"agents":{"aider":{"options":{"edit_format":"whole"}}}}}`,
		"new agent":        `{"ai":{"agents":{"newtool":{"binary":"newtool"}}}}`,
		"binary on claude": `{"ai":{"agents":{"claude":{"binary":"claude"}}}}`,
		"opt-in":           `{"ai":{"allow_custom_agents_api":true}}`,
		"opt-in casing":    `{"ai":{"Allow_Custom_Agents_API":true}}`,
		"unknown option":   `{"ai":{"agents":{"aider":{"nope":1}}}}`,
		"bad option":       `{"ai":{"agents":{"aider":{"edit_format":"patch"}}}}`,
		"warning on":       `{"ai":{"agents":{"aider":{"yolo":true}}}}`,
		"model not listed": `{"ai":{"agents":{"aider":{"model":"other"}}}}`,
		"bad flags":        `{"ai":{"agents":{"aider":{"extra_flags":"--model=x"}}}}`,
		"flags type":       `{"ai":{"agents":{"aider":{"extra_flags":1}}}}`,
		"prompt type":      `{"ai":{"agents":{"aider":{"prompt":1}}}}`,
		"timeout type":     `{"ai":{"agents":{"aider":{"execution_timeout":1}}}}`,
		"bad timeout":      `{"ai":{"agents":{"aider":{"execution_timeout":"-1m"}}}}`,
		"shared casing":    `{"ai":{"agents":{"aider":{"Extra_Flags":"--verbose"}}}}`,
		"agent casing":     `{"ai":{"agents":{"AIDER":{"edit_format":"whole"}}}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if w := agentReq(t, srv, "PATCH", "/config", body); w.Code != http.StatusBadRequest {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
		})
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("a refused PATCH must not touch config.toml")
	}
	// review_limits is validated with the whole config, not here.
	if w := agentReq(t, srv, "PATCH", "/config", `{"ai":{"agents":{"aider":{"review_limits":{"per_day":3}}}}}`); w.Code != http.StatusOK {
		t.Fatalf("review_limits: %d %s", w.Code, w.Body)
	}
}

func TestPutConfigCustomAgentAcceptsOnlySharedSettings(t *testing.T) {
	srv, _ := customAgentServer(t, false, "")
	ok := `{"agent_configs":{"aider":{"model":"gpt-4o","prompt":"p","execution_timeout":"3m"}}}`
	if w := agentReq(t, srv, "PUT", "/config", ok); w.Code != http.StatusOK {
		t.Fatalf("shared settings: %d %s", w.Code, w.Body)
	}
	for name, body := range map[string]string{
		"option":    `{"agent_configs":{"aider":{"edit_format":"whole"}}}`,
		"flags":     `{"agent_configs":{"aider":{"extra_flags":"--verbose"}}}`,
		"effort":    `{"agent_configs":{"aider":{"effort":"high"}}}`,
		"bad model": `{"agent_configs":{"aider":{"model":"unknown-model"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if w := agentReq(t, srv, "PUT", "/config", body); w.Code != http.StatusBadRequest {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
		})
	}
}

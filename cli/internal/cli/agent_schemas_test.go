package cli

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theburrowhub/heimdallm/cli/internal/api"
)

const testSchemas = `{"custom_agents_api_enabled":%s,"schemas":[
 {"name":"claude","builtin":true,"fields":[
   {"key":"effort","type":"select","label":"--effort","flag":"--effort","options":["low","high"]},
   {"key":"max_turns","type":"number","label":"--max-turns","flag":"--max-turns","min":0},
   {"key":"dangerously_skip_perms","type":"bool","label":"danger","flag":"--dangerously-skip-permissions","warning":true}]},
 {"name":"aider","binary":"aider","fields":[{"key":"notes","type":"text","label":"Notes","flag":"--notes"}]}]}`

type schemaDaemon struct {
	apiEnabled bool
	fail       bool
	requests   []string
	bodies     []string
}

func (f *schemaDaemon) serve(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		f.bodies = append(f.bodies, string(data))
		switch {
		case f.fail:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"boom"}`))
		case r.Method == "GET" && r.URL.Path == "/agent-schemas":
			enabled := "false"
			if f.apiEnabled {
				enabled = "true"
			}
			_, _ = w.Write([]byte(strings.Replace(testSchemas, "%s", enabled, 1)))
		case r.Method == "PUT" && !f.apiEnabled:
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"disabled"}`))
		case r.Method == "DELETE" && r.URL.Path == "/agent-schemas/ghost":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"custom agent not found"}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// runAgentsCmd runs `agents <args>` against the fake daemon.
func runAgentsCmd(t *testing.T, f *schemaDaemon, stdin string, args ...string) (string, error) {
	t.Helper()
	srv := f.serve(t)
	root := newAgentsCmd()
	sub, rest, err := root.Find(args)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	sub.SetOut(&out)
	sub.SetIn(strings.NewReader(stdin))
	sub.SetContext(contextWithClient(api.New(srv.URL, "")))
	if err := sub.ParseFlags(rest); err != nil {
		return "", err
	}
	if sub.Args != nil {
		if err := sub.Args(sub, sub.Flags().Args()); err != nil {
			return "", err
		}
	}
	err = sub.RunE(sub, sub.Flags().Args())
	return out.String(), err
}

func TestAgentsSubcommandsAreRegistered(t *testing.T) {
	root := newAgentsCmd()
	for _, name := range []string{"schema", "add", "remove", "set", "new"} {
		if c, _, err := root.Find([]string{name}); err != nil || c == root {
			t.Errorf("agents %s is not registered", name)
		}
	}
}

func TestAgentsSchemaCommand(t *testing.T) {
	out, err := runAgentsCmd(t, &schemaDaemon{}, "", "schema", "aider")
	if err != nil || !strings.Contains(out, `"binary": "aider"`) {
		t.Fatalf("schema aider: %v\n%s", err, out)
	}
	out, err = runAgentsCmd(t, &schemaDaemon{}, "", "schema")
	if err != nil || !strings.Contains(out, `"name": "claude"`) || !strings.Contains(out, `"name": "aider"`) {
		t.Fatalf("schema: %v\n%s", err, out)
	}
	if _, err := runAgentsCmd(t, &schemaDaemon{}, "", "schema", "nope"); err == nil {
		t.Fatal("an unknown agent must fail")
	}
	if _, err := runAgentsCmd(t, &schemaDaemon{fail: true}, "", "schema"); err == nil {
		t.Fatal("a daemon error must fail")
	}
}

const toolSchema = `{"name":"tool","binary":"tool-cli","fields":[{"key":"fast","type":"bool","label":"Fast","flag":"--fast"}]}`

func TestAgentsAddCommand(t *testing.T) {
	f := &schemaDaemon{apiEnabled: true}
	out, err := runAgentsCmd(t, f, toolSchema, "add", "-f", "-")
	if err != nil || !strings.Contains(out, "Saved custom agent tool") {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if f.requests[1] != "PUT /agent-schemas/tool" || !strings.Contains(f.bodies[1], `"binary":"tool-cli"`) {
		t.Fatalf("requests = %v %v", f.requests, f.bodies)
	}

	// A file, renamed with --name.
	path := filepath.Join(t.TempDir(), "s.json")
	if err := os.WriteFile(path, []byte(toolSchema), 0o600); err != nil {
		t.Fatal(err)
	}
	f = &schemaDaemon{apiEnabled: true}
	if _, err := runAgentsCmd(t, f, "", "add", "-f", path, "--name", "tool2"); err != nil {
		t.Fatal(err)
	}
	if f.requests[1] != "PUT /agent-schemas/tool2" {
		t.Fatalf("requests = %v", f.requests)
	}

	// Without the opt-in: the section to paste, and an error.
	out, err = runAgentsCmd(t, &schemaDaemon{}, toolSchema, "add", "-f", "-")
	if err == nil || !strings.Contains(out, "[ai.agents.tool]\nbinary = \"tool-cli\"") {
		t.Fatalf("add without opt-in: %v\n%s", err, out)
	}

	for name, stdin := range map[string]string{
		"not json":  "{",
		"invalid":   `{"name":"claude","binary":"x","fields":[]}`,
		"too large": `{"name":"` + strings.Repeat("a", maxSchemaFile) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := runAgentsCmd(t, &schemaDaemon{apiEnabled: true}, stdin, "add", "-f", "-"); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	if _, err := runAgentsCmd(t, &schemaDaemon{}, "", "add", "-f", filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("a missing file must fail")
	}
	if _, err := runAgentsCmd(t, &schemaDaemon{fail: true}, toolSchema, "add", "-f", "-"); err == nil {
		t.Fatal("a daemon error must fail")
	}
}

func TestAgentsRemoveCommand(t *testing.T) {
	f := &schemaDaemon{apiEnabled: true}
	out, err := runAgentsCmd(t, f, "", "remove", "tool")
	if err != nil || !strings.Contains(out, "Removed custom agent tool") || f.requests[0] != "DELETE /agent-schemas/tool" {
		t.Fatalf("remove: %v %v\n%s", err, f.requests, out)
	}
	if _, err := runAgentsCmd(t, f, "", "remove", "ghost"); err == nil || !strings.Contains(err.Error(), "custom agent not found") {
		t.Fatalf("remove ghost = %v", err)
	}
}

func TestAgentsSetCommand(t *testing.T) {
	f := &schemaDaemon{}
	out, err := runAgentsCmd(t, f, "", "set", "claude", "effort=high", "max_turns=", "dangerously_skip_perms=false")
	if err != nil || !strings.Contains(out, "Updated claude") {
		t.Fatalf("set: %v\n%s", err, out)
	}
	want := `{"ai":{"agents":{"claude":{"dangerously_skip_perms":false,"effort":"high","max_turns":0}}}}`
	if f.requests[1] != "PATCH /config" || f.bodies[1] != want {
		t.Fatalf("PATCH = %v %s", f.requests, f.bodies[1])
	}
	if _, err := runAgentsCmd(t, &schemaDaemon{}, "", "set", "aider", "notes="); err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string][]string{
		"unknown agent": {"set", "nope", "a=b"},
		"unknown key":   {"set", "claude", "nope=1"},
		"no equals":     {"set", "claude", "effort"},
		"bad value":     {"set", "claude", "effort=extreme"},
		"turn on":       {"set", "claude", "dangerously_skip_perms=true"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := runAgentsCmd(t, &schemaDaemon{}, "", args...); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	if _, err := runAgentsCmd(t, &schemaDaemon{fail: true}, "", "set", "claude", "effort=high"); err == nil {
		t.Fatal("a daemon error must fail")
	}
}

func TestAgentsNewNeedsTheSchemas(t *testing.T) {
	if _, err := runAgentsCmd(t, &schemaDaemon{fail: true}, "", "new"); err == nil {
		t.Fatal("the wizard needs the daemon's schemas")
	}
}

func TestConfigSanitizesAgentSettings(t *testing.T) {
	lines := strings.Join(cfgAILines(map[string]any{"agent_configs": map[string]any{
		"aider": map[string]any{"binary": "aider", "options": map[string]any{"notes": "a\x1b]0;x\x07b"}},
	}}), "\n")
	if strings.Contains(lines, "\x1b") || !strings.Contains(lines, "Notes") {
		t.Fatalf("lines = %q", lines)
	}
}

func TestPrintAgentsShowsCustomAgents(t *testing.T) {
	var buf bytes.Buffer
	printAgents(&buf, &api.CLIAgentCatalog{Agents: []api.CLIAgent{
		{ID: "aider", Name: "Aider", Kind: "custom", Installed: true, Configured: true, Binary: "aider"},
	}})
	if !strings.Contains(buf.String(), "configured, custom: aider") {
		t.Fatalf("out = %s", buf.String())
	}
}

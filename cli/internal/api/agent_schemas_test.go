package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func ptr(f float64) *float64 { return &f }

const schemasJSON = `{"custom_agents_api_enabled":true,"schemas":[
 {"name":"claude","display_name":"Claude Code","builtin":true,"fields":[{"key":"model","type":"select","label":"Model","flag":"--model"}]},
 {"name":"aider","binary":"aider","fields":[{"key":"edit_format","type":"select","label":"Format","flag":"--edit-format","options":["whole","diff"]}]}]}`

func TestAgentSchemaEndpoints(t *testing.T) {
	var seen []string
	var body string
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		data, _ := io.ReadAll(r.Body)
		body = string(data)
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"nope"}`))
			return
		}
		_, _ = w.Write([]byte(schemasJSON))
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")

	cat, err := c.GetAgentSchemas()
	if err != nil {
		t.Fatal(err)
	}
	if !cat.CustomAgentsAPIEnabled || len(cat.Schemas) != 2 {
		t.Fatalf("catalog = %+v", cat)
	}
	claude, ok := cat.ByName("claude")
	if !ok || claude.Title() != "Claude Code" || !claude.Builtin {
		t.Fatalf("claude = %+v", claude)
	}
	if _, ok := cat.ByName("nope"); ok {
		t.Fatal("unknown agent found")
	}
	var nilCat *AgentSchemaCatalog
	if _, ok := nilCat.ByName("claude"); ok {
		t.Fatal("a nil catalog has no agents")
	}

	aider, _ := cat.ByName("aider")
	aider.Builtin = true
	if err := c.PutAgentSchema(aider); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "builtin") {
		t.Fatalf("PUT must not send builtin: %s", body)
	}
	if err := c.DeleteAgentSchema("aider"); err != nil {
		t.Fatal(err)
	}
	if err := c.PatchConfig(AgentSettingsPatch("aider", map[string]any{"edit_format": "diff"})); err != nil {
		t.Fatal(err)
	}
	if body != `{"ai":{"agents":{"aider":{"edit_format":"diff"}}}}` {
		t.Fatalf("PATCH body = %s", body)
	}
	want := []string{"GET /agent-schemas", "PUT /agent-schemas/aider", "DELETE /agent-schemas/aider", "PATCH /config"}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("requests = %v", seen)
	}

	status = http.StatusForbidden
	_, err = c.GetAgentSchemas()
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusForbidden || httpErr.Message() != "nope" {
		t.Fatalf("error = %v", err)
	}
	if httpErr.Error() != `HTTP 403: {"error":"nope"}` {
		t.Fatalf("Error() = %q", httpErr.Error())
	}
	if (&HTTPError{Status: 500, Body: "plain"}).Message() != "plain" {
		t.Fatal("a body that is not JSON is the message")
	}
	status = http.StatusOK
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{`)) })
	if _, err := c.GetAgentSchemas(); err == nil {
		t.Fatal("invalid JSON must fail")
	}
}

func TestParseValue(t *testing.T) {
	sel := AgentSchemaField{Key: "f", Type: FieldSelect, Options: []string{"a", "b"}}
	custom := AgentSchemaField{Key: "m", Type: FieldSelect, AllowCustom: true}
	num := AgentSchemaField{Key: "n", Type: FieldNumber, Min: ptr(1), Max: ptr(10)}
	onlyMax := AgentSchemaField{Key: "n", Type: FieldNumber, Max: ptr(10)}
	onlyMin := AgentSchemaField{Key: "n", Type: FieldNumber, Min: ptr(2)}
	unbounded := AgentSchemaField{Key: "n", Type: FieldNumber}
	toggle := AgentSchemaField{Key: "b", Type: FieldBool}
	danger := AgentSchemaField{Key: "d", Type: FieldBool, Warning: true}
	text := AgentSchemaField{Key: "t", Type: FieldText}

	for _, tc := range []struct {
		f    AgentSchemaField
		in   string
		want any
	}{
		{sel, "a", "a"}, {sel, "", nil}, {custom, "anything", "anything"},
		{num, "5", int64(5)}, {num, "2.5", 2.5}, {num, "", nil},
		{toggle, "true", true}, {toggle, "false", false}, {danger, "false", false},
		{text, " hi ", "hi"},
	} {
		got, err := tc.f.ParseValue(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("%s.ParseValue(%q) = %v, %v; want %v", tc.f.Key, tc.in, got, err, tc.want)
		}
	}
	for _, tc := range []struct {
		f    AgentSchemaField
		in   string
		want string
	}{
		{sel, "c", "not one of"}, {num, "x", "not a number"}, {num, "11", "(1–10)"},
		{onlyMax, "11", "(0–10)"}, {onlyMin, "1", "at least 2"}, {unbounded, "-1", "not negative"},
		{toggle, "maybe", "not true or false"}, {danger, "true", "config.toml"}, {text, "--x", "must not begin"},
	} {
		if _, err := tc.f.ParseValue(tc.in); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s.ParseValue(%q) error = %v, want %q", tc.f.Key, tc.in, err, tc.want)
		}
	}
}

func TestValidateCustom(t *testing.T) {
	good := AgentSchema{Name: "aider", Binary: "aider", Fields: []AgentSchemaField{
		{Key: "f", Type: FieldSelect, Label: "F", Flag: "--f", Options: []string{"a"}},
		{Key: "b", Type: FieldBool, Label: "B", Flag: "--b", FalseFlag: "--no-b"},
	}}
	if errs := good.ValidateCustom(map[string]bool{"claude": true}); len(errs) != 0 {
		t.Fatalf("valid schema: %v", errs)
	}
	bad := AgentSchema{Name: "claude", Binary: "/bin/x", Fields: []AgentSchemaField{
		{Key: "Bad", Type: FieldText, Label: "x", Flag: "--a"},
		{Key: "prompt", Type: FieldText, Label: "x", Flag: "--b"},
		{Key: "dup", Type: FieldText, Label: "", Flag: "x"},
		{Key: "dup", Type: FieldText, Label: "x", Flag: "--a"},
		{Key: "s", Type: FieldSelect, Label: "x", Flag: "--s"},
		{Key: "o", Type: FieldSelect, Label: "x", Flag: "--o", Options: []string{"-x"}},
	}}
	errs := bad.ValidateCustom(map[string]bool{"claude": true})
	for _, want := range []string{"built-in", "binary", "lowercase", "reserved", "used twice", "label", "--name", "flag --a is used twice", "needs options", "must not begin"} {
		if !strings.Contains(strings.Join(errs, "\n"), want) {
			t.Errorf("missing %q in %v", want, errs)
		}
	}
	if errs := (AgentSchema{Name: "Bad"}).ValidateCustom(nil); len(errs) != 2 {
		t.Fatalf("errors = %v", errs)
	}
	if !ValidAgentName("aider") || ValidAgentName("Aider") || !ValidBinary("aider") || ValidBinary("a/b") || !ValidFlag("-x") || ValidFlag("x") {
		t.Fatal("validators")
	}
}

func TestSchemaRendering(t *testing.T) {
	s := AgentSchema{Name: "aider", Binary: "aider", Builtin: true, Fields: []AgentSchemaField{{Key: "f", Type: FieldText, Label: "F", Flag: "--f"}}}
	toml := s.TOMLSnippet()
	if !strings.HasPrefix(toml, "[ai.agents.aider]\nbinary = \"aider\"\nschema = '''\n") {
		t.Fatalf("snippet = %s", toml)
	}
	inner := strings.TrimSuffix(strings.SplitN(toml, "'''\n", 2)[1], "\n'''\n")
	var decoded map[string]any
	if err := json.Unmarshal([]byte(inner), &decoded); err != nil {
		t.Fatalf("schema in the snippet is not JSON: %v", err)
	}
	if _, ok := decoded["binary"]; ok {
		t.Fatal("the binary is a TOML key, not part of the schema")
	}
	if strings.Contains(s.PrettyJSON(), "builtin") || !strings.Contains(s.PrettyJSON(), "\n  \"fields\"") {
		t.Fatalf("pretty = %s", s.PrettyJSON())
	}
	quoted := AgentSchema{Name: "q", Binary: "q", DisplayName: "it'''s"}.TOMLSnippet()
	body := strings.SplitN(quoted, "'''\n", 2)[1]
	body = body[:strings.LastIndex(body, "\n'''")]
	if strings.Contains(body, "'") {
		t.Fatalf("a quote reached the ''' string: %s", quoted)
	}
	var back AgentSchema
	if err := json.Unmarshal([]byte(body), &back); err != nil || back.DisplayName != "it'''s" {
		t.Fatalf("escaped schema = %v %+v", err, back)
	}
	if (AgentSchema{Name: "x"}).Title() != "x" {
		t.Fatal("Title falls back to the name")
	}
	if _, ok := s.Field("nope"); ok {
		t.Fatal("unknown field")
	}
}

func TestAgentConfigValues(t *testing.T) {
	got := AgentConfigValues(map[string]any{
		"model": "gpt-4o", "max_turns": 0.0, "bare": false, "dangerously_skip_perms": true,
		"effort": "", "review_limits": map[string]any{"per_day": 3.0}, "binary": "aider",
		"execution_timeout": "5m", "list": []any{"x"}, "nothing": nil,
		"options": map[string]any{"edit_format": "diff", "map_tokens": 1024.0, "auto_commits": false},
	})
	want := [][2]string{
		{"binary", "aider"}, {"dangerously_skip_perms", "true"}, {"execution_timeout", "5m"},
		{"model", "gpt-4o"}, {"edit_format", "diff"}, {"map_tokens", "1024"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AgentConfigValues = %v", got)
	}
	if SettingLabel("max_turns") != "Max turns" || SettingLabel("") != "" {
		t.Fatal("SettingLabel")
	}
	if FormatValue(nil) != "" || FormatValue(2.5) != "2.5" || FormatValue(true) != "true" {
		t.Fatal("FormatValue")
	}
}

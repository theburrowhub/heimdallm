package agentschema

import (
	"encoding/json"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
)

const aiderJSON = `{
  "name": "aider",
  "display_name": "Aider",
  "binary": "aider",
  "icon_color": "#123ABC",
  "fields": [
    {"key": "model", "type": "select", "label": "Model", "flag": "--model",
     "options": ["gpt-4o", "deepseek-chat"], "default": "gpt-4o"},
    {"key": "auto_commits", "type": "bool", "label": "--auto-commits", "flag": "--auto-commits",
     "false_flag": "--no-auto-commits", "default": true},
    {"key": "edit_format", "type": "select", "label": "--edit-format", "flag": "--edit-format",
     "options": ["whole", "diff", "udiff"], "default": "diff"},
    {"key": "map_tokens", "type": "number", "label": "--map-tokens", "flag": "--map-tokens",
     "min": 0, "max": 8192},
    {"key": "notes", "type": "text", "label": "Notes", "flag": "--message-file"},
    {"key": "unbounded", "type": "number", "label": "Unbounded", "flag": "--unbounded"}
  ]
}`

func mustParse(t *testing.T, data string) *Schema {
	t.Helper()
	s, err := Parse([]byte(data))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return s
}

func notBuiltin(string) bool { return false }

func TestParseAndValidateCustom(t *testing.T) {
	s := mustParse(t, aiderJSON)
	if err := s.ValidateCustom(notBuiltin); err != nil {
		t.Fatalf("ValidateCustom: %v", err)
	}
	if s.Fields[3].Min == nil || *s.Fields[3].Max != 8192 {
		t.Fatalf("bounds not parsed: %+v", s.Fields[3])
	}
	if d := s.Fields[1].Default; d != true {
		t.Fatalf("bool default = %#v", d)
	}
	if got := s.Flags(); !reflect.DeepEqual(got, []string{"--model", "--auto-commits", "--no-auto-commits", "--edit-format", "--map-tokens", "--message-file", "--unbounded"}) {
		t.Fatalf("Flags = %v", got)
	}
}

func TestParseRejects(t *testing.T) {
	for name, data := range map[string]string{
		"unknown attribute": `{"name":"x","fields":[{"key":"a","type":"bool","label":"A","defualt":true}]}`,
		"not json":          `{`,
		"trailing data":     `{"name":"x","fields":[]} {}`,
		"bad default":       `{"name":"x","fields":[{"key":"a","type":"bool","label":"A","default":"yes"}]}`,
		"too large":         `{"name":"x","description":"` + strings.Repeat("a", MaxSchemaBytes) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(data)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestFromMap(t *testing.T) {
	s, err := FromMap(map[string]any{
		"name": "x",
		"fields": []map[string]any{
			{"key": "n", "type": "number", "label": "N", "flag": "--n", "default": int64(3)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Fields[0].Default != 3.0 {
		t.Fatalf("number default = %#v, want 3.0", s.Fields[0].Default)
	}
	if _, err := FromMap(map[string]any{"bad": math.NaN()}); err == nil {
		t.Fatal("expected an error for an unrepresentable value")
	}
}

func field(mut func(*Field)) Schema {
	f := Field{Key: "opt", Type: TypeText, Label: "Opt", Flag: "--opt"}
	mut(&f)
	return Schema{Name: "tool", Binary: "tool", Fields: []Field{f}}
}

func ptr(v float64) *float64 { return &v }

func TestValidateRejects(t *testing.T) {
	cases := map[string]Schema{
		"bad name":          {Name: "Bad Name"},
		"long display name": {Name: "x", DisplayName: strings.Repeat("a", MaxLabelLen+1)},
		"control in desc":   {Name: "x", Description: "a\x00b"},
		"bad colour":        {Name: "x", IconColor: "red"},
		"too many fields": func() Schema {
			s := Schema{Name: "x"}
			for i := 0; i <= MaxFields; i++ {
				s.Fields = append(s.Fields, Field{Key: "f" + strings.Repeat("a", i%3) + string(rune('a'+i%26)), Type: TypeBool, Label: "F"})
			}
			return s
		}(),
		"duplicate key": {Name: "x", Fields: []Field{
			{Key: "a", Type: TypeBool, Label: "A", Flag: "--a"},
			{Key: "a", Type: TypeBool, Label: "A", Flag: "--b"},
		}},
		"duplicate flag": {Name: "x", Fields: []Field{
			{Key: "a", Type: TypeBool, Label: "A", Flag: "--a"},
			{Key: "b", Type: TypeBool, Label: "B", Flag: "--a"},
		}},
		"bad key":              field(func(f *Field) { f.Key = "Bad-Key" }),
		"reserved key":         field(func(f *Field) { f.Key = "extra_flags" }),
		"no label":             field(func(f *Field) { f.Label = " " }),
		"long label":           field(func(f *Field) { f.Label = strings.Repeat("a", MaxLabelLen+1) }),
		"long description":     field(func(f *Field) { f.Description = strings.Repeat("a", MaxDescriptionLen+1) }),
		"control empty label":  field(func(f *Field) { f.EmptyLabel = "\x01" }),
		"bad flag":             field(func(f *Field) { f.Flag = "model" }),
		"false flag on text":   field(func(f *Field) { f.FalseFlag = "--no-opt" }),
		"bad false flag":       field(func(f *Field) { f.Type = TypeBool; f.FalseFlag = "no" }),
		"danger flag":          field(func(f *Field) { f.Flag = "--dangerously-do-it" }),
		"allow danger flag":    field(func(f *Field) { f.Type = TypeBool; f.Flag = "--allow-dangerously-x" }),
		"select no options":    field(func(f *Field) { f.Type = TypeSelect }),
		"too many options":     field(func(f *Field) { f.Type = TypeSelect; f.Options = make([]string, MaxOptions+1) }),
		"option dash":          field(func(f *Field) { f.Type = TypeSelect; f.Options = []string{"-x"} }),
		"empty option":         field(func(f *Field) { f.Type = TypeSelect; f.Options = []string{""} }),
		"duplicate option":     field(func(f *Field) { f.Type = TypeSelect; f.Options = []string{"a", "a"} }),
		"min above max":        field(func(f *Field) { f.Type = TypeNumber; f.Min, f.Max = ptr(5), ptr(1) }),
		"infinite bound":       field(func(f *Field) { f.Type = TypeNumber; f.Max = ptr(math.Inf(1)) }),
		"negative bound":       field(func(f *Field) { f.Type = TypeNumber; f.Min = ptr(-5) }),
		"unknown type":         field(func(f *Field) { f.Type = "date" }),
		"options on text":      field(func(f *Field) { f.Options = []string{"a"} }),
		"allow custom on bool": field(func(f *Field) { f.Type = TypeBool; f.AllowCustom = true }),
		"min on text":          field(func(f *Field) { f.Min = ptr(1) }),
		"warning on text":      field(func(f *Field) { f.Warning = true }),
		"model as bool":        field(func(f *Field) { f.Key = "model"; f.Type = TypeBool }),
		"bad default":          field(func(f *Field) { f.Default = 3 }),
		"warning default on": field(func(f *Field) {
			f.Type, f.Warning, f.Default = TypeBool, true, true
		}),
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			if err := s.Validate(); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	var missing *Schema
	if err := missing.Validate(); err == nil {
		t.Fatal("a nil schema must not validate")
	}
}

func TestValidateAcceptsDangerousWarningBool(t *testing.T) {
	s := field(func(f *Field) {
		f.Type, f.Warning, f.Flag, f.Default = TypeBool, true, "--dangerously-skip-permissions", false
	})
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateCustomRejects(t *testing.T) {
	builtin := func(n string) bool { return n == "claude" }
	cases := map[string]Schema{
		"invalid":       {Name: "BAD"},
		"builtin name":  {Name: "claude", Binary: "claude"},
		"no binary":     {Name: "tool"},
		"path binary":   {Name: "tool", Binary: "/usr/bin/tool"},
		"shell binary":  {Name: "tool", Binary: "bash"},
		"field no flag": {Name: "tool", Binary: "tool", Fields: []Field{{Key: "a", Type: TypeBool, Label: "A"}}},
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			if err := s.ValidateCustom(builtin); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestValidateBinary(t *testing.T) {
	for _, ok := range []string{"aider", "my-agent", "agent_2", "tool.v2", "pythonic"} {
		if err := ValidateBinary(ok); err != nil {
			t.Errorf("ValidateBinary(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "../aider", "bin/aider", "/bin/aider", "-x", "a b", "bash", "BASH", "sh",
		"python", "python3", "python3.12", "perl5.36", "node", "env", "sudo", "osascript", "pwsh"} {
		if err := ValidateBinary(bad); err == nil {
			t.Errorf("ValidateBinary(%q) must fail", bad)
		}
	}
}

func TestValidateValues(t *testing.T) {
	s := mustParse(t, aiderJSON)
	got, err := s.ValidateValues(map[string]any{
		"model": " deepseek-chat ", "auto_commits": false, "map_tokens": int64(1024), "notes": "",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"model": "deepseek-chat", "auto_commits": false, "map_tokens": 1024.0, "notes": ""}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ValidateValues = %#v, want %#v", got, want)
	}
	for name, values := range map[string]map[string]any{
		"unknown key":    {"nope": true},
		"not an option":  {"edit_format": "patch"},
		"bool as string": {"auto_commits": "true"},
		"string as bool": {"model": true},
		"below min":      {"map_tokens": -1},
		"negative":       {"unbounded": -2},
		"above max":      {"map_tokens": json.Number("9000")},
		"bad number":     {"map_tokens": json.Number("x")},
		"number type":    {"map_tokens": "5"},
		"nan":            {"map_tokens": math.NaN()},
		"dash value":     {"notes": "--evil"},
		"bypass value":   {"notes": "BypassPermissions"},
		"control char":   {"notes": "a\nb"},
		"too long":       {"notes": strings.Repeat("a", MaxTextValueLen+1)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.ValidateValues(values); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestToFloatAcceptsEveryNumericKind(t *testing.T) {
	for _, v := range []any{float32(2), int(2), int8(2), int16(2), int32(2), int64(2), uint(2), uint8(2), uint16(2), uint32(2), uint64(2), json.Number("2"), 2.0} {
		if n, err := toFloat(v); err != nil || n != 2 {
			t.Errorf("toFloat(%T) = %v, %v", v, n, err)
		}
	}
	if _, err := normalizeValue(Field{Type: "date"}, "x"); err == nil {
		t.Error("an unknown type must not normalize")
	}
}

func TestAllowCustomSelect(t *testing.T) {
	s := &Schema{Name: "x", Fields: []Field{{Key: "model", Type: TypeSelect, Label: "Model", Flag: "--model", AllowCustom: true, Options: []string{"a"}}}}
	if _, err := s.ValidateValue("model", "anything-else"); err != nil {
		t.Fatalf("allow_custom must accept an unlisted value: %v", err)
	}
	if _, err := s.ValidateValue("missing", "x"); err == nil {
		t.Fatal("an unknown field must fail")
	}
}

func TestBuildArgs(t *testing.T) {
	s := mustParse(t, aiderJSON)
	args, err := s.BuildArgs(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"--model", "gpt-4o", "--auto-commits", "--edit-format", "diff"}; !reflect.DeepEqual(args, want) {
		t.Fatalf("defaults: %v, want %v", args, want)
	}
	args, err = s.BuildArgs(map[string]any{"auto_commits": false, "map_tokens": 2048.0, "notes": "notes.md", "edit_format": ""})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"--model", "gpt-4o", "--no-auto-commits", "--map-tokens", "2048", "--message-file", "notes.md"}; !reflect.DeepEqual(args, want) {
		t.Fatalf("values: %v, want %v", args, want)
	}
	args, _ = s.BuildArgs(map[string]any{"map_tokens": 0.5})
	if !strings.Contains(strings.Join(args, " "), "--map-tokens 0.5") {
		t.Fatalf("fractional numbers keep their digits: %v", args)
	}
	if _, err := s.BuildArgs(map[string]any{"edit_format": "patch"}); err == nil {
		t.Fatal("BuildArgs must refuse a value that fails validation")
	}
	// A bool without a false_flag that is off, and a zero number, add nothing.
	plain := &Schema{Name: "x", Fields: []Field{
		{Key: "b", Type: TypeBool, Label: "B", Flag: "--b"},
		{Key: "n", Type: TypeNumber, Label: "N", Flag: "--n", Default: 0.0},
	}}
	if args, _ := plain.BuildArgs(map[string]any{"b": false}); len(args) != 0 {
		t.Fatalf("expected no args, got %v", args)
	}
}

func TestCloneIsDeep(t *testing.T) {
	s := mustParse(t, aiderJSON)
	c := s.Clone()
	c.Fields[0].Options[0] = "changed"
	*c.Fields[3].Max = 1
	if s.Fields[0].Options[0] != "gpt-4o" || *s.Fields[3].Max != 8192 {
		t.Fatal("Clone shares state with the original")
	}
	var nilSchema *Schema
	if nilSchema.Clone() != nil {
		t.Fatal("Clone(nil) must be nil")
	}
	if _, ok := nilSchema.Field("x"); ok {
		t.Fatal("a nil schema has no fields")
	}
}

func TestBuiltins(t *testing.T) {
	if err := BuiltinError(); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range Builtins() {
		names = append(names, s.Name)
		if s.Binary != "" {
			t.Errorf("%s: built-in schemas must not declare a binary", s.Name)
		}
		if _, ok := s.Field(ModelKey); !ok {
			t.Errorf("%s: every built-in agent has a model setting", s.Name)
		}
	}
	want := []string{"claude", "codex", "copilot", "cursor_cli", "gemini", "opencode", "openrouter"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("Builtins = %v, want %v", names, want)
	}
	c, ok := Builtin("claude")
	if !ok {
		t.Fatal("claude schema missing")
	}
	c.Fields[0].Label = "changed"
	again, _ := Builtin("claude")
	if again.Fields[0].Label == "changed" {
		t.Fatal("Builtin must return a copy")
	}
	danger, _ := again.Field("dangerously_skip_perms")
	if !danger.Warning || danger.Type != TypeBool {
		t.Fatalf("dangerously_skip_perms must be a warning toggle: %+v", danger)
	}
	if _, ok := Builtin("aider"); ok {
		t.Fatal("aider is not built in")
	}
	if !IsAgentName("aider") || IsAgentName("Aider") {
		t.Fatal("IsAgentName")
	}
}

// testdata/custom_schema_cases.json is shared with the app's tests
// (flutter_app/test/core/models/agent_schema_test.dart), so the app's
// client-side checks cannot drift from these rules.
func TestSharedCustomSchemaCases(t *testing.T) {
	data, err := os.ReadFile("testdata/custom_schema_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Case   string          `json:"case"`
		Valid  bool            `json:"valid"`
		Schema json.RawMessage `json:"schema"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	builtin := func(n string) bool { return n == "claude" }
	for _, c := range cases {
		t.Run(c.Case, func(t *testing.T) {
			s, err := Parse(c.Schema)
			if err == nil {
				err = s.ValidateCustom(builtin)
			}
			if (err == nil) != c.Valid {
				t.Fatalf("valid = %v, want %v (err: %v)", err == nil, c.Valid, err)
			}
		})
	}
}

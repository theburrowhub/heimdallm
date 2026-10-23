// Package agentschema describes the options an AI agent accepts as data: a
// schema lists the agent's fields (dropdowns, toggles, numbers and text) with
// their types, defaults, allowed values and the CLI flag each one sets.
//
// The GUI and the TUI render an agent's settings form from its schema, and the
// daemon validates the values it stores against it. Built-in agents ship a
// schema that describes their typed settings; their command lines are still
// built by the executor. Custom agents — any CLI the operator declares in
// config.toml — are run from their schema alone: BuildArgs turns their values
// into flags.
package agentschema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode"
)

// FieldType is the kind of control a field renders as and the type of value
// it holds.
type FieldType string

// Field types.
const (
	TypeSelect FieldType = "select" // one string out of Options
	TypeBool   FieldType = "bool"   // a toggle
	TypeNumber FieldType = "number" // a number, optionally bounded by Min/Max
	TypeText   FieldType = "text"   // free text
)

// Field is one configurable option of an agent.
type Field struct {
	// Key is where the value is stored under [ai.agents.<name>].
	Key   string    `json:"key"`
	Type  FieldType `json:"type"`
	Label string    `json:"label"`
	// Description is the help text the UIs show next to the control.
	Description string `json:"description,omitempty"`
	// Flag is the CLI option the value is passed with, e.g. "--model".
	Flag string `json:"flag,omitempty"`
	// FalseFlag is passed when a bool field is off, for CLIs whose option is
	// on by default (aider's --no-auto-commits).
	FalseFlag string `json:"false_flag,omitempty"`
	// Options are the values a select field accepts.
	Options []string `json:"options,omitempty"`
	// AllowCustom lets a select field take a value outside Options too: the
	// options are then suggestions (models a CLI may not list).
	AllowCustom bool `json:"allow_custom,omitempty"`
	// Default is the value used when the operator has not set one.
	Default any `json:"default,omitempty"`
	// EmptyLabel names the empty choice of a select ("CLI default").
	EmptyLabel string   `json:"empty_label,omitempty"`
	Min        *float64 `json:"min,omitempty"`
	Max        *float64 `json:"max,omitempty"`
	// Warning marks a dangerous bool: the UIs and the HTTP API may only turn
	// it off. Turning it on takes a direct edit of config.toml.
	Warning bool `json:"warning,omitempty"`
}

// Schema describes one agent and its fields.
type Schema struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	Description string `json:"description,omitempty"`
	IconColor   string `json:"icon_color,omitempty"`
	// Binary is the executable a custom agent runs. Built-in agents leave it
	// empty: the executor knows their executables.
	Binary string  `json:"binary,omitempty"`
	Fields []Field `json:"fields"`
}

// Limits on a schema, so a typo or a hostile file cannot make the daemon or a
// UI render something unbounded.
const (
	MaxFields         = 64
	MaxOptions        = 200
	MaxLabelLen       = 128
	MaxDescriptionLen = 1000
	MaxOptionLen      = 128
	MaxTextValueLen   = 512
	MaxSchemaBytes    = 64 << 10
)

var (
	agentNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	fieldKeyRe  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	flagRe      = regexp.MustCompile(`^--?[A-Za-z0-9][A-Za-z0-9-]*$`)
	binaryRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	iconColorRe = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
)

// ReservedKeys are the settings every agent shares (or that define a custom
// agent). A schema field may not reuse them: their values live outside the
// schema's options. "model" is the exception — it is shared, but a schema
// declares it to say how its CLI takes it.
var ReservedKeys = map[string]bool{
	"binary":            true,
	"schema":            true,
	"schema_path":       true,
	"options":           true,
	"prompt":            true,
	"extra_flags":       true,
	"execution_timeout": true,
	"review_limits":     true,
}

// ModelKey is the shared model setting a schema may declare.
const ModelKey = "model"

// Parse decodes a schema from JSON, rejecting unknown attributes so a typo
// ("defualt") fails loudly instead of being ignored.
func Parse(data []byte) (*Schema, error) {
	if len(data) > MaxSchemaBytes {
		return nil, fmt.Errorf("agentschema: schema is %d bytes, at most %d", len(data), MaxSchemaBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	var s Schema
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("agentschema: invalid schema JSON: %w", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("agentschema: invalid schema JSON: trailing data after the schema")
	}
	if err := s.normalize(); err != nil {
		return nil, err
	}
	return &s, nil
}

// FromMap decodes a schema from a generic map (an inline TOML table or a
// decoded JSON body).
func FromMap(m map[string]any) (*Schema, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("agentschema: schema is not representable as JSON: %w", err)
	}
	return Parse(data)
}

// normalize converts every default and bound to the canonical Go types the
// rest of the package works with (string, bool, float64).
func (s *Schema) normalize() error {
	for i := range s.Fields {
		f := &s.Fields[i]
		if f.Default == nil {
			continue
		}
		v, err := normalizeValue(*f, f.Default)
		if err != nil {
			return fmt.Errorf("agentschema: field %q default: %w", f.Key, err)
		}
		f.Default = v
	}
	return nil
}

// Clone returns a deep copy, so callers can hand a schema out without sharing
// its slices.
func (s *Schema) Clone() *Schema {
	if s == nil {
		return nil
	}
	out := *s
	out.Fields = make([]Field, len(s.Fields))
	for i, f := range s.Fields {
		f.Options = append([]string(nil), f.Options...)
		if f.Min != nil {
			v := *f.Min
			f.Min = &v
		}
		if f.Max != nil {
			v := *f.Max
			f.Max = &v
		}
		out.Fields[i] = f
	}
	return &out
}

// Field returns the field with key, if the schema has one.
func (s *Schema) Field(key string) (Field, bool) {
	if s == nil {
		return Field{}, false
	}
	for _, f := range s.Fields {
		if f.Key == key {
			return f, true
		}
	}
	return Field{}, false
}

// Validate checks a schema's structure: the rules every schema, built-in or
// custom, must follow.
func (s *Schema) Validate() error {
	if s == nil {
		return fmt.Errorf("agentschema: schema is missing")
	}
	if !agentNameRe.MatchString(s.Name) {
		return fmt.Errorf("agentschema: name %q must match %s", s.Name, agentNameRe)
	}
	if err := checkText("display_name", s.DisplayName, MaxLabelLen); err != nil {
		return err
	}
	if err := checkText("description", s.Description, MaxDescriptionLen); err != nil {
		return err
	}
	if s.IconColor != "" && !iconColorRe.MatchString(s.IconColor) {
		return fmt.Errorf("agentschema: icon_color %q must be a #RRGGBB colour", s.IconColor)
	}
	if len(s.Fields) > MaxFields {
		return fmt.Errorf("agentschema: %d fields, at most %d", len(s.Fields), MaxFields)
	}
	keys := map[string]bool{}
	flags := map[string]string{}
	for _, f := range s.Fields {
		if err := f.validate(); err != nil {
			return fmt.Errorf("agentschema: field %q: %w", f.Key, err)
		}
		if keys[f.Key] {
			return fmt.Errorf("agentschema: field %q is declared twice", f.Key)
		}
		keys[f.Key] = true
		for _, flag := range []string{f.Flag, f.FalseFlag} {
			if flag == "" {
				continue
			}
			if other, dup := flags[flag]; dup {
				return fmt.Errorf("agentschema: flag %q is used by both %q and %q", flag, other, f.Key)
			}
			flags[flag] = f.Key
		}
	}
	return nil
}

// ValidateCustom checks a custom agent's schema: Validate plus what running an
// arbitrary CLI from it requires — an executable to run and a flag for every
// field. builtin reports whether a name belongs to a built-in agent.
func (s *Schema) ValidateCustom(builtin func(string) bool) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if builtin != nil && builtin(s.Name) {
		return fmt.Errorf("agentschema: %q is a built-in agent; pick another name", s.Name)
	}
	if err := ValidateBinary(s.Binary); err != nil {
		return err
	}
	for _, f := range s.Fields {
		if f.Flag == "" {
			return fmt.Errorf("agentschema: field %q needs a flag: a custom agent's options reach its CLI only as flags", f.Key)
		}
	}
	return nil
}

func (f Field) validate() error {
	if !fieldKeyRe.MatchString(f.Key) {
		return fmt.Errorf("key must match %s", fieldKeyRe)
	}
	if ReservedKeys[f.Key] {
		return fmt.Errorf("key is reserved for a setting every agent shares")
	}
	if strings.TrimSpace(f.Label) == "" {
		return fmt.Errorf("label is required")
	}
	if err := checkText("label", f.Label, MaxLabelLen); err != nil {
		return err
	}
	if err := checkText("description", f.Description, MaxDescriptionLen); err != nil {
		return err
	}
	if err := checkText("empty_label", f.EmptyLabel, MaxLabelLen); err != nil {
		return err
	}
	if f.Flag != "" && !flagRe.MatchString(f.Flag) {
		return fmt.Errorf("flag %q must look like --name or -n", f.Flag)
	}
	if f.FalseFlag != "" {
		if f.Type != TypeBool {
			return fmt.Errorf("false_flag only applies to bool fields")
		}
		if !flagRe.MatchString(f.FalseFlag) {
			return fmt.Errorf("false_flag %q must look like --name or -n", f.FalseFlag)
		}
	}
	if isDangerousFlag(f.Flag) || isDangerousFlag(f.FalseFlag) {
		if f.Type != TypeBool || !f.Warning {
			return fmt.Errorf("a --danger*/--allow-dangerously* flag must be a bool field marked warning")
		}
	}
	switch f.Type {
	case TypeSelect:
		if len(f.Options) == 0 && !f.AllowCustom {
			return fmt.Errorf("a select needs options (or allow_custom)")
		}
		if len(f.Options) > MaxOptions {
			return fmt.Errorf("%d options, at most %d", len(f.Options), MaxOptions)
		}
		seen := map[string]bool{}
		for _, o := range f.Options {
			if err := checkArgValue(o, MaxOptionLen); err != nil {
				return fmt.Errorf("option %q: %w", o, err)
			}
			if o == "" {
				return fmt.Errorf("options must not be empty; the empty choice is implicit")
			}
			if seen[o] {
				return fmt.Errorf("option %q is listed twice", o)
			}
			seen[o] = true
		}
	case TypeBool, TypeText:
	case TypeNumber:
		if f.Min != nil && f.Max != nil && *f.Min > *f.Max {
			return fmt.Errorf("min %v is above max %v", *f.Min, *f.Max)
		}
		for _, b := range []*float64{f.Min, f.Max} {
			if b != nil && (math.IsNaN(*b) || math.IsInf(*b, 0)) {
				return fmt.Errorf("min and max must be finite")
			}
			if b != nil && *b < 0 {
				return fmt.Errorf("min and max must not be negative: a negative value would reach the CLI as an option")
			}
		}
	default:
		return fmt.Errorf("type %q must be select, bool, number or text", f.Type)
	}
	if f.Type != TypeSelect && (len(f.Options) > 0 || f.AllowCustom) {
		return fmt.Errorf("options and allow_custom only apply to select fields")
	}
	if f.Type != TypeNumber && (f.Min != nil || f.Max != nil) {
		return fmt.Errorf("min and max only apply to number fields")
	}
	if f.Warning && f.Type != TypeBool {
		return fmt.Errorf("warning only applies to bool fields")
	}
	if f.Key == ModelKey && f.Type != TypeSelect && f.Type != TypeText {
		return fmt.Errorf("model must be a select or a text field")
	}
	if f.Default != nil {
		v, err := normalizeValue(f, f.Default)
		if err != nil {
			return fmt.Errorf("default: %w", err)
		}
		if f.Warning && v == true {
			return fmt.Errorf("a warning field must default to off")
		}
	}
	return nil
}

// ValidateBinary checks a custom agent's executable: a bare name looked up on
// PATH. Paths are refused so a schema cannot point the daemon at an arbitrary
// file, and shells and interpreters are refused because the review prompt —
// which carries a pull request's content — reaches the agent on stdin, where
// an interpreter would run it. The denylist is defence in depth: declaring a
// custom agent is already restricted to config.toml or an explicit opt-in.
func ValidateBinary(binary string) error {
	if !binaryRe.MatchString(binary) {
		return fmt.Errorf("agentschema: binary %q must be an executable name found on PATH (letters, digits, '.', '_' and '-'; no paths)", binary)
	}
	if isDeniedBinary(binary) {
		return fmt.Errorf("agentschema: binary %q is a shell or an interpreter; it would execute the review prompt it reads on stdin", binary)
	}
	return nil
}

// deniedBinaries are shells, interpreters and launchers that run their input
// or another program. Entries are lowercase: isDeniedBinary lowercases.
var deniedBinaries = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "fish": true, "dash": true, "ksh": true,
	"mksh": true, "csh": true, "tcsh": true, "ash": true, "busybox": true, "nu": true,
	"elvish": true, "xonsh": true, "pwsh": true, "powershell": true, "cmd": true,
	"env": true, "sudo": true, "doas": true, "su": true, "xargs": true, "nohup": true,
	"timeout": true, "nice": true, "exec": true, "eval": true, "command": true,
	"script": true, "expect": true, "watch": true, "time": true, "stdbuf": true,
	"node": true, "nodejs": true, "deno": true, "bun": true, "npx": true, "bunx": true,
	"ruby": true, "irb": true, "perl": true, "php": true, "lua": true, "luajit": true,
	"tclsh": true, "wish": true, "osascript": true, "awk": true, "gawk": true,
	"mawk": true, "nawk": true, "sed": true, "jshell": true, "java": true,
	"groovy": true, "scala": true, "r": true, "rscript": true, "julia": true,
	"swift": true, "dotnet": true, "go": true, "make": true, "gdb": true, "lldb": true,
	"vim": true, "vi": true, "nvim": true, "emacs": true, "ed": true, "ex": true,
	"docker": true, "podman": true, "kubectl": true, "ssh": true, "nc": true,
	"ncat": true, "socat": true, "curl": true, "wget": true, "git": true, "find": true,
	"heimdallm": true, "ipython": true, "bpython": true,
}

// interpreterFamilies match versioned or variant names of the denied
// programs: python3, python3.12, ksh93, tclsh8.6, ipython3, perl5.36…
var interpreterFamilies = []string{
	"python", "pypy", "ipython", "bpython", "perl", "ruby", "irb", "php",
	"node", "lua", "luajit", "deno", "bun", "julia", "rscript", "java",
	"tclsh", "wish", "awk", "gawk", "sh", "ash", "dash", "bash", "zsh",
	"ksh", "mksh", "csh", "tcsh", "fish", "pwsh", "powershell",
}

// isDeniedBinary compares case-insensitively and without a Windows
// executable suffix: on a case-insensitive filesystem RSCRIPT runs Rscript,
// and bash.exe is bash.
func isDeniedBinary(binary string) bool {
	name := strings.ToLower(binary)
	for _, ext := range []string{".exe", ".com", ".cmd", ".bat"} {
		if strings.HasSuffix(name, ext) {
			name = strings.TrimSuffix(name, ext)
			break
		}
	}
	if deniedBinaries[name] {
		return true
	}
	for _, prefix := range interpreterFamilies {
		if rest, ok := strings.CutPrefix(name, prefix); ok &&
			(rest == "" || strings.Trim(rest, "0123456789.-") == "") {
			return true
		}
	}
	return false
}

func isDangerousFlag(flag string) bool {
	lower := strings.ToLower(flag)
	return strings.HasPrefix(lower, "--danger") || strings.HasPrefix(lower, "--allow-dangerously")
}

// IsAgentName reports whether name is a valid custom agent name.
func IsAgentName(name string) bool { return agentNameRe.MatchString(name) }

func checkText(name, value string, max int) error {
	if len([]rune(value)) > max {
		return fmt.Errorf("agentschema: %s is longer than %d characters", name, max)
	}
	for _, r := range value {
		if r != '\n' && unicode.IsControl(r) {
			return fmt.Errorf("agentschema: %s contains a control character", name)
		}
	}
	return nil
}

// checkArgValue rejects a value that would reach a CLI as something other
// than the value of its flag.
func checkArgValue(v string, max int) error {
	if len([]rune(v)) > max {
		return fmt.Errorf("is longer than %d characters", max)
	}
	if strings.HasPrefix(strings.TrimSpace(v), "-") {
		return fmt.Errorf("must not begin with '-': the CLI would read it as another option")
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return fmt.Errorf("contains a control character")
		}
	}
	return nil
}

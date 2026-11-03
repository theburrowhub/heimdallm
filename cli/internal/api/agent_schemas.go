package api

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Field types of an agent schema. Mirrors the daemon's agentschema package.
const (
	FieldSelect = "select"
	FieldBool   = "bool"
	FieldNumber = "number"
	FieldText   = "text"
)

// AgentSchemaField is one configurable option of an agent.
type AgentSchemaField struct {
	Key         string   `json:"key"`
	Type        string   `json:"type"`
	Label       string   `json:"label"`
	Description string   `json:"description,omitempty"`
	Flag        string   `json:"flag,omitempty"`
	FalseFlag   string   `json:"false_flag,omitempty"`
	Options     []string `json:"options,omitempty"`
	AllowCustom bool     `json:"allow_custom,omitempty"`
	Default     any      `json:"default,omitempty"`
	EmptyLabel  string   `json:"empty_label,omitempty"`
	Min         *float64 `json:"min,omitempty"`
	Max         *float64 `json:"max,omitempty"`
	// Warning marks a dangerous toggle: it may only be turned off here.
	Warning bool `json:"warning,omitempty"`
}

// AgentSchema describes an agent and the fields of its settings form.
type AgentSchema struct {
	Name        string             `json:"name"`
	DisplayName string             `json:"display_name,omitempty"`
	Description string             `json:"description,omitempty"`
	IconColor   string             `json:"icon_color,omitempty"`
	Binary      string             `json:"binary,omitempty"`
	Fields      []AgentSchemaField `json:"fields"`
	// Builtin is set by the daemon for the agents Heimdallm ships.
	Builtin bool `json:"builtin,omitempty"`
}

// AgentSchemaCatalog is the GET /agent-schemas body.
type AgentSchemaCatalog struct {
	Schemas []AgentSchema `json:"schemas"`
	// CustomAgentsAPIEnabled reports [ai] allow_custom_agents_api: whether
	// custom agents may be created, changed and deleted through the API.
	CustomAgentsAPIEnabled bool `json:"custom_agents_api_enabled"`
}

// Title is the name to show for the agent.
func (s AgentSchema) Title() string {
	if s.DisplayName != "" {
		return s.DisplayName
	}
	return s.Name
}

// Field returns the field with key.
func (s AgentSchema) Field(key string) (AgentSchemaField, bool) {
	for _, f := range s.Fields {
		if f.Key == key {
			return f, true
		}
	}
	return AgentSchemaField{}, false
}

// ByName returns the schema of an agent.
func (c *AgentSchemaCatalog) ByName(name string) (AgentSchema, bool) {
	if c == nil {
		return AgentSchema{}, false
	}
	for _, s := range c.Schemas {
		if s.Name == name {
			return s, true
		}
	}
	return AgentSchema{}, false
}

// GetAgentSchemas returns every agent's options schema.
func (c *Client) GetAgentSchemas() (*AgentSchemaCatalog, error) {
	data, err := c.do("GET", "/agent-schemas")
	if err != nil {
		return nil, err
	}
	var cat AgentSchemaCatalog
	if err := json.Unmarshal(data, &cat); err != nil {
		return nil, fmt.Errorf("parsing agent schemas: %w", err)
	}
	return &cat, nil
}

// PutAgentSchema creates or replaces a custom agent. The daemon answers 403
// unless config.toml opts in with [ai] allow_custom_agents_api = true.
func (c *Client) PutAgentSchema(s AgentSchema) error {
	s.Builtin = false
	_, err := c.doJSON("PUT", "/agent-schemas/"+url.PathEscape(s.Name), s)
	return err
}

// DeleteAgentSchema removes a custom agent.
func (c *Client) DeleteAgentSchema(name string) error {
	_, err := c.do("DELETE", "/agent-schemas/"+url.PathEscape(name))
	return err
}

// PatchConfig merges patch into the daemon's config.toml (PATCH /config).
func (c *Client) PatchConfig(patch map[string]any) error {
	_, err := c.doJSON("PATCH", "/config", patch)
	return err
}

// AgentSettingsPatch is the PATCH /config body that sets one agent's values:
// they sit under ai.agents.<name>, a custom agent's options included.
func AgentSettingsPatch(agent string, values map[string]any) map[string]any {
	return map[string]any{"ai": map[string]any{"agents": map[string]any{agent: values}}}
}

// ParseValue converts a value typed by the user to the field's type, and
// checks it the way the daemon will. An empty string means "not set" (nil).
func (f AgentSchemaField) ParseValue(raw string) (any, error) {
	raw = strings.TrimSpace(raw)
	switch f.Type {
	case FieldBool:
		if raw == "" {
			// Not set. A built-in agent's typed toggle stores that as off;
			// callers decide what it means for a custom agent's option.
			return nil, nil
		}
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %q is not true or false", f.Key, raw)
		}
		if b && f.Warning {
			return nil, fmt.Errorf("%s can only be turned on in config.toml", f.Key)
		}
		return b, nil
	case FieldNumber:
		if raw == "" {
			return nil, nil
		}
		n, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, fmt.Errorf("%s: %q is not a number", f.Key, raw)
		}
		// Never negative, whatever Min says: the daemon refuses a negative
		// value (and a negative min or max in a schema) because "-1" would
		// reach the CLI as an option.
		if n < 0 || (f.Min != nil && n < *f.Min) || (f.Max != nil && n > *f.Max) {
			return nil, fmt.Errorf("%s: %v is out of range%s", f.Key, n, f.rangeText())
		}
		if n == math.Trunc(n) && n <= maxExactInt {
			return int64(n), nil
		}
		return n, nil
	default:
		if raw == "" {
			return nil, nil
		}
		if strings.HasPrefix(raw, "-") {
			return nil, fmt.Errorf("%s: a value must not begin with '-'", f.Key)
		}
		if f.Type == FieldSelect && !f.AllowCustom && !containsString(f.Options, raw) {
			return nil, fmt.Errorf("%s: %q is not one of: %s", f.Key, raw, strings.Join(f.Options, ", "))
		}
		return raw, nil
	}
}

// maxExactInt is the largest whole number a float64 holds exactly; larger
// whole numbers stay float64 rather than convert to a wrong int64.
const maxExactInt = 1 << 53

func (f AgentSchemaField) rangeText() string {
	switch {
	case f.Min != nil && f.Max != nil:
		return fmt.Sprintf(" (%v–%v)", *f.Min, *f.Max)
	case f.Min != nil:
		return fmt.Sprintf(" (at least %v)", *f.Min)
	case f.Max != nil:
		return fmt.Sprintf(" (0–%v)", *f.Max)
	default:
		return " (not negative)"
	}
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// Client-side checks that mirror the daemon's custom schema rules
// (daemon/internal/agentschema), so the wizard and `agents add` point at a
// problem before sending. The daemon validates again. The daemon, the app and
// this package all run daemon/internal/agentschema/testdata/
// custom_schema_cases.json, so the three cannot drift apart.
var (
	agentNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	fieldKeyRe  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	flagRe      = regexp.MustCompile(`^--?[A-Za-z0-9][A-Za-z0-9-]*$`)
	binaryRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	iconColorRe = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
)

// Limits of the daemon's agentschema package.
const (
	MaxSchemaFields   = 64
	MaxSchemaOptions  = 200
	MaxLabelLen       = 128
	MaxDescriptionLen = 1000
	MaxOptionLen      = 128
	MaxTextValueLen   = 512
)

// ReservedFieldKeys are settings every agent shares; a field may not use them.
var ReservedFieldKeys = map[string]bool{
	"binary": true, "schema": true, "schema_path": true, "options": true,
	"prompt": true, "extra_flags": true, "execution_timeout": true, "review_limits": true,
}

// modelKey is the shared model setting a schema may declare.
const modelKey = "model"

// ValidAgentName reports whether name can name a custom agent.
func ValidAgentName(name string) bool { return agentNameRe.MatchString(name) }

// ValidBinary reports whether binary is an executable name without a path.
func ValidBinary(binary string) bool { return binaryRe.MatchString(binary) }

// ValidFlag reports whether flag looks like --name or -n.
func ValidFlag(flag string) bool { return flagRe.MatchString(flag) }

// ValidateCustom lists the problems of a custom agent's schema, empty when it
// looks valid. reserved holds names a custom agent may not take.
func (s AgentSchema) ValidateCustom(reserved map[string]bool) []string {
	var errs []string
	if !ValidAgentName(s.Name) {
		errs = append(errs, "name: lowercase letters, digits, '-' or '_', starting with a letter")
	} else if reserved[s.Name] {
		errs = append(errs, fmt.Sprintf("name: %q is a built-in agent", s.Name))
	}
	if !ValidBinary(s.Binary) {
		errs = append(errs, "binary: an executable name on PATH, without a path")
	} else if isDeniedBinary(s.Binary) {
		errs = append(errs, fmt.Sprintf("binary: %q is a shell or an interpreter; it would execute the review prompt it reads on stdin", s.Binary))
	}
	if p := textProblem(s.DisplayName, MaxLabelLen); p != "" {
		errs = append(errs, "display name: "+p)
	}
	if p := textProblem(s.Description, MaxDescriptionLen); p != "" {
		errs = append(errs, "description: "+p)
	}
	if s.IconColor != "" && !iconColorRe.MatchString(s.IconColor) {
		errs = append(errs, "icon color: a #RRGGBB colour")
	}
	if len(s.Fields) > MaxSchemaFields {
		errs = append(errs, fmt.Sprintf("fields: %d fields, at most %d", len(s.Fields), MaxSchemaFields))
	}
	keys, flags := map[string]bool{}, map[string]bool{}
	for i, f := range s.Fields {
		where := fmt.Sprintf("option %d", i+1)
		if f.Key != "" {
			where += " (" + f.Key + ")"
		}
		switch {
		case !fieldKeyRe.MatchString(f.Key):
			errs = append(errs, where+": key must be lowercase letters, digits or '_'")
		case ReservedFieldKeys[f.Key]:
			errs = append(errs, where+": key is reserved")
		case keys[f.Key]:
			errs = append(errs, where+": key is used twice")
		}
		keys[f.Key] = true
		checkFlags := []string{f.Flag}
		if f.FalseFlag != "" {
			checkFlags = append(checkFlags, f.FalseFlag)
		}
		for _, flag := range checkFlags {
			if !ValidFlag(flag) {
				errs = append(errs, where+": flags must look like --name or -n")
			} else if flags[flag] {
				errs = append(errs, where+": flag "+flag+" is used twice")
			}
			flags[flag] = true
		}
		for _, p := range f.problems() {
			errs = append(errs, where+": "+p)
		}
	}
	return errs
}

// problems lists what the daemon's agentschema refuses in one field, besides
// its key and flags (ValidateCustom checks those across fields).
func (f AgentSchemaField) problems() []string {
	var errs []string
	if strings.TrimSpace(f.Label) == "" {
		errs = append(errs, "label is required")
	} else if p := textProblem(f.Label, MaxLabelLen); p != "" {
		errs = append(errs, "label "+p)
	}
	if p := textProblem(f.Description, MaxDescriptionLen); p != "" {
		errs = append(errs, "description "+p)
	}
	if p := textProblem(f.EmptyLabel, MaxLabelLen); p != "" {
		errs = append(errs, "empty label "+p)
	}
	if f.FalseFlag != "" && f.Type != FieldBool {
		errs = append(errs, "a false flag only applies to a toggle (bool)")
	}
	if (isDangerousFlag(f.Flag) || isDangerousFlag(f.FalseFlag)) && (f.Type != FieldBool || !f.Warning) {
		errs = append(errs, "a --danger*/--allow-dangerously* flag must be a toggle (bool) marked warning")
	}
	switch f.Type {
	case FieldSelect:
		if len(f.Options) == 0 && !f.AllowCustom {
			errs = append(errs, "a dropdown needs options")
		}
		if len(f.Options) > MaxSchemaOptions {
			errs = append(errs, fmt.Sprintf("%d choices, at most %d", len(f.Options), MaxSchemaOptions))
		}
		seen := map[string]bool{}
		for _, o := range f.Options {
			switch {
			case o == "":
				errs = append(errs, "choices must not be empty; the empty choice is implicit")
			case argValueProblem(o, MaxOptionLen) != "":
				errs = append(errs, fmt.Sprintf("choice %q %s", o, argValueProblem(o, MaxOptionLen)))
			case seen[o]:
				errs = append(errs, fmt.Sprintf("choice %q is listed twice", o))
			}
			seen[o] = true
		}
	case FieldBool, FieldText:
	case FieldNumber:
		if f.Min != nil && f.Max != nil && *f.Min > *f.Max {
			errs = append(errs, fmt.Sprintf("min %v is above max %v", *f.Min, *f.Max))
		}
		for _, b := range []*float64{f.Min, f.Max} {
			if b != nil && (math.IsNaN(*b) || math.IsInf(*b, 0) || *b < 0) {
				errs = append(errs, "min and max must be finite and not negative: a negative value would reach the CLI as an option")
				break
			}
		}
	default:
		errs = append(errs, "type must be select, bool, number or text")
	}
	if f.Type != FieldSelect && (len(f.Options) > 0 || f.AllowCustom) {
		errs = append(errs, "choices and allow_custom only apply to a dropdown (select)")
	}
	if f.Type != FieldNumber && (f.Min != nil || f.Max != nil) {
		errs = append(errs, "min and max only apply to a number")
	}
	if f.Warning && f.Type != FieldBool {
		errs = append(errs, "warning only applies to a toggle (bool)")
	}
	if f.Key == modelKey && f.Type != FieldSelect && f.Type != FieldText {
		errs = append(errs, "model must be a dropdown (select) or text")
	}
	if f.Default != nil {
		if p := f.defaultProblem(); p != "" {
			errs = append(errs, "default: "+p)
		} else if f.Warning && f.Default == true {
			errs = append(errs, "a warning toggle must default to off")
		}
	}
	return errs
}

// defaultProblem checks the field's default the way the daemon's
// normalizeValue does. Empty when it is fine.
func (f AgentSchemaField) defaultProblem() string {
	switch f.Type {
	case FieldBool:
		if _, ok := f.Default.(bool); !ok {
			return fmt.Sprintf("must be true or false, got %T", f.Default)
		}
	case FieldNumber:
		n, ok := toFloat(f.Default)
		switch {
		case !ok:
			return fmt.Sprintf("must be a number, got %T", f.Default)
		case math.IsNaN(n) || math.IsInf(n, 0):
			return "must be a finite number"
		case n < 0:
			return fmt.Sprintf("%v must not be negative", n)
		case f.Min != nil && n < *f.Min:
			return fmt.Sprintf("%v is below the minimum %v", n, *f.Min)
		case f.Max != nil && n > *f.Max:
			return fmt.Sprintf("%v is above the maximum %v", n, *f.Max)
		}
	case FieldSelect, FieldText:
		v, ok := f.Default.(string)
		if !ok {
			return fmt.Sprintf("must be a string, got %T", f.Default)
		}
		v = strings.TrimSpace(v)
		max := MaxTextValueLen
		if f.Type == FieldSelect {
			max = MaxOptionLen
		}
		if p := argValueProblem(v, max); p != "" {
			return p
		}
		if strings.EqualFold(v, "bypassPermissions") {
			return fmt.Sprintf("%q is never passed to an agent: it removes every permission check", v)
		}
		if f.Type == FieldSelect && v != "" && !f.AllowCustom && !containsString(f.Options, v) {
			return fmt.Sprintf("%q is not one of: %s", v, strings.Join(f.Options, ", "))
		}
	}
	return ""
}

// toFloat accepts the numbers a default can hold: float64 from a JSON file,
// int64 from the wizard (ParseValue), json.Number from a UseNumber decoder.
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

// textProblem checks a label or description: its length and no control
// characters other than a newline. Empty when it is fine.
func textProblem(v string, max int) string {
	if len([]rune(v)) > max {
		return fmt.Sprintf("is longer than %d characters", max)
	}
	for _, r := range v {
		if r != '\n' && unicode.IsControl(r) {
			return "contains a control character"
		}
	}
	return ""
}

// argValueProblem checks a value that reaches the CLI as a flag's value.
// Empty when it is fine.
func argValueProblem(v string, max int) string {
	if len([]rune(v)) > max {
		return fmt.Sprintf("is longer than %d characters", max)
	}
	if strings.HasPrefix(strings.TrimSpace(v), "-") {
		return "must not begin with '-': the CLI would read it as another option"
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return "contains a control character"
		}
	}
	return ""
}

func isDangerousFlag(flag string) bool {
	lower := strings.ToLower(flag)
	return strings.HasPrefix(lower, "--danger") || strings.HasPrefix(lower, "--allow-dangerously")
}

// deniedBinaries are shells, interpreters and launchers that run their input
// or another program: the review prompt reaches a custom agent on stdin, where
// they would execute it. Mirrors the daemon's list (TestDenylistMatchesDaemon
// compares them); entries are lowercase.
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
	"heimdallm": true, "ipython": true, "bpython": true, "php-win": true,
	"python-dbg": true, "python_d": true, "pythonw_d": true, "jrunscript": true,
	"jjs": true,
}

// interpreterFamilies match versioned or variant names of the denied
// programs (python3.12, ksh93, luajit-2.1.0-beta3, php-cgi8.2…).
var interpreterFamilies = []string{
	"python", "pythonw", "pypy", "pypyw", "ipython", "bpython", "perl", "wperl",
	"ruby", "rubyw", "irb", "php", "php-cgi", "phpdbg", "node", "lua", "luajit",
	"deno", "bun", "julia", "rscript", "java", "javaw", "javaws",
	"tclsh", "wish", "awk", "gawk", "sh", "ash", "dash", "bash", "zsh",
	"ksh", "mksh", "csh", "tcsh", "fish", "pwsh", "powershell",
}

// interpreterVersionRe matches what may follow a family's name in a versioned
// build: separators, then a version that starts with a digit. Letters count
// only after a digit, which keeps javac, shellcheck and pythonic allowed.
var interpreterVersionRe = regexp.MustCompile(`^[.-]*([0-9][0-9a-z._-]*)?$`)

// isDeniedBinary compares case-insensitively and without a Windows executable
// suffix or trailing dots and spaces, like the daemon.
func isDeniedBinary(binary string) bool {
	name := strings.TrimRight(strings.ToLower(binary), ". ")
	for _, ext := range []string{".exe", ".com", ".cmd", ".bat"} {
		if strings.HasSuffix(name, ext) {
			name = strings.TrimRight(strings.TrimSuffix(name, ext), ". ")
			break
		}
	}
	if deniedBinaries[name] {
		return true
	}
	for _, prefix := range interpreterFamilies {
		if rest, ok := strings.CutPrefix(name, prefix); ok &&
			(rest == "" || interpreterVersionRe.MatchString(rest)) {
			return true
		}
	}
	return false
}

// TOMLSnippet is the config.toml section that declares the agent with its
// schema inline, for operators who define custom agents by hand.
func (s AgentSchema) TOMLSnippet() string {
	s.Builtin = false
	binary := s.Binary
	s.Binary = ""
	data, _ := json.Marshal(s)
	// A quote in a label must not close the ''' string: as JSON, \u0027
	// reads back as the same quote.
	escaped := strings.ReplaceAll(string(data), "'", `\u0027`)
	quoted, _ := json.Marshal(binary)
	return fmt.Sprintf("[ai.agents.%s]\nbinary = %s\nschema = '''\n%s\n'''\n", s.Name, quoted, escaped)
}

// PrettyJSON is the schema as an indented JSON document.
func (s AgentSchema) PrettyJSON() string {
	s.Builtin = false
	data, _ := json.MarshalIndent(s, "", "  ")
	return string(data)
}

// FormatValue renders a setting's value the way the user typed it.
func FormatValue(v any) string {
	switch x := v.(type) {
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case nil:
		return ""
	default:
		return fmt.Sprint(x)
	}
}

// AgentConfigValues lists the settings of a GET /config agent_configs entry
// that are set, as key/value pairs. Zero values ("", 0, false) are the
// daemon's "not set"; a custom agent's options are listed by their own keys,
// after the shared settings. Agents have different settings (each one's
// schema decides), so there is no fixed list.
func AgentConfigValues(agent map[string]any) [][2]string {
	var out [][2]string
	// A built-in agent's typed settings report zero values when not set; a
	// custom agent's options are only present when set, so an explicit false
	// (which may pass a false_flag such as --no-auto-commits) is shown.
	option := false
	add := func(key string, v any) {
		switch x := v.(type) {
		case nil, map[string]any, []any:
			return
		case string:
			if x == "" {
				return
			}
		case bool:
			if !x && !option {
				return
			}
		case float64:
			if x == 0 {
				return
			}
		}
		out = append(out, [2]string{key, FormatValue(v)})
	}
	for _, key := range sortedKeys(agent) {
		if key != "options" {
			add(key, agent[key])
		}
	}
	if options, ok := agent["options"].(map[string]any); ok {
		option = true
		for _, key := range sortedKeys(options) {
			add(key, options[key])
		}
	}
	return out
}

// SettingLabel turns a setting key into a label: max_turns → "Max turns".
func SettingLabel(key string) string {
	label := strings.ReplaceAll(key, "_", " ")
	if label == "" {
		return label
	}
	return strings.ToUpper(label[:1]) + label[1:]
}

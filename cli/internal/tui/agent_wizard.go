package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/theburrowhub/heimdallm/cli/internal/api"
)

// wizardStep is one question of the custom agent wizard.
type wizardStep int

const (
	stepName wizardStep = iota
	stepDisplayName
	stepBinary
	stepAddField
	stepFieldKey
	stepFieldType
	stepFieldLabel
	stepFieldFlag
	stepFieldOptions
	stepFieldAllowCustom
	stepFieldFalseFlag
	stepFieldWarning
	stepFieldMin
	stepFieldMax
	stepFieldDefault
	stepReview
)

// agentWizard builds a custom agent's schema one question at a time: its
// name and binary, then each option (key, type, label, flag and what the
// type needs), then a review. On review the caller saves the schema through
// the API when the daemon allows it, or shows the config.toml section to
// paste. It is a plain state machine so the dashboard and the standalone
// `agents new` command share it.
type agentWizard struct {
	step     wizardStep
	schema   api.AgentSchema
	field    api.AgentSchemaField
	input    textInput
	err      string
	reserved map[string]bool
	// apiEnabled reports whether the daemon accepts PUT /agent-schemas.
	apiEnabled bool
	// status is shown on the review step (a save error, or the result).
	status string
}

func newAgentWizard(catalog *api.AgentSchemaCatalog) agentWizard {
	w := agentWizard{reserved: map[string]bool{"cursor": true}}
	if catalog != nil {
		w.apiEnabled = catalog.CustomAgentsAPIEnabled
		for _, s := range catalog.Schemas {
			if s.Builtin {
				w.reserved[s.Name] = true
			}
		}
	}
	return w
}

// wizardAction is what a key asked the caller to do.
type wizardAction int

const (
	wizardNone wizardAction = iota
	wizardCancel
	wizardSave
)

func (w *agentWizard) prompt() (question, hint string) {
	switch w.step {
	case stepName:
		return "Agent name", "the id used in config.toml, e.g. aider"
	case stepDisplayName:
		return "Display name", "optional, e.g. Aider"
	case stepBinary:
		return "Binary", "an executable on the daemon's PATH, no path"
	case stepAddField:
		return fmt.Sprintf("Add an option? (%d so far)", len(w.schema.Fields)), "y / n"
	case stepFieldKey:
		return "Option key", "stored as <key> = … under the agent, e.g. edit_format"
	case stepFieldType:
		return "Type", "select | bool | number | text"
	case stepFieldLabel:
		return "Label", "what the settings form shows"
	case stepFieldFlag:
		return "Flag", "the CLI option it sets, e.g. --edit-format"
	case stepFieldOptions:
		return "Options", "comma-separated, e.g. whole, diff, udiff"
	case stepFieldAllowCustom:
		return "Accept other values too?", "y / n"
	case stepFieldFalseFlag:
		return "Flag when off", "optional, e.g. --no-auto-commits"
	case stepFieldWarning:
		return "Dangerous? Only config.toml would turn it on", "y / n"
	case stepFieldMin:
		return "Minimum", "optional, not negative"
	case stepFieldMax:
		return "Maximum", "optional"
	case stepFieldDefault:
		return "Default", w.defaultHint()
	default:
		return "", ""
	}
}

func (w *agentWizard) defaultHint() string {
	switch w.field.Type {
	case api.FieldBool:
		return "y / n (on or off)"
	case api.FieldSelect:
		return "optional, one of the options"
	default:
		return "optional"
	}
}

func (w *agentWizard) handleKey(msg tea.KeyMsg) wizardAction {
	if w.step == stepReview {
		switch msg.String() {
		case "esc", "q":
			return wizardCancel
		case "s", "enter":
			if !w.apiEnabled {
				return wizardNone
			}
			if errs := w.schema.ValidateCustom(w.reserved); len(errs) > 0 {
				w.err = errs[0]
				return wizardNone
			}
			return wizardSave
		}
		return wizardNone
	}
	switch msg.String() {
	case "esc":
		return wizardCancel
	case "enter":
		if err := w.answer(strings.TrimSpace(w.input.String())); err != nil {
			w.err = err.Error()
			return wizardNone
		}
		w.err = ""
		w.input = newTextInput("")
	default:
		w.input.update(msg)
	}
	return wizardNone
}

func yesNo(answer string) (bool, error) {
	switch strings.ToLower(answer) {
	case "y", "yes":
		return true, nil
	case "n", "no", "":
		return false, nil
	}
	return false, fmt.Errorf("answer y or n")
}

func optionalNumber(answer string) (*float64, error) {
	if answer == "" {
		return nil, nil
	}
	n, err := strconv.ParseFloat(answer, 64)
	if err != nil || n < 0 {
		return nil, fmt.Errorf("enter a number that is not negative, or nothing")
	}
	return &n, nil
}

// answer records the answer to the current question and moves on.
func (w *agentWizard) answer(a string) error {
	switch w.step {
	case stepName:
		if !api.ValidAgentName(a) {
			return fmt.Errorf("use lowercase letters, digits, '-' or '_', starting with a letter")
		}
		if w.reserved[a] {
			return fmt.Errorf("%q is a built-in agent", a)
		}
		w.schema.Name = a
		w.step = stepDisplayName
	case stepDisplayName:
		w.schema.DisplayName = a
		w.step = stepBinary
	case stepBinary:
		if !api.ValidBinary(a) {
			return fmt.Errorf("an executable name without a path")
		}
		w.schema.Binary = a
		w.step = stepAddField
	case stepAddField:
		more, err := yesNo(a)
		if err != nil {
			return err
		}
		if more {
			w.field = api.AgentSchemaField{}
			w.step = stepFieldKey
		} else {
			w.step = stepReview
		}
	case stepFieldKey:
		probe := api.AgentSchema{Name: "x", Binary: "x", Fields: []api.AgentSchemaField{{Key: a, Type: api.FieldText, Label: "x", Flag: "--x"}}}
		if errs := probe.ValidateCustom(nil); len(errs) > 0 {
			return fmt.Errorf("%s", strings.TrimPrefix(errs[0], "option 1 ("+a+"): "))
		}
		if _, dup := w.schema.Field(a); dup {
			return fmt.Errorf("the agent already has an option %q", a)
		}
		w.field.Key = a
		w.step = stepFieldType
	case stepFieldType:
		switch a {
		case api.FieldSelect, api.FieldBool, api.FieldNumber, api.FieldText:
		default:
			return fmt.Errorf("type must be select, bool, number or text")
		}
		w.field.Type = a
		w.step = stepFieldLabel
	case stepFieldLabel:
		if a == "" {
			return fmt.Errorf("a label is required")
		}
		w.field.Label = a
		w.step = stepFieldFlag
	case stepFieldFlag:
		if !api.ValidFlag(a) {
			return fmt.Errorf("a flag looks like --name or -n")
		}
		w.field.Flag = a
		switch w.field.Type {
		case api.FieldSelect:
			w.step = stepFieldOptions
		case api.FieldBool:
			w.step = stepFieldFalseFlag
		case api.FieldNumber:
			w.step = stepFieldMin
		default:
			w.step = stepFieldDefault
		}
	case stepFieldOptions:
		var opts []string
		for _, o := range strings.Split(a, ",") {
			if o = strings.TrimSpace(o); o != "" {
				if strings.HasPrefix(o, "-") {
					return fmt.Errorf("options must not begin with '-'")
				}
				opts = append(opts, o)
			}
		}
		w.field.Options = opts
		w.step = stepFieldAllowCustom
	case stepFieldAllowCustom:
		allow, err := yesNo(a)
		if err != nil {
			return err
		}
		if len(w.field.Options) == 0 && !allow {
			return fmt.Errorf("with no options the dropdown must accept other values")
		}
		w.field.AllowCustom = allow
		w.step = stepFieldDefault
	case stepFieldFalseFlag:
		if a != "" && (!api.ValidFlag(a) || a == w.field.Flag) {
			return fmt.Errorf("a different flag that looks like --name, or nothing")
		}
		w.field.FalseFlag = a
		w.step = stepFieldWarning
	case stepFieldWarning:
		danger, err := yesNo(a)
		if err != nil {
			return err
		}
		w.field.Warning = danger
		if danger {
			// A dangerous toggle always defaults to off.
			w.finishField()
		} else {
			w.step = stepFieldDefault
		}
	case stepFieldMin:
		n, err := optionalNumber(a)
		if err != nil {
			return err
		}
		w.field.Min = n
		w.step = stepFieldMax
	case stepFieldMax:
		n, err := optionalNumber(a)
		if err != nil {
			return err
		}
		if n != nil && w.field.Min != nil && *n < *w.field.Min {
			return fmt.Errorf("the maximum is below the minimum")
		}
		w.field.Max = n
		w.step = stepFieldDefault
	case stepFieldDefault:
		if a != "" {
			var (
				v   any
				err error
			)
			if w.field.Type == api.FieldBool {
				v, err = yesNo(a)
			} else {
				v, err = w.field.ParseValue(a)
			}
			if err != nil {
				return err
			}
			w.field.Default = v
		}
		w.finishField()
	}
	return nil
}

func (w *agentWizard) finishField() {
	w.schema.Fields = append(w.schema.Fields, w.field)
	w.field = api.AgentSchemaField{}
	w.step = stepAddField
}

// view renders the wizard: what is decided so far, then the question.
func (w agentWizard) view() []string {
	muted := lipgloss.NewStyle().Foreground(colorMuted)
	val := lipgloss.NewStyle().Foreground(lipgloss.Color("#E5E7EB"))
	lines := []string{headerStyle.Render("── New custom agent ")}
	if w.schema.Name != "" {
		lines = append(lines, muted.Render("  name: ")+val.Render(w.schema.Name)+
			muted.Render("   binary: ")+val.Render(w.schema.Binary))
	}
	for _, f := range w.schema.Fields {
		lines = append(lines, muted.Render(fmt.Sprintf("  • %s (%s) %s", f.Key, f.Type, f.Flag)))
	}
	lines = append(lines, "")
	if w.step == stepReview {
		lines = append(lines, w.reviewLines()...)
	} else {
		q, hint := w.prompt()
		lines = append(lines, val.Render("  "+q+": ")+w.input.view())
		lines = append(lines, muted.Render("    "+hint+"   (enter to answer, esc to cancel)"))
	}
	if w.err != "" {
		lines = append(lines, "", statusError.Render("  "+api.DisplayText(w.err, 200)))
	}
	if w.status != "" {
		lines = append(lines, "", muted.Render("  "+api.DisplayText(w.status, 300)))
	}
	return lines
}

func (w agentWizard) reviewLines() []string {
	muted := lipgloss.NewStyle().Foreground(colorMuted)
	var lines []string
	if errs := w.schema.ValidateCustom(w.reserved); len(errs) > 0 {
		for _, e := range errs {
			lines = append(lines, statusError.Render("  "+e))
		}
		return append(lines, muted.Render("  esc to close"))
	}
	if w.apiEnabled {
		lines = append(lines, muted.Render("  Schema:"))
		for _, l := range strings.Split(w.schema.PrettyJSON(), "\n") {
			lines = append(lines, "    "+l)
		}
		return append(lines, "", muted.Render("  s or enter: save to the daemon   esc: discard"))
	}
	lines = append(lines,
		muted.Render("  Custom agents are defined in config.toml. Add this section to it"),
		muted.Render("  (or set [ai] allow_custom_agents_api = true there to save from here):"),
		"")
	for _, l := range strings.Split(w.schema.TOMLSnippet(), "\n") {
		lines = append(lines, "    "+l)
	}
	return append(lines, muted.Render("  esc to close"))
}

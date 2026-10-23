package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/theburrowhub/heimdallm/cli/internal/api"
)

// agentForm edits one agent's settings, rendered from its schema: a select
// cycles through its options, a toggle flips, and numbers and text are typed.
// A toggle marked warning can only be turned off. Nothing is sent until the
// user saves; patch() is the PATCH /config body.
type agentForm struct {
	schema api.AgentSchema
	custom bool
	// values are the settings as the daemon reported them, by field key.
	values map[string]any
	// changed holds the edits to send.
	changed map[string]any
	cursor  int
	editing bool
	input   textInput
	err     string
}

// newAgentForm reads an agent's current values from its GET /config entry
// (agent_configs.<name>): a built-in agent's fields are its typed settings, a
// custom agent's are its options plus the shared model.
func newAgentForm(schema api.AgentSchema, agentConfig map[string]any) agentForm {
	f := agentForm{
		schema:  schema,
		custom:  !schema.Builtin,
		values:  map[string]any{},
		changed: map[string]any{},
	}
	options, _ := agentConfig["options"].(map[string]any)
	for _, field := range schema.Fields {
		var v any
		if f.custom && field.Key != "model" {
			v = options[field.Key]
		} else {
			v = agentConfig[field.Key]
		}
		if v = normalizeFormValue(field, v); v != nil {
			f.values[field.Key] = v
		}
	}
	return f
}

// normalizeFormValue maps the daemon's "not set" encodings ("" and 0) to nil,
// and JSON numbers to float64.
func normalizeFormValue(field api.AgentSchemaField, v any) any {
	switch x := v.(type) {
	case string:
		if x == "" {
			return nil
		}
	case float64:
		if x == 0 && field.Type == api.FieldNumber {
			return nil
		}
	case int64:
		if x == 0 {
			return nil
		}
		return float64(x)
	case int:
		if x == 0 {
			return nil
		}
		return float64(x)
	}
	return v
}

// value is the field's current value: an edit, else the daemon's value.
func (f agentForm) value(key string) any {
	if v, ok := f.changed[key]; ok {
		return v
	}
	return f.values[key]
}

func (f agentForm) boolValue(field api.AgentSchemaField) bool {
	v := f.value(field.Key)
	if v == nil {
		v = field.Default
	}
	b, _ := v.(bool)
	return b
}

func (f *agentForm) set(key string, v any) {
	f.changed[key] = v
	f.err = ""
}

// formAction is what a key asked the dashboard to do with the form.
type formAction int

const (
	formNone formAction = iota
	formClose
	formSave
)

func (f *agentForm) handleKey(msg tea.KeyMsg) formAction {
	if len(f.schema.Fields) == 0 {
		if msg.String() == "esc" || msg.String() == "q" {
			return formClose
		}
		return formNone
	}
	field := f.schema.Fields[f.cursor]
	if f.editing {
		switch msg.String() {
		case "enter":
			v, err := field.ParseValue(f.input.String())
			if err != nil {
				f.err = err.Error()
				return formNone
			}
			f.set(field.Key, v)
			f.editing = false
		case "esc":
			f.editing = false
			f.err = ""
		default:
			f.input.update(msg)
		}
		return formNone
	}
	switch msg.String() {
	case "esc", "q":
		return formClose
	case "s", "ctrl+s":
		if len(f.changed) == 0 {
			f.err = "nothing to save"
			return formNone
		}
		return formSave
	case "j", "down":
		if f.cursor < len(f.schema.Fields)-1 {
			f.cursor++
		}
	case "k", "up":
		if f.cursor > 0 {
			f.cursor--
		}
	case "enter", " ", "right", "l":
		f.activate(field, 1)
	case "left", "h":
		f.activate(field, -1)
	}
	return formNone
}

// activate changes the selected field: a toggle flips, a select moves by
// step through its choices (the empty one included), and numbers and text
// open the input.
func (f *agentForm) activate(field api.AgentSchemaField, step int) {
	switch {
	case field.Type == api.FieldBool:
		on := f.boolValue(field)
		if !on && field.Warning {
			f.err = field.Label + " can only be turned on in config.toml"
			return
		}
		f.set(field.Key, !on)
	case field.Type == api.FieldSelect && !field.AllowCustom:
		choices := append([]string{""}, field.Options...)
		current, _ := f.value(field.Key).(string)
		idx := 0
		for i, c := range choices {
			if c == current {
				idx = i
			}
		}
		next := choices[(idx+step+len(choices))%len(choices)]
		if next == "" {
			f.set(field.Key, nil)
		} else {
			f.set(field.Key, next)
		}
	default:
		if step < 0 {
			return
		}
		current := ""
		if v := f.value(field.Key); v != nil {
			current = formatFormValue(v)
		}
		f.input = newTextInput(current)
		f.editing = true
	}
}

func formatFormValue(v any) string {
	switch x := v.(type) {
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int64:
		return fmt.Sprintf("%d", x)
	default:
		return fmt.Sprintf("%v", x)
	}
}

// patch is the PATCH /config body for the edits. "Not set" is sent the way
// the daemon stores it: "" for text and selects, 0 for numbers.
func (f agentForm) patch() map[string]any {
	values := map[string]any{}
	for key, v := range f.changed {
		field, _ := f.schema.Field(key)
		if v == nil {
			switch field.Type {
			case api.FieldNumber:
				v = 0
			case api.FieldBool:
				v = false
			default:
				v = ""
			}
		}
		if n, ok := v.(float64); ok && n == float64(int64(n)) {
			v = int64(n)
		}
		values[key] = v
	}
	return api.AgentSettingsPatch(f.schema.Name, values)
}

// view renders the form, one line per field.
func (f agentForm) view() []string {
	keyStyle := lipgloss.NewStyle().Foreground(colorMuted)
	valStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#E5E7EB"))
	warnStyle := lipgloss.NewStyle().Foreground(colorWarning)
	lines := []string{headerStyle.Render(fmt.Sprintf("── %s settings ", api.DisplayText(f.schema.Title(), 40)))}
	if f.schema.Description != "" {
		lines = append(lines, keyStyle.Render("  "+api.DisplayText(f.schema.Description, 200)))
	}
	if len(f.schema.Fields) == 0 {
		lines = append(lines, keyStyle.Render("  This agent has no options."))
	}
	for i, field := range f.schema.Fields {
		marker := "  "
		if i == f.cursor {
			marker = "▸ "
		}
		label := fmt.Sprintf("%-28s", api.DisplayText(field.Label, 28))
		var val string
		switch {
		case i == f.cursor && f.editing:
			val = f.input.view()
		case field.Type == api.FieldBool:
			val = "off"
			if f.boolValue(field) {
				val = "on"
			}
		default:
			if v := f.value(field.Key); v != nil {
				val = api.DisplayText(formatFormValue(v), 60)
			} else if field.Default != nil {
				val = "default (" + api.DisplayText(formatFormValue(field.Default), 40) + ")"
			} else if field.EmptyLabel != "" {
				val = api.DisplayText(field.EmptyLabel, 40)
			} else {
				val = "not set"
			}
		}
		if _, edited := f.changed[field.Key]; edited {
			val += " *"
		}
		line := marker + keyStyle.Render(label) + " " + valStyle.Render(val)
		if field.Warning {
			line += warnStyle.Render("  ⚠ only config.toml turns it on")
		}
		lines = append(lines, line)
		if i == f.cursor && field.Description != "" {
			lines = append(lines, keyStyle.Render("    "+api.DisplayText(strings.ReplaceAll(field.Description, "\n", " "), 160)))
		}
		if i == f.cursor && len(field.Options) > 0 && (field.AllowCustom || field.Type == api.FieldText) {
			lines = append(lines, keyStyle.Render("    suggestions: "+api.DisplayText(strings.Join(field.Options, ", "), 160)))
		}
	}
	if f.err != "" {
		lines = append(lines, "", statusError.Render("  "+api.DisplayText(f.err, 200)))
	}
	if len(f.changed) > 0 {
		keys := make([]string, 0, len(f.changed))
		for k := range f.changed {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		lines = append(lines, "", keyStyle.Render("  unsaved: "+strings.Join(keys, ", ")))
	}
	return lines
}

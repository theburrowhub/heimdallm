package tui

import (
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/theburrowhub/heimdallm/cli/internal/api"
)

// agentsMode is what the agents panel shows.
type agentsMode int

const (
	agentsList agentsMode = iota
	agentsForm
	agentsWizard
)

// agentsPanel is the Config tab's agent editor, opened with [a]: the list of
// agents, the settings form of one (rendered from its schema), or the wizard
// that creates a custom agent.
type agentsPanel struct {
	open    bool
	mode    agentsMode
	cursor  int
	loading bool
	catalog *api.AgentSchemaCatalog
	err     string
	status  string
	form    agentForm
	wizard  agentWizard
}

type agentSchemasMsg struct {
	catalog *api.AgentSchemaCatalog
	err     error
}

type agentSettingsSavedMsg struct {
	agent string
	err   error
}

type agentSchemaSavedMsg struct {
	name string
	err  error
}

func (d *Dashboard) fetchAgentSchemas() tea.Msg {
	cat, err := d.client.GetAgentSchemas()
	return agentSchemasMsg{catalog: cat, err: err}
}

func (d *Dashboard) openAgentsPanel() tea.Cmd {
	d.agentsPanel = agentsPanel{open: true, loading: true}
	return d.fetchAgentSchemas
}

func (d *Dashboard) agentConfig(name string) map[string]any {
	if d.config == nil {
		return nil
	}
	all, _ := d.config["agent_configs"].(map[string]any)
	cfg, _ := all[name].(map[string]any)
	return cfg
}

func (d *Dashboard) handleAgentsMsg(msg tea.Msg) (tea.Cmd, bool) {
	p := &d.agentsPanel
	switch msg := msg.(type) {
	case agentSchemasMsg:
		p.loading = false
		if msg.err != nil {
			p.err = "Could not load the agent schemas: " + msg.err.Error()
			return nil, true
		}
		p.catalog = msg.catalog
		return nil, true
	case agentSettingsSavedMsg:
		if msg.err != nil {
			p.form.err = "Save failed: " + daemonMessage(msg.err)
			return nil, true
		}
		p.mode = agentsList
		p.status = "Saved " + msg.agent + "."
		// The dashboard shows the daemon's config: read it again.
		return d.fetchData, true
	case agentSchemaSavedMsg:
		if msg.err != nil {
			p.wizard.err = "Save failed: " + daemonMessage(msg.err)
			return nil, true
		}
		p.mode = agentsList
		p.status = "Created " + msg.name + "."
		return tea.Batch(d.fetchAgentSchemas, d.fetchData), true
	}
	return nil, false
}

// daemonMessage is the daemon's own error text when it sent one.
func daemonMessage(err error) string {
	var httpErr *api.HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Message()
	}
	return err.Error()
}

func (d *Dashboard) handleAgentsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := &d.agentsPanel
	if msg.String() == "ctrl+c" {
		if d.sseCancel != nil {
			d.sseCancel()
		}
		return d, tea.Quit
	}
	switch p.mode {
	case agentsForm:
		switch p.form.handleKey(msg) {
		case formClose:
			p.mode = agentsList
		case formSave:
			patch, agent := p.form.patch(), p.form.schema.Name
			return d, func() tea.Msg {
				return agentSettingsSavedMsg{agent: agent, err: d.client.PatchConfig(patch)}
			}
		}
		return d, nil
	case agentsWizard:
		switch p.wizard.handleKey(msg) {
		case wizardCancel:
			p.mode = agentsList
		case wizardSave:
			schema := p.wizard.schema
			return d, func() tea.Msg {
				return agentSchemaSavedMsg{name: schema.Name, err: d.client.PutAgentSchema(schema)}
			}
		}
		return d, nil
	}
	schemas := p.schemas()
	switch msg.String() {
	case "esc", "q", "a":
		p.open = false
	case "j", "down":
		if p.cursor < len(schemas)-1 {
			p.cursor++
		}
	case "k", "up":
		if p.cursor > 0 {
			p.cursor--
		}
	case "enter":
		if p.cursor < len(schemas) {
			s := schemas[p.cursor]
			p.form = newAgentForm(s, d.agentConfig(s.Name))
			p.mode = agentsForm
			p.status = ""
		}
	case "n":
		if p.catalog != nil {
			p.wizard = newAgentWizard(p.catalog)
			p.mode = agentsWizard
			p.status = ""
		}
	}
	return d, nil
}

func (p agentsPanel) schemas() []api.AgentSchema {
	if p.catalog == nil {
		return nil
	}
	return p.catalog.Schemas
}

func (d *Dashboard) renderAgentsPanel(height int) string {
	p := d.agentsPanel
	var lines []string
	switch {
	case p.loading:
		lines = []string{lipgloss.NewStyle().Foreground(colorMuted).Render("  Loading agent schemas…")}
	case p.err != "":
		lines = []string{statusError.Render("  " + api.DisplayText(p.err, 200))}
	case p.mode == agentsForm:
		lines = p.form.view()
	case p.mode == agentsWizard:
		lines = p.wizard.view()
	default:
		lines = d.agentsListLines()
	}
	if len(lines) > height {
		// Keep the cursor's end of a long form or review in view.
		lines = lines[len(lines)-height:]
	}
	return strings.Join(lines, "\n")
}

func (d *Dashboard) agentsListLines() []string {
	p := d.agentsPanel
	muted := lipgloss.NewStyle().Foreground(colorMuted)
	lines := []string{headerStyle.Render("── Agents ")}
	for i, s := range p.schemas() {
		kind := "built-in"
		if !s.Builtin {
			kind = "custom · " + api.DisplayText(s.Binary, 30)
		}
		line := fmt.Sprintf("  %-26s %-12s %s", api.DisplayText(s.Title(), 26), api.DisplayText(s.Name, 12), kind)
		if i == p.cursor {
			lines = append(lines, selectedRowStyle.Render(line))
		} else {
			lines = append(lines, line)
		}
	}
	if p.catalog != nil && !p.catalog.CustomAgentsAPIEnabled {
		lines = append(lines, "", muted.Render(
			"  New custom agents are written to config.toml by hand; [n] generates the section to paste."))
	}
	if p.status != "" {
		lines = append(lines, "", statusOnline.Render("  "+api.DisplayText(p.status, 200)))
	}
	return lines
}

func (d *Dashboard) agentsHelp() string {
	switch d.agentsPanel.mode {
	case agentsForm:
		if d.agentsPanel.form.editing {
			return "[enter]set  [esc]cancel  [ctrl+u]clear"
		}
		return "[j/k]move  [enter/space]change  [←/→]cycle  [s]ave  [esc]back"
	case agentsWizard:
		if d.agentsPanel.wizard.step == stepReview {
			if d.agentsPanel.wizard.apiEnabled {
				return "[s/enter]save  [esc]discard"
			}
			return "[esc]close"
		}
		return "[enter]answer  [esc]cancel"
	default:
		return "[j/k]move  [enter]edit settings  [n]ew custom agent  [esc]close"
	}
}

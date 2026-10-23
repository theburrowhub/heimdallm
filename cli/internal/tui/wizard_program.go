package tui

import (
	"io"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/theburrowhub/heimdallm/cli/internal/api"
)

// wizardProgram runs the custom agent wizard on its own, for
// `heimdallm-cli agents new`.
type wizardProgram struct {
	client *api.Client
	wizard agentWizard
	saving bool
	// result is printed after the program exits: what was created, or the
	// config.toml section to paste.
	result string
}

func (m *wizardProgram) Init() tea.Cmd { return nil }

func (m *wizardProgram) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if m.saving {
			return m, nil
		}
		switch m.wizard.handleKey(msg) {
		case wizardCancel:
			if m.wizard.step == stepReview && !m.wizard.apiEnabled &&
				len(m.wizard.schema.ValidateCustom(m.wizard.reserved)) == 0 {
				m.result = m.wizard.schema.TOMLSnippet()
			}
			return m, tea.Quit
		case wizardSave:
			m.saving = true
			schema := m.wizard.schema
			return m, func() tea.Msg {
				return agentSchemaSavedMsg{name: schema.Name, err: m.client.PutAgentSchema(schema)}
			}
		}
	case agentSchemaSavedMsg:
		m.saving = false
		if msg.err != nil {
			m.wizard.err = "Save failed: " + daemonMessage(msg.err)
			return m, nil
		}
		m.result = "Created custom agent " + msg.name + ".\n"
		return m, tea.Quit
	}
	return m, nil
}

func (m *wizardProgram) View() string {
	lines := m.wizard.view()
	if m.saving {
		lines = append(lines, "", "  Saving…")
	}
	return strings.Join(lines, "\n") + "\n"
}

// RunAgentWizard asks for a custom agent's definition interactively. It saves
// it through the daemon when config.toml allows it, and otherwise writes the
// config.toml section to add to out once the wizard closes.
func RunAgentWizard(client *api.Client, catalog *api.AgentSchemaCatalog, in io.Reader, out io.Writer) error {
	m := &wizardProgram{client: client, wizard: newAgentWizard(catalog)}
	opts := []tea.ProgramOption{tea.WithOutput(out)}
	if in != nil {
		opts = append(opts, tea.WithInput(in))
	}
	if _, err := tea.NewProgram(m, opts...).Run(); err != nil {
		return err
	}
	if m.result != "" {
		_, err := io.WriteString(out, m.result)
		return err
	}
	return nil
}

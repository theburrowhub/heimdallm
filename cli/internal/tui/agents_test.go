package tui

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/theburrowhub/heimdallm/cli/internal/api"
)

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "ctrl+u":
		return tea.KeyMsg{Type: tea.KeyCtrlU}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func typeText(t *testing.T, send func(tea.KeyMsg), text string) {
	t.Helper()
	for _, r := range text {
		if r == ' ' {
			send(key(" "))
		} else {
			send(key(string(r)))
		}
	}
}

func fptr(f float64) *float64 { return &f }

var claudeSchema = api.AgentSchema{Name: "claude", DisplayName: "Claude Code", Builtin: true, Fields: []api.AgentSchemaField{
	{Key: "model", Type: api.FieldSelect, Label: "Model", Flag: "--model", AllowCustom: true, Options: []string{"opus"}, EmptyLabel: "CLI default"},
	{Key: "max_turns", Type: api.FieldNumber, Label: "--max-turns", Flag: "--max-turns", Min: fptr(0), Description: "turn cap"},
	{Key: "effort", Type: api.FieldSelect, Label: "--effort", Flag: "--effort", Options: []string{"low", "high"}},
	{Key: "bare", Type: api.FieldBool, Label: "--bare", Flag: "--bare"},
	{Key: "dangerously_skip_perms", Type: api.FieldBool, Label: "danger", Flag: "--dangerously-skip-permissions", Warning: true},
}}

var aiderSchema = api.AgentSchema{Name: "aider", Binary: "aider", Description: "Aider pairs", Fields: []api.AgentSchemaField{
	{Key: "model", Type: api.FieldSelect, Label: "Model", Flag: "--model", Options: []string{"gpt-4o"}},
	{Key: "auto_commits", Type: api.FieldBool, Label: "Commits", Flag: "--auto-commits", Default: true},
	{Key: "notes", Type: api.FieldText, Label: "Notes", Flag: "--notes", Options: []string{"a.md"}},
	{Key: "depth", Type: api.FieldNumber, Label: "Depth", Flag: "--depth", Default: 3.0},
}}

func TestTextInput(t *testing.T) {
	in := newTextInput("ab")
	if !in.update(key("c")) || in.String() != "abc" {
		t.Fatalf("insert: %q", in.String())
	}
	in.update(key(" "))
	in.update(key("backspace"))
	in.update(key("backspace"))
	if in.String() != "ab" || in.view() != "ab█" {
		t.Fatalf("backspace: %q", in.String())
	}
	if in.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'\x1b'}}) {
		t.Fatal("control runes are dropped")
	}
	in.update(key("ctrl+u"))
	if in.String() != "" || in.update(key("backspace")) || in.update(key("ctrl+u")) || in.update(key("up")) {
		t.Fatalf("clear: %q", in.String())
	}
	long := newTextInput(strings.Repeat("a", maxInputRunes))
	if long.update(key("b")) {
		t.Fatal("the input is bounded")
	}
}

func TestAgentFormBuiltin(t *testing.T) {
	f := newAgentForm(claudeSchema, map[string]any{
		"model": "", "max_turns": 0.0, "effort": "low", "bare": false, "dangerously_skip_perms": false,
	})
	view := strings.Join(f.view(), "\n")
	for _, want := range []string{"Claude Code settings", "CLI default", "not set", "low", "only config.toml"} {
		if !strings.Contains(view, want) {
			t.Errorf("missing %q in\n%s", want, view)
		}
	}
	if f.handleKey(key("s")) != formNone || f.err != "nothing to save" {
		t.Fatal("saving nothing must be refused")
	}

	// model: type a value that is not listed.
	f.handleKey(key("enter"))
	if !f.editing {
		t.Fatal("enter edits a free-form select")
	}
	typeText(t, func(k tea.KeyMsg) { f.handleKey(k) }, "sonnet")
	f.handleKey(key("enter"))
	// max_turns: a bad value keeps the input open; esc cancels.
	f.handleKey(key("down"))
	if !strings.Contains(strings.Join(f.view(), "\n"), "turn cap") {
		t.Fatal("the selected field shows its description")
	}
	f.handleKey(key("enter"))
	typeText(t, func(k tea.KeyMsg) { f.handleKey(k) }, "x")
	f.handleKey(key("enter"))
	if !f.editing || !strings.Contains(f.err, "not a number") {
		t.Fatalf("bad number: editing=%v err=%q", f.editing, f.err)
	}
	f.handleKey(key("esc"))
	f.handleKey(key("enter"))
	typeText(t, func(k tea.KeyMsg) { f.handleKey(k) }, "12")
	f.handleKey(key("enter"))
	// effort: cycle forwards and backwards through "", low, high.
	f.handleKey(key("down"))
	f.handleKey(key("right"))
	if f.value("effort") != "high" {
		t.Fatalf("effort = %v", f.value("effort"))
	}
	f.handleKey(key("right"))
	if f.value("effort") != nil {
		t.Fatal("cycling wraps to the empty choice")
	}
	f.handleKey(key("left"))
	// bare: toggle.
	f.handleKey(key("down"))
	f.handleKey(key(" "))
	// danger: off and cannot be turned on.
	f.handleKey(key("down"))
	f.handleKey(key("enter"))
	if f.boolValue(claudeSchema.Fields[4]) || !strings.Contains(f.err, "config.toml") {
		t.Fatalf("danger toggle turned on: err=%q", f.err)
	}
	f.handleKey(key("down")) // already last
	f.handleKey(key("left")) // nothing for a toggle going back
	if f.handleKey(key("s")) != formSave {
		t.Fatal("save")
	}
	got := f.patch()["ai"].(map[string]any)["agents"].(map[string]any)["claude"].(map[string]any)
	if got["model"] != "sonnet" || got["max_turns"] != int64(12) || got["effort"] != "high" || got["bare"] != true {
		t.Fatalf("patch = %v", got)
	}
	if _, set := got["dangerously_skip_perms"]; set {
		t.Fatal("a refused toggle must not be sent")
	}
	view = strings.Join(f.view(), "\n")
	if !strings.Contains(view, "unsaved: bare, effort, max_turns, model") {
		t.Fatalf("view:\n%s", view)
	}
	for f.cursor > 0 {
		f.handleKey(key("up"))
	}
	f.handleKey(key("up"))
	if f.handleKey(key("esc")) != formClose {
		t.Fatal("esc closes")
	}
}

func TestAgentFormDangerousToggleThatIsOn(t *testing.T) {
	f := newAgentForm(claudeSchema, map[string]any{"dangerously_skip_perms": true})
	f.cursor = 4
	f.handleKey(key("enter"))
	if f.boolValue(claudeSchema.Fields[4]) {
		t.Fatal("an enabled dangerous toggle can be turned off")
	}
	f.handleKey(key("enter"))
	if f.boolValue(claudeSchema.Fields[4]) {
		t.Fatal("and not back on")
	}
}

func TestAgentFormCustom(t *testing.T) {
	f := newAgentForm(aiderSchema, map[string]any{
		"model": "gpt-4o", "binary": "aider",
		"options": map[string]any{"auto_commits": false, "notes": "", "depth": int64(5)},
	})
	if f.value("model") != "gpt-4o" || f.value("auto_commits") != false || f.value("notes") != nil || f.value("depth") != 5.0 {
		t.Fatalf("values = %v", f.values)
	}
	view := strings.Join(f.view(), "\n")
	if !strings.Contains(view, "Aider pairs") {
		t.Fatalf("view:\n%s", view)
	}
	// notes: suggestions show, empty input unsets.
	f.cursor = 2
	if !strings.Contains(strings.Join(f.view(), "\n"), "suggestions: a.md") {
		t.Fatal("suggestions")
	}
	f.handleKey(key("enter"))
	f.handleKey(key("enter"))
	// depth: clear it.
	f.cursor = 3
	f.handleKey(key("enter"))
	f.handleKey(key("ctrl+u"))
	f.handleKey(key("enter"))
	if !strings.Contains(strings.Join(f.view(), "\n"), "default (3)") {
		t.Fatalf("view:\n%s", strings.Join(f.view(), "\n"))
	}
	got := f.patch()["ai"].(map[string]any)["agents"].(map[string]any)["aider"].(map[string]any)
	if !reflect.DeepEqual(got, map[string]any{"notes": "", "depth": 0}) {
		t.Fatalf("patch = %#v", got)
	}
	// An unset toggle falls back to its default.
	g := newAgentForm(aiderSchema, map[string]any{"binary": "aider"})
	if !g.boolValue(aiderSchema.Fields[1]) {
		t.Fatal("default on")
	}
	g.cursor = 1
	g.handleKey(key("enter"))
	if g.patch()["ai"].(map[string]any)["agents"].(map[string]any)["aider"].(map[string]any)["auto_commits"] != false {
		t.Fatal("toggle off")
	}
	if normalizeFormValue(api.AgentSchemaField{}, 0) != nil || normalizeFormValue(api.AgentSchemaField{}, 2) != 2.0 {
		t.Fatal("normalizeFormValue ints")
	}
	if formatFormValue(int64(3)) != "3" || formatFormValue("x") != "x" || formatFormValue(1.5) != "1.5" {
		t.Fatal("formatFormValue")
	}
}

func TestAgentFormWithoutFields(t *testing.T) {
	f := newAgentForm(api.AgentSchema{Name: "x", Binary: "x"}, nil)
	if !strings.Contains(strings.Join(f.view(), "\n"), "no options") {
		t.Fatal("empty form")
	}
	if f.handleKey(key("j")) != formNone || f.handleKey(key("q")) != formClose {
		t.Fatal("keys of an empty form")
	}
	// A nil patch value for a toggle is sent as off.
	f.schema.Fields = []api.AgentSchemaField{{Key: "b", Type: api.FieldBool}}
	f.changed["b"] = nil
	if f.patch()["ai"].(map[string]any)["agents"].(map[string]any)["x"].(map[string]any)["b"] != false {
		t.Fatal("nil toggle")
	}
}

func answer(t *testing.T, w *agentWizard, text string) {
	t.Helper()
	typeText(t, func(k tea.KeyMsg) { w.handleKey(k) }, text)
	w.handleKey(key("enter"))
}

var schemaCatalog = &api.AgentSchemaCatalog{Schemas: []api.AgentSchema{claudeSchema, aiderSchema}}

func TestAgentWizardBuildsASchema(t *testing.T) {
	w := newAgentWizard(schemaCatalog)
	steps := []struct{ answer, wantErr string }{
		{"claude", "built-in"}, {"Bad", "lowercase"}, {"tool", ""},
		{"Tool", ""},
		{"/bin/x", "without a path"}, {"tool-cli", ""},
		{"maybe", "y or n"}, {"y", ""},
		{"Bad", "lowercase"}, {"mode", ""},
		{"date", "select, bool"}, {"select", ""},
		{"", "required"}, {"Mode", ""},
		{"mode", "--name"}, {"--mode", ""},
		{"a, -b", "must not begin"}, {"fast, deep", ""},
		{"x", "y or n"}, {"n", ""},
		{"slow", "not one of"}, {"fast", ""},
		{"y", ""}, {"mode", "already has"}, {"commits", ""}, {"bool", ""}, {"Commits", ""}, {"--commits", ""},
		{"--commits", "different flag"}, {"--no-commits", ""}, {"n", ""}, {"y", ""},
		{"y", ""}, {"yolo", ""}, {"bool", ""}, {"Yolo", ""}, {"--yolo", ""}, {"", ""}, {"y", ""},
		{"y", ""}, {"depth", ""}, {"number", ""}, {"Depth", ""}, {"--depth", ""},
		{"-1", "not negative"}, {"2", ""}, {"1", "below the minimum"}, {"9", ""}, {"20", "out of range"}, {"4", ""},
		{"y", ""}, {"notes", ""}, {"text", ""}, {"Notes", ""}, {"--notes", ""}, {"", ""},
		{"y", ""}, {"model", ""}, {"select", ""}, {"Model", ""}, {"--model", ""}, {"", ""}, {"n", "must accept"}, {"y", ""}, {"", ""},
		{"n", ""},
	}
	for i, s := range steps {
		q, _ := w.prompt()
		answer(t, &w, s.answer)
		if s.wantErr == "" && w.err != "" {
			t.Fatalf("step %d (%s = %q): unexpected error %q", i, q, s.answer, w.err)
		}
		if s.wantErr != "" && !strings.Contains(w.err, s.wantErr) {
			t.Fatalf("step %d (%s = %q): error %q, want %q", i, q, s.answer, w.err, s.wantErr)
		}
		if s.wantErr != "" {
			// Clear the rejected answer before retrying.
			w.input = newTextInput("")
		}
	}
	if w.step != stepReview {
		t.Fatalf("step = %v", w.step)
	}
	data, _ := json.Marshal(w.schema)
	var got map[string]any
	_ = json.Unmarshal(data, &got)
	fields := got["fields"].([]any)
	if len(fields) != 6 || got["binary"] != "tool-cli" || got["display_name"] != "Tool" {
		t.Fatalf("schema = %s", data)
	}
	if fields[0].(map[string]any)["default"] != "fast" || fields[1].(map[string]any)["false_flag"] != "--no-commits" ||
		fields[1].(map[string]any)["default"] != true || fields[2].(map[string]any)["warning"] != true ||
		fields[3].(map[string]any)["max"] != 9.0 || fields[3].(map[string]any)["default"] != 4.0 ||
		fields[5].(map[string]any)["allow_custom"] != true {
		t.Fatalf("fields = %s", data)
	}

	// Without the opt-in the review shows the config.toml section and
	// saving does nothing.
	view := strings.Join(w.view(), "\n")
	if !strings.Contains(view, "[ai.agents.tool]") || !strings.Contains(view, "• depth (number) --depth") {
		t.Fatalf("review:\n%s", view)
	}
	if w.handleKey(key("s")) != wizardNone || w.handleKey(key("x")) != wizardNone {
		t.Fatal("review keys")
	}
	w.apiEnabled = true
	if !strings.Contains(strings.Join(w.view(), "\n"), "s or enter: save") {
		t.Fatal("review with the opt-in")
	}
	if w.handleKey(key("enter")) != wizardSave {
		t.Fatal("save")
	}
	if w.handleKey(key("esc")) != wizardCancel {
		t.Fatal("esc")
	}
}

func TestAgentWizardReviewOfAnInvalidSchema(t *testing.T) {
	w := newAgentWizard(nil)
	w.step = stepReview
	w.apiEnabled = true
	w.schema = api.AgentSchema{Name: "BAD"}
	if !strings.Contains(strings.Join(w.view(), "\n"), "esc to close") {
		t.Fatal("invalid review")
	}
	if w.handleKey(key("s")) != wizardNone || w.err == "" {
		t.Fatal("an invalid schema is not saved")
	}
	v := newAgentWizard(nil)
	if v.handleKey(key("esc")) != wizardCancel {
		t.Fatal("esc cancels")
	}
	for _, step := range []wizardStep{stepFieldMin, stepFieldMax, stepFieldWarning, stepFieldAllowCustom, stepReview} {
		v.step = step
		v.prompt()
	}
	v.field.Type = api.FieldBool
	v.step = stepFieldDefault
	if _, hint := v.prompt(); !strings.Contains(hint, "on or off") {
		t.Fatal("bool default hint")
	}
	v.field.Type = api.FieldSelect
	if _, hint := v.prompt(); !strings.Contains(hint, "one of") {
		t.Fatal("select default hint")
	}
	v.step = stepFieldWarning
	if err := v.answer("maybe"); err == nil {
		t.Fatal("warning y/n")
	}
	v.step = stepFieldDefault
	v.field = api.AgentSchemaField{Type: api.FieldBool}
	if err := v.answer("maybe"); err == nil {
		t.Fatal("bool default y/n")
	}
	v.step = stepFieldMax
	if err := v.answer("x"); err == nil {
		t.Fatal("max must be a number")
	}
	v.status = "Saved"
	if !strings.Contains(strings.Join(v.view(), "\n"), "Saved") {
		t.Fatal("status")
	}
}

// dashboardWithAgents serves the endpoints the agents panel uses.
func dashboardWithAgents(t *testing.T, apiEnabled bool) (*Dashboard, *[]string) {
	t.Helper()
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, r.Method+" "+r.URL.Path+" "+string(body))
		switch r.URL.Path {
		case "/agent-schemas":
			cat := *schemaCatalog
			cat.CustomAgentsAPIEnabled = apiEnabled
			_ = json.NewEncoder(w).Encode(cat)
		case "/agent-schemas/tool":
			if !apiEnabled {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":"disabled"}`))
				return
			}
			_, _ = w.Write([]byte(`{}`))
		case "/config":
			if r.Method == "PATCH" && strings.Contains(string(body), "fail") {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"bad value"}`))
				return
			}
			_, _ = w.Write([]byte(`{"agent_configs":{"claude":{"effort":"low"},"aider":{"binary":"aider","options":{"notes":"x.md"}}}}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	d := NewDashboard(srv.URL, "", "test")
	d.width, d.height = 160, 60
	d.activeTab = tabConfig
	cfg, _ := d.client.GetConfig()
	d.config = cfg
	return d, &requests
}

// press sends a key to the dashboard and runs the command it returns.
func press(d *Dashboard, k tea.KeyMsg) {
	_, cmd := d.Update(k)
	for cmd != nil {
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				if c != nil {
					d.Update(c())
				}
			}
			return
		}
		if msg == nil {
			return
		}
		_, cmd = d.Update(msg)
	}
}

func TestDashboardAgentsPanelEditsSettings(t *testing.T) {
	d, requests := dashboardWithAgents(t, false)
	if !strings.Contains(d.renderHelp(), "[a]gents") {
		t.Fatal("the Config tab advertises the agents panel")
	}
	press(d, key("a"))
	if !d.agentsPanel.open || d.agentsPanel.loading {
		t.Fatalf("panel = %+v", d.agentsPanel)
	}
	view := d.renderContent(d.contentHeight())
	for _, want := range []string{"Claude Code", "built-in", "custom · aider", "[n] generates"} {
		if !strings.Contains(view, want) {
			t.Errorf("missing %q in\n%s", want, view)
		}
	}
	if !strings.Contains(d.renderHelp(), "[n]ew custom agent") {
		t.Fatal("list help")
	}
	press(d, key("down"))
	press(d, key("up"))
	press(d, key("enter"))
	if d.agentsPanel.mode != agentsForm || d.agentsPanel.form.value("effort") != "low" {
		t.Fatalf("form = %+v", d.agentsPanel.form)
	}
	if !strings.Contains(d.renderHelp(), "[s]ave") {
		t.Fatal("form help")
	}
	press(d, key("enter")) // edit the model
	if !strings.Contains(d.renderHelp(), "[enter]set") {
		t.Fatal("editing help")
	}
	press(d, key("esc"))
	press(d, key("down"))
	press(d, key("down"))
	press(d, key("right"))
	press(d, key("s"))
	if d.agentsPanel.mode != agentsList || !strings.Contains(d.agentsPanel.status, "Saved claude") {
		t.Fatalf("after save: %+v", d.agentsPanel)
	}
	var patched bool
	for _, r := range *requests {
		if strings.HasPrefix(r, `PATCH /config {"ai":{"agents":{"claude":{"effort":"high"}}}}`) {
			patched = true
		}
	}
	if !patched {
		t.Fatalf("requests = %v", *requests)
	}
	if !strings.Contains(d.renderContent(d.contentHeight()), "Saved claude") {
		t.Fatal("status line")
	}
	press(d, key("esc"))
	if d.agentsPanel.open {
		t.Fatal("esc closes the panel")
	}
}

func TestDashboardAgentsPanelSaveError(t *testing.T) {
	d, _ := dashboardWithAgents(t, false)
	press(d, key("a"))
	press(d, key("down")) // aider
	press(d, key("enter"))
	d.agentsPanel.form.cursor = 2
	press(d, key("enter"))
	typeText(t, func(k tea.KeyMsg) { press(d, k) }, "fail")
	press(d, key("enter"))
	press(d, key("s"))
	if d.agentsPanel.mode != agentsForm || !strings.Contains(d.agentsPanel.form.err, "bad value") {
		t.Fatalf("form err = %q", d.agentsPanel.form.err)
	}
	press(d, key("esc"))
	if d.agentsPanel.mode != agentsList {
		t.Fatal("esc returns to the list")
	}
}

func TestDashboardAgentsPanelWizard(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		d, _ := dashboardWithAgents(t, enabled)
		press(d, key("a"))
		press(d, key("n"))
		if d.agentsPanel.mode != agentsWizard || !strings.Contains(d.renderHelp(), "[enter]answer") {
			t.Fatal("wizard")
		}
		for _, a := range []string{"tool", "", "tool"} {
			typeText(t, func(k tea.KeyMsg) { press(d, k) }, a)
			press(d, key("enter"))
		}
		press(d, key("n"))
		press(d, key("enter"))
		wantHelp := "[esc]close"
		if enabled {
			wantHelp = "[s/enter]save"
		}
		if !strings.Contains(d.renderHelp(), wantHelp) {
			t.Fatalf("review help = %q", d.renderHelp())
		}
		press(d, key("s"))
		if enabled {
			if d.agentsPanel.mode != agentsList || !strings.Contains(d.agentsPanel.status, "Created tool") {
				t.Fatalf("enabled: %+v", d.agentsPanel)
			}
			continue
		}
		if d.agentsPanel.mode != agentsWizard || !strings.Contains(d.renderContent(d.contentHeight()), "[ai.agents.tool]") {
			t.Fatalf("disabled: %+v", d.agentsPanel)
		}
		press(d, key("esc"))
		if d.agentsPanel.mode != agentsList {
			t.Fatal("esc closes the wizard")
		}
	}
}

func TestDashboardAgentsPanelErrors(t *testing.T) {
	d := NewDashboard("http://127.0.0.1:1", "", "test")
	d.width, d.height = 120, 40
	d.activeTab = tabConfig
	press(d, key("a"))
	if !strings.Contains(d.renderContent(d.contentHeight()), "Could not load the agent schemas") {
		t.Fatal("load error")
	}
	press(d, key("n")) // no catalog: nothing happens
	if d.agentsPanel.mode != agentsList {
		t.Fatal("no wizard without a catalog")
	}
	d.agentsPanel.loading = true
	d.agentsPanel.err = ""
	if !strings.Contains(d.renderContent(d.contentHeight()), "Loading") {
		t.Fatal("loading")
	}
	_, cmd := d.Update(key("ctrl+c"))
	if cmd == nil {
		t.Fatal("ctrl+c quits from the panel")
	}
	// A save error from the wizard stays in the wizard.
	d.agentsPanel = agentsPanel{open: true, mode: agentsWizard, wizard: newAgentWizard(schemaCatalog)}
	d.Update(agentSchemaSavedMsg{name: "tool", err: errors.New("boom")})
	if !strings.Contains(d.agentsPanel.wizard.err, "boom") {
		t.Fatal("wizard save error")
	}
	// A long panel keeps its end in view.
	d.agentsPanel = agentsPanel{open: true, catalog: &api.AgentSchemaCatalog{}}
	for i := 0; i < 100; i++ {
		d.agentsPanel.catalog.Schemas = append(d.agentsPanel.catalog.Schemas, api.AgentSchema{Name: "a", Builtin: true})
	}
	if got := strings.Count(d.renderAgentsPanel(10), "\n"); got != 9 {
		t.Fatalf("lines = %d", got+1)
	}
	if d.agentConfig("x") != nil {
		t.Fatal("no config")
	}
	if daemonMessage(errors.New("plain")) != "plain" {
		t.Fatal("daemonMessage")
	}
}

func TestDashboardConfigListsEveryAgentSetting(t *testing.T) {
	d := NewDashboard("http://127.0.0.1:1", "", "test")
	d.config = map[string]any{"agent_configs": map[string]any{
		"aider": map[string]any{"binary": "aider", "model": "gpt-4o", "options": map[string]any{"edit_format": "diff"}},
	}}
	joined := strings.Join(d.buildConfigLines(), "\n")
	for _, want := range []string{"aider", "binary:", "model:", "gpt-4o", "edit_format:", "diff"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in\n%s", want, joined)
		}
	}
}

func TestWizardProgram(t *testing.T) {
	d, requests := dashboardWithAgents(t, true)
	m := &wizardProgram{client: d.client, wizard: newAgentWizard(schemaCatalog)}
	m.wizard.apiEnabled = true
	if m.Init() != nil {
		t.Fatal("Init")
	}
	send := func(k tea.KeyMsg) tea.Cmd {
		_, cmd := m.Update(k)
		return cmd
	}
	for _, a := range []string{"tool", "", "tool", "n"} {
		typeText(t, func(k tea.KeyMsg) { send(k) }, a)
		send(key("enter"))
	}
	cmd := send(key("s"))
	if !m.saving || !strings.Contains(m.View(), "Saving") {
		t.Fatal("saving")
	}
	if send(key("x")) != nil {
		t.Fatal("keys are ignored while saving")
	}
	msg := cmd()
	if _, cmd = m.Update(msg); cmd == nil || m.result != "Created custom agent tool.\n" {
		t.Fatalf("result = %q", m.result)
	}
	if !strings.Contains(strings.Join(*requests, "\n"), "PUT /agent-schemas/tool") {
		t.Fatalf("requests = %v", *requests)
	}

	// A failed save stays open with the daemon's message.
	m.saving = true
	m.Update(agentSchemaSavedMsg{name: "tool", err: &api.HTTPError{Status: 403, Body: `{"error":"disabled"}`}})
	if m.saving || !strings.Contains(m.wizard.err, "disabled") {
		t.Fatalf("err = %q", m.wizard.err)
	}

	// Without the opt-in, closing the review prints the section to paste.
	n := &wizardProgram{wizard: newAgentWizard(schemaCatalog)}
	n.wizard.step = stepReview
	n.wizard.schema = api.AgentSchema{Name: "tool", Binary: "tool"}
	n.Update(key("esc"))
	if !strings.HasPrefix(n.result, "[ai.agents.tool]") {
		t.Fatalf("result = %q", n.result)
	}
	if _, cmd := n.Update(key("ctrl+c")); cmd == nil {
		t.Fatal("ctrl+c quits")
	}
	if _, cmd := n.Update(tea.WindowSizeMsg{}); cmd != nil {
		t.Fatal("other messages are ignored")
	}
}

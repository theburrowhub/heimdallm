package flows

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
)

type scriptRunner struct {
	mu      sync.Mutex
	outputs []string
	errs    []error
	prompts []string
	dirs    []string
	block   chan struct{}
}

func (s *scriptRunner) Run(ctx context.Context, agent, prompt, workDir string) ([]byte, error) {
	s.mu.Lock()
	i := len(s.prompts)
	s.prompts = append(s.prompts, prompt)
	s.dirs = append(s.dirs, workDir)
	s.mu.Unlock()
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	var err error
	if i < len(s.errs) {
		err = s.errs[i]
	}
	out := ""
	if i < len(s.outputs) {
		out = s.outputs[i]
	}
	return []byte(out), err
}

var actx = AssistContext{
	Agents: []AssistAgent{{ID: "claude", Runnable: true, Models: []string{"opus"}}, {ID: "codex", Runnable: true}, {ID: "openrouter"}},
	Now:    time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC),
}

const goodOutput = "Here you go:\n```json\n" + `{"plan":"Codex at night, Claude otherwise.","flow":{"name":"Night","start":"c1","nodes":{
"c1":{"type":"condition","yes":"a1","no":"a2","schedule":{"s1":{"from":"22:00","to":"06:00"}}},
"a1":{"type":"agent","agent":"codex","next":"a2"},
"a2":{"type":"agent","agent":"gemini"}}}}` + "\n```\nDone."

func TestAssistReturnsValidatedDraft(t *testing.T) {
	r := &scriptRunner{outputs: []string{goodOutput}}
	a := &Assistant{Runner: r}
	res, err := a.Assist(context.Background(), AssistRequest{Agent: "claude", Instruction: "codex at night"}, actx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Flow.Start != "c1" || res.Plan == "" || res.Agent != "claude" {
		t.Fatalf("%+v", res)
	}
	joined := strings.Join(res.Warnings, "\n")
	if !strings.Contains(joined, "gemini is not installed") || !strings.Contains(joined, "positions") {
		t.Fatalf("warnings %v", res.Warnings)
	}
	if !strings.Contains(r.prompts[0], "codex at night") || !strings.Contains(r.prompts[0], "claude (models: opus)") {
		t.Fatalf("prompt lacks instruction or agents:\n%s", r.prompts[0])
	}
}

func TestAssistRepairsOnce(t *testing.T) {
	bad := `{"plan":"p","flow":{"name":"x","start":"a1","nodes":{"a1":{"type":"agent","agent":"claude","next":"ghost"}}}}`
	r := &scriptRunner{outputs: []string{bad, goodOutput}}
	res, err := (&Assistant{Runner: r}).Assist(context.Background(), AssistRequest{Agent: "claude", Instruction: "x"}, actx)
	if err != nil || res.Flow.Start != "c1" {
		t.Fatalf("%+v %v", res, err)
	}
	if len(r.prompts) != 2 || !strings.Contains(r.prompts[1], "ghost") {
		t.Fatalf("repair prompt must carry the issues:\n%s", r.prompts[1])
	}
}

func TestAssistGivesUpWith422(t *testing.T) {
	r := &scriptRunner{outputs: []string{"no json at all", "still nothing " + strings.Repeat("x", 5000)}}
	_, err := (&Assistant{Runner: r}).Assist(context.Background(), AssistRequest{Agent: "claude", Instruction: "x"}, actx)
	var ae *AssistError
	if !errors.As(err, &ae) || ae.Status != http.StatusUnprocessableEntity || len(ae.RawExcerpt) > 2048 {
		t.Fatalf("%v", err)
	}
}

func TestAssistValidatesRequest(t *testing.T) {
	a := &Assistant{Runner: &scriptRunner{}}
	cases := map[string]AssistRequest{
		"empty instruction": {Agent: "claude", Instruction: "  "},
		"too long":          {Agent: "claude", Instruction: strings.Repeat("ñ", MaxInstructionRunes+1)},
		"not installed":     {Agent: "gemini", Instruction: "x"},
		"review-only agent": {Agent: "openrouter", Instruction: "x"},
	}
	for name, req := range cases {
		_, err := a.Assist(context.Background(), req, actx)
		var ae *AssistError
		if !errors.As(err, &ae) || ae.Status != http.StatusBadRequest {
			t.Fatalf("%s: %v", name, err)
		}
	}
	r := &scriptRunner{outputs: []string{goodOutput}}
	if _, err := (&Assistant{Runner: r}).Assist(context.Background(),
		AssistRequest{Agent: "claude", Instruction: strings.Repeat("ñ", MaxInstructionRunes)}, actx); err != nil {
		t.Fatalf("4000 runes must be accepted: %v", err)
	}
}

func TestAssistBusy(t *testing.T) {
	r := &scriptRunner{outputs: []string{goodOutput}, block: make(chan struct{})}
	a := &Assistant{Runner: r}
	done := make(chan error)
	go func() {
		_, err := a.Assist(context.Background(), AssistRequest{Agent: "claude", Instruction: "x"}, actx)
		done <- err
	}()
	for {
		r.mu.Lock()
		started := len(r.prompts) > 0
		r.mu.Unlock()
		if started {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := a.Assist(context.Background(), AssistRequest{Agent: "claude", Instruction: "y"}, actx); !errors.Is(err, ErrAssistBusy) {
		t.Fatalf("want busy, got %v", err)
	}
	close(r.block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestAssistTimeoutAndAgentFailure(t *testing.T) {
	r := &scriptRunner{block: make(chan struct{})}
	_, err := (&Assistant{Runner: r, Timeout: 20 * time.Millisecond}).Assist(context.Background(), AssistRequest{Agent: "claude", Instruction: "x"}, actx)
	var ae *AssistError
	if !errors.As(err, &ae) || ae.Status != http.StatusGatewayTimeout {
		t.Fatalf("%v", err)
	}
	r2 := &scriptRunner{errs: []error{errors.New("exit status 1: not logged in")}}
	_, err = (&Assistant{Runner: r2}).Assist(context.Background(), AssistRequest{Agent: "claude", Instruction: "x"}, actx)
	if !errors.As(err, &ae) || ae.Status != http.StatusBadGateway || !strings.Contains(ae.Message, "not logged in") {
		t.Fatalf("%v", err)
	}
}

func TestAssistCancellationRemovesTempDir(t *testing.T) {
	r := &scriptRunner{block: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	_, err := (&Assistant{Runner: r}).Assist(ctx, AssistRequest{Agent: "claude", Instruction: "x"}, actx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
	if _, statErr := os.Stat(r.dirs[0]); !os.IsNotExist(statErr) {
		t.Fatalf("temp dir %s left behind", r.dirs[0])
	}
}

func TestAssistPromptCarriesCurrentFlow(t *testing.T) {
	cur := config.FlowConfig{Name: "Mine", Start: "a1", Nodes: map[string]config.FlowNode{"a1": {Type: config.NodeAgent, Agent: "claude"}}}
	p := BuildAssistPrompt(AssistRequest{Agent: "claude", Instruction: "add codex as fallback", Flow: &cur}, actx)
	for _, want := range []string{`"Mine"`, "add codex as fallback", "2026-10-12", "CURRENT FLOW"} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt lacks %q", want)
		}
	}
}

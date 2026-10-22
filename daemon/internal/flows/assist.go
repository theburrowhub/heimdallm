package flows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/executor"
	"github.com/heimdallm/daemon/internal/quota"
)

// MaxInstructionRunes caps the operator's request.
const MaxInstructionRunes = 4000

// MaxPlanRunes caps the plan returned with a draft: it comes from the agent.
const MaxPlanRunes = 4000

// noDraftAgents are CLI agents the assistant does not run. opencode's
// `opencode run` keeps its own allow-all tool defaults, which the cleared
// permission flags cannot narrow.
var noDraftAgents = map[string]bool{"opencode": true}

// CanDraft reports whether the assistant may run agent: a CLI agent (HTTP
// agents only run reviews) whose tool use the daemon does not leave open.
// Custom agents only review: Heimdallm cannot know what their tools do.
func CanDraft(agent string) bool {
	return !executor.IsHTTPOnly(agent) && !noDraftAgents[agent] && !executor.IsCustomAgent(agent)
}

// DefaultAssistTimeout bounds one agent run.
const DefaultAssistTimeout = 180 * time.Second

// AssistWriteBudget is how long an HTTP response may take to be written: the
// whole assist run (both attempts share one Timeout) plus a margin. It
// replaces the server's short WriteTimeout for this one route.
const AssistWriteBudget = DefaultAssistTimeout + 30*time.Second

// ErrAssistBusy is returned while another assist request runs.
var ErrAssistBusy = errors.New("flows: an assist request is already running")

// Runner runs one prompt with an agent in workDir and returns its stdout.
type Runner interface {
	Run(ctx context.Context, agent, prompt, workDir string) ([]byte, error)
}

// AssistRequest is a POST /flows/assist body.
type AssistRequest struct {
	Agent       string             `json:"agent"`
	Instruction string             `json:"instruction"`
	Flow        *config.FlowConfig `json:"flow,omitempty"`
}

// AssistAgent is an installed agent. Runnable agents can run the assistant
// (see CanDraft); the rest can only appear in the generated flow.
type AssistAgent struct {
	ID       string
	Models   []string
	Runnable bool
}

// AssistContext is what the prompt tells the agent about this machine.
type AssistContext struct {
	Agents []AssistAgent
	Quotas []quota.Provider
	Now    time.Time
}

// AssistResult is an unsaved draft.
type AssistResult struct {
	Flow       config.FlowConfig `json:"flow"`
	Plan       string            `json:"plan"`
	Warnings   []string          `json:"warnings"`
	Agent      string            `json:"agent"`
	DurationMS int64             `json:"duration_ms"`
}

// AssistError carries the HTTP status the handler answers with.
type AssistError struct {
	Status     int                `json:"-"`
	Message    string             `json:"error"`
	Issues     []config.FlowIssue `json:"errors,omitempty"`
	RawExcerpt string             `json:"raw_excerpt,omitempty"`
}

func (e *AssistError) Error() string { return e.Message }

// Assistant drafts flows with an installed agent. One request runs at a time.
type Assistant struct {
	Runner  Runner
	Timeout time.Duration
	Now     func() time.Time

	mu   sync.Mutex
	busy bool
}

type assistAnswer struct {
	Plan string            `json:"plan"`
	Flow config.FlowConfig `json:"flow"`
}

// Assist runs the request and returns a validated, unsaved draft.
func (a *Assistant) Assist(ctx context.Context, req AssistRequest, actx AssistContext) (AssistResult, error) {
	if err := checkAssistRequest(req, actx); err != nil {
		return AssistResult{}, err
	}
	a.mu.Lock()
	if a.busy {
		a.mu.Unlock()
		return AssistResult{}, ErrAssistBusy
	}
	a.busy = true
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.busy = false
		a.mu.Unlock()
	}()

	now := time.Now
	if a.Now != nil {
		now = a.Now
	}
	start := now()
	timeout := a.Timeout
	if timeout <= 0 {
		timeout = DefaultAssistTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	workDir, err := os.MkdirTemp("", "heimdallm-assist-*")
	if err != nil {
		return AssistResult{}, fmt.Errorf("flows: assist workspace: %w", err)
	}
	defer os.RemoveAll(workDir)

	prompt := BuildAssistPrompt(req, actx)
	out, err := a.run(runCtx, ctx, req.Agent, prompt, workDir)
	if err != nil {
		return AssistResult{}, err
	}
	answer, issues := parseAssistAnswer(out)
	if len(issues) > 0 {
		out, err = a.run(runCtx, ctx, req.Agent, BuildRepairPrompt(prompt, out, issues), workDir)
		if err != nil {
			return AssistResult{}, err
		}
		answer, issues = parseAssistAnswer(out)
		if len(issues) > 0 {
			return AssistResult{}, &AssistError{
				Status:     http.StatusUnprocessableEntity,
				Message:    req.Agent + " did not produce a valid flow: " + issues[0].Error(),
				Issues:     issues,
				RawExcerpt: excerpt(out, 2048),
			}
		}
	}
	return AssistResult{
		Flow:       answer.Flow,
		Plan:       truncateRunes(strings.TrimSpace(answer.Plan), MaxPlanRunes),
		Warnings:   assistWarnings(answer.Flow, actx),
		Agent:      req.Agent,
		DurationMS: now().Sub(start).Milliseconds(),
	}, nil
}

func (a *Assistant) run(runCtx, reqCtx context.Context, agent, prompt, workDir string) ([]byte, error) {
	out, err := a.Runner.Run(runCtx, agent, prompt, workDir)
	switch {
	case reqCtx.Err() != nil:
		return nil, reqCtx.Err()
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		return nil, &AssistError{Status: http.StatusGatewayTimeout, Message: agent + " did not answer in time"}
	case err != nil:
		return nil, &AssistError{Status: http.StatusBadGateway, Message: agent + " failed: " + excerpt([]byte(err.Error()), 1024)}
	}
	return out, nil
}

func checkAssistRequest(req AssistRequest, actx AssistContext) error {
	bad := func(msg string) error { return &AssistError{Status: http.StatusBadRequest, Message: msg} }
	if strings.TrimSpace(req.Instruction) == "" {
		return bad("describe the flow you want")
	}
	if utf8.RuneCountInString(req.Instruction) > MaxInstructionRunes {
		return bad(fmt.Sprintf("the description is longer than %d characters", MaxInstructionRunes))
	}
	for _, a := range actx.Agents {
		if a.ID == req.Agent {
			switch {
			case executor.IsHTTPOnly(req.Agent):
				return bad(req.Agent + " only runs reviews; pick a CLI agent to draft flows")
			case !a.Runnable || !CanDraft(req.Agent):
				return bad(req.Agent + " cannot draft flows")
			}
			return nil
		}
	}
	return bad(req.Agent + " is not installed")
}

// truncateRunes keeps the first max runes of s.
func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max])
}

// parseAssistAnswer extracts and validates the agent's JSON. Every problem
// is returned as an issue so the repair round can quote them.
func parseAssistAnswer(out []byte) (assistAnswer, []config.FlowIssue) {
	raw := executor.StripToJSON(out)
	var ans assistAnswer
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ans); err != nil {
		return ans, []config.FlowIssue{{Message: "the answer is not the requested JSON object: " + err.Error()}}
	}
	if len(ans.Flow.Nodes) == 0 {
		return ans, []config.FlowIssue{{Field: "nodes", Message: "the flow has no steps"}}
	}
	if errs, _ := config.CheckFlow("draft", ans.Flow); len(errs) > 0 {
		return ans, errs
	}
	return ans, nil
}

func assistWarnings(f config.FlowConfig, actx AssistContext) []string {
	installed := map[string]bool{}
	for _, a := range actx.Agents {
		installed[a.ID] = true
	}
	var out []string
	_, warns := config.CheckFlow("draft", f)
	for _, w := range warns {
		out = append(out, w.Error())
	}
	seen := map[string]bool{}
	missingPos := false
	for _, id := range f.SortedNodeIDs() {
		n := f.Nodes[id]
		if n.Type == config.NodeAgent && !installed[n.Agent] && !seen[n.Agent] {
			seen[n.Agent] = true
			out = append(out, n.Agent+" is not installed")
		}
		if n.X == nil || n.Y == nil {
			missingPos = true
		}
	}
	if missingPos {
		out = append(out, "some steps have no positions; the editor arranges them")
	}
	return out
}

package pipeline_test

import (
	"strings"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/executor"
	"github.com/heimdallm/daemon/internal/pipeline"
	"github.com/heimdallm/daemon/internal/store"
)

const checkoutNoteMarker = "may not contain this PR's changes"

// runForPrompts reviews the same PR twice (a first review, then a re-review of
// a new commit) and returns both prompts.
func runForPrompts(t *testing.T, opts pipeline.RunOptions) []string {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	exec := &fakePromptExec{}
	p := pipeline.New(s, &fakeGHCounter{diff: fileDiff("main.go", "+func added() {}")}, exec, &fakeNotify{})
	p.SetBotLogin("heimdallm-bot")
	opts.Primary, opts.Force = "claude", true

	pr := tokenPR("aaaaaaa1")
	if _, err := p.Run(pr, opts); err != nil {
		t.Fatalf("first review: %v", err)
	}
	pr.Head.SHA = "bbbbbbb2"
	pr.UpdatedAt = time.Now().Add(time.Minute)
	if _, err := p.Run(pr, opts); err != nil {
		t.Fatalf("re-review: %v", err)
	}
	return exec.prompts
}

// checkNoteOutsideUserContent asserts the note appears once and after the
// untrusted diff block closes, i.e. as Heimdallm's instruction, not PR data.
func checkNoteOutsideUserContent(t *testing.T, prompt string) {
	t.Helper()
	if n := strings.Count(prompt, checkoutNoteMarker); n != 1 {
		t.Fatalf("checkout note appears %d times, want once:\n%s", n, prompt)
	}
	if strings.Index(prompt, checkoutNoteMarker) < strings.Index(prompt, "</user_content>") {
		t.Errorf("checkout note sits inside <user_content>:\n%s", prompt)
	}
}

// A review whose agent explores a checkout is told the checkout may not be
// the PR head (#895), on first reviews and re-reviews, in both templates.
func TestRun_CheckoutNoteWhenTheAgentHasAWorkDir(t *testing.T) {
	for name, ts := range map[string]pipeline.TokenSaving{
		"default template": {},
		"compact template": {CompactPrompt: true, IncrementalDiff: true},
	} {
		t.Run(name, func(t *testing.T) {
			prompts := runForPrompts(t, pipeline.RunOptions{
				TokenSaving: ts,
				ExecOpts:    executor.ExecOptions{WorkDir: "/home/heimdallm/repos/repo"},
			})
			for i, prompt := range prompts {
				t.Run([]string{"first review", "re-review"}[i], func(t *testing.T) {
					checkNoteOutsideUserContent(t, prompt)
				})
			}
		})
	}
}

// A flow agent with its own options counts too: the prompt is shared by every
// agent of the flow.
func TestRun_CheckoutNoteWhenOnlyAFlowAgentHasAWorkDir(t *testing.T) {
	prompts := runForPrompts(t, pipeline.RunOptions{
		AgentExecOpts: map[string]executor.ExecOptions{"claude": {WorkDir: "/home/heimdallm/repos/repo"}},
	})
	checkNoteOutsideUserContent(t, prompts[0])
}

// Without a working directory the agent explores nothing, and the prompt is
// exactly what it was before.
func TestRun_NoCheckoutNoteWithoutAWorkDir(t *testing.T) {
	for _, prompt := range runForPrompts(t, pipeline.RunOptions{}) {
		if strings.Contains(prompt, checkoutNoteMarker) {
			t.Errorf("checkout note without a working directory:\n%s", prompt)
		}
	}
}

package pipeline

import (
	"testing"

	"github.com/heimdallm/daemon/internal/executor"
)

// With no agent named yet the executor detects one later, so the note is kept
// rather than guessed away (#895).
func TestReviewHasCheckout_UnnamedAgentAssumesItExplores(t *testing.T) {
	wd := executor.ExecOptions{WorkDir: "/w"}
	if !reviewHasCheckout(RunOptions{ExecOpts: wd}) {
		t.Error("an unnamed agent with a working directory must get the note")
	}
	if reviewHasCheckout(RunOptions{}) {
		t.Error("no working directory, no note")
	}
	if reviewHasCheckout(RunOptions{Primary: "cursor_cli", ExecOpts: wd}) {
		t.Error("Cursor alone never explores the checkout")
	}
}

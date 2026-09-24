package mergetrack

import (
	"testing"

	"github.com/heimdallm/daemon/internal/executor"
)

func TestEnsureWritePerms_DefaultsUnsetKnobs(t *testing.T) {
	if got := ensureWritePerms("claude", executor.ExecOptions{}); got.PermissionMode != "acceptEdits" {
		t.Errorf("claude permission_mode = %q, want acceptEdits", got.PermissionMode)
	}
	if got := ensureWritePerms("codex", executor.ExecOptions{}); got.ApprovalMode != "never" {
		t.Errorf("codex approval_mode = %q, want never", got.ApprovalMode)
	}
	if got := ensureWritePerms("gemini", executor.ExecOptions{}); got.ApprovalMode != "auto_edit" {
		t.Errorf("gemini approval_mode = %q, want auto_edit", got.ApprovalMode)
	}
}

func TestEnsureWritePerms_RespectsOperatorChoice(t *testing.T) {
	if got := ensureWritePerms("claude", executor.ExecOptions{PermissionMode: "plan"}); got.PermissionMode != "plan" {
		t.Errorf("claude operator permission_mode overridden: %q", got.PermissionMode)
	}
	if got := ensureWritePerms("claude", executor.ExecOptions{DangerouslySkipPerms: true}); got.PermissionMode != "" {
		t.Errorf("claude with skip-perms must stay untouched, got %q", got.PermissionMode)
	}
	if got := ensureWritePerms("codex", executor.ExecOptions{ApprovalMode: "on-request"}); got.ApprovalMode != "on-request" {
		t.Errorf("codex operator approval_mode overridden: %q", got.ApprovalMode)
	}
	if got := ensureWritePerms("gemini", executor.ExecOptions{ApprovalMode: "default"}); got.ApprovalMode != "default" {
		t.Errorf("gemini operator approval_mode overridden: %q", got.ApprovalMode)
	}
	if got := ensureWritePerms("other", executor.ExecOptions{}); got != (executor.ExecOptions{}) {
		t.Errorf("unknown cli must be untouched, got %+v", got)
	}
}

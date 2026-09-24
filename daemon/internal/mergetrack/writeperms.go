package mergetrack

import (
	"log/slog"
	"strings"

	"github.com/heimdallm/daemon/internal/executor"
)

// ensureWritePerms promotes the CLI flags into write mode for the agent that
// resolves merge conflicts, defaulting each provider's approval/permission
// knob only when the operator left it unset. The agent must be able to edit
// files in the checkout without stopping to ask; the scope guard in
// ConflictResolver verifies afterwards that it only touched conflicted paths.
func ensureWritePerms(cli string, opts executor.ExecOptions) executor.ExecOptions {
	switch cli {
	case "claude":
		if strings.TrimSpace(opts.PermissionMode) == "" && !opts.DangerouslySkipPerms {
			opts.PermissionMode = "acceptEdits"
			slog.Info("mergetrack: defaulting claude permission_mode to acceptEdits for conflict resolution",
				"cli", cli)
		}
	case "codex":
		if strings.TrimSpace(opts.ApprovalMode) == "" {
			opts.ApprovalMode = "never"
			slog.Info("mergetrack: defaulting codex approval_mode to never for conflict resolution",
				"cli", cli)
		}
	case "gemini":
		if strings.TrimSpace(opts.ApprovalMode) == "" {
			opts.ApprovalMode = "auto_edit"
			slog.Info("mergetrack: defaulting gemini approval_mode to auto_edit for conflict resolution",
				"cli", cli)
		}
	}
	return opts
}

package main

import (
	"reflect"

	"github.com/heimdallm/daemon/internal/config"
)

// agentConfigMap is one agent's entry under agent_configs in GET /config.
// A custom agent also reports its binary and its option values; its schema
// is served by GET /agent-schemas.
func agentConfigMap(ac config.CLIAgentConfig) map[string]any {
	m := map[string]any{
		"model":                  ac.Model,
		"max_turns":              ac.MaxTurns,
		"approval_mode":          ac.ApprovalMode,
		"extra_flags":            ac.ExtraFlags,
		"prompt":                 ac.PromptID,
		"effort":                 ac.Effort,
		"permission_mode":        ac.PermissionMode,
		"bare":                   ac.Bare,
		"dangerously_skip_perms": ac.DangerouslySkipPerms,
		"no_session_persistence": ac.NoSessionPersistence,
		"execution_timeout":      ac.ExecutionTimeout,
	}
	if ac.ReviewLimits != nil {
		m["review_limits"] = reviewLimitsMap(*ac.ReviewLimits)
	}
	if ac.IsCustom() {
		m["binary"] = ac.Binary
		options := map[string]any{}
		for k, v := range ac.Options {
			options[k] = v
		}
		m["options"] = options
	}
	return m
}

// sameCustomAgents reports whether two configs declare the same custom
// agents (names, binaries and schemas).
func sameCustomAgents(a, b *config.Config) bool {
	return reflect.DeepEqual(a.CustomAgents(), b.CustomAgents())
}

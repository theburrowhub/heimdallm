package executor

import (
	"strings"
	"testing"
)

func TestSanitizeExecutorEnvironmentRemovesDaemonSecretsAndGitRouting(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"HOME=/tmp/home",
		"OPENAI_API_KEY=openai-provider-key",
		"ANTHROPIC_API_KEY=anthropic-provider-key",
		"GITHUB_TOKEN=github-secret",
		"GH_TOKEN=gh-secret",
		"HEIMDALLM_LOCAL_DIR_BASE=/operator/repos",
		"HEIMDALLM_DATA_DIR=/operator/data",
		"GIT_DIR=/operator/repo/.git",
		"GIT_WORK_TREE=/operator/repo",
		"GIT_CONFIG=/operator/gitconfig",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=core.worktree",
		"GIT_CONFIG_VALUE_0=/operator/repo",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=/operator/repo/.git/objects",
		"GIT_ASKPASS=/operator/askpass",
		"GIT_EXEC_PATH=/operator/git-core",
		"GIT_EXTERNAL_DIFF=/operator/diff",
		"GIT_TEMPLATE_DIR=/operator/templates",
		"GIT_TRACE=/operator/trace.log",
		"GIT_TRACE_CURL=1",
		"GIT_TRACE_REDACT=0",
		"GIT_CURL_VERBOSE=1",
		"GIT_SSL_NO_VERIFY=1",
		"SSH_AUTH_SOCK=/operator/ssh-agent.sock",
		"SSH_AGENT_PID=1234",
		"KEEP_ME=yes",
	}

	got := strings.Join(sanitizeExecutorEnvironment(env), "\n")
	for _, forbidden := range []string{
		"GITHUB_TOKEN=", "GH_TOKEN=", "HEIMDALLM_",
		"GIT_DIR=", "GIT_WORK_TREE=", "GIT_CONFIG=/operator",
		"GIT_CONFIG_KEY_0=core.worktree", "GIT_CONFIG_VALUE_0=/operator/repo",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=/operator", "GIT_ASKPASS=",
		"GIT_EXEC_PATH=/operator", "GIT_EXTERNAL_DIFF=/operator",
		"GIT_TEMPLATE_DIR=/operator", "SSH_AUTH_SOCK=", "SSH_AGENT_PID=",
		"GIT_TRACE=", "GIT_TRACE_CURL=", "GIT_TRACE_REDACT=",
		"GIT_CURL_VERBOSE=", "GIT_SSL_NO_VERIFY=",
	} {
		if strings.Contains(got, forbidden) {
			t.Errorf("sanitized executor environment still contains %q:\n%s", forbidden, got)
		}
	}
	for _, required := range []string{
		"PATH=/usr/bin",
		"HOME=/tmp/home",
		"OPENAI_API_KEY=openai-provider-key",
		"ANTHROPIC_API_KEY=anthropic-provider-key",
		"KEEP_ME=yes",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=core.hooksPath",
		"GIT_CONFIG_VALUE_0=/dev/null",
	} {
		if !strings.Contains(got, required) {
			t.Errorf("sanitized executor environment is missing %q:\n%s", required, got)
		}
	}
}

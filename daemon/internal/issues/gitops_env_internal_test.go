package issues

import (
	"strings"
	"testing"
)

func TestCleanGitEnvironmentRemovesRepositoryRouting(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"GIT_DIR=/operator/repo/.git",
		"GIT_COMMON_DIR=/operator/repo/.git",
		"GIT_WORK_TREE=/operator/repo",
		"GIT_INDEX_FILE=/operator/repo/.git/index",
		"GIT_OBJECT_DIRECTORY=/operator/repo/.git/objects",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=/operator/cache",
		"GIT_NAMESPACE=other",
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=core.worktree",
		"GIT_CONFIG_VALUE_0=/operator/repo",
		"GIT_CONFIG_GLOBAL=/operator/global.gitconfig",
		"GIT_CONFIG_SYSTEM=/operator/system.gitconfig",
		"GIT_CONFIG_NOSYSTEM=0",
		"GIT_CONFIG_PARAMETERS='core.hooksPath'='/operator/hooks'",
		"GIT_CONFIG=/operator/repo/config",
		"GIT_IMPLICIT_WORK_TREE=1",
		"GIT_GRAFT_FILE=/operator/repo/grafts",
		"GIT_REPLACE_REF_BASE=refs/evil/",
		"GIT_NO_REPLACE_OBJECTS=0",
		"GIT_PREFIX=../../operator/",
		"GIT_INTERNAL_SUPER_PREFIX=../../operator/",
		"GIT_SHALLOW_FILE=/operator/repo/shallow",
		"GIT_CEILING_DIRECTORIES=/operator",
		"GIT_DISCOVERY_ACROSS_FILESYSTEM=1",
		"GIT_EXEC_PATH=/operator/git-core",
		"GIT_EXTERNAL_DIFF=/operator/diff",
		"GIT_TEMPLATE_DIR=/operator/templates",
		"GIT_TRACE=/operator/trace.log",
		"GIT_TRACE_CURL=1",
		"GIT_TRACE_REDACT=0",
		"GIT_CURL_VERBOSE=1",
		"GIT_SSL_NO_VERIFY=1",
		"GIT_SSH_COMMAND=/operator/ssh",
		"KEEP_ME=yes",
	}

	got := cleanGitEnvironment(env)
	joined := strings.Join(got, "\n")
	for _, forbidden := range []string{
		"GIT_DIR=", "GIT_COMMON_DIR=", "GIT_WORK_TREE=", "GIT_INDEX_FILE=",
		"GIT_OBJECT_DIRECTORY=/operator", "GIT_NAMESPACE=other",
		"GIT_CONFIG_GLOBAL=/operator", "GIT_CONFIG_SYSTEM=/operator",
		"GIT_CONFIG=/operator", "GIT_CONFIG_PARAMETERS=",
		"GIT_IMPLICIT_WORK_TREE=1", "GIT_GRAFT_FILE=/operator",
		"GIT_REPLACE_REF_BASE=refs/evil", "GIT_PREFIX=../../operator",
		"GIT_INTERNAL_SUPER_PREFIX=../../operator", "GIT_SHALLOW_FILE=/operator",
		"GIT_CEILING_DIRECTORIES=/operator", "GIT_DISCOVERY_ACROSS_FILESYSTEM=1",
		"GIT_EXEC_PATH=/operator", "GIT_EXTERNAL_DIFF=/operator",
		"GIT_TEMPLATE_DIR=/operator", "GIT_SSH_COMMAND=/operator",
		"GIT_TRACE=", "GIT_TRACE_CURL=", "GIT_TRACE_REDACT=",
		"GIT_CURL_VERBOSE=", "GIT_SSL_NO_VERIFY=",
		"GIT_CONFIG_KEY_0=core.worktree", "GIT_CONFIG_VALUE_0=/operator/repo",
	} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("sanitized environment still contains %q:\n%s", forbidden, joined)
		}
	}
	if !strings.Contains(joined, "PATH=/usr/bin") || !strings.Contains(joined, "KEEP_ME=yes") {
		t.Fatalf("unrelated environment was removed:\n%s", joined)
	}
	for _, required := range []string{
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=core.hooksPath",
		"GIT_CONFIG_VALUE_0=/dev/null",
	} {
		if !strings.Contains(joined, required) {
			t.Errorf("sanitized environment is missing %q:\n%s", required, joined)
		}
	}
}

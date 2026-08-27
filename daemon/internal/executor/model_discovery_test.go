package executor

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDiscoverModelsUsesEachCLINativeCatalog(t *testing.T) {
	claude := writeModelDiscoveryCLI(t, "claude", `
if [ "$1" = "--help" ]; then
  printf '%s\n' '--safe-mode --no-session-persistence'
  exit 0
fi
IFS= read -r request
case "$request" in *'"subtype":"list_models"'*) ;; *) exit 2 ;; esac
printf '%s\n' '{"type":"control_response","response":{"subtype":"success","request_id":"heimdallm-models","response":{"models":[{"value":"sonnet"},{"value":"opus[1m]"},{"value":"sonnet"}]}}}'
`)
	codex := writeModelDiscoveryCLI(t, "codex", `
IFS= read -r initialize
case "$initialize" in *'"method":"initialize"'*) ;; *) exit 2 ;; esac
printf '%s\n' '{"id":0,"result":{"userAgent":"fake"}}'
IFS= read -r initialized
case "$initialized" in *'"method":"initialized"'*) ;; *) exit 2 ;; esac
IFS= read -r first_page
case "$first_page" in *'"method":"model/list"'*) ;; *) exit 2 ;; esac
printf '%s\n' '{"id":1,"result":{"data":[{"model":"gpt-current"}],"nextCursor":"page-2"}}'
IFS= read -r second_page
case "$second_page" in *'"cursor":"page-2"'*) ;; *) exit 2 ;; esac
printf '%s\n' '{"id":2,"result":{"data":[{"model":"gpt-next"}],"nextCursor":null}}'
`)
	gemini := writeModelDiscoveryCLI(t, "gemini", `
IFS= read -r initialize
case "$initialize" in *'"method":"initialize"'*) ;; *) exit 2 ;; esac
printf '%s\n' '{"jsonrpc":"2.0","id":0,"result":{"protocolVersion":1}}'
IFS= read -r new_session
case "$new_session" in *'"method":"session/new"'*) ;; *) exit 2 ;; esac
printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"models":{"availableModels":[{"modelId":"gemini-auto"},{"modelId":"gemini-pro"}]}}}'
`)
	opencode := writeModelDiscoveryCLI(t, "opencode", `
[ "$1" = "models" ]
printf '%s\n' 'anthropic/claude-current' 'openai/gpt-current' 'openai/gpt-current' 'status: ready'
`)

	paths := map[string]string{
		"claude": claude, "codex": codex, "gemini": gemini, "opencode": opencode,
	}
	got := discoverModels(context.Background(), func(cli string) string { return paths[cli] })
	want := map[string][]string{
		"claude":   {"sonnet", "opus[1m]"},
		"codex":    {"gpt-current", "gpt-next"},
		"gemini":   {"gemini-auto", "gemini-pro"},
		"opencode": {"anthropic/claude-current", "openai/gpt-current"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DiscoverModels() = %#v, want %#v", got, want)
	}
}

func TestDiscoverModelsKeepsPartialResults(t *testing.T) {
	opencode := writeModelDiscoveryCLI(t, "opencode", `printf '%s\n' 'openai/gpt-live'`)
	got := discoverModels(context.Background(), func(cli string) string {
		if cli == "opencode" {
			return opencode
		}
		return ""
	})

	if !reflect.DeepEqual(got["opencode"], []string{"openai/gpt-live"}) {
		t.Fatalf("opencode models = %#v", got["opencode"])
	}
	for _, cli := range []string{"claude", "gemini", "codex"} {
		if got[cli] == nil || len(got[cli]) != 0 {
			t.Fatalf("%s models = %#v, want a non-nil empty list", cli, got[cli])
		}
	}
}

func TestNormalizeDiscoveredModelsRejectsUnsafeAndDuplicateValues(t *testing.T) {
	got := normalizeDiscoveredModels([]string{
		" model-a ", "model-a", "", "-option", "line\nbreak", "two words", "model-b",
	})
	want := []string{"model-a", "model-b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeDiscoveredModels() = %#v, want %#v", got, want)
	}
}

func writeModelDiscoveryCLI(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	contents := "#!/bin/sh\nset -eu\n" + body + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatalf("write fake %s CLI: %v", name, err)
	}
	return path
}

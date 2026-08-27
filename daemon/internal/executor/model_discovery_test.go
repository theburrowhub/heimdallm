package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

func TestDiscoverModelsIgnoresProviderFailure(t *testing.T) {
	failing := writeModelDiscoveryCLI(t, "opencode", `exit 1`)
	got := discoverModels(context.Background(), func(cli string) string {
		if cli == "opencode" {
			return failing
		}
		return ""
	})
	if got["opencode"] == nil || len(got["opencode"]) != 0 {
		t.Fatalf("opencode models = %#v, want a non-nil empty list", got["opencode"])
	}
}

func TestDiscoverModelsForCLIRejectsUnknownProvider(t *testing.T) {
	if _, err := discoverModelsForCLI(context.Background(), "unknown", "/unused"); err == nil {
		t.Fatal("expected an unsupported CLI error")
	}
}

func TestDiscoverClaudeModelsFailsClosed(t *testing.T) {
	t.Run("safe mode unavailable", func(t *testing.T) {
		cli := writeModelDiscoveryCLI(t, "claude", `
if [ "$1" = "--help" ]; then exit 0; fi
exit 2
`)
		if _, err := discoverClaudeModels(context.Background(), cli); err == nil {
			t.Fatal("expected safe-mode discovery to fail")
		}
	})

	t.Run("malformed response", func(t *testing.T) {
		cli := writeModelDiscoveryCLI(t, "claude", `
if [ "$1" = "--help" ]; then printf '%s\n' '--safe-mode'; exit 0; fi
printf '%s\n' '{}'
`)
		if _, err := discoverClaudeModels(context.Background(), cli); err == nil {
			t.Fatal("expected malformed discovery response to fail")
		}
	})

	t.Run("process error", func(t *testing.T) {
		cli := writeModelDiscoveryCLI(t, "claude", `
if [ "$1" = "--help" ]; then printf '%s\n' '--safe-mode'; exit 0; fi
exit 1
`)
		if _, err := discoverClaudeModels(context.Background(), cli); err == nil {
			t.Fatal("expected failed discovery process to fail")
		}
	})
}

func TestParseClaudeModelsReportsScannerFailure(t *testing.T) {
	oversized := bytes.Repeat([]byte("x"), maxModelOutputSize+1)
	if _, err := parseClaudeModels(oversized); err == nil {
		t.Fatal("expected oversized response to fail")
	}
}

func TestModelProtocolResponseValidation(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		{name: "provider error", output: `{"id":1,"error":{"message":"no"}}` + "\n"},
		{name: "missing result", output: `{"id":1}` + "\n"},
		{name: "malformed result", output: `{"id":1,"result":"wrong"}` + "\n"},
		{name: "unexpected eof", output: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := protocolWithOutput(tt.output)
			var result map[string]any
			if err := client.response(1, &result); err == nil {
				t.Fatal("expected protocol response error")
			}
		})
	}

	t.Run("skips unrelated messages", func(t *testing.T) {
		client := protocolWithOutput("not-json\n" +
			`{"id":2,"result":{}}` + "\n" +
			`{"id":1,"result":{"model":"ok"}}` + "\n")
		var result map[string]any
		if err := client.response(1, &result); err != nil {
			t.Fatalf("response: %v", err)
		}
		if result["model"] != "ok" {
			t.Fatalf("result = %#v", result)
		}
	})

	t.Run("scanner error", func(t *testing.T) {
		scanner := bufio.NewScanner(strings.NewReader(strings.Repeat("x", maxModelOutputSize+1)))
		scanner.Buffer(make([]byte, 64<<10), maxModelOutputSize)
		client := &modelProtocol{scanner: scanner}
		var result map[string]any
		if err := client.response(1, &result); err == nil {
			t.Fatal("expected scanner error")
		}
	})
}

func TestModelProtocolSendReportsWriterFailure(t *testing.T) {
	client := &modelProtocol{encoder: json.NewEncoder(failingWriter{})}
	if err := client.send(map[string]string{"method": "test"}); err == nil {
		t.Fatal("expected writer error")
	}
}

func TestStartModelProtocolReportsStartFailure(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-cli")
	if _, err := startModelProtocol(context.Background(), missing, t.TempDir()); err == nil {
		t.Fatal("expected process start error")
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

func TestNormalizeDiscoveredModelsCapsCatalogSize(t *testing.T) {
	models := make([]string, maxDiscoveredModels+1)
	for index := range models {
		models[index] = "model-" + strings.Repeat("x", index%10) + string(rune('a'+index%26))
	}
	// Make every identifier unique without adding whitespace.
	for index := range models {
		models[index] += strings.Repeat("z", index/26)
	}
	if got := normalizeDiscoveredModels(models); len(got) != maxDiscoveredModels {
		t.Fatalf("catalog size = %d, want %d", len(got), maxDiscoveredModels)
	}
}

func TestCappedBuffer(t *testing.T) {
	buffer := newCappedBuffer(3)
	if written, err := buffer.Write([]byte("hello")); err != nil || written != 5 {
		t.Fatalf("first write = (%d, %v)", written, err)
	}
	if written, err := buffer.Write([]byte("world")); err != nil || written != 5 {
		t.Fatalf("second write = (%d, %v)", written, err)
	}
	if got := buffer.String(); got != "hel" {
		t.Fatalf("buffer = %q, want hel", got)
	}
}

func TestUpsertModelEnv(t *testing.T) {
	if got := upsertModelEnv([]string{"TERM=xterm"}, "TERM", "dumb"); !reflect.DeepEqual(got, []string{"TERM=dumb"}) {
		t.Fatalf("replace env = %#v", got)
	}
	if got := upsertModelEnv([]string{"PATH=/bin"}, "TERM", "dumb"); !reflect.DeepEqual(got, []string{"PATH=/bin", "TERM=dumb"}) {
		t.Fatalf("append env = %#v", got)
	}
}

func protocolWithOutput(output string) *modelProtocol {
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 64<<10), maxModelOutputSize)
	return &modelProtocol{scanner: scanner}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
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

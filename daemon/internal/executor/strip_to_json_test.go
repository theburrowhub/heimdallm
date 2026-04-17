package executor_test

import (
	"testing"

	"github.com/heimdallm/daemon/internal/executor"
)

func TestStripToJSON_PlainObject(t *testing.T) {
	in := []byte(`{"a":1}`)
	got := string(executor.StripToJSON(in))
	if got != `{"a":1}` {
		t.Errorf("got %q, want %q", got, `{"a":1}`)
	}
}

func TestStripToJSON_TrimsWhitespace(t *testing.T) {
	in := []byte("   \n  {\"a\":1}  \n\n  ")
	got := string(executor.StripToJSON(in))
	if got != `{"a":1}` {
		t.Errorf("got %q, want %q", got, `{"a":1}`)
	}
}

func TestStripToJSON_MarkdownFence(t *testing.T) {
	in := []byte("```json\n{\"a\":1}\n```")
	got := string(executor.StripToJSON(in))
	if got != `{"a":1}` {
		t.Errorf("got %q, want %q", got, `{"a":1}`)
	}
}

func TestStripToJSON_FenceWithTrailingProse(t *testing.T) {
	// Regression: before the explicit closing-fence scan this case left the
	// trailing prose inside the JSON slice, which only survived by accident
	// because the final '}' was still the last brace.
	in := []byte("```json\n{\"a\":1}\n```\nthanks, hope that helps!")
	got := string(executor.StripToJSON(in))
	if got != `{"a":1}` {
		t.Errorf("got %q, want %q", got, `{"a":1}`)
	}
}

func TestStripToJSON_FenceWithoutClosing(t *testing.T) {
	// LLM forgets to close the fence — still recover what we can.
	in := []byte("```json\n{\"a\":1}")
	got := string(executor.StripToJSON(in))
	if got != `{"a":1}` {
		t.Errorf("got %q, want %q", got, `{"a":1}`)
	}
}

func TestStripToJSON_ProseAroundObject(t *testing.T) {
	in := []byte("Here is my triage:\n{\"severity\":\"low\"}\nHope it helps.")
	got := string(executor.StripToJSON(in))
	if got != `{"severity":"low"}` {
		t.Errorf("got %q, want %q", got, `{"severity":"low"}`)
	}
}

func TestStripToJSON_NestedObjectsKeepOutermost(t *testing.T) {
	in := []byte(`{"triage":{"severity":"high"},"summary":"x"}`)
	got := string(executor.StripToJSON(in))
	if got != `{"triage":{"severity":"high"},"summary":"x"}` {
		t.Errorf("got %q (nested braces changed the slice)", got)
	}
}

func TestStripToJSON_NoBracesReturnsUnchanged(t *testing.T) {
	// No JSON at all — return what we have so the caller's Unmarshal
	// surfaces a descriptive error. Nothing to strip here.
	in := []byte("not json at all")
	got := string(executor.StripToJSON(in))
	if got != "not json at all" {
		t.Errorf("got %q, want input back", got)
	}
}

func TestStripToJSON_Empty(t *testing.T) {
	if got := string(executor.StripToJSON(nil)); got != "" {
		t.Errorf("got %q, want empty", got)
	}
	if got := string(executor.StripToJSON([]byte(""))); got != "" {
		t.Errorf("empty input should produce empty output, got %q", got)
	}
}

func TestStripToJSON_MultipleObjectsKnownLimitation(t *testing.T) {
	// Documented limitation: StripToJSON scans from the first '{' to the
	// LAST '}'. When the LLM returns two top-level objects the result is
	// not valid JSON — the caller's Unmarshal must surface that cleanly.
	// Pinning the behaviour here prevents a silent change that would be
	// hard to catch in production.
	in := []byte(`{"a":1}{"b":2}`)
	got := string(executor.StripToJSON(in))
	if got != `{"a":1}{"b":2}` {
		t.Errorf("got %q (documented limitation: outer-slice behaviour)", got)
	}
}

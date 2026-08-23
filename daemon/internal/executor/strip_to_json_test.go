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

func TestStripToJSON_TemplateBracesBeforeObject(t *testing.T) {
	// Regression: reviewing a GitHub Actions PR, the CLI prefaced its JSON
	// with prose quoting `${{ matrix.env }}`. Anchoring on the first '{'
	// sliced from the template braces instead of the object, and Unmarshal
	// failed with "invalid character '{' looking for beginning of object key
	// string". Template syntax in prose is routine for workflow, Helm and
	// Jinja reviews, so the scan has to skip braces that cannot open an object.
	in := []byte("The `${{ matrix.env }}` usage in `run:` blocks is fixed.\n{\"severity\":\"high\"}")
	got := string(executor.StripToJSON(in))
	if got != `{"severity":"high"}` {
		t.Errorf("got %q, want %q", got, `{"severity":"high"}`)
	}
}

func TestStripToJSON_TemplateBracesAfterObject(t *testing.T) {
	// The mirror image of the above: scanning to the LAST '}' swallows
	// trailing prose whenever that prose closes a template expression.
	in := []byte("{\"severity\":\"low\"}\nNote: `${{ matrix.env }}` still needs quoting.")
	got := string(executor.StripToJSON(in))
	if got != `{"severity":"low"}` {
		t.Errorf("got %q, want %q", got, `{"severity":"low"}`)
	}
}

func TestStripToJSON_SkipsBraceRunThatCannotOpenAnObject(t *testing.T) {
	// A '{' followed by anything other than a string key or an immediate
	// close is not a JSON object, so the scan must move on to the next
	// candidate rather than giving up.
	in := []byte("prose { not an object } more prose {\"a\":1} trailing")
	got := string(executor.StripToJSON(in))
	if got != `{"a":1}` {
		t.Errorf("got %q, want %q", got, `{"a":1}`)
	}
}

func TestStripToJSON_BracesInsideStringValuesAreNotStructural(t *testing.T) {
	// Guard for the balanced scan: braces inside string values must not
	// affect nesting depth. This already held under the outer-slice
	// behaviour; it must keep holding.
	in := []byte(`{"finding":"quote ${{ matrix.env }} in env:","severity":"medium"}`)
	got := string(executor.StripToJSON(in))
	if got != `{"finding":"quote ${{ matrix.env }} in env:","severity":"medium"}` {
		t.Errorf("got %q (braces inside a string changed the slice)", got)
	}
}

func TestStripToJSON_EscapedQuoteBeforeBrace(t *testing.T) {
	// Guard: an escaped quote must not be read as the end of the string,
	// which would make a following '}' look structural.
	in := []byte(`{"note":"an escaped \" then } brace","severity":"low"}`)
	got := string(executor.StripToJSON(in))
	if got != `{"note":"an escaped \" then } brace","severity":"low"}` {
		t.Errorf("got %q (escape handling changed the slice)", got)
	}
}

func TestStripToJSON_EmptyObject(t *testing.T) {
	in := []byte("here you go: {} done")
	got := string(executor.StripToJSON(in))
	if got != `{}` {
		t.Errorf("got %q, want %q", got, `{}`)
	}
}

func TestStripToJSON_UnbalancedObjectFallsBack(t *testing.T) {
	// Truncated CLI output: the object never closes, so there is no valid
	// candidate. Return what we have and let the caller's Unmarshal report it
	// — swallowing this into an empty string would hide the truncation.
	in := []byte(`partial result {"severity":"high"`)
	got := string(executor.StripToJSON(in))
	if got != `partial result {"severity":"high"` {
		t.Errorf("got %q, want the input back for a descriptive Unmarshal error", got)
	}
}

func TestStripToJSON_MalformedObjectFallsBackToOuterSlice(t *testing.T) {
	// Balanced braces but invalid JSON. No candidate validates, so the old
	// outer-slice behaviour applies and the caller sees the malformed object
	// rather than the surrounding prose.
	in := []byte(`here: {"severity":} done`)
	got := string(executor.StripToJSON(in))
	if got != `{"severity":}` {
		t.Errorf("got %q, want the malformed object %q", got, `{"severity":}`)
	}
}

func TestStripToJSON_TrailingBraceAtEndOfInput(t *testing.T) {
	// A '{' as the final byte has nothing after it to open an object with.
	in := []byte("nothing useful here {")
	got := string(executor.StripToJSON(in))
	if got != "nothing useful here {" {
		t.Errorf("got %q, want the input back", got)
	}
}

func TestStripToJSON_MultipleObjectsTakesTheFirst(t *testing.T) {
	// This used to assert the outer-slice limitation: the scan ran to the
	// LAST '}', so two top-level objects produced invalid JSON and the
	// caller's Unmarshal reported it. That pin existed to catch a silent
	// change; the balanced scan changes it deliberately. Returning the first
	// complete object is strictly more useful than returning something no
	// caller can parse, and our prompts ask for a single object anyway.
	in := []byte(`{"a":1}{"b":2}`)
	got := string(executor.StripToJSON(in))
	if got != `{"a":1}` {
		t.Errorf("got %q, want the first complete object %q", got, `{"a":1}`)
	}
}

package issues

import (
	"errors"
	"strings"
	"testing"
)

func TestCircuitBreakerErrorIncludesReasonAndUnwraps(t *testing.T) {
	err := &CircuitBreakerError{Reason: "per-repository cap reached"}
	if got := err.Error(); !strings.Contains(got, err.Reason) || !strings.Contains(got, ErrCircuitBreakerTripped.Error()) {
		t.Fatalf("Error() = %q, want sentinel and reason", got)
	}
	if !errors.Is(err, ErrCircuitBreakerTripped) {
		t.Fatal("CircuitBreakerError does not unwrap to ErrCircuitBreakerTripped")
	}
}

func TestMarshalEventFallsBackForUnsupportedFutureField(t *testing.T) {
	if got := marshalEvent(map[string]any{"unsupported": make(chan struct{})}); got != "{}" {
		t.Fatalf("marshalEvent unsupported value = %q, want {}", got)
	}
}

func TestSanitizeTitleDoesNotSplitUTF8AtByteLimit(t *testing.T) {
	input := strings.Repeat("a", maxTitleBytes-1) + "é"
	got := sanitizeTitle(input)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("sanitizeTitle() = %q, want ellipsis", got)
	}
	if strings.ContainsRune(got, '�') {
		t.Fatalf("sanitizeTitle() split a UTF-8 rune: %q", got)
	}
	if got != strings.Repeat("a", maxTitleBytes-1)+"…" {
		t.Fatalf("sanitizeTitle() = %q, want complete prefix plus ellipsis", got)
	}
}

package executor

import (
	"testing"
	"time"
)

func TestEffectiveExecutionTimeout(t *testing.T) {
	if got := effectiveExecutionTimeout(0); got != 20*time.Minute {
		t.Fatalf("effectiveExecutionTimeout(0) = %v, want 20m", got)
	}
	if got := effectiveExecutionTimeout(30 * time.Minute); got != 30*time.Minute {
		t.Fatalf("effectiveExecutionTimeout(30m) = %v, want 30m", got)
	}
}

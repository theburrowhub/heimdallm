package pipeline_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/heimdallm/daemon/internal/executor"
	"github.com/heimdallm/daemon/internal/pipeline"
)

func TestUserFacingReviewErrorHidesCancelledExecutionOutput(t *testing.T) {
	const secret = "raw partial CLI output must stay out of the UI"
	err := fmt.Errorf("executor: run codex: %w (output: %s)", executor.ErrExecutionCancelled, secret)

	got := pipeline.UserFacingReviewError(err)
	if got != "Review cancelled manually." {
		t.Fatalf("UserFacingReviewError() = %q, want manual cancellation status", got)
	}
	if strings.Contains(got, secret) {
		t.Fatalf("UserFacingReviewError() leaked captured CLI output: %q", got)
	}
}

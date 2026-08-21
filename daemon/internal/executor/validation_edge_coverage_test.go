package executor_test

import (
	"testing"

	"github.com/heimdallm/daemon/internal/executor"
)

func TestLegacyApprovalSuggestNormalizesToCodexUntrusted(t *testing.T) {
	got, err := executor.NormalizeApprovalModeForCLI("codex", " Suggest ")
	if err != nil || got != "untrusted" {
		t.Fatalf("NormalizeApprovalModeForCLI(codex, suggest) = (%q, %v), want (untrusted, nil)", got, err)
	}
}

func TestNormalizeLegacyCLIFlagsRejectsUnknownProvider(t *testing.T) {
	if _, _, err := executor.NormalizeLegacyCLIFlagsForCLI("shell", "--verbose"); err == nil {
		t.Fatal("NormalizeLegacyCLIFlagsForCLI accepted an unknown provider")
	}
}

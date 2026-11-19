package executor_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heimdallm/daemon/internal/executor"
)

// An answer that decodes as JSON but carries neither a summary nor a finding
// is not a review. Accepting it turned an empty object, a CLI's error object,
// or the first finding salvaged from a truncated answer into a clean review
// that approved the PR (#890).
func TestParseReview_RejectsAnswersThatAreNotAReview(t *testing.T) {
	cases := map[string]string{
		"empty object":         `{}`,
		"error object":         `{"error":"rate limited","code":429}`,
		"bare finding":         `{"file":"main.go","line":3,"description":"nil deref","severity":"low"}`,
		"blank summary":        `{"summary":"","issues":[],"severity":"low"}`,
		"whitespace summary":   `{"summary":" \n\t ","issues":[],"severity":"low"}`,
		"null summary":         `{"summary":null,"issues":[],"severity":"low"}`,
		"severity only":        `{"severity":"low"}`,
		"fenced empty object":  "```json\n{}\n```",
		"prose around nothing": "Here is my review: {} Thanks!",
		// The outer object never closes, so StripToJSON falls back to the
		// first complete nested object: the finding, not the review.
		"truncated answer": `{"summary":"Two problems","issues":[{"file":"a.go","line":1,"description":"d","severity":"low"},{"file":"b.go"`,
		"error with a 429": `{"error":{"code":429,"message":"quota exceeded"}}`,
	}
	for name, answer := range cases {
		t.Run(name, func(t *testing.T) {
			result, err := executor.ParseReview([]byte(answer))
			if err == nil {
				t.Fatalf("ParseReview(%q) = %+v, want an error", answer, result)
			}
			if !errors.Is(err, executor.ErrNotAReview) {
				t.Fatalf("ParseReview(%q) error = %v, want ErrNotAReview", answer, err)
			}
			// The answer stays out of the error, so a "429" inside it cannot
			// make failure classification read the review as out of quota.
			if executor.IsQuotaError(err) {
				t.Errorf("error %q classifies as a quota error", err)
			}
		})
	}
}

func TestParseReview_RejectsInvalidJSON(t *testing.T) {
	for _, answer := range []string{"plain text, no JSON", `{"summary":}`, `["summary"]`} {
		if _, err := executor.ParseReview([]byte(answer)); err == nil {
			t.Errorf("ParseReview(%q) succeeded, want a parse error", answer)
		}
	}
}

func TestParseReview_AcceptsReviews(t *testing.T) {
	cases := []struct {
		name       string
		answer     string
		wantIssues int
		wantSev    string
	}{
		{"clean review", `{"summary":"LGTM","issues":[],"severity":"low"}`, 0, "low"},
		{"no issues key", `{"summary":"Looks good","severity":"low"}`, 0, "low"},
		{"missing severity defaults to low", `{"summary":"Looks good","issues":[]}`, 0, "low"},
		{"with findings", `{"summary":"One problem","issues":[{"file":"a.go","line":1,"description":"d","severity":"high"}],"severity":"high"}`, 1, "high"},
		{"fenced", "```json\n{\"summary\":\"ok\",\"issues\":[],\"severity\":\"low\"}\n```", 0, "low"},
		{"wrapped in prose", "Review follows.\n{\"summary\":\"ok\",\"issues\":[],\"severity\":\"medium\"}\nDone.", 0, "medium"},
		// A custom prompt may ask only for findings and a severity.
		{"findings without a summary", `{"issues":[{"file":"a.go","line":1,"description":"d","severity":"high"}],"severity":"high"}`, 1, "high"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := executor.ParseReview([]byte(tc.answer))
			if err != nil {
				t.Fatalf("ParseReview(%q): %v", tc.answer, err)
			}
			if strings.TrimSpace(result.Summary) == "" && len(result.Issues) == 0 {
				t.Error("neither a summary nor a finding")
			}
			if len(result.Issues) != tc.wantIssues {
				t.Errorf("issues = %d, want %d", len(result.Issues), tc.wantIssues)
			}
			if result.Severity != tc.wantSev {
				t.Errorf("severity = %q, want %q", result.Severity, tc.wantSev)
			}
		})
	}
}

// End to end through a real subprocess: a CLI that exits 0 with something
// that is not a review must make Execute fail, so the pipeline hands the
// review to the next agent instead of publishing an approval.
func TestExecute_FailsWhenTheAgentAnswerIsNotAReview(t *testing.T) {
	for name, answer := range map[string]string{
		"empty object":     `{}`,
		"truncated answer": `{"summary":"Two problems","issues":[{"file":"a.go","line":1,"description":"d","severity":"low"},{"file":"b.go"`,
	} {
		t.Run(name, func(t *testing.T) {
			binDir := t.TempDir()
			script := "#!/bin/sh\nif [ \"$1\" = \"--help\" ]; then\n  printf 'Usage: gemini\\n'\n  exit 0\nfi\ncat >/dev/null\ncat <<'EOF'\n" + answer + "\nEOF\n"
			if err := os.WriteFile(filepath.Join(binDir, "gemini"), []byte(script), 0o755); err != nil {
				t.Fatalf("write fake CLI: %v", err)
			}
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

			result, err := executor.New().Execute("gemini", "Review this diff", executor.ExecOptions{})
			if err == nil {
				t.Fatalf("Execute = %+v, want an error", result)
			}
			if !errors.Is(err, executor.ErrNotAReview) {
				t.Errorf("Execute error = %v, want ErrNotAReview", err)
			}
		})
	}
}

package executor_test

import (
	"strings"
	"testing"

	"github.com/heimdallm/daemon/internal/executor"
)

func TestBuildPromptFromTemplate_CommentsSubstituted(t *testing.T) {
	tmpl := "Diff: {diff}\n{comments}\nReview."
	ctx := executor.PRContext{
		Diff:     "some diff",
		Comments: "Existing PR discussion:\n<user_content>\n@alice: LGTM\n</user_content>",
	}
	result := executor.BuildPromptFromTemplate(tmpl, ctx)
	if !strings.Contains(result, "@alice: LGTM") {
		t.Errorf("expected comments in result, got: %s", result)
	}
	if strings.Contains(result, "{comments}") {
		t.Errorf("placeholder {comments} not substituted in result")
	}
}

func TestBuildPromptFromTemplate_CommentsAppendedWhenNoPlaceholder(t *testing.T) {
	tmpl := "Diff: {diff}\nReview now."
	ctx := executor.PRContext{
		Diff:     "some diff",
		Comments: "Existing PR discussion:\n<user_content>\n@bob: fix the typo\n</user_content>",
	}
	result := executor.BuildPromptFromTemplate(tmpl, ctx)
	if !strings.Contains(result, "@bob: fix the typo") {
		t.Errorf("expected appended comments in result, got: %s", result)
	}
	if !strings.Contains(result, "Review now.") {
		t.Errorf("original template content missing from result")
	}
	reviewIdx := strings.Index(result, "Review now.")
	commentsIdx := strings.Index(result, "@bob:")
	if commentsIdx < reviewIdx {
		t.Errorf("expected comments after original content: reviewIdx=%d, commentsIdx=%d", reviewIdx, commentsIdx)
	}
}

func TestBuildPromptFromTemplate_EmptyCommentsNoAppend(t *testing.T) {
	tmpl := "Diff: {diff}\nReview now."
	ctx := executor.PRContext{
		Diff:     "some diff",
		Comments: "",
	}
	result := executor.BuildPromptFromTemplate(tmpl, ctx)
	if strings.Contains(result, "Existing PR discussion") {
		t.Errorf("expected no comments section when Comments is empty, got: %s", result)
	}
}

func TestBuildPromptFromTemplate_EmptyCommentsPlaceholderRemoved(t *testing.T) {
	tmpl := "Diff: {diff}\n{comments}\nReview."
	ctx := executor.PRContext{
		Diff:     "some diff",
		Comments: "",
	}
	result := executor.BuildPromptFromTemplate(tmpl, ctx)
	if strings.Contains(result, "{comments}") {
		t.Errorf("empty Comments should remove the placeholder, got: %s", result)
	}
}

func TestDefaultTemplate_ContainsCommentsPlaceholder(t *testing.T) {
	tmpl := executor.DefaultTemplate()
	if !strings.Contains(tmpl, "{comments}") {
		t.Error("defaultTemplate must contain {comments} placeholder")
	}
}

func TestDefaultTemplateWithInstructions_ContainsCommentsPlaceholder(t *testing.T) {
	tmpl := executor.DefaultTemplateWithInstructions("focus on security")
	if !strings.Contains(tmpl, "{comments}") {
		t.Error("DefaultTemplateWithInstructions must contain {comments} placeholder")
	}
}

func TestBuildPromptFromTemplate_EmptyComments_NoExtraBlankLines(t *testing.T) {
	ctx := executor.PRContext{Diff: "some diff", Comments: ""}
	result := executor.BuildPromptFromTemplate(executor.DefaultTemplate(), ctx)
	if strings.Contains(result, "\n\n\n") {
		t.Errorf("expected no triple newlines with empty Comments, got: %q", result)
	}
}

func TestBuildPromptFromTemplate_StandingInstructions(t *testing.T) {
	// Built-in template carries the placeholder: substituted in place.
	out := executor.BuildPromptFromTemplate(executor.DefaultTemplate(), executor.PRContext{
		Diff:                 "diff",
		StandingInstructions: "PROJECT STANDING INSTRUCTIONS (set by repo maintainers — authoritative):\n- no auth needed\n",
	})
	if !strings.Contains(out, "no auth needed") {
		t.Fatalf("standing instructions missing from prompt:\n%s", out)
	}

	// Custom template without the placeholder: section is prepended.
	out = executor.BuildPromptFromTemplate("Review this:\n{diff}", executor.PRContext{
		Diff:                 "diff",
		StandingInstructions: "PROJECT STANDING INSTRUCTIONS:\n- rule\n",
	})
	if !strings.HasPrefix(out, "PROJECT STANDING INSTRUCTIONS") {
		t.Fatalf("expected standing instructions prepended:\n%s", out)
	}

	// Empty standing instructions: nothing injected, no dangling header.
	out = executor.BuildPromptFromTemplate(executor.DefaultTemplate(), executor.PRContext{Diff: "diff"})
	if strings.Contains(out, "PROJECT STANDING INSTRUCTIONS") {
		t.Fatalf("did not expect standing-instructions header:\n%s", out)
	}
}

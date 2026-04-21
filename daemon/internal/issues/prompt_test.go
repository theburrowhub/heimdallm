package issues_test

import (
	"strings"
	"testing"

	"github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/issues"
)

func baseCtx() issues.PromptContext {
	return issues.PromptContext{
		Repo:      "org/repo",
		Number:    42,
		Title:     "Panic on startup",
		Author:    "alice",
		Labels:    []string{"bug", "regression"},
		Assignees: []string{"bob"},
		Body:      "Process crashes during initialisation.",
		Comments: []github.Comment{
			{Author: "carol", Body: "Seen since 0.1.4."},
		},
	}
}

func TestBuildImplementPrompt_DefaultTemplateContainsSafetyRules(t *testing.T) {
	got := issues.BuildImplementPrompt(baseCtx())

	for _, want := range []string{
		"You are Heimdallm",
		"Repository: org/repo",
		"Issue: #42 — Panic on startup",
		"Author: @alice",
		"Labels: bug, regression",
		"Assignees: bob",
		"Implement what the issue asks for",
		"Keep the change minimal",
		"leave the tree untouched",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("default implement prompt missing %q", want)
		}
	}
}

func TestBuildImplementPrompt_ExistingSignatureUnchanged(t *testing.T) {
	// Guard against accidentally dropping the zero-arg entry point — the
	// runAutoImplement fallback still calls it when no agent profile is
	// selected. Delegating to BuildImplementPromptWithProfile("","") must
	// produce a byte-identical result.
	viaDefault := issues.BuildImplementPrompt(baseCtx())
	viaProfile := issues.BuildImplementPromptWithProfile(baseCtx(), "", "")
	if viaDefault != viaProfile {
		t.Errorf("BuildImplementPrompt must equal BuildImplementPromptWithProfile(_, \"\", \"\")")
	}
}

func TestBuildImplementPromptWithProfile_CustomTemplateReplacesDefault(t *testing.T) {
	tmpl := "Implement issue {number} in {repo} titled '{title}' for @{author}. Labels: {labels}. Body: {body}. Assignees: {assignees}."
	got := issues.BuildImplementPromptWithProfile(baseCtx(), tmpl, "")

	for _, want := range []string{
		"Implement issue 42 in org/repo",
		"titled 'Panic on startup'",
		"for @alice",
		"Labels: bug, regression",
		"Assignees: bob",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("custom template missing %q, got: %q", want, got)
		}
	}
	// Default template MUST NOT leak through when a custom one is set.
	if strings.Contains(got, "You are Heimdallm") {
		t.Errorf("custom template should fully replace default, got default preamble: %q", got)
	}
	if strings.Contains(got, "Keep the change minimal") {
		t.Errorf("custom template should fully replace default rules: %q", got)
	}
}

func TestBuildImplementPromptWithProfile_InstructionsInjectedIntoDefault(t *testing.T) {
	instructions := "Use go 1.22 generics where helpful. Never add new deps without justification."
	got := issues.BuildImplementPromptWithProfile(baseCtx(), "", instructions)

	// Default scaffolding must stay — we are enriching, not replacing.
	if !strings.Contains(got, "Implement what the issue asks for") {
		t.Errorf("default scaffolding dropped when using instructions injection: %q", got)
	}
	if !strings.Contains(got, "Use go 1.22 generics where helpful") {
		t.Errorf("custom instructions not injected: %q", got)
	}
	if !strings.Contains(got, "Never add new deps without justification") {
		t.Errorf("custom instructions truncated: %q", got)
	}

	// Position guard: the injection must land BEFORE the "leave the tree
	// untouched" escape hatch — that escape hatch is the no-op sentinel the
	// outer pipeline relies on, and a maintainer's style nudge must not be
	// able to move past it.
	instrIdx := strings.Index(got, "Use go 1.22 generics")
	escapeIdx := strings.Index(got, "leave the tree untouched")
	if instrIdx == -1 || escapeIdx == -1 {
		t.Fatalf("test markers missing — instrIdx=%d escapeIdx=%d", instrIdx, escapeIdx)
	}
	if instrIdx > escapeIdx {
		t.Errorf("custom instructions (idx=%d) must appear before the escape hatch (idx=%d)", instrIdx, escapeIdx)
	}
}

func TestBuildImplementPromptWithProfile_TemplateWinsOverInstructions(t *testing.T) {
	// Contract parity with BuildPromptWithProfile: a non-empty custom
	// template takes precedence; instructions are ignored when both are set.
	got := issues.BuildImplementPromptWithProfile(
		baseCtx(),
		"TEMPLATE for {repo}",
		"THESE INSTRUCTIONS SHOULD NOT APPEAR",
	)
	if strings.Contains(got, "THESE INSTRUCTIONS SHOULD NOT APPEAR") {
		t.Errorf("instructions leaked when custom template was set: %q", got)
	}
	if !strings.HasPrefix(got, "TEMPLATE for org/repo") {
		t.Errorf("custom template not applied first: %q", got)
	}
}

func TestBuildPRDescriptionPrompt_ContainsRequiredElements(t *testing.T) {
	diff := []byte("diff --git a/main.go b/main.go\n+func NewWidget() {}\n")
	got := issues.BuildPRDescriptionPrompt(42, "Add widget factory", diff)

	for _, want := range []string{
		"Issue: #42",
		"Add widget factory",
		"diff --git a/main.go",
		"Write a pull request title",
		"conventional commit",
		`"title"`,
		`"body"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("PR description prompt missing %q", want)
		}
	}
}

func TestBuildPRDescriptionPrompt_TruncatesLargeDiff(t *testing.T) {
	largeDiff := make([]byte, 64*1024)
	for i := range largeDiff {
		largeDiff[i] = 'x'
	}
	got := issues.BuildPRDescriptionPrompt(1, "big change", largeDiff)

	if !strings.Contains(got, "... (diff truncated)") {
		t.Error("large diff should be truncated")
	}
	// The prompt should not contain the full 64KB diff.
	if len(got) > 40*1024 {
		t.Errorf("prompt too large: %d bytes", len(got))
	}
}

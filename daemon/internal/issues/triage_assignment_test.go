package issues

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type fakeHistoryRunner struct {
	outByCmd map[string]string
	errByCmd map[string]error
	calls    [][]string
}

func (f *fakeHistoryRunner) Output(_ context.Context, _ string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	key := strings.Join(args, "\x00")
	if err := f.errByCmd[key]; err != nil {
		return nil, err
	}
	return []byte(f.outByCmd[key]), nil
}

func historyKey(args ...string) string {
	return strings.Join(args, "\x00")
}

func TestResolveTriageAssignee_VerifiesSuggestedAssigneeInHistory(t *testing.T) {
	runner := &fakeHistoryRunner{outByCmd: map[string]string{
		historyKey("rev-parse", "--is-shallow-repository"):                                                     "false\n",
		historyKey("log", "--max-count=500", "--no-merges", "--format=%ae%x00%an", "--", "daemon/pipeline.go"): "123+alice@users.noreply.github.com\x00Alice\nbob@users.noreply.github.com\x00Bob\nalice@users.noreply.github.com\x00Alice\n",
	}}
	triage := Triage{
		AffectedPaths:      []string{"daemon/pipeline.go"},
		SuggestedAssignee:  "@alice",
		AssigneeConfidence: "high",
	}

	assignee, source, _, evidence := resolveTriageAssignee(context.Background(), "/repo", "owner", triage, runner)
	if assignee != "alice" || source != "suggested_assignee_verified" {
		t.Fatalf("assignee/source = %q/%q, want alice/suggested_assignee_verified", assignee, source)
	}
	if len(evidence) == 0 || !strings.Contains(evidence[0], "@alice") {
		t.Fatalf("expected alice evidence, got %v", evidence)
	}
}

func TestResolveTriageAssignee_KeepsSuggestedWhenHistoryPointsElsewhereWithoutFallback(t *testing.T) {
	runner := &fakeHistoryRunner{outByCmd: map[string]string{
		historyKey("rev-parse", "--is-shallow-repository"):                                                     "false\n",
		historyKey("log", "--max-count=500", "--no-merges", "--format=%ae%x00%an", "--", "daemon/pipeline.go"): "alice@users.noreply.github.com\x00Alice\nbob@users.noreply.github.com\x00Bob\nalice@users.noreply.github.com\x00Alice\n",
	}}
	triage := Triage{
		AffectedPaths:      []string{"daemon/pipeline.go"},
		SuggestedAssignee:  "ghost",
		AssigneeConfidence: "medium",
	}

	assignee, source, diagnostic, _ := resolveTriageAssignee(context.Background(), "/repo", "", triage, runner)
	if assignee != "ghost" || source != "suggested_assignee_unverified" {
		t.Fatalf("assignee/source = %q/%q, want ghost/suggested_assignee_unverified", assignee, source)
	}
	if !strings.Contains(diagnostic, "keeping suggested assignee") {
		t.Fatalf("diagnostic should explain the suggested assignee was kept, got %q", diagnostic)
	}
}

func TestResolveTriageAssignee_KeepsSuggestedWhenOnlyRecognizedHistoryIsAnotherContributor(t *testing.T) {
	runner := &fakeHistoryRunner{outByCmd: map[string]string{
		historyKey("rev-parse", "--is-shallow-repository"):                                                 "false\n",
		historyKey("log", "--max-count=500", "--no-merges", "--format=%ae%x00%an", "--", "docs/config.md"): "carol@users.noreply.github.com\x00Carol\n",
	}}
	triage := Triage{
		AffectedPaths:      []string{"docs/config.md"},
		SuggestedAssignee:  "alice",
		AssigneeConfidence: "high",
	}

	assignee, source, diagnostic, evidence := resolveTriageAssignee(context.Background(), "/repo", "owner", triage, runner)
	if assignee != "alice" || source != "suggested_assignee_unverified" {
		t.Fatalf("assignee/source = %q/%q, want alice/suggested_assignee_unverified", assignee, source)
	}
	if !strings.Contains(diagnostic, "alice") {
		t.Fatalf("diagnostic should mention unverified suggestion, got %q", diagnostic)
	}
	if len(evidence) == 0 || !strings.Contains(evidence[0], "@carol") {
		t.Fatalf("expected history evidence without assigning carol, got %v", evidence)
	}
}

func TestResolveTriageAssignee_KeepsSuggestedWhenHistoryHasNoRecognizedLogins(t *testing.T) {
	runner := &fakeHistoryRunner{outByCmd: map[string]string{
		historyKey("rev-parse", "--is-shallow-repository"):                                                     "false\n",
		historyKey("log", "--max-count=500", "--no-merges", "--format=%ae%x00%an", "--", "daemon/pipeline.go"): "dev@example.com\x00Dev One\nmaintainer@company.test\x00Maintainer\n",
	}}
	triage := Triage{
		AffectedPaths:      []string{"daemon/pipeline.go"},
		SuggestedAssignee:  "alice",
		AssigneeConfidence: "high",
	}

	assignee, source, diagnostic, evidence := resolveTriageAssignee(context.Background(), "/repo", "owner", triage, runner)
	if assignee != "alice" || source != "suggested_assignee_unverified" {
		t.Fatalf("assignee/source = %q/%q, want alice/suggested_assignee_unverified", assignee, source)
	}
	if !strings.Contains(diagnostic, "no GitHub-login-like contributors") {
		t.Fatalf("diagnostic should mention unrecognized history, got %q", diagnostic)
	}
	if evidence != nil {
		t.Fatalf("evidence = %v, want nil", evidence)
	}
}

func TestResolveTriageAssignee_LowConfidenceFallsBackToTriageOwner(t *testing.T) {
	assignee, source, diagnostic, evidence := resolveTriageAssignee(context.Background(), "/repo", "owner", Triage{
		AffectedPaths:      []string{"daemon/pipeline.go"},
		SuggestedAssignee:  "alice",
		AssigneeConfidence: "low",
	}, &fakeHistoryRunner{})

	if assignee != "owner" || source != "triage_owner" {
		t.Fatalf("assignee/source = %q/%q, want owner/triage_owner", assignee, source)
	}
	if !strings.Contains(diagnostic, "confidence") {
		t.Fatalf("diagnostic should mention confidence, got %q", diagnostic)
	}
	if len(evidence) == 0 || !strings.Contains(evidence[0], "triage_owner") {
		t.Fatalf("expected fallback evidence, got %v", evidence)
	}
}

func TestEnrichTriageResult_PreservesTentativeAssignee(t *testing.T) {
	result := &IssueReviewResult{
		Severity: "medium",
		Triage: Triage{
			Severity:           "medium",
			SuggestedAssignee:  "alice",
			TentativeAssignee:  "@bob",
			AssigneeConfidence: "low",
		},
	}

	enrichTriageResult(context.Background(), result, "", "owner", nil)
	if result.Triage.TentativeAssignee != "bob" {
		t.Fatalf("tentative assignee = %q, want bob", result.Triage.TentativeAssignee)
	}
	if result.Triage.AssignedAssignee != "owner" {
		t.Fatalf("assigned assignee = %q, want owner fallback", result.Triage.AssignedAssignee)
	}
}

func TestResolveTriageAssignee_ShallowHistoryKeepsSuggestedUnverified(t *testing.T) {
	runner := &fakeHistoryRunner{outByCmd: map[string]string{
		historyKey("rev-parse", "--is-shallow-repository"): "true\n",
	}}
	assignee, source, diagnostic, _ := resolveTriageAssignee(context.Background(), "/repo", "owner", Triage{
		AffectedPaths:      []string{"daemon/pipeline.go"},
		SuggestedAssignee:  "alice",
		AssigneeConfidence: "high",
	}, runner)

	if assignee != "alice" || source != "suggested_assignee_unverified" {
		t.Fatalf("assignee/source = %q/%q, want alice/suggested_assignee_unverified", assignee, source)
	}
	if !strings.Contains(diagnostic, errShallowHistory.Error()) {
		t.Fatalf("diagnostic should mention shallow history, got %q", diagnostic)
	}
}

func TestResolveTriageAssignee_NoSuggestedNoFallbackDoesNotAssignTopContributor(t *testing.T) {
	runner := &fakeHistoryRunner{outByCmd: map[string]string{
		historyKey("rev-parse", "--is-shallow-repository"):                                       "false\n",
		historyKey("log", "--max-count=500", "--no-merges", "--format=%ae%x00%an", "--", "x.go"): "alice@users.noreply.github.com\x00Alice\n",
	}}

	assignee, source, diagnostic, evidence := resolveTriageAssignee(context.Background(), "/repo", "", Triage{
		AffectedPaths:      []string{"x.go"},
		AssigneeConfidence: "high",
	}, runner)

	if assignee != "" || source != "none" {
		t.Fatalf("assignee/source = %q/%q, want empty/none", assignee, source)
	}
	if !strings.Contains(diagnostic, "no suggested_assignee") {
		t.Fatalf("diagnostic should mention missing suggestion, got %q", diagnostic)
	}
	if evidence != nil {
		t.Fatalf("evidence = %v, want nil", evidence)
	}
}

func TestContributorsForPaths_GitFailureReturnsError(t *testing.T) {
	runner := &fakeHistoryRunner{
		errByCmd: map[string]error{
			historyKey("rev-parse", "--is-shallow-repository"): errors.New("git missing"),
		},
	}
	_, err := contributorsForPaths(context.Background(), runner, "/repo", []string{"x.go"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestContributorsForPaths_IgnoresPlainEmailLocalParts(t *testing.T) {
	runner := &fakeHistoryRunner{outByCmd: map[string]string{
		historyKey("rev-parse", "--is-shallow-repository"):                                       "false\n",
		historyKey("log", "--max-count=500", "--no-merges", "--format=%ae%x00%an", "--", "x.go"): "victim@example.com\x00Victim\n123+alice@users.noreply.github.com\x00Alice\n",
	}}

	got, err := contributorsForPaths(context.Background(), runner, "/repo", []string{"x.go"})
	if err != nil {
		t.Fatalf("contributorsForPaths: %v", err)
	}
	if len(got) != 1 || got[0].login != "alice" {
		t.Fatalf("contributors = %+v, want only alice from GitHub noreply email", got)
	}
}

func TestSanitizeAffectedPaths(t *testing.T) {
	got := sanitizeAffectedPaths([]string{
		" daemon/internal/issues/pipeline.go ",
		"../secret",
		"/abs/path",
		"-n",
		".git/config",
		"daemon/internal/issues/pipeline.go",
		`flutter_app\lib\main.dart`,
	})
	want := []string{"daemon/internal/issues/pipeline.go", "flutter_app/lib/main.dart"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sanitizeAffectedPaths = %#v, want %#v", got, want)
	}
}

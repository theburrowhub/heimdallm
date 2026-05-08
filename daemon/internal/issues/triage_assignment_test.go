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

func TestResolveTriageAssignee_UsesTopContributorWhenNoFallbackOwner(t *testing.T) {
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
	if assignee != "alice" || source != "history_top_contributor" {
		t.Fatalf("assignee/source = %q/%q, want alice/history_top_contributor", assignee, source)
	}
	if !strings.Contains(diagnostic, "ghost") {
		t.Fatalf("diagnostic should mention rejected suggestion, got %q", diagnostic)
	}
}

func TestResolveTriageAssignee_KeepsFallbackWhenSuggestedMissingFromHistory(t *testing.T) {
	runner := &fakeHistoryRunner{outByCmd: map[string]string{
		historyKey("rev-parse", "--is-shallow-repository"):                                                 "false\n",
		historyKey("log", "--max-count=500", "--no-merges", "--format=%ae%x00%an", "--", "docs/config.md"): "vbuenog@users.noreply.github.com\x00V Bueno\n",
	}}
	triage := Triage{
		AffectedPaths:      []string{"docs/config.md"},
		SuggestedAssignee:  "ivanmunozruiz",
		AssigneeConfidence: "high",
	}

	assignee, source, diagnostic, evidence := resolveTriageAssignee(context.Background(), "/repo", "ivanmunozruiz", triage, runner)
	if assignee != "ivanmunozruiz" || source != "triage_owner" {
		t.Fatalf("assignee/source = %q/%q, want ivanmunozruiz/triage_owner", assignee, source)
	}
	if !strings.Contains(diagnostic, "ivanmunozruiz") {
		t.Fatalf("diagnostic should mention rejected suggestion, got %q", diagnostic)
	}
	if len(evidence) == 0 || !strings.Contains(evidence[0], "triage_owner") {
		t.Fatalf("expected fallback evidence, got %v", evidence)
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

func TestResolveTriageAssignee_ShallowHistoryFallsBackToTriageOwner(t *testing.T) {
	runner := &fakeHistoryRunner{outByCmd: map[string]string{
		historyKey("rev-parse", "--is-shallow-repository"): "true\n",
	}}
	assignee, source, diagnostic, _ := resolveTriageAssignee(context.Background(), "/repo", "owner", Triage{
		AffectedPaths:      []string{"daemon/pipeline.go"},
		SuggestedAssignee:  "alice",
		AssigneeConfidence: "high",
	}, runner)

	if assignee != "owner" || source != "triage_owner" {
		t.Fatalf("assignee/source = %q/%q, want owner/triage_owner", assignee, source)
	}
	if !strings.Contains(diagnostic, errShallowHistory.Error()) {
		t.Fatalf("diagnostic should mention shallow history, got %q", diagnostic)
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

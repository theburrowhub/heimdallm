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
		historyKey("rev-parse", "--is-shallow-repository"):                            "false\n",
		historyKey("log", "--format=%ae%x00%an", "--all", "--", "daemon/pipeline.go"): "123+alice@users.noreply.github.com\x00Alice\nbob@users.noreply.github.com\x00Bob\nalice@users.noreply.github.com\x00Alice\n",
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

func TestResolveTriageAssignee_ReplacesHallucinatedSuggestedWithTopContributor(t *testing.T) {
	runner := &fakeHistoryRunner{outByCmd: map[string]string{
		historyKey("rev-parse", "--is-shallow-repository"):                            "false\n",
		historyKey("log", "--format=%ae%x00%an", "--all", "--", "daemon/pipeline.go"): "alice@users.noreply.github.com\x00Alice\nbob@users.noreply.github.com\x00Bob\nalice@users.noreply.github.com\x00Alice\n",
	}}
	triage := Triage{
		AffectedPaths:      []string{"daemon/pipeline.go"},
		SuggestedAssignee:  "ghost",
		AssigneeConfidence: "medium",
	}

	assignee, source, diagnostic, _ := resolveTriageAssignee(context.Background(), "/repo", "owner", triage, runner)
	if assignee != "alice" || source != "history_top_contributor" {
		t.Fatalf("assignee/source = %q/%q, want alice/history_top_contributor", assignee, source)
	}
	if !strings.Contains(diagnostic, "ghost") {
		t.Fatalf("diagnostic should mention rejected suggestion, got %q", diagnostic)
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

func TestSanitizeAffectedPaths(t *testing.T) {
	got := sanitizeAffectedPaths([]string{
		" daemon/internal/issues/pipeline.go ",
		"../secret",
		"/abs/path",
		".git/config",
		"daemon/internal/issues/pipeline.go",
		`flutter_app\lib\main.dart`,
	})
	want := []string{"daemon/internal/issues/pipeline.go", "flutter_app/lib/main.dart"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sanitizeAffectedPaths = %#v, want %#v", got, want)
	}
}

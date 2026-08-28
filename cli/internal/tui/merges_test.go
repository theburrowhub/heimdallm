package tui

import (
	"strings"
	"testing"

	"github.com/theburrowhub/heimdallm/cli/internal/api"
)

func TestRenderMerges_EmptyStateTellsTheOperatorWhatToDo(t *testing.T) {
	d := &Dashboard{width: 120}
	out := d.renderMerges(10)
	if !strings.Contains(out, "merge_tracking") {
		t.Errorf("empty state should name the config section that turns it on, got:\n%s", out)
	}
}

// The marker is what makes a CI problem visible while scanning a list.
func TestRenderMerges_MarksRowsBlockedByChecks(t *testing.T) {
	d := &Dashboard{
		width: 120,
		merges: []api.MergeTrackingEntry{
			{Repo: "acme/widgets", Number: 7, Title: "Failing", Phase: "blocked",
				ChecksRequiredFailing: 1},
			{Repo: "acme/widgets", Number: 8, Title: "Waiting", Phase: "blocked",
				ChecksRequiredPending: 2},
			{Repo: "acme/widgets", Number: 9, Title: "Fine", Phase: "idle"},
		},
	}
	out := d.renderMerges(10)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d rows, want 3:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[0], "!") {
		t.Errorf("a failing check should be marked with !, got %q", lines[0])
	}
	if !strings.Contains(lines[1], "~") {
		t.Errorf("a pending check should be marked with ~, got %q", lines[1])
	}
	if strings.Contains(lines[2], "!") || strings.Contains(lines[2], "~") {
		t.Errorf("a clean row should carry no marker, got %q", lines[2])
	}
}

func TestBuildMergeDetailLines_ExplainsAFailingCheck(t *testing.T) {
	entry := api.MergeTrackingEntry{
		Repo: "acme/widgets", Number: 7, Title: "Add widget cache",
		Phase:       "blocked",
		HeadRef:     "feature",
		BaseRef:     "main",
		BlockReason: "checks_failing",
		BlockDetail: "1 required check is failing: build (GitHub Actions)",
		Decision: &api.MergeDecision{
			ChecksSummary: api.MergeChecksSummary{
				Total: 2, RequiredTotal: 2, RequiredFailing: 1, RequiredSuccess: 1,
			},
			Checks: []api.MergeCheck{
				{Name: "build", State: "failure", Required: true, App: "GitHub Actions"},
				{Name: "lint", State: "success", Required: true},
			},
		},
	}
	out := strings.Join(buildMergeDetailLines(entry, 120), "\n")

	if !strings.Contains(out, "1 required check is failing: build (GitHub Actions)") {
		t.Errorf("the detail must name the failing check:\n%s", out)
	}
	// The same sentence the GUI shows, so the two surfaces agree.
	if !strings.Contains(out, "This PR cannot be merged: 1 of the 2 required checks is failing.") {
		t.Errorf("the plain-language headline is missing:\n%s", out)
	}
	if !strings.Contains(out, "build") || !strings.Contains(out, "lint") {
		t.Errorf("every check should be listed:\n%s", out)
	}
	if !strings.Contains(out, "feature") || !strings.Contains(out, "main") {
		t.Errorf("the branches should be shown:\n%s", out)
	}
}

// A force-push by the agent is recoverable only if the operator can find the
// commit the branch was at.
func TestBuildMergeDetailLines_SurfacesThePreRebaseSHA(t *testing.T) {
	entry := api.MergeTrackingEntry{
		Repo: "acme/widgets", Number: 7, Phase: "idle",
		PreRebaseSHA: "abc123def456",
	}
	out := strings.Join(buildMergeDetailLines(entry, 120), "\n")
	if !strings.Contains(out, "abc123def456") {
		t.Errorf("the pre-rebase sha must be shown so a bad resolution can be undone:\n%s", out)
	}
}

func TestBuildMergeDetailLines_UnevaluatedPRSaysSo(t *testing.T) {
	entry := api.MergeTrackingEntry{Repo: "acme/widgets", Number: 7, Phase: "idle"}
	out := strings.Join(buildMergeDetailLines(entry, 120), "\n")
	if !strings.Contains(out, "not evaluated") {
		t.Errorf("an unevaluated PR should say so rather than render an empty table:\n%s", out)
	}
}

func TestMergeChecksHeadlineTUI_CoversEveryShape(t *testing.T) {
	cases := []struct {
		name    string
		summary api.MergeChecksSummary
		want    string
	}{
		{"truncated", api.MergeChecksSummary{Truncated: true}, "cannot be confirmed"},
		{"missing", api.MergeChecksSummary{MissingRequired: []string{"e2e"}}, "have not run"},
		{"failing", api.MergeChecksSummary{RequiredTotal: 3, RequiredFailing: 2}, "cannot be merged"},
		{"pending", api.MergeChecksSummary{RequiredTotal: 1, RequiredPending: 1}, "merges on its own"},
		{"optional red", api.MergeChecksSummary{RequiredTotal: 2, RequiredSuccess: 2, OptionalFailing: 1}, "does not block"},
		{"none", api.MergeChecksSummary{}, "no checks configured"},
		{"all green", api.MergeChecksSummary{Total: 2, RequiredTotal: 2, RequiredSuccess: 2}, "All 2 checks passed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeChecksHeadlineTUI(tc.summary)
			if !strings.Contains(got, tc.want) {
				t.Errorf("headline = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

func TestViewportRange_KeepsTheCursorVisible(t *testing.T) {
	d := &Dashboard{cursor: 12}
	start, end := d.viewportRange(20, 5)
	if d.cursor < start || d.cursor >= end {
		t.Errorf("cursor %d outside viewport [%d,%d)", d.cursor, start, end)
	}
	if end-start != 5 {
		t.Errorf("viewport height = %d, want 5", end-start)
	}
}

func TestViewportRange_HandlesShortLists(t *testing.T) {
	d := &Dashboard{cursor: 0}
	start, end := d.viewportRange(2, 10)
	if start != 0 || end != 2 {
		t.Errorf("range = [%d,%d), want [0,2)", start, end)
	}
	start, end = d.viewportRange(0, 10)
	if start != 0 || end != 0 {
		t.Errorf("empty list range = [%d,%d), want [0,0)", start, end)
	}
}

// The list shrinks under the cursor when a PR merges and drops out between two
// refreshes. An out-of-range cursor put start past total, start > end made the
// render loop a no-op, and the tab went blank until the operator moved.
func TestViewportRange_SurvivesAListThatShrankUnderTheCursor(t *testing.T) {
	d := &Dashboard{cursor: 12}
	start, end := d.viewportRange(3, 5)
	if start > end {
		t.Fatalf("range = [%d,%d), which renders nothing", start, end)
	}
	if start != 0 || end != 3 {
		t.Errorf("range = [%d,%d), want the whole short list [0,3)", start, end)
	}
}

// The tab list and the enum must stay in step, or every tab after the new one
// renders under the wrong name.
func TestTabNames_MatchTheEnum(t *testing.T) {
	if len(tabNames) != int(tabServer)+1 {
		t.Fatalf("tabNames has %d entries but the enum has %d", len(tabNames), int(tabServer)+1)
	}
	if tabNames[tabMerges] != "Merges" {
		t.Errorf("tabNames[tabMerges] = %q, want Merges", tabNames[tabMerges])
	}
	if tabNames[tabIssues] != "Issues" {
		t.Errorf("tabNames[tabIssues] = %q — inserting a tab shifted the others", tabNames[tabIssues])
	}
	if tabNames[tabServer] != "Server" {
		t.Errorf("tabNames[tabServer] = %q", tabNames[tabServer])
	}
}

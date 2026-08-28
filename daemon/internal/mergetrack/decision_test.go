package mergetrack_test

import (
	"strings"
	"testing"

	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/mergetrack"
	"github.com/heimdallm/daemon/internal/store"
)

func TestDecision_ExplainCoversEveryShape(t *testing.T) {
	ready := mergetrack.Decision{Ready: true, HeadSHA: "abcdef1234567890"}
	if got := ready.Explain(); !strings.Contains(got, "ready to merge") || !strings.Contains(got, "abcdef12") {
		t.Errorf("explain = %q, want a short sha and the ready phrasing", got)
	}
	// A short sha is not truncated into nonsense.
	short := mergetrack.Decision{Ready: true, HeadSHA: "abc"}
	if got := short.Explain(); !strings.Contains(got, "abc") {
		t.Errorf("explain = %q", got)
	}

	empty := mergetrack.Decision{}
	if got := empty.Explain(); got != "no action" {
		t.Errorf("explain = %q, want 'no action'", got)
	}

	blocked := mergetrack.Decision{Blocks: []mergetrack.Block{
		{Reason: mergetrack.ReasonChecksFailing, Detail: "build"},
		{Reason: mergetrack.ReasonDraft},
	}}
	got := blocked.Explain()
	if !strings.Contains(got, "checks_failing (build)") || !strings.Contains(got, "draft") {
		t.Errorf("explain = %q, want both blocks with the detail in parentheses", got)
	}
}

func TestDecision_PrimaryAccessorsOnAnEmptyDecision(t *testing.T) {
	var d mergetrack.Decision
	if d.PrimaryReason() != mergetrack.ReasonNone {
		t.Errorf("reason = %q, want none", d.PrimaryReason())
	}
	if d.PrimaryDetail() != "" {
		t.Errorf("detail = %q, want empty", d.PrimaryDetail())
	}
}

func TestChecksSummary_AnyProblem(t *testing.T) {
	cases := map[string]struct {
		s    mergetrack.ChecksSummary
		want bool
	}{
		"clean":     {mergetrack.ChecksSummary{Total: 3, RequiredSuccess: 3}, false},
		"failing":   {mergetrack.ChecksSummary{RequiredFailing: 1}, true},
		"pending":   {mergetrack.ChecksSummary{RequiredPending: 1}, true},
		"missing":   {mergetrack.ChecksSummary{MissingRequired: []string{"e2e"}}, true},
		"truncated": {mergetrack.ChecksSummary{Truncated: true}, true},
		// An optional red check is a problem for the reader but not for the
		// merge, so it does not raise the listing warning on its own.
		"optional red": {mergetrack.ChecksSummary{OptionalFailing: 2}, false},
	}
	for name, tc := range cases {
		if got := tc.s.AnyProblem(); got != tc.want {
			t.Errorf("%s: AnyProblem = %v, want %v", name, got, tc.want)
		}
	}
}

// The headline is the sentence every surface shows, so each shape has to read
// as a sentence rather than as a template with a hole in it.
func TestDecision_HeadlineNamesTheMissingChecks(t *testing.T) {
	cases := []struct {
		name    string
		summary mergetrack.ChecksSummary
		want    []string
	}{
		{"one missing", mergetrack.ChecksSummary{MissingRequired: []string{"e2e"}},
			[]string{"e2e", "a required check", "it has"}},
		{"two missing", mergetrack.ChecksSummary{MissingRequired: []string{"e2e", "smoke"}},
			[]string{"e2e and smoke", "they have"}},
		{"three missing", mergetrack.ChecksSummary{MissingRequired: []string{"a", "b", "c"}},
			[]string{"a, b and c"}},
		{"many missing", mergetrack.ChecksSummary{MissingRequired: []string{"a", "b", "c", "d"}},
			[]string{"a, b and 2 more"}},
		{"one failing", mergetrack.ChecksSummary{RequiredTotal: 2, RequiredFailing: 1},
			[]string{"1 of the 2 required checks is failing"}},
		{"two failing", mergetrack.ChecksSummary{RequiredTotal: 3, RequiredFailing: 2},
			[]string{"2 of the 3 required checks are failing"}},
		{"one pending", mergetrack.ChecksSummary{RequiredPending: 1},
			[]string{"1 required check", "merges on its own"}},
		{"two pending", mergetrack.ChecksSummary{RequiredPending: 2},
			[]string{"2 required checks"}},
		{"optional red", mergetrack.ChecksSummary{RequiredTotal: 2, RequiredSuccess: 2, OptionalFailing: 1},
			[]string{"1 optional check is failing", "does not block"}},
		{"optional reds", mergetrack.ChecksSummary{RequiredTotal: 2, RequiredSuccess: 2, OptionalFailing: 3},
			[]string{"3 optional checks are failing"}},
		{"none", mergetrack.ChecksSummary{}, []string{"no checks configured"}},
		{"all green", mergetrack.ChecksSummary{Total: 4, RequiredTotal: 4, RequiredSuccess: 4},
			[]string{"All 4 checks passed"}},
		{"truncated", mergetrack.ChecksSummary{Truncated: true},
			[]string{"cannot be confirmed"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mergetrack.Decision{ChecksSummary: tc.summary}.Headline()
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("headline = %q, want it to contain %q", got, want)
				}
			}
		})
	}
}

func TestReason_IsTerminalAndCheckRelated(t *testing.T) {
	terminal := []mergetrack.Reason{
		mergetrack.ReasonAlreadyMerged, mergetrack.ReasonClosed, mergetrack.ReasonNotTracked,
		mergetrack.ReasonCrossFork, mergetrack.ReasonInsufficientPermission,
		mergetrack.ReasonMergeMethodNotAllowed,
	}
	for _, r := range terminal {
		if !r.IsTerminal() {
			t.Errorf("%q should be terminal", r)
		}
	}
	for _, r := range []mergetrack.Reason{
		mergetrack.ReasonChecksFailing, mergetrack.ReasonBehindBase, mergetrack.ReasonNone,
	} {
		if r.IsTerminal() {
			t.Errorf("%q must not be terminal — it can clear on its own", r)
		}
	}

	for _, r := range []mergetrack.Reason{
		mergetrack.ReasonChecksFailing, mergetrack.ReasonChecksPending,
		mergetrack.ReasonRequiredCheckMissing, mergetrack.ReasonChecksUnknown,
	} {
		if !r.IsCheckRelated() {
			t.Errorf("%q should be check-related", r)
		}
	}
	if mergetrack.ReasonDraft.IsCheckRelated() {
		t.Error("draft is not a check problem")
	}
	if got := mergetrack.ReasonDraft.String(); got != "draft" {
		t.Errorf("String() = %q", got)
	}
}

func TestPhaseHelpers_CoverTheRestingStates(t *testing.T) {
	if got := mergetrack.PhaseFor(mergetrack.ActionNone); got != store.MergePhaseIdle {
		t.Errorf("PhaseFor(none) = %q, want idle", got)
	}
	if got := mergetrack.RestPhaseFor(mergetrack.Decision{Action: mergetrack.ActionMarkMerged}); got != store.MergePhaseMerged {
		t.Errorf("rest phase = %q, want merged", got)
	}
	if got := mergetrack.RestPhaseFor(mergetrack.Decision{Action: mergetrack.ActionAbandon}); got != store.MergePhaseAbandoned {
		t.Errorf("rest phase = %q, want abandoned", got)
	}
	if got := mergetrack.RestPhaseFor(mergetrack.Decision{Ready: true}); got != store.MergePhaseIdle {
		t.Errorf("rest phase = %q, want idle for a ready PR", got)
	}
	blocked := mergetrack.Decision{Blocks: []mergetrack.Block{{Reason: mergetrack.ReasonDraft}}}
	if got := mergetrack.RestPhaseFor(blocked); got != store.MergePhaseBlocked {
		t.Errorf("rest phase = %q, want blocked", got)
	}
	// Arming has a counter of its own: without one, a repeated failure backed
	// off by a flat minute forever instead of growing.
	if got := mergetrack.AttemptKindFor(mergetrack.ActionArmAutoMerge); got != store.MergeAttemptArm {
		t.Errorf("attempt kind = %q, want arm", got)
	}
	if got := mergetrack.AttemptKindFor(mergetrack.ActionUpdateBranchLocal); got != store.MergeAttemptUpdate {
		t.Errorf("attempt kind = %q, want update", got)
	}
}

func TestMergeMethodForGitHub_Normalises(t *testing.T) {
	for in, want := range map[string]string{
		"SQUASH": "squash", " Rebase ": "rebase", "merge": "merge", "": "",
	} {
		if got := mergetrack.MergeMethodForGitHub(in); got != want {
			t.Errorf("MergeMethodForGitHub(%q) = %q, want %q", in, got, want)
		}
	}
}

// Presentation order is load-bearing: the first row a reader sees should be the
// one blocking the merge.
func TestEvaluate_OrdersChecksForDisplay(t *testing.T) {
	st := cleanStatus()
	st.MergeStateStatus = gh.MergeStateBlocked
	st.Checks = []gh.CheckContext{
		{Name: "zzz-ok", State: gh.CheckStateSuccess, Required: true},
		{Name: "optional-red", State: gh.CheckStateFailure, Required: false},
		{Name: "aaa-pending", State: gh.CheckStatePending, Required: true},
		{Name: "bbb-failing", State: gh.CheckStateFailure, Required: true},
		{Name: "optional-ok", State: gh.CheckStateSuccess, Required: false},
	}
	d := mergetrack.Evaluate(st, baseInput(enabledCfg()))

	var order []string
	for _, c := range d.Checks {
		order = append(order, c.Name)
	}
	want := []string{"bbb-failing", "aaa-pending", "optional-red", "zzz-ok", "optional-ok"}
	for i, w := range want {
		if order[i] != w {
			t.Fatalf("check order = %v, want %v", order, want)
		}
	}
}

// A check with no name at all is dropped rather than rendered as a blank row.
func TestEvaluate_DropsNamelessChecks(t *testing.T) {
	st := cleanStatus()
	st.Checks = []gh.CheckContext{
		{Name: "", State: gh.CheckStateSuccess, Required: true},
		{Name: "build", State: gh.CheckStateSuccess, Required: true},
	}
	d := mergetrack.Evaluate(st, baseInput(enabledCfg()))
	if len(d.Checks) != 1 || d.Checks[0].Name != "build" {
		t.Errorf("checks = %+v, want only the named one", d.Checks)
	}
}

// Branch protection listing a blank context must not produce a phantom
// "required check that never reported" with no name.
func TestEvaluate_IgnoresBlankRequiredContexts(t *testing.T) {
	st := cleanStatus()
	st.Protection = &gh.BranchProtection{
		RequiresStatusChecks:        true,
		RequiredStatusCheckContexts: []string{"  ", "build"},
	}
	d := mergetrack.Evaluate(st, baseInput(enabledCfg()))
	if len(d.ChecksSummary.MissingRequired) != 0 {
		t.Errorf("missing required = %v, want none — the blank entry is not a check",
			d.ChecksSummary.MissingRequired)
	}
	if !d.Ready {
		t.Fatalf("the PR should be ready, blocks = %v", d.Blocks)
	}
}

// Two checks in the same group sort by name, so the list is stable between
// evaluations rather than reordering on every refresh.
func TestEvaluate_SortsWithinAGroupByName(t *testing.T) {
	st := cleanStatus()
	st.MergeStateStatus = gh.MergeStateBlocked
	st.Checks = []gh.CheckContext{
		{Name: "Zebra", State: gh.CheckStateFailure, Required: true},
		{Name: "alpha", State: gh.CheckStateFailure, Required: true},
	}
	d := mergetrack.Evaluate(st, baseInput(enabledCfg()))
	if d.Checks[0].Name != "alpha" || d.Checks[1].Name != "Zebra" {
		t.Errorf("order = %q, %q — want a case-insensitive name sort",
			d.Checks[0].Name, d.Checks[1].Name)
	}
}

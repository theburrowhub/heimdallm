package mergetrack_test

import (
	"strings"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/mergetrack"
	"github.com/heimdallm/daemon/internal/store"
)

// decide runs the full pipeline the reconciler runs.
func decide(st *gh.MergeStatus, in mergetrack.Input) mergetrack.Decision {
	return mergetrack.Decide(mergetrack.Evaluate(st, in), st, in)
}

func allOn() config.MergeTrackingConfig {
	c := enabledCfg()
	c.EnableAutoMerge = true
	c.UpdateBranch = true
	c.ResolveConflicts = true
	c.Merge = true
	return c
}

// With every automation off, nothing is ever acted on — the state a fresh
// install is in, and the guarantee that enabling tracking alone is inert.
func TestDecide_AllTogglesOffNeverActs(t *testing.T) {
	cases := map[string]func(*gh.MergeStatus){
		"clean":    func(*gh.MergeStatus) {},
		"behind":   func(s *gh.MergeStatus) { s.MergeStateStatus = gh.MergeStateBehind },
		"dirty":    func(s *gh.MergeStatus) { s.Mergeable = gh.MergeableNo },
		"unstable": func(s *gh.MergeStatus) { s.MergeStateStatus = gh.MergeStateUnstable },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			st := cleanStatus()
			mutate(st)
			in := baseInput(enabledCfg()) // tracking on, all four automations off
			if got := decide(st, in).Action; got != mergetrack.ActionNone {
				t.Errorf("action = %q, want none", got)
			}
		})
	}
}

// The two-phase merge the operator asked for: arm GitHub's native auto-merge
// first, record it, and only merge directly on a LATER pass if GitHub has not.
func TestDecide_TwoPhaseMerge_ArmsFirst(t *testing.T) {
	st := cleanStatus()
	in := baseInput(allOn())

	d := decide(st, in)
	if d.Action != mergetrack.ActionArmAutoMerge {
		t.Fatalf("action = %q, want arm_auto_merge on the first pass", d.Action)
	}
	if !d.Ready {
		t.Error("the PR is ready; arming is the chosen path, not a fallback for a blocked PR")
	}
}

// Armed during THIS pass: merging now would race GitHub, which has not had a
// chance to act yet.
func TestDecide_TwoPhaseMerge_DoesNotMergeInTheSamePass(t *testing.T) {
	st := cleanStatus()
	st.AutoMerge = &gh.AutoMergeRequest{MergeMethod: "SQUASH"}

	in := baseInput(allOn())
	in.State.Phase = store.MergePhaseAutoMergeArmed
	in.State.AutoMergeHeadSHA = headSHA
	in.State.AutoMergeArmedAt = in.TickStart // armed during this very cycle

	d := decide(st, in)
	if d.Action == mergetrack.ActionMerge {
		t.Fatal("must not merge directly in the same pass that armed auto-merge")
	}
	if d.PrimaryReason() != mergetrack.ReasonAutoMergeWaiting {
		t.Errorf("reason = %q, want automerge_waiting", d.PrimaryReason())
	}
}

// Armed on an EARLIER pass and GitHub still has not merged: take over.
func TestDecide_TwoPhaseMerge_MergesOnALaterPass(t *testing.T) {
	st := cleanStatus()
	st.AutoMerge = &gh.AutoMergeRequest{MergeMethod: "SQUASH"}

	in := baseInput(allOn())
	in.State.Phase = store.MergePhaseAutoMergeArmed
	in.State.AutoMergeHeadSHA = headSHA
	in.State.AutoMergeArmedAt = in.TickStart.Add(-10 * time.Minute)

	d := decide(st, in)
	if d.Action != mergetrack.ActionMerge {
		t.Fatalf("action = %q, want merge once GitHub has had its turn", d.Action)
	}
}

// An armed state anchored to a commit that no longer exists is not a licence to
// merge the new one.
func TestDecide_ArmedForAnOlderCommitDoesNotAuthoriseMerge(t *testing.T) {
	st := cleanStatus()
	st.AutoMerge = &gh.AutoMergeRequest{MergeMethod: "SQUASH"}

	in := baseInput(allOn())
	in.State.Phase = store.MergePhaseAutoMergeArmed
	in.State.AutoMergeHeadSHA = oldSHA // a push has happened since
	in.State.AutoMergeArmedAt = in.TickStart.Add(-time.Hour)

	if got := decide(st, in).Action; got == mergetrack.ActionMerge {
		t.Fatal("an auto-merge armed for a previous commit must not authorise merging this one")
	}
}

// Our row says armed, GitHub says otherwise (someone turned it off in the web
// UI). GitHub wins, and we re-arm rather than merging on a stale belief.
func TestDecide_StaleArmedRowSelfHeals(t *testing.T) {
	st := cleanStatus()
	st.AutoMerge = nil

	in := baseInput(allOn())
	in.State.Phase = store.MergePhaseAutoMergeArmed
	in.State.AutoMergeHeadSHA = headSHA
	in.State.AutoMergeArmedAt = in.TickStart.Add(-time.Hour)

	d := decide(st, in)
	if d.Action != mergetrack.ActionArmAutoMerge {
		t.Errorf("action = %q, want arm_auto_merge when GitHub shows no auto-merge request", d.Action)
	}
}

// With auto-merge off, a ready PR is merged directly and immediately.
func TestDecide_DirectMergeWhenAutoMergeDisabled(t *testing.T) {
	cfg := allOn()
	cfg.EnableAutoMerge = false
	if got := decide(cleanStatus(), baseInput(cfg)).Action; got != mergetrack.ActionMerge {
		t.Errorf("action = %q, want merge", got)
	}
}

// Arming while the PR is red is the whole point of native auto-merge: GitHub
// merges the moment the last check goes green.
func TestDecide_ArmsAutoMergeWhileChecksArePending(t *testing.T) {
	st := cleanStatus()
	st.Checks = []gh.CheckContext{{Name: "build", State: gh.CheckStatePending, Required: true}}

	cfg := enabledCfg()
	cfg.EnableAutoMerge = true // merge deliberately off

	d := decide(st, baseInput(cfg))
	if d.Action != mergetrack.ActionArmAutoMerge {
		t.Fatalf("action = %q, want arm_auto_merge with checks pending", d.Action)
	}
	if d.Ready {
		t.Error("the PR is not ready; arming is still correct")
	}
}

// Arming a conflicted PR would leave a standing instruction to merge something
// nobody is going to fix on its own.
func TestDecide_DoesNotArmAutoMergeOnConflicts(t *testing.T) {
	st := cleanStatus()
	st.Mergeable = gh.MergeableNo
	cfg := enabledCfg()
	cfg.EnableAutoMerge = true

	if got := decide(st, baseInput(cfg)).Action; got == mergetrack.ActionArmAutoMerge {
		t.Fatal("must not arm auto-merge on a conflicted PR")
	}
}

// Auto-merge wanted but the repo forbids the configured method: fall back to a
// direct merge rather than doing nothing, since the PR is ready and the
// operator asked for it to be merged.
func TestDecide_FallsBackToDirectMergeWhenAutoMergeImpossible(t *testing.T) {
	st := cleanStatus()
	st.AllowedMergeMethods = gh.MergeMethodSet{Squash: true} // squash only, which we use
	st.NodeID = ""                                           // no node id: the mutation cannot run

	if got := decide(st, baseInput(allOn())).Action; got != mergetrack.ActionMerge {
		t.Errorf("action = %q, want merge as the fallback", got)
	}
}

func TestDecide_ConflictsRunTheAgentWhenEnabled(t *testing.T) {
	st := cleanStatus()
	st.Mergeable = gh.MergeableNo
	st.MergeStateStatus = gh.MergeStateDirty

	cfg := enabledCfg()
	cfg.ResolveConflicts = true

	if got := decide(st, baseInput(cfg)).Action; got != mergetrack.ActionResolveConflicts {
		t.Errorf("action = %q, want resolve_conflicts", got)
	}
}

func TestDecide_ConflictAttemptCapStopsTheAgent(t *testing.T) {
	st := cleanStatus()
	st.Mergeable = gh.MergeableNo

	cfg := enabledCfg()
	cfg.ResolveConflicts = true
	in := baseInput(cfg)
	in.State.ConflictAttempts = cfg.MaxResolveAttempts

	d := decide(st, in)
	if d.Action != mergetrack.ActionNone {
		t.Errorf("action = %q, want none once the attempt cap is reached", d.Action)
	}
	if d.PrimaryReason() != mergetrack.ReasonAttemptCap {
		t.Errorf("reason = %q, want attempt_cap_reached", d.PrimaryReason())
	}
	if !strings.Contains(d.PrimaryDetail(), "2") {
		t.Errorf("detail %q should quote the cap", d.PrimaryDetail())
	}
}

func TestDecide_BehindBaseUpdatesWhenEnabled(t *testing.T) {
	st := cleanStatus()
	st.MergeStateStatus = gh.MergeStateBehind

	cfg := enabledCfg()
	cfg.UpdateBranch = true

	if got := decide(st, baseInput(cfg)).Action; got != mergetrack.ActionUpdateBranchRemote {
		t.Errorf("action = %q, want update_branch_remote", got)
	}
}

func TestDecide_UpdateAttemptCapStops(t *testing.T) {
	st := cleanStatus()
	st.MergeStateStatus = gh.MergeStateBehind

	cfg := enabledCfg()
	cfg.UpdateBranch = true
	in := baseInput(cfg)
	in.State.UpdateAttempts = cfg.MaxUpdateAttempts

	d := decide(st, in)
	if d.Action != mergetrack.ActionNone {
		t.Errorf("action = %q, want none once the update cap is reached", d.Action)
	}
	if d.PrimaryReason() != mergetrack.ReasonAttemptCap {
		t.Errorf("reason = %q, want attempt_cap_reached", d.PrimaryReason())
	}
}

func TestDecide_MergeAttemptCapStops(t *testing.T) {
	cfg := allOn()
	cfg.EnableAutoMerge = false
	in := baseInput(cfg)
	in.State.MergeAttempts = cfg.MaxMergeAttempts

	d := decide(cleanStatus(), in)
	if d.Action == mergetrack.ActionMerge {
		t.Fatal("must not keep retrying a merge past the cap")
	}
	if d.PrimaryReason() != mergetrack.ReasonAttemptCap {
		t.Errorf("reason = %q, want attempt_cap_reached", d.PrimaryReason())
	}
}

func TestDecide_CooldownSuppressesAction(t *testing.T) {
	in := baseInput(allOn())
	in.State.CooldownUntil = in.Now.Add(5 * time.Minute)

	d := decide(cleanStatus(), in)
	if d.Action != mergetrack.ActionNone {
		t.Errorf("action = %q, want none during a cooldown", d.Action)
	}
	if d.PrimaryReason() != mergetrack.ReasonCooldown {
		t.Errorf("reason = %q, want cooldown", d.PrimaryReason())
	}
}

func TestDecide_ExpiredCooldownDoesNotSuppress(t *testing.T) {
	in := baseInput(allOn())
	in.State.CooldownUntil = in.Now.Add(-time.Second)

	if got := decide(cleanStatus(), in).Action; got == mergetrack.ActionNone {
		t.Error("an expired cooldown must not suppress the action")
	}
}

// An excluded PR is inert regardless of config.
func TestDecide_ExcludedPRIsInert(t *testing.T) {
	in := baseInput(allOn())
	in.State.Excluded = true

	d := decide(cleanStatus(), in)
	if d.Action != mergetrack.ActionNone {
		t.Errorf("action = %q, want none for an excluded PR", d.Action)
	}
	if d.PrimaryReason() != mergetrack.ReasonExcluded {
		t.Errorf("reason = %q, want excluded", d.PrimaryReason())
	}
}

// merge=false with auto_merge=true is a coherent conservative setup: hand the
// merge to GitHub, never do it ourselves.
func TestDecide_MergeDisabledStillArms(t *testing.T) {
	cfg := enabledCfg()
	cfg.EnableAutoMerge = true
	cfg.Merge = false

	if got := decide(cleanStatus(), baseInput(cfg)).Action; got != mergetrack.ActionArmAutoMerge {
		t.Errorf("action = %q, want arm_auto_merge", got)
	}
}

func TestDecide_MergeAndAutoMergeBothDisabledReportsWhy(t *testing.T) {
	d := decide(cleanStatus(), baseInput(enabledCfg()))
	if d.Action != mergetrack.ActionNone {
		t.Errorf("action = %q, want none", d.Action)
	}
	if !strings.Contains(d.PrimaryDetail(), "merge_tracking.merge") {
		t.Errorf("detail %q should name the setting that is off", d.PrimaryDetail())
	}
}

func TestPhaseFor_MapsEveryMutatingAction(t *testing.T) {
	cases := map[mergetrack.Action]string{
		mergetrack.ActionUpdateBranchRemote: store.MergePhaseUpdating,
		mergetrack.ActionUpdateBranchLocal:  store.MergePhaseUpdating,
		mergetrack.ActionResolveConflicts:   store.MergePhaseResolving,
		mergetrack.ActionMerge:              store.MergePhaseMerging,
		mergetrack.ActionArmAutoMerge:       store.MergePhaseAutoMergeArmed,
	}
	for action, want := range cases {
		if got := mergetrack.PhaseFor(action); got != want {
			t.Errorf("PhaseFor(%q) = %q, want %q", action, got, want)
		}
		if !action.Mutating() {
			t.Errorf("%q should be reported as mutating", action)
		}
	}
	for _, a := range []mergetrack.Action{mergetrack.ActionNone, mergetrack.ActionWait, mergetrack.ActionMarkMerged} {
		if a.Mutating() {
			t.Errorf("%q must not be reported as mutating", a)
		}
	}
}

// An operator asking "why is this stuck?" must get the PR's real blocker, not
// the poller's own attempt pacing. Reporting `cooldown` there answers a
// question nobody asked.
func TestDecide_IgnoreCooldownReportsTheRealBlocker(t *testing.T) {
	st := cleanStatus()
	st.MergeStateStatus = gh.MergeStateBehind

	in := baseInput(allOn())
	in.State.CooldownUntil = in.Now.Add(5 * time.Minute)
	in.IgnoreCooldown = true

	d := decide(st, in)
	if d.PrimaryReason() != mergetrack.ReasonBehindBase {
		t.Errorf("reason = %q, want behind_base rather than the cooldown", d.PrimaryReason())
	}
	if d.Action != mergetrack.ActionUpdateBranchRemote {
		t.Errorf("action = %q — an explicit evaluation should still choose the action", d.Action)
	}
}

// Without the flag the cooldown still applies: it is what paces the poller.
func TestDecide_CooldownAppliesToTheScheduledPass(t *testing.T) {
	st := cleanStatus()
	st.MergeStateStatus = gh.MergeStateBehind

	in := baseInput(allOn())
	in.State.CooldownUntil = in.Now.Add(5 * time.Minute)

	if got := decide(st, in).PrimaryReason(); got != mergetrack.ReasonCooldown {
		t.Errorf("reason = %q, want cooldown on a scheduled pass", got)
	}
}

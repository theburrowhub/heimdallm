package mergetrack_test

// Regressions from the review of PR #738 (theburrowhub/heimdallm). Each test
// here failed before the fix it names, and each protects a path that produced
// wrong behaviour on a real, ordinary PR rather than a corner case.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/mergetrack"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/heimdallm/daemon/internal/store"
)

// errNetwork stands in for any transient GitHub failure.
var errNetwork = errors.New("connection reset by peer")

// GitHub refuses enablePullRequestAutoMerge on a PR it would merge right now
// ("Pull request is in clean status"). That refusal used to fall through to the
// generic error path: no attempt counter, a flat one-minute cooldown, and a
// decision that picked arming again on the very next cycle. Any PR that was
// already green when first considered — daemon start, a repo just enabled, an
// approval landing after CI — looped arm → fail → cooldown forever and never
// merged, even with merge = true.
func TestReconcilePR_CleanStatusRefusalMergesInsteadOfLooping(t *testing.T) {
	cfg := enabledCfg()
	cfg.EnableAutoMerge = true
	cfg.Merge = true

	gw := &fakeGateway{
		statuses: []*gh.MergeStatus{cleanStatus()},
		autoErr: &gh.AutoMergeUnavailableError{
			Reason: gh.AutoMergeReasonCleanStatus,
			Body:   "Pull request is in clean status",
		},
	}
	h := newHarness(t, cfg, gw)

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if gw.mergeCalls != 1 {
		t.Fatalf("merge calls = %d, want 1: a refusal that means 'already mergeable' is an instruction to merge", gw.mergeCalls)
	}
	row := h.row(t)
	if row.Phase != store.MergePhaseMerged {
		t.Errorf("phase = %q, want merged", row.Phase)
	}
	if row.LastError != "" {
		t.Errorf("last_error = %q, want empty", row.LastError)
	}
}

// Same refusal with merge = false: there is nothing left to automate, so the
// row must park with an explanation rather than retrying every minute.
func TestReconcilePR_CleanStatusRefusalWithoutMergeParksWithAReason(t *testing.T) {
	cfg := enabledCfg()
	cfg.EnableAutoMerge = true

	gw := &fakeGateway{
		statuses: []*gh.MergeStatus{cleanStatus()},
		autoErr: &gh.AutoMergeUnavailableError{
			Reason: gh.AutoMergeReasonCleanStatus,
			Body:   "Pull request is in clean status",
		},
	}
	h := newHarness(t, cfg, gw)

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if gw.mergeCalls != 0 {
		t.Fatalf("merge calls = %d, want 0: merge_tracking.merge is off", gw.mergeCalls)
	}
	row := h.row(t)
	if row.BlockReason == "" {
		t.Error("the operator must be told why nothing happened")
	}
	if !row.CooldownUntil.After(h.now.Add(30 * time.Minute)) {
		t.Errorf("cooldown = %v, want a long park: nothing about this changes in a minute", row.CooldownUntil)
	}
}

// An arming failure with no counter of its own used to back off by a flat
// minute forever. It now grows like every other attempt.
func TestReconcilePR_ArmingFailuresBackOffProgressively(t *testing.T) {
	cfg := enabledCfg()
	cfg.EnableAutoMerge = true

	st := cleanStatus()
	st.MergeStateStatus = gh.MergeStateBlocked
	st.ReviewDecision = gh.ReviewDecisionReviewRequired
	st.Reviews = nil

	gw := &fakeGateway{statuses: []*gh.MergeStatus{st}, autoErr: errNetwork}
	h := newHarness(t, cfg, gw)

	var cooldowns []time.Duration
	for i := 0; i < 3; i++ {
		if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err == nil {
			t.Fatal("an arming failure must be reported")
		}
		row := h.row(t)
		cooldowns = append(cooldowns, row.CooldownUntil.Sub(h.now))
		if err := h.st.ClearMergeTrackingCooldown(h.prID); err != nil {
			t.Fatalf("clear cooldown: %v", err)
		}
	}
	if !(cooldowns[0] < cooldowns[1] && cooldowns[1] < cooldowns[2]) {
		t.Errorf("cooldowns = %v, want a growing backoff", cooldowns)
	}
	if h.row(t).ArmAttempts != 3 {
		t.Errorf("arm attempts = %d, want 3", h.row(t).ArmAttempts)
	}
}

// The maxUnknownWaits cap was dead: nothing incremented the counter, so a PR
// GitHub never finishes computing was re-queried every 45 seconds forever on a
// GraphQL budget shared with the review pipeline.
func TestReconcilePR_UnknownMergeabilityIsCountedAndCapped(t *testing.T) {
	st := cleanStatus()
	st.Mergeable = gh.MergeableUnknown
	st.MergeStateStatus = gh.MergeStateUnknown

	gw := &fakeGateway{statuses: []*gh.MergeStatus{st}}
	h := newHarness(t, enabledCfg(), gw)

	for i := 1; i <= 5; i++ {
		if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
		if got := h.row(t).UnknownWaits; got != i {
			t.Fatalf("after %d cycles unknown_waits = %d, want %d", i, got, i)
		}
	}

	// The sixth read is past the cap: stop polling every 45 seconds.
	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	row := h.row(t)
	if !row.CooldownUntil.After(h.now.Add(30 * time.Minute)) {
		t.Errorf("cooldown = %v, want the capped long wait", row.CooldownUntil.Sub(h.now))
	}
}

// A base branch that keeps moving makes GitHub recompute, so the counter has to
// clear as soon as a real answer arrives — otherwise a long-lived PR reaches
// the cap on a head GitHub is perfectly willing to compute.
func TestReconcilePR_UnknownWaitCounterClearsOnAKnownState(t *testing.T) {
	unknown := cleanStatus()
	unknown.Mergeable = gh.MergeableUnknown
	unknown.MergeStateStatus = gh.MergeStateUnknown

	gw := &fakeGateway{statuses: []*gh.MergeStatus{unknown, cleanStatus()}}
	h := newHarness(t, enabledCfg(), gw)

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if h.row(t).UnknownWaits != 1 {
		t.Fatalf("unknown_waits = %d, want 1", h.row(t).UnknownWaits)
	}
	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := h.row(t).UnknownWaits; got != 0 {
		t.Errorf("unknown_waits = %d, want 0 once GitHub answered", got)
	}
}

// syncRow re-anchored an auto-merge GitHub reports for another commit using
// GitHub's own enabledAt, typically well in the past. The freshly read row then
// satisfied "armed before this pass started" in the same pass, so the wait-a-
// pass rule — the one that gives GitHub its turn before Heimdallm merges
// directly — never applied on that path.
func TestReconcilePR_ReanchoredAutoMergeStillWaitsAPass(t *testing.T) {
	cfg := enabledCfg()
	cfg.EnableAutoMerge = true
	cfg.Merge = true

	st := cleanStatus()
	st.AutoMerge = &gh.AutoMergeRequest{
		MergeMethod: "SQUASH",
		EnabledAt:   time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC), // long before this tick
	}

	gw := &fakeGateway{statuses: []*gh.MergeStatus{st}}
	h := newHarness(t, cfg, gw)

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if gw.mergeCalls != 0 {
		t.Fatalf("merge calls = %d, want 0: GitHub has not had a pass at this commit yet", gw.mergeCalls)
	}
	row := h.row(t)
	if !row.AutoMergeArmedAt.Equal(h.now) {
		t.Errorf("armed at = %v, want the current tick %v", row.AutoMergeArmedAt, h.now)
	}

	// Next pass, past the cooldown the evaluation set: GitHub had its turn and
	// did not merge, so Heimdallm does.
	h.now = h.now.Add(time.Hour)
	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if gw.mergeCalls != 1 {
		t.Errorf("merge calls = %d, want 1 on the following pass", gw.mergeCalls)
	}
}

// recordFailure and every decision write happen around the claim, so a failed
// automation must still be announced. The Flutter listing refreshes on this
// event and on nothing else after a failure.
func TestReconcilePR_FailedAutomationEmitsAnErrorEvent(t *testing.T) {
	cfg := enabledCfg()
	cfg.UpdateBranch = true

	st := cleanStatus()
	st.MergeStateStatus = gh.MergeStateBehind

	gw := &fakeGateway{statuses: []*gh.MergeStatus{st}, updateErr: errNetwork}
	h := newHarness(t, cfg, gw)

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err == nil {
		t.Fatal("the update failure must be reported")
	}
	if !h.pub.has(sse.EventMergeTrackError) {
		t.Error("a failed automation must emit merge_track_error: it is the only event the UI gets")
	}
}

// Second round of review findings on PR #738.

// include_assigned is overridable per org and repo, but the search that finds
// candidate PRs uses the global value. Enrolling one the repo does not want
// costs a GraphQL query every tick to reach an Evaluate that abandons it again
// — and because discovery revives an abandoned row, the phase flapped
// idle↔abandoned forever on a budget shared with the review pipeline.
func TestTick_DoesNotEnrolAnAssignedPRTheRepoExcludes(t *testing.T) {
	global := enabledCfg()
	global.IncludeAssigned = true
	perRepo := enabledCfg()
	perRepo.IncludeAssigned = false

	gw := &fakeGateway{
		prs: []*gh.TrackedPR{{
			PullRequest: &gh.PullRequest{
				ID: 222, Number: 9, Title: "Someone else's", State: "open",
				HTMLURL: "https://github.com/acme/widgets/pull/9",
				Repo:    "acme/widgets",
			},
			IsAssignee: true,
		}},
	}
	h := newHarnessWithConfigs(t, global, perRepo, gw)

	for i := 0; i < 3; i++ {
		if stats := h.r.Tick(context.Background(), []string{"acme/widgets"}); stats.Discovered != 0 {
			t.Fatalf("discovered = %d, want 0", stats.Discovered)
		}
	}

	// The harness tracks its own PR, so the check is that number 9 never got a
	// row: with one, every tick would spend a GetMergeStatus reaching an
	// Evaluate that abandons it, and the revive would bring it back next tick.
	rows, err := h.st.ListMergeTracking()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, row := range rows {
		if row.Number == 9 {
			t.Fatalf("PR 9 was enrolled despite the repo excluding assignees: %+v", row)
		}
	}
}

// The author's own PRs are tracked whatever include_assigned says.
func TestTick_StillEnrolsOwnPRsWhenAssigneesAreExcluded(t *testing.T) {
	global := enabledCfg()
	global.IncludeAssigned = true
	perRepo := enabledCfg()
	perRepo.IncludeAssigned = false

	gw := &fakeGateway{
		prs: []*gh.TrackedPR{{
			PullRequest: &gh.PullRequest{
				ID: 222, Number: 9, Title: "Mine", State: "open",
				HTMLURL: "https://github.com/acme/widgets/pull/9",
				Repo:    "acme/widgets",
			},
			IsAuthor: true,
		}},
		statuses: []*gh.MergeStatus{cleanStatus()},
	}
	h := newHarnessWithConfigs(t, global, perRepo, gw)

	if stats := h.r.Tick(context.Background(), []string{"acme/widgets"}); stats.Discovered != 1 {
		t.Errorf("discovered = %d, want 1", stats.Discovered)
	}
}

// The clean-status fallback ran a direct merge while the decision still said
// "arm": its failures were counted under arm_attempts, so max_merge_attempts —
// checked before the arming branch — was never reached and the PR retried
// arm → clean → merge at the backoff cap forever.
func TestReconcilePR_CleanStatusMergeFailureCountsAsAMerge(t *testing.T) {
	cfg := enabledCfg()
	cfg.EnableAutoMerge = true
	cfg.Merge = true

	gw := &fakeGateway{
		statuses: []*gh.MergeStatus{cleanStatus()},
		autoErr: &gh.AutoMergeUnavailableError{
			Reason: gh.AutoMergeReasonCleanStatus,
			Body:   "Pull request is in clean status",
		},
		mergeErr: errNetwork,
	}
	h := newHarness(t, cfg, gw)

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err == nil {
		t.Fatal("the merge failure must be reported")
	}
	row := h.row(t)
	if row.MergeAttempts != 1 {
		t.Errorf("merge attempts = %d, want 1: the action that ran was a merge", row.MergeAttempts)
	}
	if row.ArmAttempts != 0 {
		t.Errorf("arm attempts = %d, want 0: arming is not what failed", row.ArmAttempts)
	}
}

// The row is claimed as auto_merge_armed when the fallback fires, and that is
// not an in-flight phase — a direct merge run from there had none of the
// single-flight protection ActionMerge gets, so a second daemon could claim the
// same row and merge concurrently.
func TestReconcilePR_CleanStatusMergeTakesTheInFlightClaim(t *testing.T) {
	cfg := enabledCfg()
	cfg.EnableAutoMerge = true
	cfg.Merge = true

	gw := &fakeGateway{
		statuses: []*gh.MergeStatus{cleanStatus()},
		autoErr: &gh.AutoMergeUnavailableError{
			Reason: gh.AutoMergeReasonCleanStatus,
			Body:   "Pull request is in clean status",
		},
	}
	h := newHarness(t, cfg, gw)

	// A second daemon reaches the row while the first is still waiting for the
	// auto-merge mutation to answer. The arming phase is not an in-flight lock,
	// so the competitor can take the merging claim. Once GitHub answers "clean",
	// the first daemon must re-claim and stand down rather than merging too.
	var competitorClaimed bool
	gw.onEnableAutoMerge = func() {
		var err error
		competitorClaimed, err = h.st.ClaimMergeTrackingAction(
			h.prID, headSHA, store.MergePhaseMerging, h.now)
		if err != nil {
			t.Fatalf("competitor claim: %v", err)
		}
	}

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !competitorClaimed {
		t.Fatal("precondition: the competing daemon must take the in-flight claim")
	}
	if h.gw.mergeCalls != 0 {
		t.Errorf("merge calls = %d, want 0: the competing claim owns the merge", h.gw.mergeCalls)
	}
	if got := h.row(t).Phase; got != store.MergePhaseMerging {
		t.Errorf("phase = %q, want merging: the competing claim must remain intact", got)
	}
}

// The documented precedence is repo > org > global, but the discovery search is
// global: asking only the global include_assigned made the widening direction a
// silent no-op, so `include_assigned = false` globally with `true` on one repo
// never returned a single assigned-only PR for it.
func TestTick_SearchesForAssigneesWhenOnlyOneRepoWantsThem(t *testing.T) {
	global := enabledCfg()
	global.IncludeAssigned = false
	perRepo := enabledCfg()
	perRepo.IncludeAssigned = true

	gw := &fakeGateway{
		prs: []*gh.TrackedPR{{
			PullRequest: &gh.PullRequest{
				ID: 222, Number: 9, Title: "Assigned to me", State: "open",
				HTMLURL: "https://github.com/acme/widgets/pull/9",
				Repo:    "acme/widgets",
			},
			IsAssignee: true,
		}},
		statuses: []*gh.MergeStatus{cleanStatus()},
	}
	h := newHarnessWithConfigs(t, global, perRepo, gw)

	if stats := h.r.Tick(context.Background(), []string{"acme/widgets"}); stats.Discovered != 1 {
		t.Fatalf("discovered = %d, want the repo's override to widen the search", stats.Discovered)
	}
	if !gw.lastIncludeAssigned {
		t.Error("the search must carry the assignee qualifier when any repo wants it")
	}
}

// And with nobody asking, the qualifier stays off: it doubles the search cost.
func TestTick_OmitsTheAssigneeQualifierWhenNobodyWantsIt(t *testing.T) {
	cfg := enabledCfg()
	cfg.IncludeAssigned = false

	gw := &fakeGateway{prs: nil}
	h := newHarnessWithConfigs(t, cfg, cfg, gw)
	h.r.Tick(context.Background(), []string{"acme/widgets"})

	if gw.lastIncludeAssigned {
		t.Error("no repo wants assignees; the qualifier must be omitted")
	}
}

// The Merge tab needs its own way in. POST /prs/add routes through the review
// pipeline, which refuses PRs the authenticated account authored — and since
// Heimdallm authenticates as the operator, that is every PR they open. The
// feature that exists for exactly those PRs had no door.
func TestEnrolExistingPR_CreatesADueRowWithoutAReview(t *testing.T) {
	h := newHarness(t, enabledCfg(), &fakeGateway{})

	prID, err := h.st.UpsertPR(&store.PR{
		GithubID: 999, Repo: "acme/widgets", Number: 12, State: "open",
		URL: "https://github.com/acme/widgets/pull/12", UpdatedAt: h.now, FetchedAt: h.now,
	})
	if err != nil {
		t.Fatalf("upsert pr: %v", err)
	}

	if err := h.r.EnrolExistingPR(prID, "acme/widgets", 12); err != nil {
		t.Fatalf("enrol: %v", err)
	}

	row, err := h.st.GetMergeTracking(prID)
	if err != nil {
		t.Fatalf("get row: %v", err)
	}
	if row.Phase != store.MergePhaseIdle {
		t.Errorf("phase = %q, want idle", row.Phase)
	}
	if !row.CooldownUntil.IsZero() {
		t.Errorf("cooldown = %v, want none: the operator expects it evaluated now", row.CooldownUntil)
	}
	due, err := h.st.ListMergeTrackingDue(h.now, 10)
	if err != nil {
		t.Fatalf("list due: %v", err)
	}
	var found bool
	for _, d := range due {
		if d.PRID == prID {
			found = true
		}
	}
	if !found {
		t.Error("the freshly added PR must be due on the next cycle")
	}
	if !h.pub.has(sse.EventMergeTrackDetected) {
		t.Error("the Merge tab refreshes on this event")
	}
}

// A repo with merge tracking off must say so rather than silently creating a
// row nothing will ever evaluate.
func TestEnrolExistingPR_RefusesWhenTrackingIsOffForTheRepo(t *testing.T) {
	cfg := enabledCfg()
	cfg.Enabled = false
	h := newHarness(t, cfg, &fakeGateway{})

	err := h.r.EnrolExistingPR(h.prID, "acme/widgets", 7)
	if err == nil {
		t.Fatal("enrolling into a disabled repo must be refused")
	}
	if !errors.Is(err, mergetrack.ErrTrackingDisabled) {
		t.Errorf("error = %v, want ErrTrackingDisabled", err)
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Errorf("error = %q, want it to name the reason", err)
	}
}

// ConfigForRepo is optional for small embedders and tests. In that shape the
// global switch still governs manual enrolment; nil must not bypass it.
func TestEnrolExistingPR_FallsBackToTheGlobalEnabledSwitch(t *testing.T) {
	h := newHarness(t, enabledCfg(), &fakeGateway{})
	disabled := enabledCfg()
	disabled.Enabled = false
	h.r = mergetrack.NewReconciler(mergetrack.ReconcilerOptions{
		Store:        h.st,
		Publisher:    h.pub,
		GlobalConfig: func() config.MergeTrackingConfig { return disabled },
	})

	err := h.r.EnrolExistingPR(h.prID, "acme/widgets", 7)
	if !errors.Is(err, mergetrack.ErrTrackingDisabled) {
		t.Fatalf("error = %v, want ErrTrackingDisabled", err)
	}
}

// GitHub collapses its whole verdict into one mergeStateStatus and ranks
// BLOCKED above BEHIND, so a PR that is both out of date and waiting on a
// review or a check never reports BEHIND. Verified against two open PRs in this
// repository: hundreds of commits behind, both reporting DIRTY. Relying on that
// field alone meant Heimdallm sat waiting for checks that were running against
// a stale base, and never updated the branch.
func TestEvaluate_OutOfDateWinsOverEveryGatingSignal(t *testing.T) {
	st := cleanStatus()
	// Behind, and blocked on a review — the shape GitHub reports as BLOCKED.
	st.BaseOID = "base-old"
	st.BaseTipOID = "base-new"
	st.MergeStateStatus = gh.MergeStateBlocked
	st.ReviewDecision = gh.ReviewDecisionReviewRequired
	st.Reviews = nil
	st.Checks = []gh.CheckContext{
		{Name: "build", Kind: "check_run", State: gh.CheckStateFailure, Required: true},
	}

	cfg := enabledCfg()
	cfg.UpdateBranch = true
	d := mergetrack.Evaluate(st, baseInput(cfg))
	if got := d.PrimaryReason(); got != mergetrack.ReasonBehindBase {
		t.Fatalf("primary reason = %q, want behind_base: updating comes first", got)
	}
}

// A moved base is not itself a merge requirement on a non-strict repository.
// With branch updates disabled (the default), it must remain evidence rather
// than suppressing whichever merge automation the operator explicitly chose;
// enabling branch updates still gives that action precedence.
func TestDecide_OutOfDateUsesConfiguredAutomation(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*config.MergeTrackingConfig)
		want      mergetrack.Action
	}{
		{
			name: "enable auto merge",
			configure: func(cfg *config.MergeTrackingConfig) {
				cfg.EnableAutoMerge = true
			},
			want: mergetrack.ActionArmAutoMerge,
		},
		{
			name: "merge directly",
			configure: func(cfg *config.MergeTrackingConfig) {
				cfg.Merge = true
			},
			want: mergetrack.ActionMerge,
		},
		{
			name: "update when enabled",
			configure: func(cfg *config.MergeTrackingConfig) {
				cfg.EnableAutoMerge = true
				cfg.Merge = true
				cfg.UpdateBranch = true
			},
			want: mergetrack.ActionUpdateBranchRemote,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := cleanStatus()
			st.BaseOID = "base-old"
			st.BaseTipOID = "base-new"

			cfg := enabledCfg()
			tt.configure(&cfg)
			in := baseInput(cfg)
			d := mergetrack.Decide(mergetrack.Evaluate(st, in), st, in)

			if d.Action != tt.want {
				t.Errorf("action = %q, want %q", d.Action, tt.want)
			}
			if !d.Evidence.BehindBase {
				t.Error("the advanced base should remain visible in the decision evidence")
			}
		})
	}
}

// And with update_branch on, the decision is to update rather than to report
// the review or the check.
func TestDecide_OutOfDatePRIsUpdatedBeforeAnythingElse(t *testing.T) {
	st := cleanStatus()
	st.BaseOID = "base-old"
	st.BaseTipOID = "base-new"
	st.MergeStateStatus = gh.MergeStateBlocked
	st.ReviewDecision = gh.ReviewDecisionReviewRequired
	st.Reviews = nil

	cfg := enabledCfg()
	cfg.UpdateBranch = true
	in := baseInput(cfg)

	d := mergetrack.Decide(mergetrack.Evaluate(st, in), st, in)
	if d.Action != mergetrack.ActionUpdateBranchRemote {
		t.Errorf("action = %q, want update_branch_remote", d.Action)
	}
}

// A PR level with its base is not behind, whatever else is wrong with it.
func TestEvaluate_UpToDatePRIsNotReportedBehind(t *testing.T) {
	st := cleanStatus()
	st.BaseOID = "same"
	st.BaseTipOID = "same"
	st.MergeStateStatus = gh.MergeStateBlocked
	st.ReviewDecision = gh.ReviewDecisionReviewRequired
	st.Reviews = nil

	d := mergetrack.Evaluate(st, baseInput(enabledCfg()))
	if got := d.PrimaryReason(); got == mergetrack.ReasonBehindBase {
		t.Error("a PR level with its base must not be reported as behind")
	}
}

// Missing either side is not evidence of staleness, and acting on a guess would
// force-push a branch nobody asked to move.
func TestBehindBase_UnknownWhenEitherSideIsMissing(t *testing.T) {
	for name, st := range map[string]*gh.MergeStatus{
		"no base oid": {BaseTipOID: "tip"},
		"no tip":      {BaseOID: "base"},
		"neither":     {},
		"nil":         nil,
	} {
		if st.BehindBase() {
			t.Errorf("%s: must not report behind without both sides", name)
		}
	}
}

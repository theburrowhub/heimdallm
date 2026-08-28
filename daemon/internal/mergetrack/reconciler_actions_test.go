package mergetrack_test

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

// fakeWorktreeRunner records what the reconciler asked the worktree layer to do.
type fakeWorktreeRunner struct {
	resolveResult mergetrack.ConflictResult
	resolveErr    error
	rebaseSHA     string
	rebaseErr     error

	resolveCalls int
	rebaseCalls  int
	lastReq      mergetrack.ConflictRequest
}

func (f *fakeWorktreeRunner) ResolveConflicts(_ context.Context, req mergetrack.ConflictRequest) (mergetrack.ConflictResult, error) {
	f.resolveCalls++
	f.lastReq = req
	return f.resolveResult, f.resolveErr
}

func (f *fakeWorktreeRunner) RebaseAndForcePush(_ context.Context, req mergetrack.ConflictRequest) (string, error) {
	f.rebaseCalls++
	f.lastReq = req
	return f.rebaseSHA, f.rebaseErr
}

// withWorktree rebuilds the harness reconciler with a worktree runner attached.
func (h *harness) withWorktree(cfg config.MergeTrackingConfig, wt mergetrack.WorktreeRunner) {
	h.r = mergetrack.NewReconciler(mergetrack.ReconcilerOptions{
		Gateway:       h.gw,
		Store:         h.st,
		Publisher:     h.pub,
		Worktree:      wt,
		ConfigForRepo: func(string) config.MergeTrackingConfig { return cfg },
		GlobalConfig:  func() config.MergeTrackingConfig { return cfg },
		Viewer:        func() string { return viewer },
		Now:           func() time.Time { return h.now },
	})
}

func TestReconcilePR_ConflictsRunTheAgentAndPostTheAuditComment(t *testing.T) {
	st := cleanStatus()
	st.Mergeable = gh.MergeableNo
	st.MergeStateStatus = gh.MergeStateDirty

	cfg := enabledCfg()
	cfg.ResolveConflicts = true
	gw := &fakeGateway{statuses: []*gh.MergeStatus{st}}
	h := newHarness(t, cfg, gw)

	wt := &fakeWorktreeRunner{resolveResult: mergetrack.ConflictResult{
		Pushed: true, PreRebaseSHA: headSHA, NewHeadSHA: "newhead",
		Files: []string{"a.go"}, CommentBody: "## Heimdallm resolved merge conflicts",
	}}
	h.withWorktree(cfg, wt)

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if wt.resolveCalls != 1 {
		t.Fatalf("resolve calls = %d, want 1", wt.resolveCalls)
	}
	// The request must carry the SHA the decision was made against, or the
	// resolver cannot detect that the branch moved.
	if wt.lastReq.ExpectedRemoteHeadSHA != headSHA {
		t.Errorf("expected head sha = %q, want %q", wt.lastReq.ExpectedRemoteHeadSHA, headSHA)
	}
	if wt.lastReq.HeadRef != "feature" || wt.lastReq.BaseRef != "main" {
		t.Errorf("refs = %q/%q", wt.lastReq.HeadRef, wt.lastReq.BaseRef)
	}
	if gw.commentCalls != 1 {
		t.Errorf("comment calls = %d, want 1 — the audit comment is the recovery path", gw.commentCalls)
	}
	if !h.pub.has(sse.EventMergeTrackConflictResolved) {
		t.Error("a conflict-resolved event should be emitted")
	}
	// The pre-rebase SHA has to survive: it is what a human resets to.
	if got := h.row(t).PreRebaseSHA; got != headSHA {
		t.Errorf("pre-rebase sha = %q, want %q", got, headSHA)
	}
}

// The agent declining is not a failure of the daemon, but the PR must still be
// told, and the attempt must count against the cap.
func TestReconcilePR_ConflictGiveUpStillCommentsAndCountsTheAttempt(t *testing.T) {
	st := cleanStatus()
	st.Mergeable = gh.MergeableNo

	cfg := enabledCfg()
	cfg.ResolveConflicts = true
	gw := &fakeGateway{statuses: []*gh.MergeStatus{st}}
	h := newHarness(t, cfg, gw)
	h.withWorktree(cfg, &fakeWorktreeRunner{
		resolveResult: mergetrack.ConflictResult{CommentBody: "## could not resolve"},
		resolveErr:    mergetrack.ErrConflictUnresolved,
	})

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err == nil {
		t.Fatal("an unresolved conflict should surface as an error to the caller")
	}
	if gw.commentCalls != 1 {
		t.Errorf("comment calls = %d, want 1", gw.commentCalls)
	}
	row := h.row(t)
	if row.ConflictAttempts != 1 {
		t.Errorf("conflict attempts = %d, want 1", row.ConflictAttempts)
	}
	if row.CooldownUntil.IsZero() {
		t.Error("a failed attempt must set a cooldown so the next cycle does not retry immediately")
	}
	if !h.pub.has(sse.EventMergeTrackError) {
		t.Error("an error event should be emitted")
	}
}

func TestReconcilePR_BehindBaseFallsBackToALocalRebase(t *testing.T) {
	st := cleanStatus()
	st.MergeStateStatus = gh.MergeStateBehind

	cfg := enabledCfg()
	cfg.UpdateBranch = true
	gw := &fakeGateway{
		statuses: []*gh.MergeStatus{st},
		// 422 is GitHub refusing a merge commit, typically because the base
		// requires linear history. That is the local-rebase signal.
		updateErr: &gh.UpdateBranchRejectedError{
			StatusCode: 422,
			Reason:     gh.UpdateBranchReasonUnprocessable,
			Body:       "merge conflict between base and head",
		},
	}
	h := newHarness(t, cfg, gw)
	wt := &fakeWorktreeRunner{rebaseSHA: "rebased"}
	h.withWorktree(cfg, wt)

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if gw.updateCalls != 1 {
		t.Errorf("update calls = %d, want 1 — GitHub is tried first", gw.updateCalls)
	}
	if wt.rebaseCalls != 1 {
		t.Errorf("rebase calls = %d, want 1 after the 422", wt.rebaseCalls)
	}
	if !h.pub.has(sse.EventMergeTrackBranchUpdated) {
		t.Error("a branch-updated event should be emitted")
	}
}

// A rejection that is not the linear-history case must not silently rewrite the
// branch locally.
func TestReconcilePR_UpdateBranchSHAMismatchDoesNotRebaseLocally(t *testing.T) {
	st := cleanStatus()
	st.MergeStateStatus = gh.MergeStateBehind

	cfg := enabledCfg()
	cfg.UpdateBranch = true
	gw := &fakeGateway{
		statuses: []*gh.MergeStatus{st},
		updateErr: &gh.UpdateBranchRejectedError{
			StatusCode: 409, Reason: gh.UpdateBranchReasonSHAMismatch,
		},
	}
	h := newHarness(t, cfg, gw)
	wt := &fakeWorktreeRunner{}
	h.withWorktree(cfg, wt)

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err == nil {
		t.Fatal("expected the rejection to surface")
	}
	if wt.rebaseCalls != 0 {
		t.Fatal("a stale-SHA rejection must not fall through to a local rebase")
	}
	if got := h.row(t).UpdateAttempts; got != 1 {
		t.Errorf("update attempts = %d, want 1", got)
	}
}

func TestReconcilePR_LocalRebaseRecordsThePreRebaseSHA(t *testing.T) {
	st := cleanStatus()
	st.MergeStateStatus = gh.MergeStateBehind

	cfg := enabledCfg()
	cfg.UpdateBranch = true
	gw := &fakeGateway{
		statuses:  []*gh.MergeStatus{st},
		updateErr: &gh.UpdateBranchRejectedError{StatusCode: 422, Reason: gh.UpdateBranchReasonUnprocessable},
	}
	h := newHarness(t, cfg, gw)
	h.withWorktree(cfg, &fakeWorktreeRunner{rebaseSHA: "rebased"})

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := h.row(t).PreRebaseSHA; got != headSHA {
		t.Errorf("pre-rebase sha = %q, want %q — a force-push has to be recoverable", got, headSHA)
	}
}

// An action that needs a worktree with none wired must fail loudly rather than
// silently doing nothing.
func TestReconcilePR_MissingWorktreeRunnerIsReported(t *testing.T) {
	st := cleanStatus()
	st.Mergeable = gh.MergeableNo

	cfg := enabledCfg()
	cfg.ResolveConflicts = true
	h := newHarness(t, cfg, &fakeGateway{statuses: []*gh.MergeStatus{st}})

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err == nil {
		t.Fatal("expected an error when no worktree runner is wired")
	}
}

// GitHub refusing auto-merge for the repository will refuse it every cycle.
// Retrying forever would burn budget for nothing.
func TestReconcilePR_AutoMergeNotAllowedParksWithALongCooldown(t *testing.T) {
	cfg := allOn()
	cfg.Merge = false
	gw := &fakeGateway{
		statuses: []*gh.MergeStatus{cleanStatus()},
		autoErr: &gh.AutoMergeUnavailableError{
			Reason: gh.AutoMergeReasonNotAllowedForRepo,
			Body:   "Auto merge is not allowed for this repository",
		},
	}
	h := newHarness(t, cfg, gw)

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("a known-unavailable auto-merge is a block, not an error: %v", err)
	}
	row := h.row(t)
	if row.Phase != store.MergePhaseBlocked {
		t.Errorf("phase = %q, want blocked", row.Phase)
	}
	if row.CooldownUntil.Sub(h.now) < 30*time.Minute {
		t.Errorf("cooldown until %v — want a long one, this cannot change on its own", row.CooldownUntil)
	}
	if !strings.Contains(row.LastError, "not allowed") {
		t.Errorf("last error = %q, want GitHub's reason recorded", row.LastError)
	}
}

// GitHub reporting the PR already has auto-merge on is the state we wanted.
func TestReconcilePR_AutoMergeAlreadyEnabledIsRecordedAsArmed(t *testing.T) {
	cfg := allOn()
	gw := &fakeGateway{
		statuses: []*gh.MergeStatus{cleanStatus()},
		autoErr: &gh.AutoMergeUnavailableError{
			Reason: gh.AutoMergeReasonAlreadyEnabled,
			Body:   "Auto merge is already enabled",
		},
	}
	h := newHarness(t, cfg, gw)

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	row := h.row(t)
	if !row.AutoMergeArmedFor(headSHA) {
		t.Errorf("row should record the armed state: %+v", row)
	}
}

// A transport failure on the mutation is a real error and must be retried with
// a backoff, not swallowed.
func TestReconcilePR_AutoMergeTransportFailureBacksOff(t *testing.T) {
	cfg := allOn()
	gw := &fakeGateway{
		statuses: []*gh.MergeStatus{cleanStatus()},
		autoErr:  errors.New("connection reset"),
	}
	h := newHarness(t, cfg, gw)

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err == nil {
		t.Fatal("a transport failure must surface")
	}
	if h.row(t).CooldownUntil.IsZero() {
		t.Error("a failed action must set a cooldown")
	}
}

// GitHub already has auto-merge on for a commit our row does not cover. The row
// is re-anchored so the next pass can act, and nothing is done this pass.
func TestReconcilePR_ReAnchorsAnAutoMergeArmedElsewhere(t *testing.T) {
	st := cleanStatus()
	armedAt := time.Date(2026, 8, 28, 11, 0, 0, 0, time.UTC)
	st.AutoMerge = &gh.AutoMergeRequest{MergeMethod: "SQUASH", EnabledAt: armedAt}

	// merge deliberately off, so the pass stops after the re-anchor rather than
	// promoting straight to a direct merge.
	cfg := allOn()
	cfg.Merge = false
	gw := &fakeGateway{statuses: []*gh.MergeStatus{st}}
	h := newHarness(t, cfg, gw)

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if gw.enableCalls != 0 {
		t.Error("must not re-arm what GitHub already has armed")
	}
	row := h.row(t)
	if !row.AutoMergeArmedFor(headSHA) {
		t.Errorf("the row should have been re-anchored to the current head: %+v", row)
	}
	// The anchor is now, not GitHub's enabledAt. The licence to merge directly
	// is granted per commit, and GitHub has not yet had a pass at this one;
	// using its (typically much older) enabledAt let the very same cycle
	// promote to a direct merge, defeating the wait-a-pass rule. The re-anchor
	// only fires while the row points at another commit, so this does not
	// restart the clock every cycle.
	if !row.AutoMergeArmedAt.Equal(h.now) {
		t.Errorf("armed at = %v, want the current tick %v (GitHub reported %v)",
			row.AutoMergeArmedAt, h.now, armedAt)
	}
}

// GitHub says auto-merge is off; our row said armed. GitHub wins.
func TestReconcilePR_ClearsAnAutoMergeGitHubNoLongerHas(t *testing.T) {
	cfg := enabledCfg() // no automations, so nothing re-arms it
	h := newHarness(t, cfg, &fakeGateway{statuses: []*gh.MergeStatus{cleanStatus()}})
	if err := h.st.ArmNativeAutoMerge(h.prID, headSHA, "squash", h.now.Add(-time.Hour)); err != nil {
		t.Fatalf("arm: %v", err)
	}

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	row := h.row(t)
	if row.AutoMergeHeadSHA != "" {
		t.Errorf("stale armed state not cleared: %+v", row)
	}
}

func TestReconcilePR_ClosedPRIsAbandoned(t *testing.T) {
	st := cleanStatus()
	st.State = "CLOSED"
	h := newHarness(t, enabledCfg(), &fakeGateway{statuses: []*gh.MergeStatus{st}})

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := h.row(t).Phase; got != store.MergePhaseAbandoned {
		t.Errorf("phase = %q, want abandoned", got)
	}
}

func TestReconcilePR_MergedPRRecordsGitHubsMergedAt(t *testing.T) {
	st := cleanStatus()
	st.Merged = true
	st.State = "MERGED"
	st.MergedAt = time.Date(2026, 8, 28, 9, 30, 0, 0, time.UTC)
	h := newHarness(t, enabledCfg(), &fakeGateway{statuses: []*gh.MergeStatus{st}})

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	row := h.row(t)
	if row.Phase != store.MergePhaseMerged {
		t.Errorf("phase = %q, want merged", row.Phase)
	}
	if !row.MergedAt.Equal(st.MergedAt) {
		t.Errorf("merged at = %v, want GitHub's %v", row.MergedAt, st.MergedAt)
	}
	if !h.pub.has(sse.EventMergeTrackMerged) {
		t.Error("a merged event should be emitted")
	}
}

// The blocked event is announced only when the reason changes; a PR waiting an
// hour on CI would otherwise produce one activity-log row per cycle.
func TestReconcilePR_BlockedEventOnlyFiresWhenTheReasonChanges(t *testing.T) {
	st := cleanStatus()
	st.Checks = []gh.CheckContext{{Name: "build", State: gh.CheckStatePending, Required: true}}
	h := newHarness(t, enabledCfg(), &fakeGateway{statuses: []*gh.MergeStatus{st}})

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("pass 1: %v", err)
	}
	first := h.pub.count(sse.EventMergeTrackBlocked)
	if first != 1 {
		t.Fatalf("blocked events after pass 1 = %d, want 1", first)
	}

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("pass 2: %v", err)
	}
	if got := h.pub.count(sse.EventMergeTrackBlocked); got != first {
		t.Errorf("blocked events after an unchanged pass = %d, want still %d", got, first)
	}
	// The evaluation event, by contrast, fires every pass — the UI needs it.
	if got := h.pub.count(sse.EventMergeTrackEvaluated); got != 2 {
		t.Errorf("evaluated events = %d, want 2", got)
	}
}

func TestTick_DiscoversEnrolsAndEvaluates(t *testing.T) {
	cfg := enabledCfg()
	gw := &fakeGateway{
		prs: []*gh.TrackedPR{{
			PullRequest: &gh.PullRequest{
				ID: 222, Number: 9, Title: "New one", State: "open",
				HTMLURL: "https://github.com/acme/widgets/pull/9",
				Repo:    "acme/widgets",
			},
			IsAuthor: true,
		}},
		statuses: []*gh.MergeStatus{cleanStatus()},
	}
	h := newHarness(t, cfg, gw)

	stats := h.r.Tick(context.Background(), []string{"acme/widgets"})
	if stats.Discovered != 1 {
		t.Errorf("discovered = %d, want 1", stats.Discovered)
	}
	if stats.Evaluated == 0 {
		t.Error("the enrolled PR should have been evaluated in the same cycle")
	}
	if !h.pub.has(sse.EventMergeTrackDetected) {
		t.Error("a detected event should be emitted for a newly tracked PR")
	}
}

// A PR outside the monitored set must not be enrolled, however the search
// found it.
func TestTick_IgnoresPRsOutsideTheMonitoredRepos(t *testing.T) {
	gw := &fakeGateway{
		prs: []*gh.TrackedPR{{
			PullRequest: &gh.PullRequest{
				ID: 333, Number: 1, State: "open", Repo: "someone/else",
			},
			IsAuthor: true,
		}},
		statuses: []*gh.MergeStatus{cleanStatus()},
	}
	h := newHarness(t, enabledCfg(), gw)

	stats := h.r.Tick(context.Background(), []string{"acme/widgets"})
	if stats.Discovered != 0 {
		t.Errorf("discovered = %d, want 0 for an unmonitored repo", stats.Discovered)
	}
}

// A rate-limit rejection means every further call this cycle is wasted, and
// keeps a secondary block alive.
func TestTick_StopsTheCycleOnRateLimit(t *testing.T) {
	gw := &fakeGateway{statusErr: &gh.RateLimitError{RetryAt: time.Now().Add(time.Hour)}}
	h := newHarness(t, enabledCfg(), gw)

	h.r.Tick(context.Background(), []string{"acme/widgets"})
	if gw.statusCalls > 1 {
		t.Errorf("status calls = %d, want the cycle to stop after the first rate limit", gw.statusCalls)
	}
}

func TestTick_NoRepositoriesIsANoOp(t *testing.T) {
	h := newHarness(t, enabledCfg(), &fakeGateway{failOnAnyCall: true})
	if stats := h.r.Tick(context.Background(), nil); stats.Evaluated != 0 {
		t.Errorf("stats = %+v, want an empty cycle", stats)
	}
}

func TestAnyEnabled(t *testing.T) {
	h := newHarness(t, enabledCfg(), &fakeGateway{})
	if !h.r.AnyEnabled([]string{"acme/widgets"}) {
		t.Error("an enabled repo should report enabled")
	}

	off := newHarness(t, config.MergeTrackingConfig{}, &fakeGateway{})
	if off.r.AnyEnabled([]string{"acme/widgets"}) {
		t.Error("a disabled repo should not report enabled")
	}
	if off.r.AnyEnabled(nil) {
		t.Error("no repos means not enabled")
	}
}

// A gate that is draining must defer the action and leave the row claimable,
// not park it in an in-flight phase nobody owns.
func TestReconcilePR_DrainingGateDefersWithoutParkingTheRow(t *testing.T) {
	cfg := allOn()
	cfg.EnableAutoMerge = false
	gw := &fakeGateway{statuses: []*gh.MergeStatus{cleanStatus()}}
	h := newHarness(t, cfg, gw)
	h.r = mergetrack.NewReconciler(mergetrack.ReconcilerOptions{
		Gateway:       gw,
		Store:         h.st,
		Publisher:     h.pub,
		Gate:          drainingGate{},
		ConfigForRepo: func(string) config.MergeTrackingConfig { return cfg },
		GlobalConfig:  func() config.MergeTrackingConfig { return cfg },
		Viewer:        func() string { return viewer },
		Now:           func() time.Time { return h.now },
	})

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("a drain is an expected pause, not an error: %v", err)
	}
	if gw.mergeCalls != 0 {
		t.Fatal("nothing may be merged while an update drains")
	}
	if row := h.row(t); row.InFlight() {
		t.Errorf("phase = %q — a deferred action must leave the row claimable", row.Phase)
	}
}

// A cancelled context must stop the cycle promptly rather than working through
// the rest of the batch during a shutdown.
func TestTick_StopsOnACancelledContext(t *testing.T) {
	gw := &fakeGateway{statuses: []*gh.MergeStatus{cleanStatus()}}
	h := newHarness(t, enabledCfg(), gw)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.r.Tick(ctx, []string{"acme/widgets"})
	if gw.statusCalls != 0 {
		t.Errorf("status calls = %d, want none on a cancelled context", gw.statusCalls)
	}
}

// Discovery returning a partial result alongside an error must still enrol what
// it got: one failing search qualifier should not blind the whole feature.
func TestTick_EnrolsPartialDiscoveryResults(t *testing.T) {
	gw := &fakeGateway{
		prs: []*gh.TrackedPR{{
			PullRequest: &gh.PullRequest{
				ID: 555, Number: 11, State: "open", Repo: "acme/widgets",
			},
			IsAuthor: true,
		}},
		prsErr:   errors.New("assignee search failed"),
		statuses: []*gh.MergeStatus{cleanStatus()},
	}
	h := newHarness(t, enabledCfg(), gw)

	if stats := h.r.Tick(context.Background(), []string{"acme/widgets"}); stats.Discovered != 1 {
		t.Errorf("discovered = %d, want the partial result enrolled", stats.Discovered)
	}
}

// Nil entries in a discovery result must not panic the cycle.
func TestTick_SkipsNilDiscoveryEntries(t *testing.T) {
	gw := &fakeGateway{
		prs:      []*gh.TrackedPR{nil, {PullRequest: nil}},
		statuses: []*gh.MergeStatus{cleanStatus()},
	}
	h := newHarness(t, enabledCfg(), gw)

	if stats := h.r.Tick(context.Background(), []string{"acme/widgets"}); stats.Discovered != 0 {
		t.Errorf("discovered = %d, want none", stats.Discovered)
	}
}

// A repo that is monitored but has merge tracking switched off must not be
// enrolled, even when another repo in the batch has it on.
func TestTick_SkipsReposWithTrackingDisabled(t *testing.T) {
	gw := &fakeGateway{
		prs: []*gh.TrackedPR{{
			PullRequest: &gh.PullRequest{ID: 1, Number: 1, State: "open", Repo: "acme/off"},
			IsAuthor:    true,
		}},
		statuses: []*gh.MergeStatus{cleanStatus()},
	}
	h := newHarness(t, enabledCfg(), gw)
	h.r = mergetrack.NewReconciler(mergetrack.ReconcilerOptions{
		Gateway:   gw,
		Store:     h.st,
		Publisher: h.pub,
		ConfigForRepo: func(repo string) config.MergeTrackingConfig {
			if repo == "acme/off" {
				return config.MergeTrackingConfig{}
			}
			return enabledCfg()
		},
		GlobalConfig: func() config.MergeTrackingConfig { return enabledCfg() },
		Viewer:       func() string { return viewer },
		Now:          func() time.Time { return h.now },
	})

	if stats := h.r.Tick(context.Background(), []string{"acme/widgets", "acme/off"}); stats.Discovered != 0 {
		t.Errorf("discovered = %d, want none for a repo with tracking off", stats.Discovered)
	}
}

// A PR the search returns that we have never seen has to be created locally
// before it can be tracked.
func TestTick_CreatesTheLocalPRRowForANewPR(t *testing.T) {
	gw := &fakeGateway{
		prs: []*gh.TrackedPR{{
			PullRequest: &gh.PullRequest{
				ID: 999, Number: 42, Title: "Brand new", State: "open",
				HTMLURL: "https://github.com/acme/widgets/pull/42",
				Repo:    "acme/widgets",
				User:    gh.User{Login: viewer},
			},
			IsAuthor: true,
		}},
		statuses: []*gh.MergeStatus{cleanStatus()},
	}
	h := newHarness(t, enabledCfg(), gw)

	h.r.Tick(context.Background(), []string{"acme/widgets"})

	pr, err := h.st.GetPRByRepoNumber("acme/widgets", 42)
	if err != nil || pr == nil {
		t.Fatalf("the PR row should have been created: %v", err)
	}
	if pr.Title != "Brand new" {
		t.Errorf("title = %q", pr.Title)
	}
	if _, err := h.st.GetMergeTracking(pr.ID); err != nil {
		t.Errorf("a tracking row should exist: %v", err)
	}
}

// Re-running discovery over a PR we already track must not announce it again.
func TestTick_DoesNotReannounceAKnownPR(t *testing.T) {
	gw := &fakeGateway{
		prs: []*gh.TrackedPR{{
			PullRequest: &gh.PullRequest{
				ID: 111, Number: 7, State: "open", Repo: "acme/widgets",
			},
			IsAuthor: true,
		}},
		statuses: []*gh.MergeStatus{cleanStatus()},
	}
	h := newHarness(t, enabledCfg(), gw)

	if stats := h.r.Tick(context.Background(), []string{"acme/widgets"}); stats.Discovered != 0 {
		t.Errorf("discovered = %d, want 0 for a PR already tracked", stats.Discovered)
	}
	if h.pub.has(sse.EventMergeTrackDetected) {
		t.Error("a known PR must not be announced as newly detected")
	}
}

// Failing to read the PR from GitHub is an error the caller sees.
func TestReconcilePR_StatusFetchFailureIsReported(t *testing.T) {
	gw := &fakeGateway{statusErr: errors.New("connection reset")}
	h := newHarness(t, enabledCfg(), gw)

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err == nil {
		t.Fatal("a failed status fetch must be reported")
	}
	if !h.pub.has(sse.EventMergeTrackError) {
		t.Error("an error event should be emitted")
	}
}

func TestReconcilePR_MissingRowIsReported(t *testing.T) {
	h := newHarness(t, enabledCfg(), &fakeGateway{})
	if _, err := h.r.ReconcilePR(context.Background(), 9999, h.now, false); err == nil {
		t.Fatal("reconciling a row that does not exist must be reported")
	}
}

// The default batch size applies when the config leaves it unset, rather than
// evaluating nothing.
func TestTick_UnsetBatchSizeUsesTheDefault(t *testing.T) {
	cfg := enabledCfg()
	cfg.MaxPRsPerTick = 0
	gw := &fakeGateway{statuses: []*gh.MergeStatus{cleanStatus()}}
	h := newHarness(t, cfg, gw)

	if stats := h.r.Tick(context.Background(), []string{"acme/widgets"}); stats.Evaluated == 0 {
		t.Error("an unset batch size should fall back to the default, not to zero")
	}
}

// SSE is optional; a reconciler with no publisher must still work.
func TestReconcilePR_WorksWithoutAPublisher(t *testing.T) {
	gw := &fakeGateway{statuses: []*gh.MergeStatus{cleanStatus()}}
	h := newHarness(t, enabledCfg(), gw)
	h.r = mergetrack.NewReconciler(mergetrack.ReconcilerOptions{
		Gateway:       gw,
		Store:         h.st,
		ConfigForRepo: func(string) config.MergeTrackingConfig { return enabledCfg() },
		GlobalConfig:  func() config.MergeTrackingConfig { return enabledCfg() },
		Viewer:        func() string { return viewer },
		Now:           func() time.Time { return h.now },
	})

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("reconcile without a publisher: %v", err)
	}
}

// Without a viewer accessor the snapshot's own viewer login is used, so the
// scope check still works.
func TestReconcilePR_FallsBackToTheSnapshotViewer(t *testing.T) {
	gw := &fakeGateway{statuses: []*gh.MergeStatus{cleanStatus()}}
	h := newHarness(t, enabledCfg(), gw)
	h.r = mergetrack.NewReconciler(mergetrack.ReconcilerOptions{
		Gateway:       gw,
		Store:         h.st,
		Publisher:     h.pub,
		ConfigForRepo: func(string) config.MergeTrackingConfig { return enabledCfg() },
		GlobalConfig:  func() config.MergeTrackingConfig { return enabledCfg() },
		Viewer:        func() string { return "" },
		Now:           func() time.Time { return h.now },
	})

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := h.row(t).Phase; got == store.MergePhaseAbandoned {
		t.Error("the snapshot's viewer should have satisfied the scope check")
	}
}

// A non-mutating decision still schedules the next look.
func TestReconcilePR_AppliesTheDefaultCooldown(t *testing.T) {
	st := cleanStatus()
	st.Checks = []gh.CheckContext{{Name: "build", State: gh.CheckStateFailure, Required: true}}
	gw := &fakeGateway{statuses: []*gh.MergeStatus{st}}
	h := newHarness(t, enabledCfg(), gw)

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if h.row(t).CooldownUntil.IsZero() {
		t.Error("a non-mutating decision should still schedule the next look")
	}
}

func TestReconcilePR_DryRunPersistsTheDecision(t *testing.T) {
	st := cleanStatus()
	st.Checks = []gh.CheckContext{{Name: "build", State: gh.CheckStateFailure, Required: true, App: "GitHub Actions"}}
	gw := &fakeGateway{statuses: []*gh.MergeStatus{st}}
	h := newHarness(t, allOn(), gw)

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, true); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	row := h.row(t)
	if row.BlockReason != string(mergetrack.ReasonChecksFailing) {
		t.Errorf("block reason = %q, want checks_failing", row.BlockReason)
	}
	if !strings.Contains(row.BlockDetail, "build") {
		t.Errorf("detail = %q, want it to name the check", row.BlockDetail)
	}
	if row.DecisionJSON == "" {
		t.Error("a dry run should still persist the decision for the UI")
	}
}

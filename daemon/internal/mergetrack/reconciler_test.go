package mergetrack_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/mergetrack"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/heimdallm/daemon/internal/store"
	"github.com/heimdallm/daemon/internal/workgate"
)

// fakeGateway is scripted per call so a test can make the second GetMergeStatus
// differ from the first — which is the only way to exercise the pre-merge
// revalidation window.
type fakeGateway struct {
	t *testing.T

	prs       []*gh.TrackedPR
	prsErr    error
	statuses  []*gh.MergeStatus // consumed in order; the last one repeats
	statusErr error
	mergeOut  gh.MergeOutcome
	mergeErr  error
	updateErr error
	autoMerge *gh.AutoMergeRequest
	autoErr   error

	// lastIncludeAssigned records the qualifier discovery searched with.
	lastIncludeAssigned bool

	// onEnableAutoMerge runs inside EnableAutoMerge, before it answers. Lets a
	// test simulate what another actor does between the arm and the fallback.
	onEnableAutoMerge func()

	// failOnAnyCall makes every method fail the test. Used to prove that a
	// disabled configuration costs zero GitHub calls.
	failOnAnyCall bool

	statusCalls   int
	mergeCalls    int
	updateCalls   int
	enableCalls   int
	disableCalls  int
	commentCalls  int
	mergeSHAs     []string
	disableBefore bool // DisableAutoMerge was called before MergePRAtSHA
}

func (f *fakeGateway) guard(name string) {
	if f.failOnAnyCall {
		f.t.Fatalf("no GitHub call expected, got %s", name)
	}
}

func (f *fakeGateway) FetchMergeTrackingPRs(includeAssigned bool) ([]*gh.TrackedPR, error) {
	f.guard("FetchMergeTrackingPRs")
	f.lastIncludeAssigned = includeAssigned
	return f.prs, f.prsErr
}

func (f *fakeGateway) GetMergeStatus(string, int) (*gh.MergeStatus, error) {
	f.guard("GetMergeStatus")
	f.statusCalls++
	if f.statusErr != nil {
		return nil, f.statusErr
	}
	if len(f.statuses) == 0 {
		return nil, errors.New("no scripted status")
	}
	idx := f.statusCalls - 1
	if idx >= len(f.statuses) {
		idx = len(f.statuses) - 1
	}
	// Return a copy: the reconciler must not be able to mutate the script.
	cp := *f.statuses[idx]
	return &cp, nil
}

func (f *fakeGateway) EnableAutoMerge(string, string, string) (*gh.AutoMergeRequest, error) {
	f.guard("EnableAutoMerge")
	f.enableCalls++
	if f.onEnableAutoMerge != nil {
		f.onEnableAutoMerge()
	}
	if f.autoErr != nil {
		return nil, f.autoErr
	}
	if f.autoMerge != nil {
		return f.autoMerge, nil
	}
	return &gh.AutoMergeRequest{MergeMethod: "SQUASH"}, nil
}

func (f *fakeGateway) DisableAutoMerge(string) error {
	f.guard("DisableAutoMerge")
	f.disableCalls++
	if f.mergeCalls == 0 {
		f.disableBefore = true
	}
	return nil
}

func (f *fakeGateway) UpdatePRBranch(string, int, string) (gh.UpdateBranchOutcome, error) {
	f.guard("UpdatePRBranch")
	f.updateCalls++
	if f.updateErr != nil {
		return gh.UpdateBranchOutcome{}, f.updateErr
	}
	return gh.UpdateBranchOutcome{Accepted: true}, nil
}

func (f *fakeGateway) MergePRAtSHA(_ string, _ int, _, expectedSHA string) (gh.MergeOutcome, error) {
	f.guard("MergePRAtSHA")
	f.mergeCalls++
	f.mergeSHAs = append(f.mergeSHAs, expectedSHA)
	if f.mergeErr != nil {
		return gh.MergeOutcome{}, f.mergeErr
	}
	if f.mergeOut.SHA == "" && f.mergeOut.Merged == false {
		return gh.MergeOutcome{Merged: true, SHA: expectedSHA}, nil
	}
	return f.mergeOut, nil
}

func (f *fakeGateway) PostComment(string, int, string) (time.Time, error) {
	f.guard("PostComment")
	f.commentCalls++
	return time.Now(), nil
}

type capturingPublisher struct{ events []sse.Event }

func (p *capturingPublisher) Publish(ev sse.Event) { p.events = append(p.events, ev) }

func (p *capturingPublisher) has(t string) bool { return p.count(t) > 0 }

func (p *capturingPublisher) count(t string) int {
	n := 0
	for _, ev := range p.events {
		if ev.Type == t {
			n++
		}
	}
	return n
}

// drainingGate always reports that an application update owns the gate.
type drainingGate struct{}

func (drainingGate) AcquireContext(context.Context, workgate.Kind) (context.Context, *workgate.Permit, bool, error) {
	return nil, nil, false, workgate.ErrDraining
}

// harness wires a real in-memory store to a fake gateway.
type harness struct {
	st   *store.Store
	gw   *fakeGateway
	pub  *capturingPublisher
	r    *mergetrack.Reconciler
	prID int64
	now  time.Time
}

func newHarness(t *testing.T, cfg config.MergeTrackingConfig, gw *fakeGateway) *harness {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	prID, err := st.UpsertPR(&store.PR{
		GithubID: 111, Repo: "acme/widgets", Number: 7,
		Title: "Add widget cache", Author: viewer,
		URL: "https://github.com/acme/widgets/pull/7", State: "open",
		UpdatedAt: now, FetchedAt: now,
	})
	if err != nil {
		t.Fatalf("upsert pr: %v", err)
	}
	if _, err := st.EnsureMergeTracking(prID, "acme/widgets", 7); err != nil {
		t.Fatalf("ensure tracking: %v", err)
	}

	pub := &capturingPublisher{}
	gw.t = t
	h := &harness{st: st, gw: gw, pub: pub, prID: prID, now: now}
	h.r = mergetrack.NewReconciler(mergetrack.ReconcilerOptions{
		Gateway:       gw,
		Store:         st,
		Publisher:     pub,
		ConfigForRepo: func(string) config.MergeTrackingConfig { return cfg },
		GlobalConfig:  func() config.MergeTrackingConfig { return cfg },
		Viewer:        func() string { return viewer },
		Now:           func() time.Time { return h.now },
	})
	return h
}

// newHarnessWithConfigs builds a reconciler whose global and per-repo config
// disagree — the shape that exposed the enrolment mismatch, since discovery
// searches with the global value and everything downstream uses the repo's.
func newHarnessWithConfigs(t *testing.T, global, perRepo config.MergeTrackingConfig, gw *fakeGateway) *harness {
	t.Helper()
	h := newHarness(t, global, gw)
	h.r = mergetrack.NewReconciler(mergetrack.ReconcilerOptions{
		Gateway:       gw,
		Store:         h.st,
		Publisher:     h.pub,
		ConfigForRepo: func(string) config.MergeTrackingConfig { return perRepo },
		GlobalConfig:  func() config.MergeTrackingConfig { return global },
		Viewer:        func() string { return viewer },
		Now:           func() time.Time { return h.now },
	})
	return h
}

func (h *harness) row(t *testing.T) *store.MergeTracking {
	t.Helper()
	row, err := h.st.GetMergeTracking(h.prID)
	if err != nil {
		t.Fatalf("get row: %v", err)
	}
	return row
}

// With everything off, a cycle must not touch GitHub at all — not a search, not
// a status query. The fake fails the test on any call.
func TestTick_DisabledEverywhereMakesNoGitHubCalls(t *testing.T) {
	cfg := config.MergeTrackingConfig{} // Enabled defaults to false
	h := newHarness(t, cfg, &fakeGateway{failOnAnyCall: true})

	stats := h.r.Tick(context.Background(), []string{"acme/widgets"})
	if stats.Evaluated != 0 || stats.Actions != 0 {
		t.Errorf("stats = %+v, want an empty cycle", stats)
	}
}

// theburrowhub/heimdallm#674: "Revalidar todo inmediatamente antes del merge
// para cerrar la ventana TOCTOU" and "Un push entre preflight y PUT merge falla
// por expected SHA".
func TestReconcilePR_HeadMovedBetweenEvaluationAndMergeBlocksWithoutMerging(t *testing.T) {
	first := cleanStatus()
	second := cleanStatus()
	second.HeadOID = "cccccccccccccccccccccccccccccccccccccccc" // a push landed

	cfg := allOn()
	cfg.EnableAutoMerge = false // straight to a direct merge
	h := newHarness(t, cfg, &fakeGateway{statuses: []*gh.MergeStatus{first, second}})

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if h.gw.statusCalls != 2 {
		t.Errorf("status calls = %d, want exactly 2 (evaluate, then revalidate)", h.gw.statusCalls)
	}
	if h.gw.mergeCalls != 0 {
		t.Fatalf("merge calls = %d, want 0 — the head moved", h.gw.mergeCalls)
	}
	row := h.row(t)
	if row.BlockReason != string(mergetrack.ReasonHeadSHAMoved) {
		t.Errorf("block reason = %q, want head_sha_moved", row.BlockReason)
	}
	if !h.pub.has(sse.EventMergeTrackBlocked) {
		t.Error("a blocked event should be emitted")
	}
}

// The requirements went red between the two reads: block, do not merge.
func TestReconcilePR_RequirementsRegressedBeforeMergeBlocks(t *testing.T) {
	first := cleanStatus()
	second := cleanStatus()
	second.Checks = []gh.CheckContext{{Name: "build", State: gh.CheckStateFailure, Required: true}}

	cfg := allOn()
	cfg.EnableAutoMerge = false
	h := newHarness(t, cfg, &fakeGateway{statuses: []*gh.MergeStatus{first, second}})

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if h.gw.mergeCalls != 0 {
		t.Fatal("must not merge when revalidation finds a failing required check")
	}
	if got := h.row(t).BlockReason; got != string(mergetrack.ReasonChecksFailing) {
		t.Errorf("block reason = %q, want checks_failing", got)
	}
}

// A PR merged by someone else between the reads is not an error: the desired
// state already holds.
func TestReconcilePR_MergedByAnotherActorIsIdempotent(t *testing.T) {
	first := cleanStatus()
	second := cleanStatus()
	second.Merged = true
	second.State = "MERGED"

	cfg := allOn()
	cfg.EnableAutoMerge = false
	h := newHarness(t, cfg, &fakeGateway{statuses: []*gh.MergeStatus{first, second}})

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if h.gw.mergeCalls != 0 {
		t.Fatal("must not merge a PR that is already merged")
	}
	if got := h.row(t).Phase; got != store.MergePhaseMerged {
		t.Errorf("phase = %q, want merged", got)
	}
}

func TestReconcilePR_DirectMergeSendsTheEvaluatedSHAAndDisarmsAutoMergeFirst(t *testing.T) {
	st := cleanStatus()
	st.AutoMerge = &gh.AutoMergeRequest{MergeMethod: "SQUASH"}

	cfg := allOn()
	h := newHarness(t, cfg, &fakeGateway{statuses: []*gh.MergeStatus{st}})
	// Pretend a previous pass armed auto-merge, so this pass promotes to a
	// direct merge.
	if err := h.st.ArmNativeAutoMerge(h.prID, headSHA, "squash", h.now.Add(-time.Hour)); err != nil {
		t.Fatalf("arm: %v", err)
	}

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if h.gw.mergeCalls != 1 {
		t.Fatalf("merge calls = %d, want 1", h.gw.mergeCalls)
	}
	if got := h.gw.mergeSHAs[0]; got != headSHA {
		t.Errorf("merge sent sha %q, want the evaluated head %q", got, headSHA)
	}
	// With auto-merge still armed, GitHub could merge concurrently with us.
	if h.gw.disableCalls != 1 {
		t.Errorf("disable auto-merge calls = %d, want 1", h.gw.disableCalls)
	}
	if !h.gw.disableBefore {
		t.Error("auto-merge must be disarmed BEFORE the direct merge, not after")
	}
	if got := h.row(t).Phase; got != store.MergePhaseMerged {
		t.Errorf("phase = %q, want merged", got)
	}
	if !h.pub.has(sse.EventMergeTrackMerged) {
		t.Error("a merged event should be emitted")
	}
}

// GitHub can reject inside its own window even after our revalidation.
func TestReconcilePR_GitHubSHAMismatchBlocksWithoutRetrying(t *testing.T) {
	cfg := allOn()
	cfg.EnableAutoMerge = false
	gw := &fakeGateway{
		statuses: []*gh.MergeStatus{cleanStatus()},
		mergeErr: &gh.MergeRejectedError{
			StatusCode: 409,
			Reason:     gh.MergeReasonSHAMismatch,
			Body:       "Head branch was modified. Review and try the merge again.",
		},
	}
	h := newHarness(t, cfg, gw)

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("a classified rejection is a block, not an error: %v", err)
	}
	if gw.mergeCalls != 1 {
		t.Errorf("merge calls = %d, want exactly 1 — no blind retry", gw.mergeCalls)
	}
	row := h.row(t)
	if row.BlockReason != string(mergetrack.ReasonHeadSHAMoved) {
		t.Errorf("block reason = %q, want head_sha_moved", row.BlockReason)
	}
	if row.Phase == store.MergePhaseMerged {
		t.Error("a rejected merge must not be recorded as merged")
	}
}

func TestReconcilePR_ArmingPersistsTheArmedState(t *testing.T) {
	h := newHarness(t, allOn(), &fakeGateway{
		statuses:  []*gh.MergeStatus{cleanStatus()},
		autoMerge: &gh.AutoMergeRequest{MergeMethod: "SQUASH", EnabledAt: time.Date(2026, 8, 28, 11, 59, 0, 0, time.UTC)},
	})

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if h.gw.enableCalls != 1 {
		t.Fatalf("enable calls = %d, want 1", h.gw.enableCalls)
	}
	if h.gw.mergeCalls != 0 {
		t.Fatal("the arming pass must not also merge")
	}
	row := h.row(t)
	if row.Phase != store.MergePhaseAutoMergeArmed {
		t.Errorf("phase = %q, want auto_merge_armed", row.Phase)
	}
	if row.AutoMergeHeadSHA != headSHA {
		t.Errorf("armed sha = %q, want %q", row.AutoMergeHeadSHA, headSHA)
	}
	if !h.pub.has(sse.EventMergeTrackAutoMergeArmed) {
		t.Error("an armed event should be emitted")
	}
}

// The two phases, run end to end across two passes.
func TestReconcilePR_ArmsThenMergesAcrossTwoPasses(t *testing.T) {
	armed := cleanStatus()
	armed.AutoMerge = &gh.AutoMergeRequest{MergeMethod: "SQUASH", EnabledAt: time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)}

	gw := &fakeGateway{statuses: []*gh.MergeStatus{cleanStatus(), armed}}
	h := newHarness(t, allOn(), gw)

	// Pass 1 arms.
	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("pass 1: %v", err)
	}
	if gw.enableCalls != 1 || gw.mergeCalls != 0 {
		t.Fatalf("pass 1: enable=%d merge=%d, want 1/0", gw.enableCalls, gw.mergeCalls)
	}

	// Pass 2, five minutes later: GitHub still has not merged.
	h.now = h.now.Add(5 * time.Minute)
	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("pass 2: %v", err)
	}
	if gw.mergeCalls != 1 {
		t.Errorf("pass 2: merge calls = %d, want 1", gw.mergeCalls)
	}
}

// An auto-merge armed during THIS pass must not be promoted in the same pass.
func TestReconcilePR_ArmedInThisPassDoesNotMerge(t *testing.T) {
	armed := cleanStatus()
	armed.AutoMerge = &gh.AutoMergeRequest{MergeMethod: "SQUASH", EnabledAt: time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)}

	gw := &fakeGateway{statuses: []*gh.MergeStatus{armed}}
	h := newHarness(t, allOn(), gw)
	if err := h.st.ArmNativeAutoMerge(h.prID, headSHA, "squash", h.now); err != nil {
		t.Fatalf("arm: %v", err)
	}

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if gw.mergeCalls != 0 {
		t.Fatal("must not merge in the same pass that armed auto-merge")
	}
}

func TestReconcilePR_BehindBaseCallsUpdateBranchWithExpectedSHA(t *testing.T) {
	st := cleanStatus()
	st.MergeStateStatus = gh.MergeStateBehind
	// A red check must not suppress the branch update: checks against a stale
	// base are evidence for the old head and will rerun after synchronisation.
	st.Checks = []gh.CheckContext{{Name: "build", State: gh.CheckStateFailure, Required: true}}

	cfg := enabledCfg()
	cfg.UpdateBranch = true
	gw := &fakeGateway{statuses: []*gh.MergeStatus{st}}
	h := newHarness(t, cfg, gw)

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if gw.updateCalls != 1 {
		t.Errorf("update calls = %d, want 1", gw.updateCalls)
	}
	if !h.pub.has(sse.EventMergeTrackBranchUpdated) {
		t.Error("a branch-updated event should be emitted")
	}
	// 202 Accepted is asynchronous. Keep a truthful observation state instead
	// of making the immediate SSE refresh repaint the old blocker/checks.
	row := h.row(t)
	if row.Phase != store.MergePhaseUpdatePending {
		t.Errorf("phase = %q, want update_pending after an accepted async update", row.Phase)
	}
	if row.CooldownUntil.IsZero() {
		t.Error("a cooldown should be set so the next pass can observe the new head")
	}
	if row.BlockReason != "" || row.ChecksRequiredFailing != 0 || row.DecisionJSON != "" {
		t.Errorf("accepted update kept stale evidence: %+v", row)
	}
	due, err := h.st.ListMergeTrackingDue(h.now.Add(time.Minute), 1)
	if err != nil {
		t.Fatalf("list confirmation: %v", err)
	}
	if len(due) != 1 || due[0].PRID != h.prID {
		t.Fatalf("confirmation row not due first: %+v", due)
	}
}

// A push resets the per-commit counters, because they described a commit that
// no longer exists.
func TestReconcilePR_NewHeadResetsAttemptCounters(t *testing.T) {
	st := cleanStatus()
	cfg := enabledCfg()
	h := newHarness(t, cfg, &fakeGateway{statuses: []*gh.MergeStatus{st}})

	if err := h.st.BumpMergeTrackingAttempt(h.prID, store.MergeAttemptMerge, time.Time{}, "boom"); err != nil {
		t.Fatalf("bump: %v", err)
	}
	// Anchor the row to an older commit so the snapshot looks like a push.
	if err := h.st.ResetMergeTrackingForNewHead(h.prID, oldSHA, h.now); err != nil {
		t.Fatalf("anchor: %v", err)
	}
	if err := h.st.BumpMergeTrackingAttempt(h.prID, store.MergeAttemptMerge, time.Time{}, "boom"); err != nil {
		t.Fatalf("bump: %v", err)
	}
	if before := h.row(t).MergeAttempts; before == 0 {
		t.Fatal("precondition: the counter should be non-zero before the push")
	}

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	row := h.row(t)
	if row.MergeAttempts != 0 {
		t.Errorf("merge attempts = %d, want 0 after a new head", row.MergeAttempts)
	}
	if row.HeadSHA != headSHA {
		t.Errorf("head sha = %q, want %q", row.HeadSHA, headSHA)
	}
}

// dryRun answers "why is this stuck?" without acting on it.
func TestReconcilePR_DryRunEvaluatesWithoutActing(t *testing.T) {
	cfg := allOn()
	cfg.EnableAutoMerge = false
	gw := &fakeGateway{statuses: []*gh.MergeStatus{cleanStatus()}}
	h := newHarness(t, cfg, gw)

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, true); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if gw.mergeCalls != 0 {
		t.Fatal("a dry run must never merge")
	}
	if !h.pub.has(sse.EventMergeTrackEvaluated) {
		t.Error("a dry run should still record the evaluation")
	}
}

// The persisted decision is what the listing and PR detail render, so it has to
// carry the check counts.
func TestReconcilePR_PersistsCheckCountsForTheUI(t *testing.T) {
	st := cleanStatus()
	st.Checks = []gh.CheckContext{
		{Name: "build", State: gh.CheckStateFailure, Required: true, App: "GitHub Actions"},
		{Name: "lint", State: gh.CheckStatePending, Required: true},
		{Name: "coverage", State: gh.CheckStateFailure, Required: false},
	}
	h := newHarness(t, enabledCfg(), &fakeGateway{statuses: []*gh.MergeStatus{st}})

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	row := h.row(t)
	if row.ChecksRequiredFailing != 1 {
		t.Errorf("required failing = %d, want 1", row.ChecksRequiredFailing)
	}
	if row.ChecksRequiredPending != 1 {
		t.Errorf("required pending = %d, want 1", row.ChecksRequiredPending)
	}
	if row.BlockReason != string(mergetrack.ReasonChecksFailing) {
		t.Errorf("block reason = %q, want checks_failing", row.BlockReason)
	}
	// The full decision, including every check, is persisted so neither the
	// listing nor the detail view needs another GitHub call.
	if row.DecisionJSON == "" {
		t.Fatal("the decision JSON must be persisted")
	}
	for _, want := range []string{"build", "GitHub Actions", "coverage", "headline_absent_ok"} {
		if want == "headline_absent_ok" {
			continue
		}
		if !contains(row.DecisionJSON, want) {
			t.Errorf("decision JSON should mention %q", want)
		}
	}
}

// A PR that vanished must stop being polled rather than 404ing every cycle.
func TestReconcilePR_MissingPRIsAbandoned(t *testing.T) {
	gw := &fakeGateway{statusErr: gh.ErrPRNotFound}
	h := newHarness(t, enabledCfg(), gw)

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("a missing PR is not an error: %v", err)
	}
	if got := h.row(t).Phase; got != store.MergePhaseAbandoned {
		t.Errorf("phase = %q, want abandoned", got)
	}
}

// A claim is exclusive: a second pass on a row already in flight must not act.
func TestReconcilePR_ClaimPreventsConcurrentActions(t *testing.T) {
	cfg := allOn()
	cfg.EnableAutoMerge = false
	gw := &fakeGateway{statuses: []*gh.MergeStatus{cleanStatus()}}
	h := newHarness(t, cfg, gw)

	// Anchor to the head and park the row in an in-flight phase, as a crashed
	// or still-running action would leave it.
	if err := h.st.ResetMergeTrackingForNewHead(h.prID, headSHA, h.now); err != nil {
		t.Fatalf("anchor: %v", err)
	}
	ok, err := h.st.ClaimMergeTrackingAction(h.prID, headSHA, store.MergePhaseMerging, h.now)
	if err != nil || !ok {
		t.Fatalf("precondition claim failed: ok=%v err=%v", ok, err)
	}

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if gw.mergeCalls != 0 {
		t.Fatal("a second pass must not act on a row that is already claimed")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (len(needle) == 0 || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

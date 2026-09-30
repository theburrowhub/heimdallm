package mergetrack_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/mergetrack"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/heimdallm/daemon/internal/store"
)

// watchCfg is what EffectiveMergeTrackingForRepo hands the reconciler for a
// repo that merge tracking leaves off but [my_prs] watches.
func watchCfg() config.MergeTrackingConfig {
	c := config.Config{}
	return c.EffectiveMergeTrackingForRepo("acme/widgets")
}

func newWatchHarness(t *testing.T, gw *fakeGateway, staleAfter time.Duration) *harness {
	t.Helper()
	h := newHarness(t, watchCfg(), gw)
	cfg := watchCfg()
	h.r = mergetrack.NewReconciler(mergetrack.ReconcilerOptions{
		Gateway:       gw,
		Store:         h.st,
		Publisher:     h.pub,
		ConfigForRepo: func(string) config.MergeTrackingConfig { return cfg },
		GlobalConfig:  func() config.MergeTrackingConfig { return cfg },
		Viewer:        func() string { return viewer },
		Now:           func() time.Time { return h.now },
		StaleAfter:    func() time.Duration { return staleAfter },
	})
	return h
}

func eventPayloads(t *testing.T, pub *capturingPublisher, typ string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, ev := range pub.events {
		if ev.Type != typ {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(ev.Data), &m); err != nil {
			t.Fatalf("decode %s payload: %v", typ, err)
		}
		out = append(out, m)
	}
	return out
}

func TestEffectiveConfig_WatchOnlyHasEveryWriteSwitchOff(t *testing.T) {
	cfg := watchCfg()
	if !cfg.Enabled || !cfg.WatchOnly {
		t.Fatalf("watch config must be enabled and watch-only: %+v", cfg)
	}
	if cfg.EnableAutoMerge || cfg.UpdateBranch || cfg.ResolveConflicts || cfg.Merge {
		t.Errorf("watch config must not enable any automation: %+v", cfg)
	}
	if !cfg.IncludeAssigned {
		t.Error("my_prs.include_assigned defaults to true")
	}
}

// A ready PR in a watched repo is reported as ready and announced once — and
// never merged, armed or updated.
func TestReconcilePR_WatchOnlyReportsReadyWithoutActing(t *testing.T) {
	gw := &fakeGateway{statuses: []*gh.MergeStatus{cleanStatus()}}
	h := newWatchHarness(t, gw, 0)

	for i := 0; i < 2; i++ {
		acted, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false)
		if err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		if acted {
			t.Fatal("a watched repo must never be acted on")
		}
	}
	if gw.mergeCalls+gw.enableCalls+gw.updateCalls+gw.disableCalls+gw.commentCalls != 0 {
		t.Fatalf("write calls in watch mode: merge=%d enable=%d update=%d disable=%d comment=%d",
			gw.mergeCalls, gw.enableCalls, gw.updateCalls, gw.disableCalls, gw.commentCalls)
	}
	row := h.row(t)
	if row.Attention != string(mergetrack.AttentionReady) {
		t.Errorf("attention = %q, want ready", row.Attention)
	}
	if row.BlockReason != "" {
		t.Errorf("a ready watched PR must carry no block, got %q (%s)", row.BlockReason, row.BlockDetail)
	}
	events := eventPayloads(t, h.pub, sse.EventMyPRAttention)
	if len(events) != 1 {
		t.Fatalf("my_pr_attention events = %d, want exactly one across two passes", len(events))
	}
	if events[0]["attention"] != "ready" || events[0]["repo"] != "acme/widgets" {
		t.Errorf("unexpected payload: %v", events[0])
	}
}

// The old symptom: a repo with merge tracking off showed every PR as blocked by
// "merge_tracking.enabled = false". Watching evaluates it for real.
func TestReconcilePR_WatchOnlyReportsTheRealBlocker(t *testing.T) {
	st := cleanStatus()
	st.ReviewDecision = gh.ReviewDecisionChangesRequested
	st.Reviews = []gh.OpinionatedReview{{Login: "reviewer", State: "CHANGES_REQUESTED", CommitOID: headSHA, CanPush: true}}
	h := newWatchHarness(t, &fakeGateway{statuses: []*gh.MergeStatus{st}}, 0)

	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	row := h.row(t)
	if row.BlockReason != string(mergetrack.ReasonChangesRequested) {
		t.Errorf("block = %q, want changes_requested", row.BlockReason)
	}
	if row.Attention != string(mergetrack.AttentionAction) {
		t.Errorf("attention = %q, want action", row.Attention)
	}
}

// Rules that only guard automation — write permission, a cross-fork head, the
// allowed merge method — must not hide a watched PR's real state.
func TestEvaluate_WatchOnlySkipsAutomationOnlyRules(t *testing.T) {
	st := cleanStatus()
	st.ViewerPermission = "READ"
	st.HeadIsFork = true
	st.HeadRepoOwner = "someone-else"
	st.AllowedMergeMethods = gh.MergeMethodSet{Merge: true}

	in := baseInput(watchCfg())
	d := mergetrack.Decide(mergetrack.Evaluate(st, in), st, in)
	if !d.Ready || len(d.Blocks) != 0 {
		t.Fatalf("watched PR should be ready, got ready=%v blocks=%v", d.Ready, d.Blocks)
	}
	if d.Action != mergetrack.ActionNone {
		t.Errorf("action = %q, want none", d.Action)
	}

	auto := enabledCfg()
	auto.Merge = true
	in = baseInput(auto)
	d = mergetrack.Evaluate(st, in)
	if d.PrimaryReason() != mergetrack.ReasonInsufficientPermission {
		t.Errorf("with automation the permission rule still applies, got %q", d.PrimaryReason())
	}
}

// Decide must refuse to act for a watched repo even if a write switch were on.
func TestDecide_WatchOnlyNeverMutates(t *testing.T) {
	cfg := watchCfg()
	cfg.Merge = true
	cfg.EnableAutoMerge = true
	in := baseInput(cfg)
	st := cleanStatus()
	d := mergetrack.Decide(mergetrack.Evaluate(st, in), st, in)
	if d.Action.Mutating() {
		t.Fatalf("watch-only decided %q", d.Action)
	}
}

// Housekeeping must run with the feature off everywhere: that early return is
// why merged PRs used to stay in the tab forever.
func TestTick_PrunesWithEverythingDisabledAndNoGitHubCalls(t *testing.T) {
	h := newHarness(t, config.MergeTrackingConfig{}, &fakeGateway{failOnAnyCall: true})

	oldID, err := h.st.UpsertPR(&store.PR{GithubID: 222, Repo: "acme/widgets", Number: 8, State: "closed", UpdatedAt: h.now, FetchedAt: h.now})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := h.st.EnsureMergeTracking(oldID, "acme/widgets", 8); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if err := h.st.MarkMergeTrackingMerged(oldID, h.now.Add(-48*time.Hour)); err != nil {
		t.Fatalf("mark merged: %v", err)
	}

	h.r.Tick(context.Background(), []string{"acme/widgets"})

	if _, err := h.st.GetMergeTracking(oldID); err == nil {
		t.Error("a PR merged two days ago must be pruned even with tracking disabled")
	}
	if _, err := h.st.GetMergeTracking(h.prID); err == nil {
		t.Error("a live row of a repo nobody tracks or watches must be dropped")
	}
}

// A merge that just happened stays visible for the retention window.
func TestTick_KeepsRecentlyMergedRows(t *testing.T) {
	h := newWatchHarness(t, &fakeGateway{}, 0)
	if err := h.st.MarkMergeTrackingMerged(h.prID, h.now.Add(-2*time.Hour)); err != nil {
		t.Fatalf("mark merged: %v", err)
	}
	h.r.Tick(context.Background(), []string{"acme/widgets"})
	row := h.row(t)
	if row.Phase != store.MergePhaseMerged {
		t.Fatalf("phase = %q, want merged", row.Phase)
	}
	if !row.TerminalAt.Equal(h.now.Add(-2 * time.Hour)) {
		t.Errorf("terminal_at = %v, want the merge time", row.TerminalAt)
	}
}

func TestReconcilePR_StaleAnnouncedOncePerIdleStretch(t *testing.T) {
	st := cleanStatus()
	st.ReviewDecision = gh.ReviewDecisionReviewRequired
	st.Reviews = nil
	gw := &fakeGateway{statuses: []*gh.MergeStatus{st}}
	h := newWatchHarness(t, gw, 72*time.Hour)
	st.UpdatedAt = h.now.Add(-96 * time.Hour)

	reconcile := func() {
		t.Helper()
		if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
	}

	reconcile()
	reconcile()
	stale := eventPayloads(t, h.pub, sse.EventMyPRStale)
	if len(stale) != 1 {
		t.Fatalf("my_pr_stale events = %d, want one for one idle stretch", len(stale))
	}
	if got := stale[0]["idle_seconds"]; got != float64(96*3600) {
		t.Errorf("idle_seconds = %v, want %d", got, 96*3600)
	}
	row := h.row(t)
	if row.StaleNotifiedAt.IsZero() {
		t.Error("stale_notified_at must record the announcement")
	}
	if !row.LastActivityAt.Equal(st.UpdatedAt) {
		t.Errorf("last_activity_at = %v, want %v", row.LastActivityAt, st.UpdatedAt)
	}

	// Activity after the announcement re-arms it: go idle again, announce again.
	h.now = h.now.Add(24 * time.Hour)
	st.UpdatedAt = row.StaleNotifiedAt.Add(time.Hour)
	h.now = st.UpdatedAt.Add(80 * time.Hour)
	reconcile()
	if n := len(eventPayloads(t, h.pub, sse.EventMyPRStale)); n != 2 {
		t.Fatalf("my_pr_stale events = %d, want a second one after new activity went idle", n)
	}
}

func TestReconcilePR_NotStaleUnderTheThreshold(t *testing.T) {
	st := cleanStatus()
	h := newWatchHarness(t, &fakeGateway{statuses: []*gh.MergeStatus{st}}, 72*time.Hour)
	st.UpdatedAt = h.now.Add(-time.Hour)
	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if h.pub.has(sse.EventMyPRStale) {
		t.Error("a PR active an hour ago is not stale")
	}
}

// A mutating decision in an automation repo means Heimdallm is on it, so the PR
// is waiting rather than needing the operator.
func TestReconcilePR_AutomationActionCountsAsWaiting(t *testing.T) {
	st := cleanStatus()
	st.MergeStateStatus = gh.MergeStateBehind
	cfg := enabledCfg()
	cfg.UpdateBranch = true
	h := newHarness(t, cfg, &fakeGateway{statuses: []*gh.MergeStatus{st}})
	if _, err := h.r.ReconcilePR(context.Background(), h.prID, h.now, false); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := h.row(t).Attention; got != string(mergetrack.AttentionWaiting) {
		t.Errorf("attention = %q, want waiting", got)
	}
	if h.pub.has(sse.EventMyPRAttention) {
		t.Error("a PR Heimdallm is updating must not be announced as needing the operator")
	}
}

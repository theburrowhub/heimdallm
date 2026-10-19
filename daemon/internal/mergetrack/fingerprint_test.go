package mergetrack_test

import (
	"context"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/mergetrack"
)

// fingerprintGateway adds the batched fingerprint capability to fakeGateway.
type fingerprintGateway struct {
	*fakeGateway
	fp       string
	fpCalls  int
	lastRefs []gh.PRRef
}

func (f *fingerprintGateway) MergeFingerprints(refs []gh.PRRef) map[string]string {
	f.fpCalls++
	f.lastRefs = refs
	out := map[string]string{}
	if f.fp == "" {
		return out
	}
	for _, r := range refs {
		out[gh.PRKey(r.Repo, r.Number)] = f.fp
	}
	return out
}

func blockedStatus() *gh.MergeStatus {
	st := cleanStatus()
	st.Checks = []gh.CheckContext{{Name: "build", State: gh.CheckStateFailure, Required: true}}
	return st
}

func newFingerprintHarness(t *testing.T, cfg *config.MergeTrackingConfig, st *gh.MergeStatus) (*harness, *fingerprintGateway) {
	t.Helper()
	gw := &fingerprintGateway{fakeGateway: &fakeGateway{statuses: []*gh.MergeStatus{st}}, fp: "state-1"}
	h := newHarness(t, *cfg, gw.fakeGateway)
	h.r = mergetrack.NewReconciler(mergetrack.ReconcilerOptions{
		Gateway:       gw,
		Store:         h.st,
		Publisher:     h.pub,
		ConfigForRepo: func(string) config.MergeTrackingConfig { return *cfg },
		GlobalConfig:  func() config.MergeTrackingConfig { return *cfg },
		Viewer:        func() string { return viewer },
		Now:           func() time.Time { return h.now },
	})
	return h, gw
}

func (h *harness) tick(t *testing.T) mergetrack.TickStats {
	t.Helper()
	return h.r.Tick(context.Background(), []string{"acme/widgets"})
}

// An unchanged blocked PR costs one fingerprint field per pass, not a full
// merge-status query; any change on GitHub's side, a config change, an
// explicit re-check or the hourly revalidation brings the full query back.
func TestTick_UnchangedFingerprintSkipsFullEvaluation(t *testing.T) {
	cfg := enabledCfg()
	h, gw := newFingerprintHarness(t, &cfg, blockedStatus())

	if s := h.tick(t); s.Evaluated != 1 || gw.statusCalls != 1 {
		t.Fatalf("first pass: stats=%+v status calls=%d", s, gw.statusCalls)
	}
	if len(gw.lastRefs) != 1 || gw.lastRefs[0].Repo != "acme/widgets" || gw.lastRefs[0].Number != 7 {
		t.Fatalf("refs = %+v", gw.lastRefs)
	}

	h.now = h.now.Add(10 * time.Minute)
	s := h.tick(t)
	if s.Unchanged != 1 || s.Evaluated != 0 || gw.statusCalls != 1 {
		t.Fatalf("unchanged pass: stats=%+v status calls=%d", s, gw.statusCalls)
	}
	if row := h.row(t); !row.CooldownUntil.After(h.now) {
		t.Fatalf("an unchanged row must be rescheduled, cooldown=%v now=%v", row.CooldownUntil, h.now)
	}

	// GitHub moved.
	h.now = h.now.Add(10 * time.Minute)
	gw.fp = "state-2"
	if s := h.tick(t); s.Evaluated != 1 || gw.statusCalls != 2 {
		t.Fatalf("changed pass: stats=%+v status calls=%d", s, gw.statusCalls)
	}

	// Config moved.
	h.now = h.now.Add(10 * time.Minute)
	cfg.UpdateBranch = !cfg.UpdateBranch
	if s := h.tick(t); s.Evaluated != 1 || gw.statusCalls != 3 {
		t.Fatalf("config pass: stats=%+v status calls=%d", s, gw.statusCalls)
	}

	// Explicit re-check.
	h.now = h.now.Add(10 * time.Minute)
	if err := h.st.ClearMergeTrackingCooldown(h.prID); err != nil {
		t.Fatal(err)
	}
	if s := h.tick(t); s.Evaluated != 1 || gw.statusCalls != 4 {
		t.Fatalf("re-check pass: stats=%+v status calls=%d", s, gw.statusCalls)
	}

	// Hourly revalidation.
	h.now = h.now.Add(2 * time.Hour)
	if s := h.tick(t); s.Evaluated != 1 || gw.statusCalls != 5 {
		t.Fatalf("revalidation pass: stats=%+v status calls=%d", s, gw.statusCalls)
	}
}

// A decision that is not a resting "nothing to do" (here: arming auto-merge)
// is never reused, whatever the fingerprint says.
func TestTick_ActingDecisionIsAlwaysReEvaluated(t *testing.T) {
	cfg := allOn()
	h, gw := newFingerprintHarness(t, &cfg, cleanStatus())
	h.tick(t)
	h.now = h.now.Add(10 * time.Minute)
	if err := h.st.ClearMergeTrackingCooldown(h.prID); err != nil {
		t.Fatal(err)
	}
	before := gw.statusCalls
	if s := h.tick(t); s.Unchanged != 0 || gw.statusCalls == before {
		t.Fatalf("stats=%+v status calls %d→%d, want a full evaluation", s, before, gw.statusCalls)
	}
}

// Without a fingerprint (the batch failed for this PR) evaluation runs in full.
func TestTick_MissingFingerprintEvaluatesInFull(t *testing.T) {
	cfg := enabledCfg()
	h, gw := newFingerprintHarness(t, &cfg, blockedStatus())
	gw.fp = ""
	h.tick(t)
	h.now = h.now.Add(10 * time.Minute)
	if s := h.tick(t); s.Evaluated != 1 || gw.statusCalls != 2 {
		t.Fatalf("stats=%+v status calls=%d", s, gw.statusCalls)
	}
}

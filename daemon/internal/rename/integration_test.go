package rename_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/heimdallm/daemon/internal/rename"
)

// TestProbe_RecoversAcrossTicksWhenPersisterFails is the end-to-end
// regression test for the partial-failure recovery contract (#489
// review feedback). It does NOT call Reconciler.Run directly — it
// drives two real Probe.Tick invocations against a shared Reconciler
// and verifies the daemon's normal probe loop converges after the
// persister recovers, mirroring the in-process flow.
//
// Pre-fix bugs this test pins:
//
//  1. Mutating in-memory config BEFORE the persister means
//     Probe.Repos() returns the new slug on the second tick → no
//     mismatch → no Run dispatch → state stuck.
//  2. Returning early on store.applied=false skips downstream on the
//     second tick even if it does dispatch.
//
// The fix is the combination of (a) reversing ApplyConfig + Persister
// ordering and (b) removing the applied=false short-circuit.
func TestProbe_RecoversAcrossTicksWhenPersisterFails(t *testing.T) {
	// In-memory cfg.Repositories shadow. ApplyConfig mutates it
	// under reposMu, Repos() reads it under the same mutex — the
	// real wiring uses cfgMu identically.
	var reposMu sync.Mutex
	repos := []string{"acme/old"}

	applyConfig := func(oldRepo, newRepo string) {
		// Caller already holds reposMu (passed as CfgMu).
		for i, r := range repos {
			if r == oldRepo {
				repos[i] = newRepo
			}
		}
	}
	reposFn := func() []string {
		reposMu.Lock()
		defer reposMu.Unlock()
		out := make([]string, len(repos))
		copy(out, repos)
		return out
	}

	store := &fakeStore{applied: true}
	persister := &fakePersister{err: errors.New("disk full")}
	purger := &fakePurger{}
	publisher := &fakePublisher{}

	rec := rename.NewReconciler(rename.Deps{
		Store:       store,
		Persister:   persister,
		Purger:      purger,
		Publisher:   publisher,
		CfgMu:       &reposMu,
		TOMLMu:      &sync.Mutex{},
		ApplyConfig: applyConfig,
		CfgPath:     "/tmp/test/config.toml",
		CloneDir:    "/tmp/test/clones",
	})

	canonical := &fakeCanonical{
		results: map[string]canonicalResult{
			"acme/old": {canonical: "acme/new"},
			"acme/new": {canonical: "acme/new"},
		},
	}
	probe := rename.NewProbe(rename.ProbeDeps{
		Probe:      canonical,
		Dispatcher: rec,
		Repos:      reposFn,
	})

	// ─── Tick #1: persister fails ─────────────────────────────────
	probe.Tick(context.Background())

	reposMu.Lock()
	current := append([]string(nil), repos...)
	reposMu.Unlock()
	if len(current) != 1 || current[0] != "acme/old" {
		t.Fatalf("after tick #1 with persister failure: repos = %v, want [acme/old] — in-memory mutation must not happen when the persister fails or the probe loses the mismatch signal", current)
	}
	if publisher.calls != 0 {
		t.Errorf("after tick #1: SSE fired %d times, want 0", publisher.calls)
	}
	if purger.calls != 0 {
		t.Errorf("after tick #1: purger called %d times, want 0", purger.calls)
	}

	// ─── Recovery: simulate the persister coming back, and the store
	// idempotency guard returning applied=false because the audit row
	// from tick #1 is still on record. ──────────────────────────────
	persister.err = nil
	store.applied = false

	// ─── Tick #2: probe re-observes the old slug (in-memory still
	// unmutated), re-dispatches Run, and the full downstream now
	// completes — converging the state. ─────────────────────────────
	probe.Tick(context.Background())

	reposMu.Lock()
	current = append([]string(nil), repos...)
	reposMu.Unlock()
	if len(current) != 1 || current[0] != "acme/new" {
		t.Fatalf("after tick #2 recovery: repos = %v, want [acme/new]", current)
	}
	if purger.calls != 1 {
		t.Errorf("after tick #2: purger.calls = %d, want 1", purger.calls)
	}
	if publisher.calls != 1 {
		t.Errorf("after tick #2: publisher.calls = %d, want 1", publisher.calls)
	}
	if !strings.Contains(publisher.events[0].Data, `"new_repo":"acme/new"`) {
		t.Errorf("recovery SSE payload: %s", publisher.events[0].Data)
	}

	// ─── Tick #3: state is now settled. Probe sees the new slug,
	// canonical(new) == new, no mismatch, no dispatch, no further
	// side effects. This is the steady-state property the
	// applied=false guard used to enforce (incorrectly, by eating
	// every downstream call). Now it falls out of the probe's
	// canonical-name compare. ───────────────────────────────────────
	probe.Tick(context.Background())

	if publisher.calls != 1 {
		t.Errorf("after tick #3: SSE storm — publisher.calls = %d, want 1 (probe must not redispatch when canonical matches)", publisher.calls)
	}
	if purger.calls != 1 {
		t.Errorf("after tick #3: purger.calls = %d, want 1", purger.calls)
	}
	if store.calls != 2 {
		t.Errorf("after tick #3: store.calls = %d, want 2 (one per tick that dispatched; tick #3 should not have dispatched)", store.calls)
	}
}

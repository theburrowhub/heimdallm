package rename_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/heimdallm/daemon/internal/rename"
	"github.com/heimdallm/daemon/internal/sse"
)

// --- fakes ---------------------------------------------------------------

type fakeStore struct {
	calls    int
	applied  bool          // value to return from RenameRepo
	err      error         // error to return
	gotPairs [][2]string   // (old, new) observed
}

func (f *fakeStore) RenameRepo(oldRepo, newRepo string) (bool, error) {
	f.calls++
	f.gotPairs = append(f.gotPairs, [2]string{oldRepo, newRepo})
	return f.applied, f.err
}

type fakePersister struct {
	calls    int
	err      error
	gotPath  string
	gotPairs [][2]string
}

func (f *fakePersister) Rename(path, oldRepo, newRepo string) error {
	f.calls++
	f.gotPath = path
	f.gotPairs = append(f.gotPairs, [2]string{oldRepo, newRepo})
	return f.err
}

type fakePurger struct {
	calls    int
	err      error
	gotRepo  string
	gotDir   string
}

func (f *fakePurger) Purge(ctx context.Context, repo, cloneDir string) error {
	f.calls++
	f.gotRepo = repo
	f.gotDir = cloneDir
	return f.err
}

type fakePublisher struct {
	calls  int
	events []sse.Event
}

func (f *fakePublisher) Publish(e sse.Event) {
	f.calls++
	f.events = append(f.events, e)
}

// applyOp tracks invocations of the in-memory config mutator the
// reconciler runs under cfgMu.
type applyOp struct {
	calls    int
	gotPairs [][2]string
}

func (a *applyOp) op(oldRepo, newRepo string) {
	a.calls++
	a.gotPairs = append(a.gotPairs, [2]string{oldRepo, newRepo})
}

func newDeps(store *fakeStore, persister *fakePersister, purger *fakePurger,
	publisher *fakePublisher, op *applyOp,
) rename.Deps {
	return rename.Deps{
		Store:     store,
		Persister: persister,
		Purger:    purger,
		Publisher: publisher,
		ApplyConfig: op.op,
		CfgMu:    &sync.Mutex{},
		CfgPath:  "/tmp/heimdallm/config.toml",
		CloneDir: "/tmp/heimdallm/clones",
	}
}

// --- tests ---------------------------------------------------------------

func TestReconciler_Run_FullPipeline(t *testing.T) {
	store := &fakeStore{applied: true}
	persister := &fakePersister{}
	purger := &fakePurger{}
	publisher := &fakePublisher{}
	op := &applyOp{}
	r := rename.NewReconciler(newDeps(store, persister, purger, publisher, op))

	if err := r.Run(context.Background(), "acme/old", "acme/new"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if store.calls != 1 || store.gotPairs[0] != [2]string{"acme/old", "acme/new"} {
		t.Errorf("store: %d calls, pairs=%v", store.calls, store.gotPairs)
	}
	if op.calls != 1 || op.gotPairs[0] != [2]string{"acme/old", "acme/new"} {
		t.Errorf("ApplyConfig: %d calls, pairs=%v", op.calls, op.gotPairs)
	}
	if persister.calls != 1 || persister.gotPath != "/tmp/heimdallm/config.toml" {
		t.Errorf("persister: %d calls, path=%q", persister.calls, persister.gotPath)
	}
	if purger.calls != 1 || purger.gotRepo != "acme/old" || purger.gotDir != "/tmp/heimdallm/clones" {
		t.Errorf("purger: %d calls, repo=%q dir=%q", purger.calls, purger.gotRepo, purger.gotDir)
	}
	if publisher.calls != 1 || publisher.events[0].Type != sse.EventRepoRenamed {
		t.Errorf("publisher: %d calls, events=%+v", publisher.calls, publisher.events)
	}
	// Payload must carry both slugs and worktree_purged=true.
	data := publisher.events[0].Data
	for _, want := range []string{`"old_repo":"acme/old"`, `"new_repo":"acme/new"`, `"worktree_purged":true`} {
		if !strings.Contains(data, want) {
			t.Errorf("payload missing %q: %s", want, data)
		}
	}
}

// TestReconciler_Run_RecoversFromPersisterFailureOnRetry pins the
// partial-failure recovery contract. When a prior Run committed the
// store (audit row written) but failed at the persister, the next
// invocation sees applied=false from the store's idempotency guard
// — and MUST still complete config + purge + SSE, otherwise the
// rename is stuck forever with DB on the new slug but config TOML
// + worktree on the old.
//
// This is the bug that motivated removing the applied=false short-
// circuit at the reconciler entry point: see
// `daemon/internal/rename/reconciler.go` Run docstring.
func TestReconciler_Run_RecoversFromPersisterFailureOnRetry(t *testing.T) {
	store := &fakeStore{applied: true} // first Run: store moves rows
	persister := &fakePersister{err: errors.New("disk full")}
	purger := &fakePurger{}
	publisher := &fakePublisher{}
	op := &applyOp{}
	r := rename.NewReconciler(newDeps(store, persister, purger, publisher, op))

	// First Run: persister fails after store commit. Reconciler
	// returns the error; downstream surfaces (purge, SSE) are
	// skipped because cfgErr aborts the function before them.
	if err := r.Run(context.Background(), "acme/old", "acme/new"); err == nil {
		t.Fatal("first Run: expected persister failure to surface")
	}
	if op.calls != 1 || persister.calls != 1 {
		t.Errorf("first Run: op=%d persister=%d, want 1/1", op.calls, persister.calls)
	}
	if purger.calls != 0 || publisher.calls != 0 {
		t.Errorf("first Run leaked past persister failure: purger=%d publisher=%d",
			purger.calls, publisher.calls)
	}

	// Simulate the real store's idempotency guard on the retry: the
	// audit row is now present, so RenameRepo returns applied=false.
	// Persister succeeds this time.
	store.applied = false
	persister.err = nil

	// Second Run: MUST complete config + purge + SSE despite
	// applied=false. Pre-fix, the reconciler returned early at this
	// point and left the system stuck in partial-failure state.
	if err := r.Run(context.Background(), "acme/old", "acme/new"); err != nil {
		t.Fatalf("second Run (recovery): %v", err)
	}
	if op.calls != 2 {
		t.Errorf("ApplyConfig calls = %d, want 2 (rerun on retry)", op.calls)
	}
	if persister.calls != 2 {
		t.Errorf("persister.calls = %d, want 2", persister.calls)
	}
	if purger.calls != 1 {
		t.Errorf("purger.calls = %d, want 1 (only the recovery attempt)", purger.calls)
	}
	if publisher.calls != 1 {
		t.Errorf("publisher.calls = %d, want 1 (only the recovery attempt)", publisher.calls)
	}
	if !strings.Contains(publisher.events[0].Data, `"old_repo":"acme/old"`) {
		t.Errorf("recovery SSE payload missing old_repo: %s", publisher.events[0].Data)
	}
}

// TestReconciler_Run_NoSSEStormOnRepeatedSuccessfulRuns pins the
// other side of the contract: in a healthy steady state where the
// store / config / worktree all agree, the probe should not actually
// dispatch Run at all (canonical name matches configured slug, no
// mismatch). But if a buggy caller drives Run repeatedly with the
// same pair after a successful rename, the downstream re-runs are
// bounded by their own idempotency — the test asserts no error and
// a single SSE per call (idempotency at the TOML / Purge layer is
// already covered in their own packages).
func TestReconciler_Run_RepeatedRunsConverge(t *testing.T) {
	store := &fakeStore{applied: true}
	persister := &fakePersister{}
	purger := &fakePurger{}
	publisher := &fakePublisher{}
	op := &applyOp{}
	r := rename.NewReconciler(newDeps(store, persister, purger, publisher, op))

	if err := r.Run(context.Background(), "acme/old", "acme/new"); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	// Second Run with the store now returning applied=false (audit
	// row present) — still runs the downstream and emits SSE. That
	// is the documented behaviour; callers that don't want this
	// should rely on the probe's canonical-name compare to avoid
	// re-dispatching when state is settled.
	store.applied = false
	if err := r.Run(context.Background(), "acme/old", "acme/new"); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if publisher.calls != 2 {
		t.Errorf("publisher.calls = %d, want 2 (one per Run)", publisher.calls)
	}
}

func TestReconciler_Run_ValidationRejectsEmpty(t *testing.T) {
	store := &fakeStore{}
	r := rename.NewReconciler(newDeps(store, &fakePersister{}, &fakePurger{}, &fakePublisher{}, &applyOp{}))

	cases := []struct {
		old, new string
	}{
		{"", "acme/new"},
		{"acme/old", ""},
		{"acme/old", "acme/old"},
		{"malformed", "acme/new"},
		{"acme/old", "no-slash"},
	}
	for _, c := range cases {
		err := r.Run(context.Background(), c.old, c.new)
		if err == nil {
			t.Errorf("Run(%q, %q) expected error, got nil", c.old, c.new)
		}
	}
	if store.calls != 0 {
		t.Errorf("validation leaked into store: calls=%d", store.calls)
	}
}

func TestReconciler_Run_StoreErrorAbortsConfig(t *testing.T) {
	// When the store call fails, the reconciler MUST NOT mutate the
	// in-memory config or rewrite the TOML file — those would create
	// disk/DB drift on restart.
	storeErr := errors.New("simulated SQLite failure")
	store := &fakeStore{err: storeErr}
	persister := &fakePersister{}
	op := &applyOp{}
	r := rename.NewReconciler(newDeps(store, persister, &fakePurger{}, &fakePublisher{}, op))

	err := r.Run(context.Background(), "acme/old", "acme/new")
	if !errors.Is(err, storeErr) {
		t.Fatalf("Run err = %v, want wrapping %v", err, storeErr)
	}
	if op.calls != 0 || persister.calls != 0 {
		t.Errorf("config touched after store error: op=%d persister=%d", op.calls, persister.calls)
	}
}

func TestReconciler_Run_WorktreePurgeFailure_NotFatal(t *testing.T) {
	// A failed worktree purge MUST NOT roll back the store/config
	// rename — the next acquire of the new slug will clone fresh
	// anyway. The SSE payload signals the inconsistency to operators.
	store := &fakeStore{applied: true}
	persister := &fakePersister{}
	purger := &fakePurger{err: errors.New("purge failed: device busy")}
	publisher := &fakePublisher{}
	op := &applyOp{}
	r := rename.NewReconciler(newDeps(store, persister, purger, publisher, op))

	if err := r.Run(context.Background(), "acme/old", "acme/new"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if publisher.calls != 1 {
		t.Fatalf("publisher: %d calls, want 1", publisher.calls)
	}
	if !strings.Contains(publisher.events[0].Data, `"worktree_purged":false`) {
		t.Errorf("payload missing worktree_purged=false: %s", publisher.events[0].Data)
	}
}


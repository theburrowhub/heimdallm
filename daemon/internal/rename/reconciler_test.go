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

func TestReconciler_Run_Idempotent(t *testing.T) {
	// Store says "already done" by returning applied=false. The
	// reconciler must short-circuit BEFORE touching config/persister/
	// purger/publisher to avoid an SSE storm on every probe tick after
	// a successful rename.
	store := &fakeStore{applied: false}
	persister := &fakePersister{}
	purger := &fakePurger{}
	publisher := &fakePublisher{}
	op := &applyOp{}
	r := rename.NewReconciler(newDeps(store, persister, purger, publisher, op))

	if err := r.Run(context.Background(), "acme/old", "acme/new"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if op.calls != 0 || persister.calls != 0 || purger.calls != 0 || publisher.calls != 0 {
		t.Errorf("idempotent path leaked side effects: op=%d persister=%d purger=%d publisher=%d",
			op.calls, persister.calls, purger.calls, publisher.calls)
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


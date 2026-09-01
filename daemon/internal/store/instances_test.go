package store

import (
	"strings"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/instances"
)

func newInstanceStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSaveAndLoadInstanceState(t *testing.T) {
	s := newInstanceStore(t)
	seen := time.Date(2026, 9, 1, 10, 30, 0, 0, time.UTC)

	want := instances.State{
		InstanceID: "srv-a", Name: "Server A", Reachable: true, Status: "ok",
		Version: "0.9.0", Role: "worker", RemoteInstanceID: "srv-a",
		UptimeSeconds: 120.5, LastSeenAt: seen,
	}
	if err := s.SaveInstanceState(want); err != nil {
		t.Fatalf("SaveInstanceState: %v", err)
	}

	got, err := s.LoadInstanceStates()
	if err != nil {
		t.Fatalf("LoadInstanceStates: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d states, want 1", len(got))
	}
	g := got[0]
	if g.InstanceID != want.InstanceID || g.Name != want.Name || !g.Reachable ||
		g.Status != want.Status || g.Version != want.Version || g.Role != want.Role ||
		g.RemoteInstanceID != want.RemoteInstanceID || g.UptimeSeconds != want.UptimeSeconds {
		t.Errorf("round trip = %+v, want %+v", g, want)
	}
	if !g.LastSeenAt.Equal(seen) {
		t.Errorf("LastSeenAt = %v, want %v", g.LastSeenAt, seen)
	}
}

// Every probe upserts. A row per probe per instance would grow without bound
// for information nobody reads.
func TestSaveInstanceStateUpserts(t *testing.T) {
	s := newInstanceStore(t)
	base := instances.State{InstanceID: "srv-a", Name: "Server A", Reachable: true, Version: "0.9.0"}
	if err := s.SaveInstanceState(base); err != nil {
		t.Fatal(err)
	}

	base.Reachable = false
	base.LastError = "connection refused"
	base.ConsecutiveFailures = 3
	if err := s.SaveInstanceState(base); err != nil {
		t.Fatal(err)
	}

	got, err := s.LoadInstanceStates()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows, want the row to have been updated in place", len(got))
	}
	if got[0].Reachable || got[0].ConsecutiveFailures != 3 || got[0].LastError != "connection refused" {
		t.Errorf("state = %+v, want the failure recorded", got[0])
	}
}

func TestSaveInstanceStateRequiresID(t *testing.T) {
	s := newInstanceStore(t)
	if err := s.SaveInstanceState(instances.State{Name: "nameless"}); err == nil {
		t.Error("SaveInstanceState with no id = nil, want an error")
	}
}

func TestLoadInstanceStatesSorted(t *testing.T) {
	s := newInstanceStore(t)
	for _, id := range []string{"zulu", "alpha", "mike"} {
		if err := s.SaveInstanceState(instances.State{InstanceID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.LoadInstanceStates()
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(got))
	for _, st := range got {
		ids = append(ids, st.InstanceID)
	}
	if strings.Join(ids, ",") != "alpha,mike,zulu" {
		t.Errorf("order = %v, want sorted by id", ids)
	}
}

// A never-probed row has no timestamp; a zero time is the right answer, not a
// hard error that would blank the whole listing.
func TestLoadInstanceStatesToleratesMissingTimestamp(t *testing.T) {
	s := newInstanceStore(t)
	if err := s.SaveInstanceState(instances.State{InstanceID: "fresh", Name: "fresh"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadInstanceStates()
	if err != nil {
		t.Fatalf("LoadInstanceStates: %v", err)
	}
	if len(got) != 1 || !got[0].LastSeenAt.IsZero() {
		t.Errorf("state = %+v, want a zero LastSeenAt rather than an error", got)
	}
}

func TestDeleteInstanceState(t *testing.T) {
	s := newInstanceStore(t)
	for _, id := range []string{"a", "b"} {
		if err := s.SaveInstanceState(instances.State{InstanceID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteInstanceState("a"); err != nil {
		t.Fatalf("DeleteInstanceState: %v", err)
	}
	got, _ := s.LoadInstanceStates()
	if len(got) != 1 || got[0].InstanceID != "b" {
		t.Errorf("states = %+v, want only b", got)
	}
	// Deleting something that is not there is not an error: deregistering an
	// instance that was never probed must not fail.
	if err := s.DeleteInstanceState("ghost"); err != nil {
		t.Errorf("DeleteInstanceState(ghost) = %v, want nil", err)
	}
}

func TestClaimDispatchDeduplicates(t *testing.T) {
	s := newInstanceStore(t)

	claimed, err := s.ClaimDispatch("review", "acme/tools#42", "sha1", "srv-a")
	if err != nil {
		t.Fatalf("ClaimDispatch: %v", err)
	}
	if !claimed {
		t.Fatal("first claim = false, want true")
	}

	// A retry, or two GUI clients clicking at once, must not dispatch twice.
	claimed, err = s.ClaimDispatch("review", "acme/tools#42", "sha1", "srv-b")
	if err != nil {
		t.Fatalf("ClaimDispatch: %v", err)
	}
	if claimed {
		t.Error("second claim for the same commit = true, want false")
	}

	target, found, err := s.DispatchTarget("review", "acme/tools#42", "sha1")
	if err != nil || !found {
		t.Fatalf("DispatchTarget = %q, %v, %v", target, found, err)
	}
	if target != "srv-a" {
		t.Errorf("target = %q, want the original claimant srv-a", target)
	}
}

func TestClaimDispatchNewCommitIsANewOperation(t *testing.T) {
	s := newInstanceStore(t)
	if ok, _ := s.ClaimDispatch("review", "acme/tools#42", "sha1", "srv-a"); !ok {
		t.Fatal("first claim failed")
	}
	// A new push is legitimately a new operation and must claim cleanly.
	ok, err := s.ClaimDispatch("review", "acme/tools#42", "sha2", "srv-b")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("claim for a new head SHA = false, want true")
	}
}

func TestClaimDispatchOpsAreIndependent(t *testing.T) {
	s := newInstanceStore(t)
	if ok, _ := s.ClaimDispatch("review", "acme/tools#42", "sha1", "srv-a"); !ok {
		t.Fatal("review claim failed")
	}
	// Reviewing and merging the same PR are different operations.
	ok, err := s.ClaimDispatch("merge", "acme/tools#42", "sha1", "srv-b")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("merge claim on an already-reviewed PR = false, want true")
	}
}

func TestDispatchTargetMissing(t *testing.T) {
	s := newInstanceStore(t)
	target, found, err := s.DispatchTarget("review", "nothing/here#1", "sha")
	if err != nil {
		t.Fatalf("DispatchTarget = %v, want no error for a missing row", err)
	}
	if found || target != "" {
		t.Errorf("DispatchTarget = %q, %v; want empty and not found", target, found)
	}
}

func TestPruneDispatches(t *testing.T) {
	s := newInstanceStore(t)
	if _, err := s.ClaimDispatch("review", "acme/a#1", "sha", "srv-a"); err != nil {
		t.Fatal(err)
	}

	// Nothing older than an hour ago yet.
	n, err := s.PruneDispatches(time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("PruneDispatches: %v", err)
	}
	if n != 0 {
		t.Errorf("pruned %d rows, want 0", n)
	}

	n, err = s.PruneDispatches(time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("PruneDispatches: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned %d rows, want 1", n)
	}
	// Once pruned, the same operation can be dispatched again.
	if ok, _ := s.ClaimDispatch("review", "acme/a#1", "sha", "srv-b"); !ok {
		t.Error("claim after pruning = false, want true")
	}
}

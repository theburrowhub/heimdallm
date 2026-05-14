package rename_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/rename"
	"github.com/heimdallm/daemon/internal/sse"
)

// newNonMonitoredProbe is a thin wrapper around rename.NewProbe that
// supplies the non-monitored axis (NonMonitored + Publisher). Tests
// pass nil for Repos because the non-monitored path runs in addition
// to the monitored one and can be exercised in isolation by leaving
// the monitored slug list empty.
func newNonMonitoredProbe(t *testing.T, canonical *fakeCanonical,
	publisher *fakePublisher, nonMonitored []string,
) *rename.Probe {
	t.Helper()
	return rename.NewProbe(rename.ProbeDeps{
		Probe:        canonical,
		Dispatcher:   &fakeDispatcher{}, // unused on this axis
		Repos:        func() []string { return nil },
		NonMonitored: func() []string { return nonMonitored },
		Publisher:    publisher,
	})
}

func TestProbe_NonMonitored_EmitsSSEAndWarnsOnStale(t *testing.T) {
	canonical := &fakeCanonical{
		results: map[string]canonicalResult{
			"acme/parked": {canonical: "acme/parked-v2"},
		},
	}
	publisher := &fakePublisher{}
	p := newNonMonitoredProbe(t, canonical, publisher, []string{"acme/parked"})

	p.Tick(context.Background())

	if publisher.calls != 1 {
		t.Fatalf("publisher.calls = %d, want 1", publisher.calls)
	}
	if publisher.events[0].Type != sse.EventRepoNonMonitoredStale {
		t.Errorf("event type = %q, want %q", publisher.events[0].Type, sse.EventRepoNonMonitoredStale)
	}
	for _, want := range []string{`"old_repo":"acme/parked"`, `"new_repo":"acme/parked-v2"`} {
		if !strings.Contains(publisher.events[0].Data, want) {
			t.Errorf("payload missing %q: %s", want, publisher.events[0].Data)
		}
	}
}

func TestProbe_NonMonitored_NoOpWhenCanonicalMatches(t *testing.T) {
	canonical := &fakeCanonical{
		results: map[string]canonicalResult{
			"acme/parked": {canonical: "acme/parked"},
		},
	}
	publisher := &fakePublisher{}
	p := newNonMonitoredProbe(t, canonical, publisher, []string{"acme/parked"})

	p.Tick(context.Background())

	if publisher.calls != 0 {
		t.Errorf("publisher.calls = %d, want 0 when canonical matches", publisher.calls)
	}
}

// TestProbe_NonMonitored_DedupesWarningsAcrossTicks pins the cost
// guard: a non-monitored stale slug must not emit one SSE event per
// probe tick forever. The probe remembers warned (old, new) pairs in
// memory and only re-emits if the canonical changes (chained rename)
// or the entry is removed and re-added.
func TestProbe_NonMonitored_DedupesWarningsAcrossTicks(t *testing.T) {
	canonical := &fakeCanonical{
		results: map[string]canonicalResult{
			"acme/parked": {canonical: "acme/parked-v2"},
		},
	}
	publisher := &fakePublisher{}
	p := newNonMonitoredProbe(t, canonical, publisher, []string{"acme/parked"})

	p.Tick(context.Background())
	p.Tick(context.Background())
	p.Tick(context.Background())

	if publisher.calls != 1 {
		t.Errorf("publisher.calls = %d, want 1 (deduped across 3 ticks)", publisher.calls)
	}
}

// TestProbe_NonMonitored_ReWarnsWhenCanonicalChanges pins the chained
// rename path: if GitHub now reports a NEW canonical name for the
// same stale slug, we want to warn again so the operator sees the
// fresh suggestion instead of acting on a stale hint.
func TestProbe_NonMonitored_ReWarnsWhenCanonicalChanges(t *testing.T) {
	canonical := &fakeCanonical{
		results: map[string]canonicalResult{
			"acme/parked": {canonical: "acme/parked-v2"},
		},
	}
	publisher := &fakePublisher{}
	p := newNonMonitoredProbe(t, canonical, publisher, []string{"acme/parked"})

	p.Tick(context.Background())
	if publisher.calls != 1 {
		t.Fatalf("after tick 1: publisher.calls = %d, want 1", publisher.calls)
	}

	// Simulate a second upstream rename: parked-v2 → final. GitHub
	// follows the chain, so the canonical for `acme/parked` becomes
	// the new tip.
	canonical.results["acme/parked"] = canonicalResult{canonical: "acme/final"}

	p.Tick(context.Background())
	if publisher.calls != 2 {
		t.Errorf("after tick 2 with new canonical: publisher.calls = %d, want 2", publisher.calls)
	}
	if !strings.Contains(publisher.events[1].Data, `"new_repo":"acme/final"`) {
		t.Errorf("re-warn payload should carry the new canonical: %s", publisher.events[1].Data)
	}
}

// TestProbe_NonMonitored_ReWarnsAfterEntryRemovedAndReAdded pins the
// dedup-set cleanup invariant: if the operator removes the slug from
// non_monitored and later re-adds it (with the rename still pending),
// the probe must emit a fresh warning instead of treating it as
// already-warned. Achieved by garbage-collecting nmWarned entries
// that are no longer in the current non_monitored snapshot.
func TestProbe_NonMonitored_ReWarnsAfterEntryRemovedAndReAdded(t *testing.T) {
	canonical := &fakeCanonical{
		results: map[string]canonicalResult{
			"acme/parked": {canonical: "acme/parked-v2"},
		},
	}
	publisher := &fakePublisher{}

	// Closure-controlled list so the test can simulate operator edits.
	var list []string
	p := rename.NewProbe(rename.ProbeDeps{
		Probe:        canonical,
		Dispatcher:   &fakeDispatcher{},
		Repos:        func() []string { return nil },
		NonMonitored: func() []string { return append([]string(nil), list...) },
		Publisher:    publisher,
	})

	list = []string{"acme/parked"}
	p.Tick(context.Background())
	if publisher.calls != 1 {
		t.Fatalf("after first tick: publisher.calls = %d, want 1", publisher.calls)
	}

	// Operator removes the entry: probe must drop it from the warned
	// set so a re-add re-warns.
	list = nil
	p.Tick(context.Background())
	if publisher.calls != 1 {
		t.Errorf("after removal tick: publisher.calls = %d, want still 1 (no entry to probe)", publisher.calls)
	}

	// Operator re-adds the same entry — must re-warn.
	list = []string{"acme/parked"}
	p.Tick(context.Background())
	if publisher.calls != 2 {
		t.Errorf("after re-add tick: publisher.calls = %d, want 2 (fresh warning)", publisher.calls)
	}
}

func TestProbe_NonMonitored_404DoesNotEmit(t *testing.T) {
	// A 404 means the non-monitored repo was deleted upstream, not
	// renamed. We log and move on — no rename-suggestion SSE.
	apiErr := &gh.APIError{StatusCode: http.StatusNotFound, Body: "Not Found"}
	canonical := &fakeCanonical{
		results: map[string]canonicalResult{
			"acme/deleted": {err: apiErr},
		},
	}
	publisher := &fakePublisher{}
	p := newNonMonitoredProbe(t, canonical, publisher, []string{"acme/deleted"})

	p.Tick(context.Background())

	if publisher.calls != 0 {
		t.Errorf("publisher.calls = %d, want 0 on 404 (deleted, not renamed)", publisher.calls)
	}
}

// TestProbe_NonMonitored_NilFunc_NoCalls pins backward-compat for
// callers that wire the probe without the non-monitored axis (e.g.,
// existing tests). When NonMonitored is nil, the probe must skip
// the scan entirely without panicking.
func TestProbe_NonMonitored_NilFunc_NoCalls(t *testing.T) {
	canonical := &fakeCanonical{}
	publisher := &fakePublisher{}
	p := rename.NewProbe(rename.ProbeDeps{
		Probe:      canonical,
		Dispatcher: &fakeDispatcher{},
		Repos:      func() []string { return nil },
		// NonMonitored intentionally nil
		Publisher: publisher,
	})

	p.Tick(context.Background())
	if canonical.calls != 0 {
		t.Errorf("canonical.calls = %d, want 0 (no monitored or non-monitored repos)", canonical.calls)
	}
	if publisher.calls != 0 {
		t.Errorf("publisher.calls = %d, want 0", publisher.calls)
	}
}

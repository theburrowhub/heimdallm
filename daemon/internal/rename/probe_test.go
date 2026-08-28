package rename_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/rename"
)

// fakeCanonical is the GH-side dep for the probe: for each `repo`
// queried, returns either canonical, err. Keyed by the input slug.
type fakeCanonical struct {
	results map[string]canonicalResult
	calls   int
	mu      sync.Mutex
}

type canonicalResult struct {
	canonical string
	err       error
}

func (f *fakeCanonical) GetCanonicalFullName(repo string) (string, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	r := f.results[repo]
	return r.canonical, r.err
}

// callCount returns f.calls under the mutex. Use from any test goroutine
// that may run concurrently with GetCanonicalFullName (e.g. Run-based
// tests) instead of inlining lock/read/unlock at each call site.
func (f *fakeCanonical) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeDispatcher records reconciler invocations so the test can pin
// which (old, new) pairs the probe dispatched. Concrete *Reconciler
// is too heavy to construct in a probe test; the Dispatcher interface
// keeps the probe loosely coupled.
type fakeDispatcher struct {
	calls    int
	gotPairs [][2]string
	err      error
	mu       sync.Mutex
}

func (f *fakeDispatcher) Run(_ context.Context, oldRepo, newRepo string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.gotPairs = append(f.gotPairs, [2]string{oldRepo, newRepo})
	return f.err
}

// callCount returns f.calls under the mutex. Use from any test goroutine
// that may run concurrently with Run (e.g. Run-based tests) instead of
// inlining lock/read/unlock at each call site.
func (f *fakeDispatcher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func newProbe(t *testing.T, canonical *fakeCanonical, dispatcher *fakeDispatcher,
	repos []string,
) *rename.Probe {
	t.Helper()
	return rename.NewProbe(rename.ProbeDeps{
		Probe:      canonical,
		Dispatcher: dispatcher,
		Repos:      func() []string { return repos },
	})
}

func TestRenameProbe_DispatchesReconcilerOnMismatch(t *testing.T) {
	canonical := &fakeCanonical{
		results: map[string]canonicalResult{
			"acme/old": {canonical: "acme/new"},
		},
	}
	dispatcher := &fakeDispatcher{}
	p := newProbe(t, canonical, dispatcher, []string{"acme/old"})

	p.Tick(context.Background())

	if dispatcher.calls != 1 {
		t.Fatalf("dispatcher.calls = %d, want 1", dispatcher.calls)
	}
	if dispatcher.gotPairs[0] != [2]string{"acme/old", "acme/new"} {
		t.Errorf("dispatched pair = %v, want (acme/old, acme/new)", dispatcher.gotPairs[0])
	}
}

func TestRenameProbe_NoOpWhenCanonicalMatches(t *testing.T) {
	canonical := &fakeCanonical{
		results: map[string]canonicalResult{
			"acme/foo": {canonical: "acme/foo"},
			"acme/bar": {canonical: "acme/bar"},
		},
	}
	dispatcher := &fakeDispatcher{}
	p := newProbe(t, canonical, dispatcher, []string{"acme/foo", "acme/bar"})

	p.Tick(context.Background())

	if dispatcher.calls != 0 {
		t.Errorf("dispatcher.calls = %d, want 0 (no mismatches)", dispatcher.calls)
	}
	if canonical.calls != 2 {
		t.Errorf("canonical.calls = %d, want 2 (one per repo)", canonical.calls)
	}
}

func TestRenameProbe_404FromGH_DoesNotDispatch(t *testing.T) {
	// A 404 means the repo was deleted, not renamed. The probe MUST
	// NOT dispatch the reconciler (renaming to "" would corrupt the
	// store + config). Out-of-scope for this PR: emitting a separate
	// "repo unreachable" SSE event; for now the probe logs and skips.
	apiErr := &gh.APIError{StatusCode: http.StatusNotFound, Body: "Not Found"}
	canonical := &fakeCanonical{
		results: map[string]canonicalResult{
			"acme/gone":    {err: apiErr},
			"acme/healthy": {canonical: "acme/healthy"},
			"acme/renamed": {canonical: "acme/new-name"},
		},
	}
	dispatcher := &fakeDispatcher{}
	p := newProbe(t, canonical, dispatcher,
		[]string{"acme/gone", "acme/healthy", "acme/renamed"})

	p.Tick(context.Background())

	// Only the actual rename triggers a dispatch; the 404 and the
	// match are skipped.
	if dispatcher.calls != 1 {
		t.Fatalf("dispatcher.calls = %d, want 1", dispatcher.calls)
	}
	if dispatcher.gotPairs[0] != [2]string{"acme/renamed", "acme/new-name"} {
		t.Errorf("dispatched pair = %v, want (acme/renamed, acme/new-name)", dispatcher.gotPairs[0])
	}
}

// TestRenameProbe_Run_FiresInitialTickBeforeIntervalElapses pins
// the "no offline-rename blind spot after restart" invariant: the
// first Tick runs immediately on Run start, not after one interval
// window. Without this, a rename that happened while the daemon
// was down sat undetected for up to `Interval` (default 1h) after
// the next start.
func TestRenameProbe_Run_FiresInitialTickBeforeIntervalElapses(t *testing.T) {
	canonical := &fakeCanonical{
		results: map[string]canonicalResult{
			"acme/old": {canonical: "acme/new"},
		},
	}
	dispatcher := &fakeDispatcher{}
	// Interval far longer than the test wait — any dispatch in the
	// test window must come from the initial Tick, not the loop.
	p := rename.NewProbe(rename.ProbeDeps{
		Probe:      canonical,
		Dispatcher: dispatcher,
		Repos:      func() []string { return []string{"acme/old"} },
		Interval:   time.Hour,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()

	// Poll for the dispatch caused by the initial Tick. Generous
	// budget so a slow CI host doesn't false-flag, but well below
	// the 1h interval that the loop tick would need.
	deadline := time.After(2 * time.Second)
	for {
		calls := canonical.callCount()
		dispatched := dispatcher.callCount()
		if calls >= 1 && dispatched >= 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("initial tick did not fire within budget: canonical.calls=%d, dispatcher.calls=%d",
				calls, dispatched)
		case <-time.After(10 * time.Millisecond):
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
}

// TestRenameProbe_NonAPIError_DoesNotDispatch belt-and-braces: a
// generic (network) error from GitHub also must not trigger a rename.
// The plan only spells out three tests but this one falls naturally
// out of the same defensive guard, so it tags along to lock the
// invariant. Kept light to honour "tests pin behaviour, not coverage".
func TestRenameProbe_NonAPIError_DoesNotDispatch(t *testing.T) {
	canonical := &fakeCanonical{
		results: map[string]canonicalResult{
			"acme/flaky": {err: errors.New("connection reset")},
		},
	}
	dispatcher := &fakeDispatcher{}
	p := newProbe(t, canonical, dispatcher, []string{"acme/flaky"})

	p.Tick(context.Background())

	if dispatcher.calls != 0 {
		t.Errorf("dispatcher.calls = %d, want 0 on transport error", dispatcher.calls)
	}
}

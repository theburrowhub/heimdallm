package main

import (
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/sse"
)

// captureStore records every SetConfig call's (key, value) pair so tests
// can assert not just that a write happened but what was written. The
// existing fakeDiscoveryStore tracks only keys, which isn't enough to
// prove the #183 regression (the bug was writing "null" to non_monitored).
type captureStore struct {
	listData map[string]string
	writes   map[string]string
}

func (c *captureStore) ListConfigs() (map[string]string, error) {
	if c.listData == nil {
		return map[string]string{}, nil
	}
	return c.listData, nil
}

func (c *captureStore) SetConfig(key, value string) (int64, error) {
	if c.writes == nil {
		c.writes = map[string]string{}
	}
	c.writes[key] = value
	return 1, nil
}

// TestProcessDiscoveredRepos_NilNonMonitoredNeverPersistsNull guards the
// #183 bug. Before the fix, a nil NonMonitored snapshot (race window
// between the caller's mutex unlock and this helper — a concurrent
// reload swaps *a.cfg to a fresh Config whose NonMonitored has not
// yet been repopulated from TOML) was marshalled as the literal string
// "null" and written to the store. MergeStoreLayer then parsed "null"
// as "no entries" on the next reload, wiping the operator's list.
//
// The fix: skip the write entirely when the snapshot is nil.
func TestProcessDiscoveredRepos_NilNonMonitoredNeverPersistsNull(t *testing.T) {
	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()

	// Store already has a valid non_monitored entry — the operator's
	// state we must preserve through the race.
	st := &captureStore{
		listData: map[string]string{
			"non_monitored": `["freepik-company/miles","freepik-company/profile-api"]`,
		},
	}

	processDiscoveredRepos(
		[]string{"org/new-repo"},                 // added — non-empty so we reach the persist block
		[]string{"org/existing", "org/new-repo"}, // reposSnap — non-nil
		nil,                                      // nonMonSnap — the race value
		st,
		broker,
		time.Unix(1_700_000_000, 0),
	)

	if _, wrote := st.writes["non_monitored"]; wrote {
		t.Errorf("non_monitored was written when snapshot is nil — got %q, want no write at all",
			st.writes["non_monitored"])
	}
}

// TestProcessDiscoveredRepos_NoopWhenReposUnchanged guards the second
// half of #183: a poll cycle with added>0 that doesn't actually shift
// the monitored/non-monitored lists should not rewrite the store rows.
// Each extra write is a new opportunity for the nil-race above to
// corrupt state.
func TestProcessDiscoveredRepos_NoopWhenReposUnchanged(t *testing.T) {
	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()

	reposSnap := []string{"org/a", "org/b"}
	nonMonSnap := []string{"org/c"}

	st := &captureStore{
		listData: map[string]string{
			// Exactly what processDiscoveredRepos would serialise. Any
			// whitespace / ordering mismatch would invalidate the guard;
			// in practice json.Marshal of []string is deterministic.
			"repositories":  `["org/a","org/b"]`,
			"non_monitored": `["org/c"]`,
		},
	}

	processDiscoveredRepos(
		[]string{"org/a"}, // non-empty so we reach the persist block
		reposSnap,
		nonMonSnap,
		st,
		broker,
		time.Unix(1_700_000_000, 0),
	)

	if v, wrote := st.writes["repositories"]; wrote {
		t.Errorf("repositories rewritten despite no change — got %q", v)
	}
	if v, wrote := st.writes["non_monitored"]; wrote {
		t.Errorf("non_monitored rewritten despite no change — got %q", v)
	}
}

// TestProcessDiscoveredRepos_WritesOnActualChange is the happy-path
// counterpart: when the caller passes a snapshot that differs from the
// existing store row, the write MUST go through. Ensures the diff
// optimisation doesn't swallow legitimate updates.
func TestProcessDiscoveredRepos_WritesOnActualChange(t *testing.T) {
	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()

	st := &captureStore{
		listData: map[string]string{
			"repositories":  `["org/a"]`,
			"non_monitored": `[]`,
		},
	}

	processDiscoveredRepos(
		[]string{"org/b"}, // the new repo
		[]string{"org/a", "org/b"},
		[]string{}, // empty but non-nil — write should happen if different from existing
		st,
		broker,
		time.Unix(1_700_000_000, 0),
	)

	if got, want := st.writes["repositories"], `["org/a","org/b"]`; got != want {
		t.Errorf("repositories = %q, want %q", got, want)
	}
	// Existing non_monitored was already "[]" so no write expected.
	if v, wrote := st.writes["non_monitored"]; wrote {
		t.Errorf("non_monitored rewritten despite no change — got %q", v)
	}
}

package main

import (
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/sse"
)

// fakeDiscoveryStore implements discoveryStore with configurable error
// injection so we can exercise the error-handling paths in
// processDiscoveredRepos without spinning up a real *store.Store.
type fakeDiscoveryStore struct {
	listErr       error
	listData      map[string]string
	setConfigKeys []string // records keys in write order
}

func (f *fakeDiscoveryStore) ListConfigs() (map[string]string, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	if f.listData == nil {
		return map[string]string{}, nil
	}
	return f.listData, nil
}

func (f *fakeDiscoveryStore) SetConfig(key, value string) (int64, error) {
	f.setConfigKeys = append(f.setConfigKeys, key)
	return 1, nil
}

// TestProcessDiscoveredRepos_SkipsFirstSeenWriteOnListConfigsError guards the
// HIGH-severity data-loss bug flagged in PR 139 review:
//
// If ListConfigs() returns an error (transient DB failure), the original code
// silently dropped the error and proceeded with an empty FirstSeenMap, then
// wrote that empty map back via SetConfig("repo_first_seen", ...), permanently
// erasing every previously-stored first-seen timestamp.
//
// The fix bails out of the first-seen update (logging a warning) when
// ListConfigs fails, leaving the existing store row untouched.
func TestProcessDiscoveredRepos_SkipsFirstSeenWriteOnListConfigsError(t *testing.T) {
	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()

	st := &fakeDiscoveryStore{
		listErr: errFakeStore("simulated DB unavailable"),
	}

	processDiscoveredRepos(
		[]string{"a/new"},
		[]string{"a/new"},
		nil,
		st,
		broker,
		time.Unix(1_700_000_000, 0),
	)

	for _, k := range st.setConfigKeys {
		if k == "repo_first_seen" {
			t.Fatalf("repo_first_seen must NOT be written when ListConfigs fails; writes=%v", st.setConfigKeys)
		}
	}
}

// TestProcessDiscoveredRepos_SkipsFirstSeenWriteOnParseError covers the second
// failure mode: ListConfigs returns corrupted JSON for the repo_first_seen row.
// ParseFirstSeen fails, and the original code silently produced an empty map
// that would then overwrite the (still-intact) stored JSON. The fix treats
// parse errors identically to list errors: log and skip.
func TestProcessDiscoveredRepos_SkipsFirstSeenWriteOnParseError(t *testing.T) {
	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()

	st := &fakeDiscoveryStore{
		listData: map[string]string{
			"repo_first_seen": "{not valid json",
		},
	}

	processDiscoveredRepos(
		[]string{"a/new"},
		[]string{"a/new"},
		nil,
		st,
		broker,
		time.Unix(1_700_000_000, 0),
	)

	for _, k := range st.setConfigKeys {
		if k == "repo_first_seen" {
			t.Fatalf("repo_first_seen must NOT be written when ParseFirstSeen fails; writes=%v", st.setConfigKeys)
		}
	}
}

type errFakeStore string

func (e errFakeStore) Error() string { return string(e) }

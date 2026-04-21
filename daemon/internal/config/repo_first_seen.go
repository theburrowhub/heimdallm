// First-seen timestamps for repos auto-discovered by the daemon. Persisted
// as JSON under the `repo_first_seen` key in the K/V config table, same
// pattern as `repositories` and `non_monitored` (see store.go).
package config

import (
	"encoding/json"
	"time"
)

// FirstSeenMap maps "org/repo" to the timestamp the daemon first discovered
// that repo. Unix-second precision is enough — the UI only uses this to
// show a NEW badge.
type FirstSeenMap map[string]time.Time

// Mark records the first-seen time for a repo. Idempotent: if the repo is
// already present, the existing timestamp wins.
func (m FirstSeenMap) Mark(repo string, t time.Time) {
	if _, exists := m[repo]; exists {
		return
	}
	m[repo] = t
}

// Marshal serialises the map to a JSON string of {repo: unix_seconds}.
func (m FirstSeenMap) Marshal() (string, error) {
	raw := make(map[string]int64, len(m))
	for k, v := range m {
		raw[k] = v.Unix()
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ParseFirstSeen decodes the JSON string returned by Marshal.
// Empty string → empty map (first call on a fresh DB).
func ParseFirstSeen(raw string) (FirstSeenMap, error) {
	out := FirstSeenMap{}
	if raw == "" {
		return out, nil
	}
	var tmp map[string]int64
	if err := json.Unmarshal([]byte(raw), &tmp); err != nil {
		return nil, err
	}
	for k, v := range tmp {
		out[k] = time.Unix(v, 0)
	}
	return out, nil
}

package github

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Expired membership answers are dropped on the next write, so the cache
// tracks recent authors instead of growing for the daemon's lifetime.
func TestIsTeamMember_PrunesExpiredEntries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"state":"active"}`))
	}))
	defer srv.Close()
	c := NewClient("fake", WithBaseURL(srv.URL))
	c.members.m = map[string]memberEntry{
		"acme/core/old":    {member: true, fetchedAt: time.Now().Add(-2 * memberCacheTTL)},
		"acme/core/recent": {member: true, fetchedAt: time.Now()},
	}
	if _, err := c.IsTeamMember("acme", "core", "new"); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.members.m["acme/core/old"]; ok {
		t.Error("expired entry kept")
	}
	if len(c.members.m) != 2 {
		t.Errorf("cache has %d entries, want recent + new", len(c.members.m))
	}
}

package main

import (
	"sync"
	"time"

	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/server"
)

// pollHealth tracks whether the PR poll reaches GitHub, for /health (#897).
// It is separate from the liveness timestamp, which every completed cycle
// updates whatever its outcome (#504), so the heartbeat and the health verdict
// keep their meaning.
type pollHealth struct {
	mu          sync.Mutex
	lastSuccess time.Time
	failures    int
	errKind     string
	errAt       time.Time
}

// record notes the outcome of one poll cycle that completed at at.
func (h *pollHealth) record(at time.Time, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err == nil {
		h.lastSuccess, h.failures, h.errKind, h.errAt = at.UTC(), 0, "", time.Time{}
		return
	}
	h.failures++
	h.errKind, h.errAt = gh.ClassifyPollError(err), at.UTC()
}

// fill copies the poll outcome into a health snapshot.
func (h *pollHealth) fill(snap *server.HealthSnapshot) {
	h.mu.Lock()
	defer h.mu.Unlock()
	snap.LastPollSuccessAt = h.lastSuccess
	snap.ConsecutivePollFailures = h.failures
	snap.LastPollErrorKind = h.errKind
	snap.LastPollErrorAt = h.errAt
}

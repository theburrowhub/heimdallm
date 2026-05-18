package tui

import (
	"errors"
	"testing"
	"time"

	"github.com/theburrowhub/heimdallm/cli/internal/api"
)

func TestDashboardWatchdogHealthyReset(t *testing.T) {
	d := NewDashboard("http://localhost:0", "", "test")
	oldCtx := d.sseCtx
	oldEvents := d.sseEvents
	oldSessionID := d.sseSessionID
	lastEventAt := time.Now().Add(-61 * time.Second)
	d.lastSSEEvent = lastEventAt

	model, cmd := d.Update(sseWatchdogMsg(time.Now()))
	d = model.(*Dashboard)
	if cmd == nil {
		t.Fatal("watchdog should schedule a health check")
	}
	if !d.sseStale || !d.sseHealthChecking {
		t.Fatalf("watchdog state: stale=%v healthChecking=%v", d.sseStale, d.sseHealthChecking)
	}

	model, cmd = d.Update(healthCheckMsg{lastEventAt: lastEventAt})
	d = model.(*Dashboard)
	if cmd == nil {
		t.Fatal("healthy stale stream should schedule SSE reconnect")
	}
	if oldCtx.Err() == nil {
		t.Fatal("old SSE context was not cancelled")
	}
	if d.sseEvents == oldEvents {
		t.Fatal("SSE channel was not replaced")
	}
	if d.sseSessionID != oldSessionID+1 {
		t.Fatalf("session id: got %d want %d", d.sseSessionID, oldSessionID+1)
	}
	if d.sseStale || d.sseHealthChecking {
		t.Fatalf("reset state: stale=%v healthChecking=%v", d.sseStale, d.sseHealthChecking)
	}
}

func TestDashboardDropsStaleHealthProbeAfterHeartbeat(t *testing.T) {
	d := NewDashboard("http://localhost:0", "", "test")
	d.connected = true
	lastEventAt := time.Now().Add(-61 * time.Second)
	d.lastSSEEvent = lastEventAt

	model, _ := d.Update(sseWatchdogMsg(time.Now()))
	d = model.(*Dashboard)
	if !d.sseHealthChecking {
		t.Fatal("watchdog did not start health check")
	}

	model, _ = d.Update(sseMsg{
		sessionID: d.sseSessionID,
		event:     api.SSEEvent{Type: "heartbeat", Data: "{}"},
	})
	d = model.(*Dashboard)
	if d.sseStale {
		t.Fatal("heartbeat did not clear stale state")
	}

	model, _ = d.Update(healthCheckMsg{err: errors.New("health failed"), lastEventAt: lastEventAt})
	d = model.(*Dashboard)
	if d.err != nil {
		t.Fatalf("stale health result should be ignored, got err %v", d.err)
	}
	if !d.connected {
		t.Fatal("stale health result should not mark dashboard disconnected")
	}
	if d.sseHealthChecking {
		t.Fatal("health check flag was not cleared")
	}
}

func TestDashboardDropsStaleReconnectAfterWatchdogReset(t *testing.T) {
	d := NewDashboard("http://localhost:0", "", "test")
	oldSessionID := d.sseSessionID

	model, cmd := d.Update(sseDisconnectMsg{sessionID: oldSessionID, err: errors.New("stream closed")})
	d = model.(*Dashboard)
	if cmd == nil {
		t.Fatal("disconnect should schedule delayed reconnect")
	}

	d.resetSSE()
	if d.sseSessionID == oldSessionID {
		t.Fatal("reset did not bump SSE session")
	}

	model, cmd = d.Update(sseReconnectMsg{sessionID: oldSessionID})
	d = model.(*Dashboard)
	if cmd != nil {
		t.Fatal("stale reconnect tick should be ignored")
	}
}

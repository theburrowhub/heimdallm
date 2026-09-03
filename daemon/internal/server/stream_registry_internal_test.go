package server

import "testing"

// http.Server.Shutdown only closes IDLE connections and then waits
// indefinitely for active ones to finish on their own — it never cancels an
// in-flight handler's own request context. A long-lived SSE handler blocked
// in a select on a broker/NATS channel would otherwise never notice shutdown
// began until the caller's ctx deadline expired, which is exactly what
// produced "server shutdown failed: context deadline exceeded" on every
// restart that had /events or /logs/stream open. registerStream/
// cancelActiveStreams close that gap: Shutdown cancels every registered
// stream's context directly, so its handler returns immediately.
func TestRegisterStreamAndCancelActiveStreams(t *testing.T) {
	srv := &Server{}

	canceled1, canceled2 := false, false
	unregister1 := srv.registerStream(func() { canceled1 = true })
	unregister2 := srv.registerStream(func() { canceled2 = true })

	srv.cancelActiveStreams()
	if !canceled1 || !canceled2 {
		t.Errorf("canceled = %v, %v; want both true", canceled1, canceled2)
	}

	// Cancelling twice (e.g. a second Shutdown call) must not panic or
	// double-invoke a cancel func that a caller may not tolerate.
	srv.cancelActiveStreams()

	unregister1()
	unregister2()
}

// unregister must remove exactly its own entry — a stream that finishes on
// its own (client disconnected) before Shutdown runs must not be cancelled
// again, and must not affect other still-open streams.
func TestUnregisterStreamRemovesOnlyItsOwnEntry(t *testing.T) {
	srv := &Server{}

	stillOpenCanceled := false
	unregisterFinished := srv.registerStream(func() { t.Error("finished stream was cancelled") })
	srv.registerStream(func() { stillOpenCanceled = true })

	unregisterFinished() // simulates the client disconnecting on its own

	srv.cancelActiveStreams()
	if !stillOpenCanceled {
		t.Error("the still-open stream was not cancelled")
	}
}

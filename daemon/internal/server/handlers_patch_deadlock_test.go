package server_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestPatchTOML_DoesNotHoldTOMLMuDuringReloadFn pins the no-deadlock
// invariant called out in the #489 review. Real-world scenario:
//
//  1. Operator hits PATCH /config (or any TOML-mutating endpoint).
//  2. patchTOML acquires tomlMu, writes the file.
//  3. patchTOML calls reloadFn — which cancels the pollers and waits
//     for the WaitGroup containing the rename probe goroutine.
//  4. The probe goroutine is mid-Reconciler.Run, blocked acquiring
//     tomlMu (shared with the server since the #489 race fix).
//  5. Without the fix, PATCH holds tomlMu while reloadFn blocks
//     waiting for the probe, the probe waits for tomlMu — deadlock.
//
// The fix releases tomlMu *before* invoking reloadFn. This test
// models the deadlock by having reloadFn attempt to acquire tomlMu
// itself; with the fix, the inner acquisition succeeds and PATCH
// returns. Without the fix, the inner acquisition blocks forever,
// PATCH never returns, and the test fails via the timeout.
func TestPatchTOML_DoesNotHoldTOMLMuDuringReloadFn(t *testing.T) {
	tomlContent := "[ai]\nprimary = \"claude\"\n"
	tomlPath := writeTempTOML(t, tomlContent)

	srv := setupServerWithToken(t, "test-token")
	srv.SetConfigPath(tomlPath)

	reloadAcquired := make(chan struct{}, 1)
	srv.SetReloadFn(func() error {
		// In production, reloadFn cancels pollers and waits on
		// oldWg.Wait(); the rename probe in that wait group holds
		// (or is acquiring) tomlMu. We simulate the wait-on-probe
		// edge with the probe's actual blocking primitive: an
		// attempt to acquire tomlMu. Pre-fix this blocked forever
		// because patchTOML still owned the mutex.
		acquired := make(chan struct{})
		go func() {
			srv.TOMLMu().Lock()
			srv.TOMLMu().Unlock()
			close(acquired)
		}()
		select {
		case <-acquired:
			reloadAcquired <- struct{}{}
			return nil
		case <-time.After(2 * time.Second):
			return errors.New("reloadFn: tomlMu still held by patchTOML (deadlock)")
		}
	})

	req := httptest.NewRequest("PATCH", "/config",
		strings.NewReader(`{"ai":{"primary":"openai"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		srv.Router().ServeHTTP(w, req)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("PATCH /config never returned — patchTOML deadlocked against reloadFn")
	}

	if w.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, body = %s", w.Code, w.Body.String())
	}
	select {
	case <-reloadAcquired:
	default:
		t.Fatal("reloadFn never confirmed it could acquire tomlMu — fix not applied")
	}
}

// TestPatchTOML_NoDeadlockWithPollerWaitingOnTOMLMu models the full
// production deadlock: a poller goroutine (modelling the rename
// probe) wants to acquire tomlMu, and reloadFn waits on that poller
// to finish (modelling oldWg.Wait). Pre-fix this is a strict 2-way
// deadlock; post-fix patchTOML's unlock breaks the chain.
func TestPatchTOML_NoDeadlockWithPollerWaitingOnTOMLMu(t *testing.T) {
	tomlContent := "[ai]\nprimary = \"claude\"\n"
	tomlPath := writeTempTOML(t, tomlContent)

	srv := setupServerWithToken(t, "test-token")
	srv.SetConfigPath(tomlPath)

	pollerStart := make(chan struct{})
	pollerDone := make(chan struct{})
	go func() {
		<-pollerStart
		// Probe-side acquire (mirror of Reconciler.Run's TOMLMu.Lock).
		srv.TOMLMu().Lock()
		srv.TOMLMu().Unlock()
		close(pollerDone)
	}()

	srv.SetReloadFn(func() error {
		// reloadFn signals the poller to start (the real reloadFn
		// cancels its context, which lets the next tick run; here
		// we just kick the goroutine off) and waits for it to exit
		// (mirroring oldWg.Wait).
		close(pollerStart)
		select {
		case <-pollerDone:
			return nil
		case <-time.After(2 * time.Second):
			return errors.New("reloadFn: poller did not finish — deadlock")
		}
	})

	req := httptest.NewRequest("PATCH", "/config",
		strings.NewReader(`{"ai":{"primary":"openai"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Heimdallm-Token", "test-token")
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		srv.Router().ServeHTTP(w, req)
		close(done)
	}()

	select {
	case <-done:
		if w.Code != http.StatusOK {
			t.Errorf("PATCH status = %d, body = %s", w.Code, w.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("3-way deadlock: PATCH holds tomlMu → reloadFn waits for poller → poller waits for tomlMu")
	}
}

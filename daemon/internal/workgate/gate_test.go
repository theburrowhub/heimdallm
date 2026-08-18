package workgate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPrepareDrainsWithoutCancellingActiveWork(t *testing.T) {
	g := New(time.Minute)
	review, err := g.Acquire(KindReview)
	if err != nil {
		t.Fatalf("Acquire review: %v", err)
	}
	implementation, err := g.Acquire(KindImplementation)
	if err != nil {
		t.Fatalf("Acquire implementation: %v", err)
	}

	snapshot, err := g.Prepare("owner-a")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if !snapshot.Draining || snapshot.LeaseID != "owner-a" || snapshot.Total() != 2 {
		t.Fatalf("Prepare snapshot = %+v, want owner-a draining with 2 active", snapshot)
	}
	if _, err := g.Acquire(KindIssue); !errors.Is(err, ErrDraining) {
		t.Fatalf("Acquire during drain error = %v, want ErrDraining", err)
	}

	review.Release()
	implementation.Release()
	if got := g.Status().Total(); got != 0 {
		t.Fatalf("active total after Release = %d, want 0", got)
	}
}

func TestPrepareRequiresOwnerAndOnlyOwnerCanRenewOrCancel(t *testing.T) {
	g := New(time.Minute)
	if _, err := g.Prepare(""); !errors.Is(err, ErrLeaseIDRequired) {
		t.Fatalf("Prepare without owner error = %v, want ErrLeaseIDRequired", err)
	}
	first, err := g.Prepare("owner-a")
	if err != nil {
		t.Fatalf("Prepare owner-a: %v", err)
	}
	if _, err := g.Prepare("owner-b"); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("Prepare owner-b error = %v, want ErrLeaseConflict", err)
	}
	if _, err := g.Cancel("owner-b"); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("Cancel owner-b error = %v, want ErrLeaseConflict", err)
	}
	if snapshot := g.Status(); !snapshot.Draining || snapshot.LeaseID != first.LeaseID {
		t.Fatalf("foreign request changed lease: %+v", snapshot)
	}

	renewed, err := g.Prepare("owner-a")
	if err != nil {
		t.Fatalf("renew owner-a: %v", err)
	}
	if renewed.LeaseID != first.LeaseID || renewed.LeaseExpiresAt.Before(first.LeaseExpiresAt) {
		t.Fatalf("renewed snapshot = %+v, first = %+v", renewed, first)
	}
	if _, err := g.Cancel("owner-a"); err != nil {
		t.Fatalf("Cancel owner-a: %v", err)
	}
	if g.Status().Draining {
		t.Fatal("owner cancel left gate draining")
	}
}

func TestLateCancelCannotOpenReplacementLease(t *testing.T) {
	g := New(time.Minute)
	if _, err := g.Prepare("owner-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Cancel("owner-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Prepare("owner-b"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Cancel("owner-a"); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("late cancel error = %v, want ErrLeaseConflict", err)
	}
	if snapshot := g.Status(); !snapshot.Draining || snapshot.LeaseID != "owner-b" {
		t.Fatalf("late cancel opened replacement lease: %+v", snapshot)
	}
}

func TestCancelWithoutActiveLeaseIsIdempotentAndEchoesOwner(t *testing.T) {
	g := New(time.Minute)

	snapshot, err := g.Cancel("owner-a")
	if err != nil {
		t.Fatalf("Cancel before Prepare: %v", err)
	}
	if snapshot.Draining || snapshot.LeaseID != "owner-a" || snapshot.Total() != 0 {
		t.Fatalf("Cancel before Prepare snapshot = %+v", snapshot)
	}
	if _, err := g.Acquire(KindReview); err != nil {
		t.Fatalf("idempotent Cancel closed a running gate: %v", err)
	}
}

func TestPrepareRenewCancelAndExpiry(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	g := New(30 * time.Second)
	g.now = func() time.Time { return now }

	first, err := g.Prepare("owner-a")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(20 * time.Second)
	renewed, err := g.Prepare("owner-a")
	if err != nil {
		t.Fatal(err)
	}
	if !renewed.LeaseExpiresAt.After(first.LeaseExpiresAt) {
		t.Fatalf("renewed expiry = %s, first = %s", renewed.LeaseExpiresAt, first.LeaseExpiresAt)
	}

	now = now.Add(29 * time.Second)
	if _, err := g.Acquire(KindReview); !errors.Is(err, ErrDraining) {
		t.Fatalf("Acquire before expiry error = %v, want ErrDraining", err)
	}
	now = now.Add(2 * time.Second)
	permit, err := g.Acquire(KindReview)
	if err != nil {
		t.Fatalf("work did not resume after abandoned lease expired: %v", err)
	}
	permit.Release()
	cancelled, err := g.Cancel("owner-a")
	if err != nil || cancelled.Draining || cancelled.LeaseID != "owner-a" {
		t.Fatalf("idempotent Cancel expired lease = (%+v, %v)", cancelled, err)
	}
}

func TestSealRequiresLiveOwnerAndNeverExpires(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	g := New(30 * time.Second)
	g.now = func() time.Time { return now }

	if _, err := g.Seal("owner-a"); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("Seal without live lease error = %v, want ErrLeaseConflict", err)
	}
	active, err := g.Acquire(KindReview)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Prepare("owner-a"); err != nil {
		t.Fatal(err)
	}
	// Existing admitted work must finish before the replacement barrier can be
	// committed, even if a client skips its own ready-state check.
	if _, err := g.Seal("owner-a"); !errors.Is(err, ErrWorkActive) {
		t.Fatalf("Seal with active work error = %v, want ErrWorkActive", err)
	}
	active.Release()
	if _, err := g.Seal("owner-b"); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("foreign Seal error = %v, want ErrLeaseConflict", err)
	}
	sealed, err := g.Seal("owner-a")
	if err != nil {
		t.Fatal(err)
	}
	if !sealed.Draining || !sealed.Sealed || !sealed.LeaseExpiresAt.IsZero() {
		t.Fatalf("sealed snapshot = %+v", sealed)
	}

	now = now.Add(24 * time.Hour)
	if _, err := g.Acquire(KindReview); !errors.Is(err, ErrDraining) {
		t.Fatalf("Acquire long after seal error = %v, want ErrDraining", err)
	}
	// A delayed renewal by the same owner is idempotent and cannot downgrade
	// the non-expiring barrier into an ordinary lease.
	renewed, err := g.Prepare("owner-a")
	if err != nil || !renewed.Sealed || !renewed.LeaseExpiresAt.IsZero() {
		t.Fatalf("Prepare sealed lease = (%+v, %v)", renewed, err)
	}
	if _, err := g.Cancel("owner-a"); !errors.Is(err, ErrBootstrapNotAuthorized) {
		t.Fatalf("Cancel unconfirmed sealed lease error = %v, want ErrBootstrapNotAuthorized", err)
	}
	if _, err := g.ConfirmBootstrap("owner-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Cancel("owner-a"); err != nil {
		t.Fatal(err)
	}
	permit, err := g.Acquire(KindReview)
	if err != nil {
		t.Fatalf("Acquire after sealed owner cancel: %v", err)
	}
	permit.Release()
}

func TestConfirmBootstrapRequiresSealedOwnerAndKeepsAdmissionClosed(t *testing.T) {
	g := New(time.Minute)
	if _, err := g.Prepare("owner-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.ConfirmBootstrap("owner-a"); !errors.Is(err, ErrLeaseNotSealed) {
		t.Fatalf("ConfirmBootstrap before Seal error = %v, want ErrLeaseNotSealed", err)
	}
	if _, err := g.Seal("owner-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.ConfirmBootstrap("owner-b"); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("foreign ConfirmBootstrap error = %v, want ErrLeaseConflict", err)
	}
	confirmed, err := g.ConfirmBootstrap("owner-a")
	if err != nil {
		t.Fatal(err)
	}
	if !confirmed.Draining || !confirmed.Sealed || !confirmed.BootstrapAuthorized {
		t.Fatalf("confirmed snapshot = %+v", confirmed)
	}
	if _, err := g.Acquire(KindReview); !errors.Is(err, ErrDraining) {
		t.Fatalf("ConfirmBootstrap opened admission: %v", err)
	}
}

func TestWaitUntilBootstrapAuthorizedRequiresConfirmationButNotCancel(t *testing.T) {
	g := New(time.Minute)
	if _, err := g.Prepare("owner-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Seal("owner-a"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- g.WaitUntilBootstrapAuthorized(context.Background()) }()
	select {
	case err := <-done:
		t.Fatalf("WaitUntilBootstrapAuthorized returned before confirmation: %v", err)
	case <-time.After(2 * openPollInterval):
	}
	if _, err := g.ConfirmBootstrap("owner-a"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WaitUntilBootstrapAuthorized after confirmation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("WaitUntilBootstrapAuthorized did not return after confirmation")
	}
	if !g.Status().Draining {
		t.Fatal("bootstrap confirmation opened the sealed admission gate")
	}
}

func TestWaitUntilBootstrapAuthorizedExpiresOrdinaryAbandonedLease(t *testing.T) {
	g := New(40 * time.Millisecond)
	if _, err := g.Prepare("owner-a"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := g.WaitUntilBootstrapAuthorized(ctx); err != nil {
		t.Fatalf("WaitUntilBootstrapAuthorized abandoned lease: %v", err)
	}
}

func TestWaitUntilBootstrapAuthorizedHonorsContextCancellation(t *testing.T) {
	g := New(time.Minute)
	if _, err := g.Prepare("owner-a"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := g.WaitUntilBootstrapAuthorized(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitUntilBootstrapAuthorized cancellation = %v, want context.Canceled", err)
	}
}

func TestLeaseIDLengthIsBounded(t *testing.T) {
	g := New(time.Minute)
	oversized := strings.Repeat("x", maxLeaseIDBytes+1)
	if _, err := g.Prepare(oversized); !errors.Is(err, ErrLeaseIDInvalid) {
		t.Fatalf("Prepare oversized lease error = %v, want ErrLeaseIDInvalid", err)
	}
	if _, err := g.Cancel(oversized); !errors.Is(err, ErrLeaseIDInvalid) {
		t.Fatalf("Cancel oversized lease error = %v, want ErrLeaseIDInvalid", err)
	}
	if _, err := g.ConfirmBootstrap(oversized); !errors.Is(err, ErrLeaseIDInvalid) {
		t.Fatalf("ConfirmBootstrap oversized lease error = %v, want ErrLeaseIDInvalid", err)
	}
}

func TestPermitReleaseIsIdempotentWithConcurrentSameKindWork(t *testing.T) {
	g := New(time.Minute)
	first, _ := g.Acquire(KindReview)
	second, _ := g.Acquire(KindReview)
	first.Release()
	first.Release()
	if got := g.Status().Total(); got != 1 {
		t.Fatalf("duplicate Release removed another permit: total = %d, want 1", got)
	}
	second.Release()
	if got := g.Status().Total(); got != 0 {
		t.Fatalf("active total = %d, want 0", got)
	}
}

func TestAcquireAndPrepareAreAtomic(t *testing.T) {
	for i := 0; i < 100; i++ {
		g := New(time.Minute)
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var permit *Permit
		var acquireErr error
		var prepared Snapshot
		go func() {
			defer wg.Done()
			<-start
			permit, acquireErr = g.Acquire(KindReview)
		}()
		go func() {
			defer wg.Done()
			<-start
			prepared, _ = g.Prepare("owner-a")
		}()
		close(start)
		wg.Wait()
		switch {
		case acquireErr == nil:
			if prepared.Total() != 1 {
				t.Fatalf("Acquire won but Prepare saw %d active", prepared.Total())
			}
			permit.Release()
		case errors.Is(acquireErr, ErrDraining):
			if prepared.Total() != 0 {
				t.Fatalf("Prepare won but snapshot saw %d active", prepared.Total())
			}
		default:
			t.Fatalf("unexpected Acquire error: %v", acquireErr)
		}
	}
}

func TestAcquireContextReusesOuterPermit(t *testing.T) {
	g := New(time.Minute)
	ctx, outer, owned, err := g.AcquireContext(context.Background(), KindAutonomous)
	if err != nil || !owned {
		t.Fatalf("outer AcquireContext = (%v, %v), want owned permit", owned, err)
	}
	if _, err := g.Prepare("owner-a"); err != nil {
		t.Fatal(err)
	}
	_, nested, nestedOwned, err := g.AcquireContext(ctx, KindImplementation)
	if err != nil || nestedOwned || nested != outer {
		t.Fatalf("nested AcquireContext = (%p, %v, %v), want reused %p", nested, nestedOwned, err, outer)
	}
	if got := g.Status().Total(); got != 1 {
		t.Fatalf("nested permit incremented total to %d, want 1", got)
	}
	outer.Release()
}

func TestPersistentLeaseSurvivesRespawnAndOwnerCanRenewAndCancel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-drain.json")
	first, err := NewPersistent(time.Minute, path)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := first.Prepare("owner-a")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("state mode = %o, want 600", info.Mode().Perm())
	}

	restarted, err := NewPersistent(time.Minute, path)
	if err != nil {
		t.Fatal(err)
	}
	restored := restarted.Status()
	if !restored.Draining || restored.LeaseID != prepared.LeaseID {
		t.Fatalf("restored snapshot = %+v", restored)
	}
	if _, err := restarted.Prepare("owner-a"); err != nil {
		t.Fatalf("renew restored lease: %v", err)
	}
	if _, err := restarted.Cancel("owner-a"); err != nil {
		t.Fatalf("cancel restored lease: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state marker still exists after cancel: %v", err)
	}
}

func TestPersistentLeaseGetsFullTTLOnRespawn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-drain.json")
	ttl := 5 * time.Minute
	first, err := NewPersistent(ttl, path)
	if err != nil {
		t.Fatal(err)
	}
	// Persist a lease with only a few seconds left according to wall time.
	first.now = func() time.Time { return time.Now().Add(-ttl + 5*time.Second) }
	if _, err := first.Prepare("owner-a"); err != nil {
		t.Fatal(err)
	}

	restartedAt := time.Now()
	restarted, err := NewPersistent(ttl, path)
	if err != nil {
		t.Fatal(err)
	}
	restored := restarted.Status()
	if !restored.Draining {
		t.Fatal("near-expiry persistent lease was not restored")
	}
	if restored.LeaseExpiresAt.Before(restartedAt.Add(ttl - 5*time.Second)) {
		t.Fatalf("restored expiry = %s, want a fresh TTL after %s", restored.LeaseExpiresAt, restartedAt)
	}
}

func TestPersistentSealedLeaseSurvivesPastTTLUntilOwnerCancels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-drain.json")
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	first, err := NewPersistent(30*time.Second, path)
	if err != nil {
		t.Fatal(err)
	}
	first.now = func() time.Time { return now }
	if _, err := first.Prepare("owner-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Seal("owner-a"); err != nil {
		t.Fatal(err)
	}

	// The persisted marker has no expiry. Advancing wall time by a year must
	// not open the replacement daemon before native version verification.
	future := &Gate{
		active:    make(map[Kind]int),
		leaseTTL:  30 * time.Second,
		statePath: path,
		now:       func() time.Time { return now.AddDate(1, 0, 0) },
		syncDir:   syncDirectory,
	}
	if err := future.restore(); err != nil {
		t.Fatal(err)
	}
	if snapshot := future.Status(); !snapshot.Draining || !snapshot.Sealed {
		t.Fatalf("restored sealed snapshot = %+v", snapshot)
	}
	if future.Status().BootstrapAuthorized {
		t.Fatal("process-local bootstrap authorization survived respawn")
	}
	// A DELETE sent after the previously confirmed process died must not open
	// the replacement's restored seal. That daemon instance has to be verified
	// and confirmed independently.
	if _, err := future.Cancel("owner-a"); !errors.Is(err, ErrBootstrapNotAuthorized) {
		t.Fatalf("late Cancel after respawn error = %v, want ErrBootstrapNotAuthorized", err)
	}
	if snapshot := future.Status(); !snapshot.Draining || !snapshot.Sealed {
		t.Fatalf("late Cancel opened restored seal: %+v", snapshot)
	}
	if _, err := future.ConfirmBootstrap("owner-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := future.Cancel("owner-a"); err != nil {
		t.Fatal(err)
	}
}

func TestLostCancelAcknowledgementCanReestablishAndSealSameOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-drain.json")
	first, err := NewPersistent(time.Minute, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Prepare("owner-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Seal("owner-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := first.ConfirmBootstrap("owner-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Cancel("owner-a"); err != nil {
		t.Fatal(err)
	}

	// Model a successful DELETE whose HTTP acknowledgement was lost, followed
	// by a daemon relaunch while the native recovery journal still exists. The
	// same owner can create a new ordinary lease and converge it back through
	// seal + confirmation without ever trusting an unowned process.
	relaunched, err := NewPersistent(time.Minute, path)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := relaunched.Prepare("owner-a")
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.Draining || prepared.Sealed || prepared.BootstrapAuthorized {
		t.Fatalf("re-established lease = %+v", prepared)
	}
	sealed, err := relaunched.Seal("owner-a")
	if err != nil {
		t.Fatal(err)
	}
	if !sealed.Sealed || sealed.BootstrapAuthorized {
		t.Fatalf("re-established seal = %+v", sealed)
	}
	confirmed, err := relaunched.ConfirmBootstrap("owner-a")
	if err != nil {
		t.Fatal(err)
	}
	if !confirmed.Sealed || !confirmed.BootstrapAuthorized || !confirmed.Draining {
		t.Fatalf("re-established confirmation = %+v", confirmed)
	}
}

func TestPersistentLeaseSurfacesDirectorySyncFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-drain.json")
	g, err := NewPersistent(time.Minute, path)
	if err != nil {
		t.Fatal(err)
	}
	g.syncDir = func(string) error { return errors.New("injected directory sync failure") }
	if _, err := g.Prepare("owner-a"); err == nil || !strings.Contains(err.Error(), "sync persistent lease directory") {
		t.Fatalf("Prepare directory sync error = %v", err)
	}
	if g.Status().Draining {
		t.Fatal("failed durable prepare changed the in-memory gate")
	}
}

func TestPersistentCancelFailsClosedOnDirectorySyncFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-drain.json")
	g, err := NewPersistent(time.Minute, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Prepare("owner-a"); err != nil {
		t.Fatal(err)
	}
	g.syncDir = func(string) error { return errors.New("injected directory sync failure") }
	if _, err := g.Cancel("owner-a"); err == nil || !strings.Contains(err.Error(), "sync persistent lease directory") {
		t.Fatalf("Cancel directory sync error = %v", err)
	}
	if !g.Status().Draining {
		t.Fatal("failed durable cancel opened the in-memory gate")
	}
}

func TestPersistentExpiredLeaseDoesNotDrainRespawn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-drain.json")
	g, err := NewPersistent(30*time.Second, path)
	if err != nil {
		t.Fatal(err)
	}
	g.now = func() time.Time { return time.Now().Add(-time.Minute) }
	if _, err := g.Prepare("owner-a"); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewPersistent(30*time.Second, path)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Status().Draining {
		t.Fatal("expired persisted lease restored as draining")
	}
}

func TestPersistentCorruptLeaseFailsClosedAtConstruction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-drain.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewPersistent(time.Minute, path); err == nil {
		t.Fatal("NewPersistent accepted corrupt marker")
	}
}

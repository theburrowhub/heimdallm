package workgate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	recoveryLeaseA = "018f6d3e-91aa-7a45-b2c0-1d8c3b4a5968"
	recoveryLeaseB = "018f6d3e-91aa-7a45-b2c0-1d8c3b4a5969"
)

func writeDesktopJournal(t *testing.T, path, phase, leaseID string) {
	t.Helper()
	journal := map[string]any{
		"schemaVersion":   1,
		"expectedVersion": "0.8.4",
		"phase":           phase,
		"leaseID":         leaseID,
	}
	if phase != "pendingInstall" {
		journal["daemonPID"] = 4242
		journal["daemonBootID"] = "boot-before-crash"
		journal["daemonVersion"] = "0.8.3"
		journal["launchAgentWasLoaded"] = true
		journal["launchAgentWasDisabled"] = false
	}
	body, err := json.Marshal(journal)
	if err != nil {
		t.Fatalf("marshal desktop journal: %v", err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write desktop journal: %v", err)
	}
}

func TestRestoreDesktopRecoveryBarrierSealsExpiredPreparingIntent(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "update-drain.json")
	journalPath := filepath.Join(dir, "app-update-recovery.json")
	writeDesktopJournal(t, journalPath, "preparing", recoveryLeaseA)

	expired := []byte(`{"lease_id":"` + recoveryLeaseA + `","expires_at":"2000-01-01T00:00:00Z"}`)
	if err := os.WriteFile(statePath, expired, 0o600); err != nil {
		t.Fatalf("write expired drain: %v", err)
	}

	recovered, err := RestoreDesktopRecoveryBarrier(time.Minute, statePath, journalPath)
	if err != nil {
		t.Fatalf("RestoreDesktopRecoveryBarrier: %v", err)
	}
	if !recovered {
		t.Fatal("preparing journal did not request a startup barrier")
	}

	gate, err := NewPersistent(time.Minute, statePath)
	if err != nil {
		t.Fatalf("restore promoted gate: %v", err)
	}
	snapshot := gate.Status()
	if !snapshot.Draining || !snapshot.Sealed || snapshot.LeaseID != recoveryLeaseA {
		t.Fatalf("promoted snapshot = %+v, want sealed recovery owner", snapshot)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	if err := gate.WaitUntilBootstrapAuthorized(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bootstrap wait before confirmation = %v, want deadline", err)
	}
	if _, err := gate.ConfirmBootstrap(recoveryLeaseA); err != nil {
		t.Fatalf("confirm promoted barrier: %v", err)
	}
	if err := gate.WaitUntilBootstrapAuthorized(context.Background()); err != nil {
		t.Fatalf("bootstrap wait after confirmation: %v", err)
	}
}

func TestRestoreDesktopRecoveryBarrierProtectsEveryPreReleasePhase(t *testing.T) {
	for _, phase := range []string{"preparing", "sealed"} {
		t.Run(phase, func(t *testing.T) {
			dir := t.TempDir()
			statePath := filepath.Join(dir, "update-drain.json")
			journalPath := filepath.Join(dir, "app-update-recovery.json")
			writeDesktopJournal(t, journalPath, phase, recoveryLeaseA)

			recovered, err := RestoreDesktopRecoveryBarrier(time.Minute, statePath, journalPath)
			if err != nil || !recovered {
				t.Fatalf("RestoreDesktopRecoveryBarrier = (%v, %v), want (true, nil)", recovered, err)
			}
			gate, err := NewPersistent(time.Minute, statePath)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot := gate.Status(); !snapshot.Sealed || snapshot.LeaseID != recoveryLeaseA {
				t.Fatalf("snapshot = %+v, want sealed %s", snapshot, recoveryLeaseA)
			}
		})
	}
}

func TestRestoreDesktopRecoveryBarrierDoesNotRecreateAcknowledgedInstallingSeal(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "update-drain.json")
	journalPath := filepath.Join(dir, "app-update-recovery.json")
	writeDesktopJournal(t, journalPath, "installing", recoveryLeaseA)

	recovered, err := RestoreDesktopRecoveryBarrier(time.Minute, statePath, journalPath)
	if err != nil || recovered {
		t.Fatalf("installing RestoreDesktopRecoveryBarrier = (%v, %v), want (false, nil)", recovered, err)
	}
	if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("installing journal recreated an ambiguously acknowledged barrier: %v", err)
	}

	// Before DELETE, installing still has its durable marker. The normal gate
	// restore remains sealed even though journal reconciliation does no write.
	marker := []byte(`{"lease_id":"` + recoveryLeaseA + `","sealed":true}`)
	if err := os.WriteFile(statePath, marker, 0o600); err != nil {
		t.Fatal(err)
	}
	gate, err := NewPersistent(time.Minute, statePath)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot := gate.Status(); !snapshot.Sealed || snapshot.LeaseID != recoveryLeaseA {
		t.Fatalf("existing installing marker snapshot = %+v, want sealed", snapshot)
	}
}

func TestRestoreDesktopRecoveryBarrierDoesNotSealPendingInstall(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "update-drain.json")
	journalPath := filepath.Join(dir, "app-update-recovery.json")
	writeDesktopJournal(t, journalPath, "pendingInstall", recoveryLeaseA)

	recovered, err := RestoreDesktopRecoveryBarrier(time.Minute, statePath, journalPath)
	if err != nil || recovered {
		t.Fatalf("pending RestoreDesktopRecoveryBarrier = (%v, %v), want (false, nil)", recovered, err)
	}
	if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending journal created update barrier: %v", err)
	}
}

func TestRestoreDesktopRecoveryBarrierRejectsContaminatedPendingInstall(t *testing.T) {
	dir := t.TempDir()
	journalPath := filepath.Join(dir, "app-update-recovery.json")
	body := []byte(`{"schemaVersion":1,"expectedVersion":"0.8.4","phase":"pendingInstall",` +
		`"leaseID":"` + recoveryLeaseA + `","daemonPID":4242}`)
	if err := os.WriteFile(journalPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreDesktopRecoveryBarrier(
		time.Minute,
		filepath.Join(dir, "update-drain.json"),
		journalPath,
	); err == nil {
		t.Fatal("pending-install journal with daemon identity was accepted")
	}
}

func TestRestoreDesktopRecoveryBarrierRejectsForeignMarkerAndUnsafeJournal(t *testing.T) {
	t.Run("foreign marker", func(t *testing.T) {
		dir := t.TempDir()
		statePath := filepath.Join(dir, "update-drain.json")
		journalPath := filepath.Join(dir, "app-update-recovery.json")
		writeDesktopJournal(t, journalPath, "preparing", recoveryLeaseA)
		foreign := []byte(`{"lease_id":"` + recoveryLeaseB + `","sealed":true}`)
		if err := os.WriteFile(statePath, foreign, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := RestoreDesktopRecoveryBarrier(time.Minute, statePath, journalPath); err == nil {
			t.Fatal("foreign persistent barrier was overwritten")
		}
	})

	t.Run("group readable", func(t *testing.T) {
		dir := t.TempDir()
		journalPath := filepath.Join(dir, "app-update-recovery.json")
		writeDesktopJournal(t, journalPath, "preparing", recoveryLeaseA)
		if err := os.Chmod(journalPath, 0o640); err != nil {
			t.Fatal(err)
		}
		if _, err := RestoreDesktopRecoveryBarrier(
			time.Minute,
			filepath.Join(dir, "update-drain.json"),
			journalPath,
		); err == nil {
			t.Fatal("group-readable recovery journal was accepted")
		}
	})

	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target.json")
		writeDesktopJournal(t, target, "preparing", recoveryLeaseA)
		journalPath := filepath.Join(dir, "app-update-recovery.json")
		if err := os.Symlink(target, journalPath); err != nil {
			t.Fatal(err)
		}
		if _, err := RestoreDesktopRecoveryBarrier(
			time.Minute,
			filepath.Join(dir, "update-drain.json"),
			journalPath,
		); err == nil {
			t.Fatal("symlink recovery journal was accepted")
		}
	})
}

func TestPersistentGateRejectsUnsafeMarkerFiles(t *testing.T) {
	marker := []byte(`{"lease_id":"` + recoveryLeaseA + `","sealed":true}`)

	t.Run("group readable", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "update-drain.json")
		if err := os.WriteFile(path, marker, 0o640); err != nil {
			t.Fatal(err)
		}
		if _, err := NewPersistent(time.Minute, path); err == nil {
			t.Fatal("group-readable persistent marker was accepted")
		}
	})

	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target.json")
		if err := os.WriteFile(target, marker, 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "update-drain.json")
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		if _, err := NewPersistent(time.Minute, path); err == nil {
			t.Fatal("symlink persistent marker was accepted")
		}
	})
}

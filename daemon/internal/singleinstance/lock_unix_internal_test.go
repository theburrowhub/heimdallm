//go:build darwin || linux

package singleinstance

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAcquireReportsOpenFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "daemon.lock")
	if _, err := Acquire(path); err == nil || !strings.Contains(err.Error(), "open lock file") {
		t.Fatalf("Acquire missing parent error = %v, want open lock file error", err)
	}
}

func TestAcquireReleasesLockWhenOwnerPIDCannotBeWritten(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("/dev/full is the deterministic Linux write-failure device")
	}
	if _, err := Acquire("/dev/full"); err == nil || !strings.Contains(err.Error(), "write owner pid") {
		t.Fatalf("Acquire /dev/full error = %v, want write owner pid error", err)
	}
}

func TestCloseHandlesNilAndClosedDescriptors(t *testing.T) {
	var nilLock *Lock
	if err := nilLock.Close(); err != nil {
		t.Fatalf("nil Close: %v", err)
	}

	lock, err := Acquire(filepath.Join(t.TempDir(), "daemon.lock"))
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := lock.file.Close(); err != nil {
		t.Fatalf("close descriptor behind Lock: %v", err)
	}
	if err := lock.Close(); err == nil || !strings.Contains(err.Error(), "unlock") {
		t.Fatalf("Close closed descriptor error = %v, want unlock error", err)
	}
	if _, err := os.Stat(lock.file.Name()); err != nil {
		t.Fatalf("lock file disappeared after close error: %v", err)
	}
}

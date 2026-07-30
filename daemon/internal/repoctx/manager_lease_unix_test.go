//go:build darwin || linux

package repoctx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestReleaseDefersCleanupUntilInheritedLeaseCloses(t *testing.T) {
	m, _, base := newTestManagerWithCap(t, 1)
	setupManagedClone(t, base)

	h, err := m.Acquire(context.Background(), Request{
		Repo:            "org/repo",
		Token:           "secret",
		Mode:            ModeRead,
		WorktreeToken:   "live-child",
		WorktreeBaseRef: strings.Repeat("a", 40),
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	snapshot := h.Path()
	leaseFiles := h.LeaseFiles()
	if len(leaseFiles) != 1 || leaseFiles[0] == nil {
		t.Fatalf("lease files = %v, want one inherited descriptor", leaseFiles)
	}
	fd, err := unix.Dup(int(leaseFiles[0].Fd()))
	if err != nil {
		t.Fatalf("duplicate child lease: %v", err)
	}
	childLease := os.NewFile(uintptr(fd), "simulated-child-lease")

	h.Release()
	if _, err := os.Stat(snapshot); err != nil {
		t.Fatalf("Release removed checkout while child lease was live: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(snapshot), worktreeCleanupFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Release authorised cleanup before child exit: %v", err)
	}

	if err := childLease.Close(); err != nil {
		t.Fatalf("close simulated child lease: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, statErr := os.Stat(snapshot)
		if errors.Is(statErr, os.ErrNotExist) {
			break
		}
		if statErr != nil {
			t.Fatalf("stat deferred snapshot: %v", statErr)
		}
		if time.Now().After(deadline) {
			t.Fatalf("snapshot still exists after inherited lease closed: %s", snapshot)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAcquireFailureRetainsSnapshotWhileInheritedLeaseIsLive(t *testing.T) {
	m, git, base := newTestManagerWithCap(t, 0)
	setupManagedClone(t, base)

	var (
		snapshot   string
		childLease *os.File
	)
	git.onRun = func(call gitCall) error {
		if len(call.Args) == 3 && call.Args[0] == "checkout" && call.Args[1] == "--detach" {
			snapshot = call.Dir
			if len(call.ExtraFiles) < 1 || call.ExtraFiles[0] == nil {
				return errors.New("checkout did not inherit lease")
			}
			fd, err := unix.Dup(int(call.ExtraFiles[0].Fd()))
			if err != nil {
				return err
			}
			childLease = os.NewFile(uintptr(fd), "simulated-child-lease")
			return errors.New("checkout failed after spawning child")
		}
		return nil
	}

	_, err := m.Acquire(context.Background(), Request{
		Repo:            "org/repo",
		Token:           "secret",
		Mode:            ModeRead,
		WorktreeToken:   "failed-with-child",
		WorktreeBaseRef: strings.Repeat("a", 40),
	})
	if err == nil || !strings.Contains(err.Error(), "checkout failed after spawning child") {
		t.Fatalf("Acquire err = %v, want checkout failure", err)
	}
	if childLease == nil {
		t.Fatal("simulated child did not inherit a duplicate lease descriptor")
	}
	if snapshot == "" {
		t.Fatal("snapshot path was not captured")
	}
	if _, statErr := os.Stat(snapshot); statErr != nil {
		t.Fatalf("rollback removed snapshot while inherited lease was live: %v", statErr)
	}
	runDir := filepath.Dir(snapshot)
	if _, statErr := os.Stat(filepath.Join(runDir, worktreeCleanupFile)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed acquisition authorised cleanup despite live child: %v", statErr)
	}

	if err := childLease.Close(); err != nil {
		t.Fatalf("close simulated child lease: %v", err)
	}
	n, pruneErr := m.PruneStaleExternalWorktrees(context.Background())
	if pruneErr != nil {
		t.Fatalf("PruneStaleExternalWorktrees: %v", pruneErr)
	}
	if n != 0 {
		t.Fatalf("pruned unmarked failed acquisition = %d, want 0", n)
	}
	if _, statErr := os.Stat(snapshot); statErr != nil {
		t.Fatalf("unmarked failed acquisition was not retained fail-closed: %v", statErr)
	}
}

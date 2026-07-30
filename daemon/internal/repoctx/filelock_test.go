//go:build darwin || linux

package repoctx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

const (
	fileLockHelperEnv       = "HEIMDALLM_FILELOCK_HELPER"
	fileLockInheritedEnv    = "HEIMDALLM_FILELOCK_INHERITED_HELPER"
	fileLockHelperPathEnv   = "HEIMDALLM_FILELOCK_PATH"
	fileLockHelperReadyEnv  = "HEIMDALLM_FILELOCK_READY"
	fileLockHelperWaitEnv   = "HEIMDALLM_FILELOCK_WAIT"
	fileLockHelperWaitValue = "wait-for-stdin"
)

func TestTryFileLockExclusiveAndRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo.lock")

	first, acquired, err := tryFileLock(path)
	if err != nil {
		t.Fatalf("first tryFileLock: %v", err)
	}
	if !acquired || first == nil {
		t.Fatal("first tryFileLock did not acquire an uncontended lock")
	}
	if first.File() == nil {
		t.Fatal("File returned nil while lock is held")
	}

	second, acquired, err := tryFileLock(path)
	if err != nil {
		t.Fatalf("contended tryFileLock: %v", err)
	}
	if acquired || second != nil {
		t.Fatalf("contended tryFileLock = (%v, %t), want (nil, false)", second, acquired)
	}

	if err := first.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("idempotent Close: %v", err)
	}
	if first.File() != nil {
		t.Fatal("File returned a descriptor after Close")
	}

	third, acquired, err := tryFileLock(path)
	if err != nil {
		t.Fatalf("tryFileLock after release: %v", err)
	}
	if !acquired || third == nil {
		t.Fatal("lock remained contended after owner closed it")
	}
	if err := third.Close(); err != nil {
		t.Fatalf("third Close: %v", err)
	}
}

func TestAcquireFileLockHonorsContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo.lock")
	owner, err := acquireFileLock(context.Background(), path)
	if err != nil {
		t.Fatalf("acquire owner: %v", err)
	}
	defer owner.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	blocked, err := acquireFileLock(ctx, path)
	if blocked != nil {
		blocked.Close()
		t.Fatal("contended acquire unexpectedly returned a lock")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended acquire error = %v, want context deadline", err)
	}
}

func TestAcquireFileLockWaitsUntilRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo.lock")
	owner, err := acquireFileLock(context.Background(), path)
	if err != nil {
		t.Fatalf("acquire owner: %v", err)
	}

	type result struct {
		lock *fileLock
		err  error
	}
	resultCh := make(chan result, 1)
	go func() {
		lock, err := acquireFileLock(context.Background(), path)
		resultCh <- result{lock: lock, err: err}
	}()

	select {
	case got := <-resultCh:
		if got.lock != nil {
			got.lock.Close()
		}
		t.Fatalf("contended acquire returned before release: %v", got.err)
	case <-time.After(75 * time.Millisecond):
	}

	if err := owner.Close(); err != nil {
		t.Fatalf("owner Close: %v", err)
	}
	select {
	case got := <-resultCh:
		if got.err != nil {
			t.Fatalf("waiting acquire: %v", got.err)
		}
		if got.lock == nil {
			t.Fatal("waiting acquire returned nil lock")
		}
		if err := got.lock.Close(); err != nil {
			t.Fatalf("waiting lock Close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiting acquire did not complete after release")
	}
}

func TestFileLockCloseIsConcurrentAndIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo.lock")
	lock, err := acquireFileLock(context.Background(), path)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	const callers = 16
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- lock.Close()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent Close: %v", err)
		}
	}

	next, acquired, err := tryFileLock(path)
	if err != nil {
		t.Fatalf("try after concurrent Close: %v", err)
	}
	if !acquired || next == nil {
		t.Fatal("concurrent Close did not release the lock")
	}
	if err := next.Close(); err != nil {
		t.Fatalf("next Close: %v", err)
	}
}

func TestFileLockReleasedWhenOwnerProcessDies(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "repo.lock")
	ready := filepath.Join(dir, "ready")

	cmd := exec.Command(os.Args[0], "-test.run=^TestFileLockHelperProcess$")
	cmd.Env = append(os.Environ(),
		fileLockHelperEnv+"=1",
		fileLockHelperPathEnv+"="+path,
		fileLockHelperReadyEnv+"="+ready,
		fileLockHelperWaitEnv+"="+fileLockHelperWaitValue,
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("helper stdin: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})

	waitForHelperReady(t, cmd, ready)
	contender, acquired, err := tryFileLock(path)
	if err != nil {
		t.Fatalf("try while helper owns lock: %v", err)
	}
	if acquired || contender != nil {
		if contender != nil {
			contender.Close()
		}
		t.Fatal("second process acquired a lock held by helper")
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill helper: %v", err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("killed helper exited successfully")
	}

	recovered, err := acquireFileLock(context.Background(), path)
	if err != nil {
		t.Fatalf("acquire after helper crash: %v", err)
	}
	if err := recovered.Close(); err != nil {
		t.Fatalf("recovered Close: %v", err)
	}
}

func TestInheritedDescriptorKeepsLockAfterParentCloses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "repo.lock")
	ready := filepath.Join(dir, "ready")

	owner, err := acquireFileLock(context.Background(), path)
	if err != nil {
		t.Fatalf("acquire owner: %v", err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestInheritedFileLockHelperProcess$")
	cmd.Env = append(os.Environ(),
		fileLockInheritedEnv+"=1",
		fileLockHelperReadyEnv+"="+ready,
	)
	cmd.ExtraFiles = []*os.File{owner.File()}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		owner.Close()
		t.Fatalf("helper stdin: %v", err)
	}
	if err := cmd.Start(); err != nil {
		owner.Close()
		t.Fatalf("start helper: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	waitForHelperReady(t, cmd, ready)

	if err := owner.Close(); err != nil {
		t.Fatalf("close parent descriptor: %v", err)
	}
	contender, acquired, err := tryFileLock(path)
	if err != nil {
		t.Fatalf("try while child inherited descriptor: %v", err)
	}
	if acquired || contender != nil {
		if contender != nil {
			contender.Close()
		}
		t.Fatal("parent Close released a lock still inherited by the child")
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill inherited-descriptor helper: %v", err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("killed inherited-descriptor helper exited successfully")
	}

	recovered, err := acquireFileLock(context.Background(), path)
	if err != nil {
		t.Fatalf("acquire after inherited child exits: %v", err)
	}
	if err := recovered.Close(); err != nil {
		t.Fatalf("close recovered lock: %v", err)
	}
}

func TestInheritedFileLockHelperProcess(t *testing.T) {
	if os.Getenv(fileLockInheritedEnv) != "1" {
		return
	}
	inherited := os.NewFile(3, "inherited-worktree-lease")
	if inherited == nil {
		t.Fatal("helper did not inherit fd 3")
	}
	if _, err := inherited.Stat(); err != nil {
		t.Fatalf("stat inherited fd: %v", err)
	}
	ready := os.Getenv(fileLockHelperReadyEnv)
	if err := os.WriteFile(ready, []byte("ready\n"), 0o600); err != nil {
		t.Fatalf("helper ready: %v", err)
	}
	var oneByte [1]byte
	_, _ = os.Stdin.Read(oneByte[:])
}

func TestFileLockHelperProcess(t *testing.T) {
	if os.Getenv(fileLockHelperEnv) != "1" {
		return
	}
	path := os.Getenv(fileLockHelperPathEnv)
	ready := os.Getenv(fileLockHelperReadyEnv)
	lock, err := acquireFileLock(context.Background(), path)
	if err != nil {
		t.Fatalf("helper acquire: %v", err)
	}
	defer lock.Close()
	if err := os.WriteFile(ready, []byte("ready\n"), 0o600); err != nil {
		t.Fatalf("helper ready: %v", err)
	}
	if os.Getenv(fileLockHelperWaitEnv) == fileLockHelperWaitValue {
		var oneByte [1]byte
		_, _ = os.Stdin.Read(oneByte[:])
	}
}

func waitForHelperReady(t *testing.T, cmd *exec.Cmd, ready string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(ready); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stat helper ready file: %v", err)
		}
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			t.Fatalf("helper exited before acquiring lock: %v", cmd.ProcessState)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("helper did not report lock acquisition")
}

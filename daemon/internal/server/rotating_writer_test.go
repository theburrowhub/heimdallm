package server

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// Scope: size-based rotation boundary behaviour introduced in #77.
// The writer lives in the server package so these tests stay internal.

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func writeLine(t *testing.T, w *RotatingWriter, line string) {
	t.Helper()
	if _, err := w.Write([]byte(line)); err != nil {
		t.Fatalf("write %q: %v", line, err)
	}
}

func TestRotatingWriter_BelowCapDoesNotRotate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	w, err := NewRotatingWriter(path, 1024, 3)
	if err != nil {
		t.Fatalf("NewRotatingWriter: %v", err)
	}
	defer w.Close()

	writeLine(t, w, "line1\n")
	writeLine(t, w, "line2\n")

	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatalf("expected no backup, but %s exists (err=%v)", path+".1", err)
	}
	if got := readFile(t, path); !strings.Contains(got, "line1") || !strings.Contains(got, "line2") {
		t.Fatalf("active file = %q, want both lines", got)
	}
}

func TestRotatingWriter_CrossingCapRotatesOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	// Cap at 10 bytes so a 6-byte write followed by a 6-byte write
	// triggers rotation. keep=3.
	w, err := NewRotatingWriter(path, 10, 3)
	if err != nil {
		t.Fatalf("NewRotatingWriter: %v", err)
	}
	defer w.Close()

	writeLine(t, w, "first\n") // 6 bytes — fits under cap
	writeLine(t, w, "second\n") // would exceed cap → rotates first

	// .1 should now hold the first line, active file the second.
	if got := readFile(t, path+".1"); !strings.Contains(got, "first") {
		t.Fatalf(".1 = %q, want to contain first", got)
	}
	if got := readFile(t, path); !strings.Contains(got, "second") {
		t.Fatalf("active = %q, want to contain second", got)
	}

	// .2 should not yet exist.
	if _, err := os.Stat(path + ".2"); !os.IsNotExist(err) {
		t.Fatalf("expected no .2 backup, got err=%v", err)
	}
}

func TestRotatingWriter_EvictsOldestBeyondKeep(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	// keep=2, cap=6. Each write is exactly 6 bytes so every *next*
	// write rotates.
	w, err := NewRotatingWriter(path, 6, 2)
	if err != nil {
		t.Fatalf("NewRotatingWriter: %v", err)
	}
	defer w.Close()

	// After 4 writes the sequence is:
	//   write 1 → active: w1
	//   write 2 → rotate (w1 → .1), active: w2
	//   write 3 → rotate (.1 → .2, w2 → .1), active: w3
	//   write 4 → rotate (.2 evicted, .1 → .2, w3 → .1), active: w4
	for i := 1; i <= 4; i++ {
		writeLine(t, w, fmt.Sprintf("w%d-xx", i)) // exactly 6 bytes
	}

	if got := readFile(t, path); !strings.Contains(got, "w4") {
		t.Fatalf("active = %q, want w4", got)
	}
	if got := readFile(t, path+".1"); !strings.Contains(got, "w3") {
		t.Fatalf(".1 = %q, want w3", got)
	}
	if got := readFile(t, path+".2"); !strings.Contains(got, "w2") {
		t.Fatalf(".2 = %q, want w2", got)
	}

	// .3 must never exist — keep=2.
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatalf("expected no .3 backup (keep=2), got err=%v", err)
	}
	// And w1 must have been evicted (no backup contains it).
	for _, p := range []string{path, path + ".1", path + ".2"} {
		if strings.Contains(readFile(t, p), "w1") {
			t.Fatalf("w1 survived in %s — should have been evicted", p)
		}
	}
}

func TestRotatingWriter_MaxBytesZeroDisablesRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	w, err := NewRotatingWriter(path, 0, 3)
	if err != nil {
		t.Fatalf("NewRotatingWriter: %v", err)
	}
	defer w.Close()

	// Write well past what a 50 MiB default would allow — rotation
	// must stay off.
	big := strings.Repeat("x", 10_000)
	writeLine(t, w, big)
	writeLine(t, w, big)

	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatalf("expected no rotation with maxBytes=0, but %s exists (err=%v)", path+".1", err)
	}
}

func TestRotatingWriter_ReopenPicksUpExistingSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	// Pre-populate the file with 5 bytes, then open the writer with
	// cap=6. The very next write should push past the cap and rotate.
	if err := os.WriteFile(path, []byte("older"), 0640); err != nil {
		t.Fatalf("seed: %v", err)
	}

	w, err := NewRotatingWriter(path, 6, 3)
	if err != nil {
		t.Fatalf("NewRotatingWriter: %v", err)
	}
	defer w.Close()

	writeLine(t, w, "xx") // 5+2 > 6 → rotates

	if got := readFile(t, path+".1"); !strings.Contains(got, "older") {
		t.Fatalf(".1 = %q, want pre-existing content", got)
	}
}

func TestRotatingWriter_ClosedWriterReturnsErrNotPanic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	w, err := NewRotatingWriter(path, 1024, 3)
	if err != nil {
		t.Fatalf("NewRotatingWriter: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// After Close, w.f is nil. A write from a late slog.Info must
	// surface os.ErrClosed instead of panicking on w.f.Write(p).
	_, err = w.Write([]byte("after-close\n"))
	if !errors.Is(err, os.ErrClosed) {
		t.Fatalf("Write after Close returned %v, want os.ErrClosed", err)
	}
}

func TestRotatingWriter_RotationRecoversFromRenameFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission semantics required")
	}
	if os.Geteuid() == 0 {
		// Running as root bypasses file-mode permissions, so the
		// recovery path is not exercised.
		t.Skip("rotation recovery requires a non-root user")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	w, err := NewRotatingWriter(path, 6, 3)
	if err != nil {
		t.Fatalf("NewRotatingWriter: %v", err)
	}
	defer w.Close()

	// Write under the cap so the file exists, then make the directory
	// read-only: subsequent rename attempts will fail with EACCES and
	// exercise the recoverActive fallback.
	writeLine(t, w, "first\n")
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatalf("chmod %s 0500: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })

	// This write crosses the cap and triggers a rotation that cannot
	// complete (no rename allowed). The writer must leave a valid file
	// handle behind and not panic.
	if _, err := w.Write([]byte("second\n")); err != nil && !errors.Is(err, os.ErrClosed) {
		// Write may succeed (recovered handle) or degrade to
		// os.ErrClosed if the fallback open also failed. Any other
		// error (or a panic) is a regression.
		t.Fatalf("Write after failed rotation: unexpected err %v", err)
	}
	// Critically: a follow-up write must not panic. That's the #78
	// regression this test guards against.
	_, _ = w.Write([]byte("third\n"))
}

func TestRotatingWriter_ConcurrentWritesStaySafe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	// Small cap forces many rotations during the run so the mutex is
	// exercised across rotation boundaries.
	w, err := NewRotatingWriter(path, 256, 5)
	if err != nil {
		t.Fatalf("NewRotatingWriter: %v", err)
	}
	defer w.Close()

	const (
		goroutines = 20
		perWorker  = 50
	)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			line := []byte(fmt.Sprintf("worker-%02d-xxxxxxxxxxxxxxxx\n", id))
			for j := 0; j < perWorker; j++ {
				if _, err := w.Write(line); err != nil {
					// os.ErrClosed is acceptable if recovery ever
					// fails, but a panic would abort the test.
					if !errors.Is(err, os.ErrClosed) {
						t.Errorf("concurrent write: %v", err)
						return
					}
				}
			}
		}(i)
	}
	wg.Wait()
}

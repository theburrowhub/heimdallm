package server

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// DefaultLogMaxBytes is the default size cap for heimdallm.log before a
// rotation fires. 50 MiB comfortably holds a few days of INFO-level output
// from a chatty poll loop without feeling cramped on a Docker volume.
const DefaultLogMaxBytes = int64(50 * 1024 * 1024)

// DefaultLogKeep is how many rotated backups are retained alongside the
// active file. 3 is a sweet spot: enough to diagnose a recent incident
// after a restart, few enough that the volume never grows past
// (Keep+1) * MaxBytes worst-case.
const DefaultLogKeep = 3

// RotatingWriter is a size-based file rotator used by setupLogging to wrap
// heimdallm.log. It implements io.Writer and is safe for concurrent use —
// the only writer in practice is slog's background emission, but callers
// that pass the handle elsewhere (e.g. MultiWriter) don't need to know.
//
// Rotation scheme (simple, good enough for a single-process log):
//   - heimdallm.log      ← active file
//   - heimdallm.log.1    ← most recently rotated
//   - heimdallm.log.2    ← one rotation older
//   - …
//   - heimdallm.log.<Keep>
//
// When rotation fires:
//  1. Close the active file.
//  2. Delete heimdallm.log.<Keep> (if it exists).
//  3. Rename heimdallm.log.<Keep-1> → .<Keep>, .<Keep-2> → .<Keep-1>, …,
//     heimdallm.log → heimdallm.log.1.
//  4. Open a fresh heimdallm.log.
//
// On rename/open failures the rotator falls back to truncating the active
// file so logging keeps working even if the underlying filesystem denies
// the rename — losing history is preferable to blocking the daemon.
type RotatingWriter struct {
	mu       sync.Mutex
	path     string
	f        *os.File
	maxBytes int64
	keep     int
	written  int64
}

// NewRotatingWriter opens (or creates) path in append mode and returns a
// writer that rotates once writes push the file past maxBytes. maxBytes
// <= 0 disables rotation entirely (plain append). keep <= 0 defaults to 1
// backup, since fewer than that defeats the purpose.
func NewRotatingWriter(path string, maxBytes int64, keep int) (*RotatingWriter, error) {
	if keep < 1 {
		keep = 1
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0640)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &RotatingWriter{
		path:     path,
		f:        f,
		maxBytes: maxBytes,
		keep:     keep,
		written:  info.Size(),
	}, nil
}

// Write appends p to the active file, rotating first if the write would
// push the file past maxBytes. Errors from rotation are logged to stderr
// but do not prevent the write — we always return the result of the final
// underlying Write so callers (slog) do not stall on a rotation hiccup.
func (w *RotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.maxBytes > 0 && w.written+int64(len(p)) > w.maxBytes {
		if err := w.rotateLocked(); err != nil {
			fmt.Fprintf(os.Stderr, "heimdallm: log rotation failed: %v\n", err)
		}
	}
	// Defensive guard: rotation's recovery path aims to always leave
	// w.f non-nil, but if every fallback also failed (e.g. the data
	// directory became read-only) the handle can still be nil. Surface
	// os.ErrClosed instead of panicking so the caller — typically slog
	// — can degrade instead of crashing the daemon.
	if w.f == nil {
		return 0, os.ErrClosed
	}
	n, err := w.f.Write(p)
	w.written += int64(n)
	return n, err
}

// Close flushes and closes the active file. Subsequent writes fail until a
// new writer is created — intended for shutdown only.
func (w *RotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// backupPath returns the rotated filename for index n (1-based):
// "<path>.1", "<path>.2", …
func (w *RotatingWriter) backupPath(n int) string {
	return fmt.Sprintf("%s.%d", w.path, n)
}

// rotateLocked performs the rename shuffle and opens a fresh active file.
// Must be called with w.mu held.
//
// Invariant: on every return path — success or error — w.f is either a
// valid, writable file handle or (as a last resort, if even the recovery
// open failed) nil. The Write path treats nil as os.ErrClosed rather
// than panicking, but we try hard to never leave it nil: any failure
// in the remove/shift/rename steps triggers recoverActive(), which
// re-opens w.path so subsequent writes keep succeeding even though the
// rotation itself gave up.
func (w *RotatingWriter) rotateLocked() error {
	if err := w.f.Close(); err != nil {
		// We closed the file; we still want to try to recover a handle.
		w.f = nil
		w.recoverActive()
		return fmt.Errorf("close active: %w", err)
	}
	w.f = nil

	// Drop the oldest backup if it exists; os.Remove is a no-op when the
	// file is absent, but we explicitly check to avoid masking real errors.
	oldest := w.backupPath(w.keep)
	if _, err := os.Stat(oldest); err == nil {
		if err := os.Remove(oldest); err != nil {
			w.recoverActive()
			return fmt.Errorf("remove %s: %w", filepath.Base(oldest), err)
		}
	}

	// Shift .N-1 → .N, .N-2 → .N-1, …, .1 → .2 (reverse order so we
	// never overwrite a backup we still need).
	for n := w.keep - 1; n >= 1; n-- {
		from := w.backupPath(n)
		to := w.backupPath(n + 1)
		if _, err := os.Stat(from); err == nil {
			if err := os.Rename(from, to); err != nil {
				w.recoverActive()
				return fmt.Errorf("rename %s → %s: %w",
					filepath.Base(from), filepath.Base(to), err)
			}
		}
	}

	// Promote the active file to .1.
	if err := os.Rename(w.path, w.backupPath(1)); err != nil {
		// Rename failed — truncate-then-reopen the active file as a
		// fallback so the daemon can keep logging. History is lost but
		// we stay healthy.
		fallback, ferr := os.OpenFile(w.path,
			os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0640)
		if ferr != nil {
			// Even truncate failed — leave w.f nil so Write returns
			// os.ErrClosed instead of panicking.
			return fmt.Errorf("rename failed (%v) and truncate fallback failed: %w", err, ferr)
		}
		w.f = fallback
		w.written = 0
		return fmt.Errorf("rename active: %w", err)
	}

	// Open a fresh active file.
	fresh, err := os.OpenFile(w.path,
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0640)
	if err != nil {
		// The old active is already renamed to .1 — recoverActive will
		// create a new empty file at w.path if it can.
		w.recoverActive()
		return fmt.Errorf("open fresh %s: %w", filepath.Base(w.path), err)
	}
	w.f = fresh
	w.written = 0
	return nil
}

// recoverActive is a best-effort attempt to leave w with a writable file
// handle after a rotation step fails. It tries O_APPEND first (so we
// keep any content that is still at w.path — e.g. the shift-backup path
// never moved the active file) and falls back to O_TRUNC|O_CREATE if
// that fails. If even that fails w.f stays nil and the next Write
// returns os.ErrClosed — preferable to a panic. Must be called with
// w.mu held and only when w.f is nil.
func (w *RotatingWriter) recoverActive() {
	f, err := os.OpenFile(w.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0640)
	if err == nil {
		if info, statErr := f.Stat(); statErr == nil {
			w.written = info.Size()
		}
		w.f = f
		return
	}
	f, err = os.OpenFile(w.path, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0640)
	if err != nil {
		fmt.Fprintf(os.Stderr, "heimdallm: log rotation recovery failed: %v\n", err)
		return
	}
	w.f = f
	w.written = 0
}

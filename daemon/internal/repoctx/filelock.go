package repoctx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

const fileLockRetryInterval = 25 * time.Millisecond

// errFileLockUnsupported is returned on platforms where repoctx has no
// process-safe file-lock implementation. Callers must fail closed rather than
// silently falling back to in-memory coordination.
var errFileLockUnsupported = errors.New("repoctx: interprocess file locking is unsupported on this platform")

// fileLock owns an exclusive advisory lock and the file descriptor carrying
// it. Keeping the descriptor open is what keeps the lock alive across
// processes.
type fileLock struct {
	mu       sync.Mutex
	file     *os.File
	closed   bool
	closeErr error
}

// acquireFileLock waits for an exclusive lock on path. The underlying platform
// operation is always non-blocking; bounded polling makes cancellation reliable
// even while another process owns the lock.
func acquireFileLock(ctx context.Context, path string) (*fileLock, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("repoctx: acquire file lock %q: %w", path, err)
	}

	file, err := openFileLock(path)
	if err != nil {
		return nil, err
	}
	closeOnError := func(lockErr error) (*fileLock, error) {
		if closeErr := file.Close(); closeErr != nil {
			lockErr = errors.Join(lockErr, fmt.Errorf("repoctx: close file lock %q: %w", path, closeErr))
		}
		return nil, lockErr
	}

	ticker := time.NewTicker(fileLockRetryInterval)
	defer ticker.Stop()
	for {
		acquired, lockErr := tryExclusiveFileLock(file)
		if lockErr != nil {
			return closeOnError(fmt.Errorf("repoctx: lock file %q: %w", path, lockErr))
		}
		if acquired {
			return &fileLock{file: file}, nil
		}

		select {
		case <-ctx.Done():
			return closeOnError(fmt.Errorf("repoctx: acquire file lock %q: %w", path, ctx.Err()))
		case <-ticker.C:
		}
	}
}

// tryFileLock attempts to take an exclusive lock without waiting. Contention
// is reported as (nil, false, nil); filesystem and platform failures are
// returned as errors so callers can fail closed.
func tryFileLock(path string) (lock *fileLock, acquired bool, err error) {
	file, err := openFileLock(path)
	if err != nil {
		return nil, false, err
	}

	acquired, err = tryExclusiveFileLock(file)
	if err != nil {
		closeErr := file.Close()
		if closeErr != nil {
			err = errors.Join(err, fmt.Errorf("repoctx: close file lock %q: %w", path, closeErr))
		}
		return nil, false, fmt.Errorf("repoctx: lock file %q: %w", path, err)
	}
	if !acquired {
		if closeErr := file.Close(); closeErr != nil {
			return nil, false, fmt.Errorf("repoctx: close contended file lock %q: %w", path, closeErr)
		}
		return nil, false, nil
	}
	return &fileLock{file: file}, true, nil
}

func openFileLock(path string) (*os.File, error) {
	if path == "" {
		return nil, fmt.Errorf("repoctx: file lock path is empty")
	}
	if before, err := os.Lstat(path); err == nil {
		if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
			return nil, fmt.Errorf("repoctx: unsafe file lock %q", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("repoctx: inspect file lock %q: %w", path, err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("repoctx: open file lock %q: %w", path, err)
	}
	opened, statErr := file.Stat()
	pathInfo, lstatErr := os.Lstat(path)
	if statErr != nil || lstatErr != nil || !opened.Mode().IsRegular() ||
		pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, pathInfo) {
		_ = file.Close()
		return nil, fmt.Errorf("repoctx: file lock %q changed or is not a regular file", path)
	}
	if opened.Mode().Perm()&0o077 != 0 {
		_ = file.Close()
		return nil, fmt.Errorf("repoctx: file lock %q permissions are %03o, want no group/other access",
			path, opened.Mode().Perm())
	}
	return file, nil
}

// File exposes the descriptor that owns the lock. It can be passed through
// exec.Cmd.ExtraFiles when a child process must keep the lease alive if its
// parent exits. The returned descriptor is nil after Close.
func (l *fileLock) File() *os.File {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file
}

// Close closes this process's descriptor. It deliberately does not issue an
// explicit LOCK_UN: descriptors inherited through exec share the same open-file
// description, and unlocking here would also drop the child's lease. The
// kernel releases the lock automatically after the last duplicate descriptor
// is closed. It is safe to call repeatedly or concurrently.
func (l *fileLock) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return l.closeErr
	}
	l.closed = true

	file := l.file
	l.file = nil
	if file == nil {
		return nil
	}
	closeErr := file.Close()
	if closeErr != nil {
		l.closeErr = fmt.Errorf("repoctx: close file lock %q: %w", file.Name(), closeErr)
	}
	return l.closeErr
}

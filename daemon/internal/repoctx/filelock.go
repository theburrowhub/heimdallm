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
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("repoctx: open file lock %q: %w", path, err)
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

// Close releases the advisory lock and closes its descriptor. It is safe to
// call repeatedly or concurrently; every caller observes the first close
// result.
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
	unlockErr := unlockFile(file)
	closeErr := file.Close()
	switch {
	case unlockErr != nil && closeErr != nil:
		l.closeErr = errors.Join(
			fmt.Errorf("repoctx: unlock file lock %q: %w", file.Name(), unlockErr),
			fmt.Errorf("repoctx: close file lock %q: %w", file.Name(), closeErr),
		)
	case unlockErr != nil:
		l.closeErr = fmt.Errorf("repoctx: unlock file lock %q: %w", file.Name(), unlockErr)
	case closeErr != nil:
		l.closeErr = fmt.Errorf("repoctx: close file lock %q: %w", file.Name(), closeErr)
	}
	return l.closeErr
}

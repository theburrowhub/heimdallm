//go:build darwin || linux

// Package singleinstance owns the daemon's process-wide lifecycle lock.
package singleinstance

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"

	"golang.org/x/sys/unix"
)

// ErrAlreadyRunning means another daemon which uses the same data directory
// still owns the lifecycle lock.
var ErrAlreadyRunning = errors.New("heimdallm daemon already running")

// Lock is an advisory OS lock held by an open file descriptor. The kernel
// releases it automatically on process exit, including crashes and SIGKILL.
type Lock struct {
	file *os.File
	once sync.Once
	err  error
}

// Acquire takes a non-blocking exclusive lock at path. The file is deliberately
// never unlinked: removing a flock file creates an inode race where old and new
// processes can each lock a different file under the same pathname.
func Acquire(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("single-instance: open lock file: %w", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, fmt.Errorf("%w: %s", ErrAlreadyRunning, path)
		}
		return nil, fmt.Errorf("single-instance: lock %s: %w", path, err)
	}

	// PID contents are diagnostic only; flock ownership, not this value, is the
	// authority. Update it only after acquiring the lock so a losing process can
	// never overwrite the active owner's PID.
	if err := f.Truncate(0); err == nil {
		_, err = f.Seek(0, 0)
	}
	if err == nil {
		_, err = f.WriteString(strconv.Itoa(os.Getpid()) + "\n")
	}
	if err != nil {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
		return nil, fmt.Errorf("single-instance: write owner pid: %w", err)
	}

	return &Lock{file: f}, nil
}

// Close releases the lock. It is idempotent.
func (l *Lock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	l.once.Do(func() {
		unlockErr := unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
		closeErr := l.file.Close()
		if unlockErr != nil {
			l.err = fmt.Errorf("single-instance: unlock: %w", unlockErr)
		} else if closeErr != nil {
			l.err = fmt.Errorf("single-instance: close lock file: %w", closeErr)
		}
	})
	return l.err
}

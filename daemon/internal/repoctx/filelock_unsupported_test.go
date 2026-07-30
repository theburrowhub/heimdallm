//go:build !darwin && !linux

package repoctx

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestFileLockFailsClosedOnUnsupportedPlatform(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo.lock")

	if lock, err := acquireFileLock(context.Background(), path); lock != nil || !errors.Is(err, errFileLockUnsupported) {
		t.Fatalf("acquireFileLock = (%v, %v), want unsupported error", lock, err)
	}
	if lock, acquired, err := tryFileLock(path); lock != nil || acquired || !errors.Is(err, errFileLockUnsupported) {
		t.Fatalf("tryFileLock = (%v, %t, %v), want unsupported error", lock, acquired, err)
	}
}

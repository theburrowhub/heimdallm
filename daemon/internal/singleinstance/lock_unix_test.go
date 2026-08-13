//go:build darwin || linux

package singleinstance_test

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/heimdallm/daemon/internal/singleinstance"
)

func TestLockExcludesUntilOwnerFinishes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	first, err := singleinstance.Acquire(path)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })

	if _, err := singleinstance.Acquire(path); !errors.Is(err, singleinstance.ErrAlreadyRunning) {
		t.Fatalf("second Acquire error = %v, want ErrAlreadyRunning", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read lock file: %v", err)
	}
	if got, want := strings.TrimSpace(string(body)), strconv.Itoa(os.Getpid()); got != want {
		t.Fatalf("owner PID = %q, want %q", got, want)
	}

	if err := first.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	second, err := singleinstance.Acquire(path)
	if err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("idempotent Close: %v", err)
	}
}

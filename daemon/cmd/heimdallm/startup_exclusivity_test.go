package main

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heimdallm/daemon/internal/singleinstance"
)

func TestRunInstanceLockPrecedesStateInitialization(t *testing.T) {
	tempDir := t.TempDir()
	lock, err := singleinstance.Acquire(filepath.Join(tempDir, "daemon.lock"))
	if err != nil {
		t.Fatalf("hold instance lock: %v", err)
	}
	defer lock.Close()

	t.Setenv("HEIMDALLM_DATA_DIR", tempDir)
	originalArgs := os.Args
	os.Args = []string{"heimdallm"}
	t.Cleanup(func() { os.Args = originalArgs })

	if code := run(); code != 1 {
		t.Fatalf("run exit code = %d, want 1 for occupied lifecycle lock", code)
	}
	if _, err := os.Stat(filepath.Join(tempDir, "heimdallm.db")); !os.IsNotExist(err) {
		t.Fatalf("SQLite was initialized before the lifecycle lock failed: err=%v", err)
	}
}

// A second process must lose single-instance ownership before it can open or
// mutate the shared SQLite database. This process-level test pins the ordering
// in run, not just the lower-level net.Listen contract.
func TestRunBindFailurePrecedesStateInitialization(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy port: %v", err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.toml")
	configBody := fmt.Sprintf(`[server]
port = %d
bind_addr = "127.0.0.1"

[ai]
primary = "codex"
fallback = "claude"
`, port)
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("HEIMDALLM_CONFIG_PATH", configPath)
	t.Setenv("HEIMDALLM_DATA_DIR", tempDir)

	originalArgs := os.Args
	originalLogger := slog.Default()
	os.Args = []string{"heimdallm"}
	t.Cleanup(func() {
		os.Args = originalArgs
		slog.SetDefault(originalLogger)
	})

	if code := run(); code != 1 {
		t.Fatalf("run exit code = %d, want 1 for occupied port", code)
	}
	if _, err := os.Stat(filepath.Join(tempDir, "heimdallm.db")); !os.IsNotExist(err) {
		t.Fatalf("SQLite was initialized before the bind failed: err=%v", err)
	}
	logBody, err := os.ReadFile(filepath.Join(tempDir, "heimdallm.log"))
	if err != nil {
		t.Fatalf("read startup log: %v", err)
	}
	if !strings.Contains(string(logBody), "cannot bind HTTP port") {
		t.Fatalf("run did not reach the bind-failure path; log=%s", logBody)
	}
}

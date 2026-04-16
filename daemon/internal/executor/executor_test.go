package executor_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/heimdallm/daemon/internal/executor"
)

func TestDetect(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	binDir := filepath.Join(filepath.Dir(file), "testdata", "bin")
	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", binDir+":"+oldPath)
	defer os.Setenv("PATH", oldPath)

	e := executor.New()
	cli, err := e.Detect("claude", "")
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if cli != "claude" {
		t.Errorf("expected fake_claude, got %q", cli)
	}
}

func TestDetect_Fallback(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	binDir := filepath.Join(filepath.Dir(file), "testdata", "bin")
	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", binDir+":"+oldPath)
	defer os.Setenv("PATH", oldPath)

	e := executor.New()
	cli, err := e.Detect("codex", "gemini")
	if err != nil {
		t.Fatalf("detect with fallback: %v", err)
	}
	if cli != "gemini" {
		t.Errorf("expected fake_gemini fallback, got %q", cli)
	}
}

func TestDetect_NoneAvailable(t *testing.T) {
	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", "")
	defer os.Setenv("PATH", oldPath)

	e := executor.New()
	_, err := e.Detect("nonexistent", "also_nonexistent")
	if err == nil {
		t.Error("expected error when no CLI available")
	}
}

func TestExecute(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	binDir := filepath.Join(filepath.Dir(file), "testdata", "bin")
	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", binDir+":"+oldPath)
	defer os.Setenv("PATH", oldPath)

	e := executor.New()
	result, err := e.Execute("claude", "Review this diff", executor.ExecOptions{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Summary == "" {
		t.Error("expected non-empty summary")
	}
	if result.Severity == "" {
		t.Error("expected non-empty severity")
	}
}

func TestValidateWorkDir(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("cannot get home dir: %v", err)
	}

	// A safe subdirectory inside HOME for the "pass" test case.
	safeDir := filepath.Join(home, "Documents")
	// If ~/Documents doesn't exist on this machine, fall back to home itself.
	if _, statErr := os.Stat(safeDir); statErr != nil {
		safeDir = home
	}

	tests := []struct {
		name    string
		dir     string
		wantErr bool
	}{
		{
			name:    "empty dir — no validation",
			dir:     "",
			wantErr: false,
		},
		{
			name:    "home subdir — allowed",
			dir:     safeDir,
			wantErr: false,
		},
		{
			name:    "filesystem root — rejected",
			dir:     "/",
			wantErr: true,
		},
		{
			name:    "ssh dir — rejected",
			dir:     filepath.Join(home, ".ssh"),
			wantErr: true,
		},
		{
			name:    "/etc — rejected",
			dir:     "/etc",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := executor.ValidateWorkDir(tc.dir)
			if tc.wantErr && err == nil {
				t.Errorf("expected error for dir %q, got nil", tc.dir)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error for dir %q: %v", tc.dir, err)
			}
		})
	}
}

func TestValidateExtraFlags(t *testing.T) {
	tests := []struct {
		name    string
		flags   string
		wantErr bool
	}{
		{
			name:    "empty flags — allowed",
			flags:   "",
			wantErr: false,
		},
		{
			name:    "safe model flag — allowed",
			flags:   "--model claude-opus-4-6",
			wantErr: false,
		},
		{
			name:    "--dangerously-skip-permissions — rejected",
			flags:   "--dangerously-skip-permissions",
			wantErr: true,
		},
		{
			name:    "bare bypassPermissions value — rejected",
			flags:   "--permission-mode bypassPermissions",
			wantErr: true,
		},
		{
			name:    "--permission-mode=bypassPermissions single token — rejected",
			flags:   "--permission-mode=bypassPermissions",
			wantErr: true,
		},
		{
			name:    "--permission-mode flag alone — rejected (dedicated field exists)",
			flags:   "--permission-mode",
			wantErr: true,
		},
		{
			name:    "mixed safe flags — allowed",
			flags:   "--model claude-opus-4-6 --max-turns 5",
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := executor.ValidateExtraFlags(tc.flags)
			if tc.wantErr && err == nil {
				t.Errorf("expected error for flags %q, got nil", tc.flags)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error for flags %q: %v", tc.flags, err)
			}
		})
	}
}

func TestBuildPrompt(t *testing.T) {
	prompt := executor.BuildPrompt("Fix nil deref", "alice", "+foo\n-bar\n")
	if len(prompt) == 0 {
		t.Error("expected non-empty prompt")
	}
	if len(prompt) > 40000 {
		t.Error("prompt too long — diff not normalized")
	}
}

package executor_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/heimdallr/daemon/internal/executor"
)

func TestDetect(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	binDir := filepath.Join(filepath.Dir(file), "testdata", "bin")
	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", binDir+":"+oldPath)
	defer os.Setenv("PATH", oldPath)

	e := executor.New()
	cli, err := e.Detect("fake_claude", "")
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if cli != "fake_claude" {
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
	cli, err := e.Detect("nonexistent_cli", "fake_gemini")
	if err != nil {
		t.Fatalf("detect with fallback: %v", err)
	}
	if cli != "fake_gemini" {
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
	result, err := e.Execute("fake_claude", "Review this diff")
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

func TestBuildPrompt(t *testing.T) {
	prompt := executor.BuildPrompt("Fix nil deref", "alice", "+foo\n-bar\n")
	if len(prompt) == 0 {
		t.Error("expected non-empty prompt")
	}
	if len(prompt) > 40000 {
		t.Error("prompt too long — diff not normalized")
	}
}

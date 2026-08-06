package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestMainReportsVersion(t *testing.T) {
	originalArgs := os.Args
	originalStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	t.Cleanup(func() {
		os.Args = originalArgs
		os.Stdout = originalStdout
		_ = reader.Close()
		_ = writer.Close()
	})

	os.Args = []string{"heimdallm-cli", "--version"}
	os.Stdout = writer
	main()
	if err := writer.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}
	os.Stdout = originalStdout

	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if got, want := string(output), "heimdallm-cli version dev\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestMainExitsOnCommandError(t *testing.T) {
	originalArgs := os.Args
	originalStderr := os.Stderr
	originalExitProcess := exitProcess
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	t.Cleanup(func() {
		os.Args = originalArgs
		os.Stderr = originalStderr
		exitProcess = originalExitProcess
		_ = reader.Close()
		_ = writer.Close()
	})

	os.Args = []string{"heimdallm-cli", "not-a-command"}
	os.Stderr = writer
	exitCode := 0
	exitProcess = func(code int) { exitCode = code }
	main()
	if err := writer.Close(); err != nil {
		t.Fatalf("close stderr writer: %v", err)
	}
	os.Stderr = originalStderr

	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	if exitCode != 1 {
		t.Fatalf("exit code = %d, want 1", exitCode)
	}
	if got := string(output); !strings.Contains(got, "unknown command") {
		t.Fatalf("stderr = %q, want unknown-command error", got)
	}
}

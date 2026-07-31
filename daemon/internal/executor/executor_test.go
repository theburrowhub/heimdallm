package executor_test

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/executor"
)

func TestDetect(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	binDir := filepath.Join(filepath.Dir(file), "testdata", "bin")
	t.Setenv("PATH", binDir)

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
	t.Setenv("PATH", binDir)
	// Neutralize the login-shell probe: without this, a real `codex` installed
	// on the developer's machine is resolved via the login shell regardless of
	// the isolated $PATH, so the primary never "fails" and the fallback path is
	// never exercised (the test then flakes on any dev box with codex present).
	defer executor.SetLoginShellLookPathForTest(func(string) string { return "" })()
	// Same for the well-known-dirs probe (e.g. codex in /opt/homebrew/bin).
	defer executor.SetWellKnownBinDirsForTest(func() []string { return nil })()

	e := executor.New()
	cli, err := e.Detect("codex", "gemini")
	if err != nil {
		t.Fatalf("detect with fallback: %v", err)
	}
	if cli != "gemini" {
		t.Errorf("expected fake_gemini fallback, got %q", cli)
	}
}

func TestDetect_WellKnownDirFallback(t *testing.T) {
	// Neither the process PATH nor the login shell can resolve the CLI — the
	// exact environment of a daemon started by launchd with the minimal system
	// PATH and a CLI installed by an installer that only edits ~/.zshrc.
	t.Setenv("PATH", t.TempDir())
	defer executor.SetLoginShellLookPathForTest(func(string) string { return "" })()

	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	defer executor.SetWellKnownBinDirsForTest(func() []string { return []string{binDir} })()

	e := executor.New()
	cli, err := e.Detect("claude", "")
	if err != nil {
		t.Fatalf("detect via well-known dir: %v", err)
	}
	if cli != "claude" {
		t.Errorf("expected claude, got %q", cli)
	}
}

func TestDetect_WellKnownDirSkipsNonExecutable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	defer executor.SetLoginShellLookPathForTest(func(string) string { return "" })()

	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte("not a binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer executor.SetWellKnownBinDirsForTest(func() []string { return []string{binDir} })()

	e := executor.New()
	if _, err := e.Detect("claude", ""); err == nil {
		t.Error("expected error: a non-executable candidate must not be selected")
	}
}

func TestDetect_WellKnownDirSkipsDirectoryNamedLikeCLI(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	defer executor.SetLoginShellLookPathForTest(func(string) string { return "" })()

	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(filepath.Join(binDir, "claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	defer executor.SetWellKnownBinDirsForTest(func() []string { return []string{binDir} })()

	e := executor.New()
	if _, err := e.Detect("claude", ""); err == nil {
		t.Error("expected error: a directory named like the CLI must not be selected")
	}
}

func TestExecuteRawWellKnownDirPrecedence(t *testing.T) {
	// System dirs, not an empty temp dir — see TestExecuteRawAddsWellKnownDirToChildPATH.
	t.Setenv("PATH", "/usr/bin:/bin")
	defer executor.SetLoginShellLookPathForTest(func(string) string { return "" })()
	defer executor.ResetLoginPathCacheForTest()()

	first := t.TempDir()
	second := t.TempDir()
	for dir, marker := range map[string]string{first: "first", second: "second"} {
		script := "#!/bin/sh\nprintf '" + marker + "'\n"
		if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	defer executor.SetWellKnownBinDirsForTest(func() []string { return []string{first, second} })()

	e := executor.New()
	out, err := e.ExecuteRaw("claude", "prompt", executor.ExecOptions{})
	if err != nil {
		t.Fatalf("ExecuteRaw: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "first" {
		t.Errorf("expected the CLI from the first well-known dir to win, got %q", got)
	}
}

func TestWellKnownBinDirsDefaults(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("HOME-based derivation is Unix-only; the daemon does not target Windows")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	dirs := executor.DefaultWellKnownBinDirsForTest()
	want := []string{
		filepath.Join(home, ".local", "bin"),
		"/opt/homebrew/bin",
		"/usr/local/bin",
	}
	for _, w := range want {
		found := false
		for _, d := range dirs {
			if d == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("default well-known dirs %v missing %q", dirs, w)
		}
	}
}

func TestExecuteRawAddsWellKnownDirToChildPATH(t *testing.T) {
	// The CLI is launched by absolute path, but if it re-invokes itself (or a
	// sibling tool installed next to it) by bare name, the child's PATH must
	// contain the well-known dir the CLI was resolved from — that dir is, by
	// definition of this fallback, missing from both the process PATH and the
	// login-shell PATH.
	//
	// Keep the system dirs (not an empty temp dir) on PATH: later tests' fake
	// CLIs need coreutils (see the comment in TestExecute). The cache reset
	// additionally guarantees this test's PATH snapshot cannot leak into other
	// tests regardless of execution order.
	t.Setenv("PATH", "/usr/bin:/bin")
	defer executor.SetLoginShellLookPathForTest(func(string) string { return "" })()
	defer executor.ResetLoginPathCacheForTest()()

	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '%s' \"$PATH\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	defer executor.SetWellKnownBinDirsForTest(func() []string { return []string{binDir} })()

	e := executor.New()
	out, err := e.ExecuteRaw("claude", "prompt", executor.ExecOptions{})
	if err != nil {
		t.Fatalf("ExecuteRaw: %v", err)
	}
	for _, p := range filepath.SplitList(strings.TrimSpace(string(out))) {
		if p == binDir {
			return
		}
	}
	t.Fatalf("child PATH %q does not contain the resolved CLI dir %q", out, binDir)
}

func TestAppendDirToPathHandlesEmptyPathValue(t *testing.T) {
	// "PATH=" + ":" + dir would create a leading empty element, which POSIX
	// PATH semantics interpret as the current directory.
	env := executor.AppendDirToPathForTest([]string{"PATH="}, "/opt/tools")
	if got, want := env[0], "PATH=/opt/tools"; got != want {
		t.Errorf("appendDirToPath on empty PATH = %q, want %q", got, want)
	}
}

func TestCliHelpChildPATHIncludesResolvedDir(t *testing.T) {
	// The --help probe (cliHelp) must run with the same PATH enrichment as the
	// real execution: a CLI resolved from a well-known dir may exec a sibling
	// tool by bare name during --help. If that fails, detectWorkDirFlags
	// silently degrades to cwd-only context and the CLI runs without
	// --add-dir even though it supports it.
	t.Setenv("PATH", "/usr/bin:/bin")
	defer executor.SetLoginShellLookPathForTest(func(string) string { return "" })()
	defer executor.ResetLoginPathCacheForTest()()

	binDir := t.TempDir()
	helper := "#!/bin/sh\nprintf 'Usage: claude\\n  --add-dir <directories...>\\n'\n"
	if err := os.WriteFile(filepath.Join(binDir, "help-sibling"), []byte(helper), 0o755); err != nil {
		t.Fatal(err)
	}
	captureArgs := filepath.Join(t.TempDir(), "args.txt")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--help\" ]; then\n" +
		"  exec help-sibling\n" +
		"fi\n" +
		"printf '%s' \"$*\" > " + captureArgs + "\n" +
		"printf '{}'\n"
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	defer executor.SetWellKnownBinDirsForTest(func() []string { return []string{binDir} })()

	e := executor.New()
	workDir := t.TempDir()
	if _, err := e.ExecuteRaw("claude", "prompt", executor.ExecOptions{WorkDir: workDir}); err != nil {
		t.Fatalf("ExecuteRaw: %v", err)
	}
	argsBytes, err := os.ReadFile(captureArgs)
	if err != nil {
		t.Fatalf("read captured args: %v", err)
	}
	if !strings.Contains(string(argsBytes), "--add-dir") {
		t.Fatalf("args %q missing --add-dir — cliHelp could not resolve the sibling tool", string(argsBytes))
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

func TestOptionsForSelectedCLI(t *testing.T) {
	leaseFile, err := os.CreateTemp(t.TempDir(), "lease-*")
	if err != nil {
		t.Fatalf("create lease file: %v", err)
	}
	defer leaseFile.Close()
	inheritedFiles := executor.NewInheritedFileSet(leaseFile)

	original := executor.ExecOptions{
		Model:                "primary-model",
		MaxTurns:             7,
		ApprovalMode:         "never",
		ExtraFlags:           "--json",
		WorkDir:              "/tmp/repo",
		Effort:               "high",
		PermissionMode:       "acceptEdits",
		Bare:                 true,
		DangerouslySkipPerms: true,
		NoSessionPersistence: true,
		Timeout:              9 * time.Minute,
		ExtraFiles:           inheritedFiles,
	}

	if got := executor.OptionsForSelectedCLI("codex", "codex", original); !reflect.DeepEqual(got, original) {
		t.Fatalf("same provider changed options:\n got: %+v\nwant: %+v", got, original)
	}

	got := executor.OptionsForSelectedCLI("codex", "gemini", original)
	want := executor.ExecOptions{
		WorkDir:    "/tmp/repo",
		Timeout:    9 * time.Minute,
		ExtraFiles: inheritedFiles,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fallback options:\n got: %+v\nwant: %+v", got, want)
	}
}

func TestNormalizeTypedModesAcceptsHarmlessCaseVariants(t *testing.T) {
	if got, err := executor.NormalizeEffort(" HIGH "); err != nil || got != "high" {
		t.Fatalf("NormalizeEffort = %q, %v; want high", got, err)
	}
	if got, err := executor.NormalizePermissionMode("ACCEPTEDITS"); err != nil || got != "acceptEdits" {
		t.Fatalf("NormalizePermissionMode = %q, %v; want acceptEdits", got, err)
	}
	if got, err := executor.NormalizeApprovalModeForCLI("codex", "FULL-AUTO"); err != nil || got != "never" {
		t.Fatalf("NormalizeApprovalModeForCLI(codex) = %q, %v; want never", got, err)
	}
	if got, err := executor.NormalizeApprovalModeForCLI("gemini", "Auto-Edit"); err != nil || got != "auto_edit" {
		t.Fatalf("NormalizeApprovalModeForCLI(gemini) = %q, %v; want auto_edit", got, err)
	}
}

func TestMigrateLegacyTypedExtraFlagsForCLI(t *testing.T) {
	tests := []struct {
		name       string
		cli        string
		opts       executor.ExecOptions
		want       executor.ExecOptions
		wantFields []string
	}{
		{
			name: "Claude promotes legacy typed fields and preserves safe flags",
			cli:  "claude",
			opts: executor.ExecOptions{
				ExtraFlags: "--model opus --maxTurns=8 --effort HIGH --verbose",
			},
			want: executor.ExecOptions{
				Model:      "opus",
				MaxTurns:   8,
				Effort:     "high",
				ExtraFlags: "--verbose",
			},
			wantFields: []string{"model", "max_turns", "effort"},
		},
		{
			name: "explicit typed model wins over legacy duplicate",
			cli:  "codex",
			opts: executor.ExecOptions{
				Model:      "typed",
				ExtraFlags: "-mlegacy --json",
			},
			want: executor.ExecOptions{
				Model:      "typed",
				ExtraFlags: "--json",
			},
			wantFields: []string{"model"},
		},
		{
			name: "invalid legacy typed value remains for strict rejection",
			cli:  "claude",
			opts: executor.ExecOptions{
				ExtraFlags: "--model --sandbox danger-full-access",
			},
			want: executor.ExecOptions{
				ExtraFlags: "--model --sandbox danger-full-access",
			},
		},
		{
			name: "approval flags are deliberately not migrated",
			cli:  "codex",
			opts: executor.ExecOptions{
				ApprovalMode: "never",
				ExtraFlags:   "--ask-for-approval on-request --json",
			},
			want: executor.ExecOptions{
				ApprovalMode: "never",
				ExtraFlags:   "--ask-for-approval on-request --json",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, fields := executor.MigrateLegacyTypedExtraFlagsForCLI(tc.cli, tc.opts)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("migrated options:\n got: %+v\nwant: %+v", got, tc.want)
			}
			if !reflect.DeepEqual(fields, tc.wantFields) {
				t.Fatalf("migrated fields = %v, want %v", fields, tc.wantFields)
			}
		})
	}
}

func TestNormalizeLegacyCLIFlagsForCLI(t *testing.T) {
	got, fields, err := executor.NormalizeLegacyCLIFlagsForCLI(
		"claude",
		"--model profile-model --max-turns 7 --effort HIGH --verbose",
	)
	if err != nil {
		t.Fatalf("NormalizeLegacyCLIFlagsForCLI: %v", err)
	}
	want := executor.ExecOptions{
		Model:      "profile-model",
		MaxTurns:   7,
		Effort:     "high",
		ExtraFlags: "--verbose",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized profile options:\n got: %+v\nwant: %+v", got, want)
	}
	if wantFields := []string{"model", "max_turns", "effort"}; !reflect.DeepEqual(fields, wantFields) {
		t.Fatalf("migrated fields = %v, want %v", fields, wantFields)
	}

	if _, _, err := executor.NormalizeLegacyCLIFlagsForCLI(
		"codex",
		"--model gpt-5 --sandbox danger-full-access",
	); err == nil {
		t.Fatal("unsafe sibling flag was accepted after typed model migration")
	}
	if _, _, err := executor.NormalizeLegacyCLIFlagsForCLI(
		"codex",
		"--ask-for-approval on-request --json",
	); err == nil {
		t.Fatal("legacy approval flag unexpectedly migrated or accepted")
	}
}

func TestExecute(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	binDir := filepath.Join(filepath.Dir(file), "testdata", "bin")
	// Prepend (not replace) so the fake `claude` is resolved first while the
	// system coreutils the fake script relies on (`cat`) stay on PATH. On a
	// dash-based /bin/sh (Linux CI runners) `cat` is not a shell builtin, so a
	// replaced PATH makes the fake CLI exit 127 with "cat: not found". The fake
	// binary wins because binDir comes first.
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

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

func TestExecuteRawAddsDetectedWorkDirFlags(t *testing.T) {
	tests := []struct {
		cli      string
		help     string
		wantFlag string
	}{
		{cli: "claude", help: "Usage: claude\n  --add-dir <directories...>\n", wantFlag: "--add-dir"},
		{cli: "gemini", help: "Usage: gemini\n  --include-directories <dirs>\n", wantFlag: "--include-directories"},
		{cli: "codex", help: "Usage: codex\n  -C, --cd <DIR>\n", wantFlag: "--cd"},
	}

	for _, tc := range tests {
		t.Run(tc.cli, func(t *testing.T) {
			binDir := t.TempDir()
			captureArgs := filepath.Join(t.TempDir(), "args.txt")
			captureCWD := filepath.Join(t.TempDir(), "cwd.txt")
			script := fakeCLIScript(tc.help, captureArgs, captureCWD)
			path := filepath.Join(binDir, tc.cli)
			if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
				t.Fatalf("write fake CLI: %v", err)
			}
			t.Setenv("PATH", binDir)

			workDir := t.TempDir()
			e := executor.New()
			if _, err := e.ExecuteRaw(tc.cli, "prompt", executor.ExecOptions{WorkDir: workDir}); err != nil {
				t.Fatalf("ExecuteRaw: %v", err)
			}

			argsBytes, err := os.ReadFile(captureArgs)
			if err != nil {
				t.Fatalf("read captured args: %v", err)
			}
			args := strings.Fields(string(argsBytes))
			if !containsInOrder(args, tc.wantFlag, workDir) {
				t.Fatalf("args = %v, want %s %s", args, tc.wantFlag, workDir)
			}
			cwdBytes, err := os.ReadFile(captureCWD)
			if err != nil {
				t.Fatalf("read captured cwd: %v", err)
			}
			requireSameDir(t, strings.TrimSpace(string(cwdBytes)), workDir)
		})
	}
}

func TestExecuteRawFallsBackToCWDWhenWorkDirFlagUnsupported(t *testing.T) {
	binDir := t.TempDir()
	captureArgs := filepath.Join(t.TempDir(), "args.txt")
	captureCWD := filepath.Join(t.TempDir(), "cwd.txt")
	script := fakeCLIScript("Usage: claude\n", captureArgs, captureCWD)
	path := filepath.Join(binDir, "claude")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake CLI: %v", err)
	}
	t.Setenv("PATH", binDir)

	workDir := t.TempDir()
	e := executor.New()
	if _, err := e.ExecuteRaw("claude", "prompt", executor.ExecOptions{WorkDir: workDir}); err != nil {
		t.Fatalf("ExecuteRaw: %v", err)
	}
	argsBytes, err := os.ReadFile(captureArgs)
	if err != nil {
		t.Fatalf("read captured args: %v", err)
	}
	if strings.Contains(string(argsBytes), workDir) {
		t.Fatalf("args = %q, workdir should only be passed via cwd when no supported flag is advertised", string(argsBytes))
	}
	cwdBytes, err := os.ReadFile(captureCWD)
	if err != nil {
		t.Fatalf("read captured cwd: %v", err)
	}
	requireSameDir(t, strings.TrimSpace(string(cwdBytes)), workDir)
}

func TestExecuteRawCodexUsesExecAndReadsPromptFromStdin(t *testing.T) {
	binDir := t.TempDir()
	captureArgs := filepath.Join(t.TempDir(), "args.txt")
	capturePrompt := filepath.Join(t.TempDir(), "prompt.txt")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--help\" ]; then\n" +
		"  printf 'Usage: codex\\n  -C, --cd <DIR>\\n'\n" +
		"  exit 0\n" +
		"fi\n" +
		"last=''\n" +
		"saw_exec=0\n" +
		"for arg in \"$@\"; do last=\"$arg\"; done\n" +
		"for arg in \"$@\"; do if [ \"$arg\" = \"exec\" ]; then saw_exec=1; fi; done\n" +
		"if [ \"$saw_exec\" != \"1\" ]; then\n" +
		"  printf 'exec subcommand missing\\n' >&2\n" +
		"  exit 2\n" +
		"fi\n" +
		"if [ \"$last\" != \"-\" ]; then\n" +
		"  printf 'stdin prompt marker missing\\n' >&2\n" +
		"  exit 2\n" +
		"fi\n" +
		"cat > " + shellQuote(capturePrompt) + "\n" +
		"printf '%s\\n' \"$*\" > " + shellQuote(captureArgs) + "\n" +
		"printf 'ok\\n'\n"
	path := filepath.Join(binDir, "codex")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake CLI: %v", err)
	}
	// Prepend so the fake `codex` wins while the script's `cat > file`
	// (stdin capture) can still resolve system `cat`. See TestExecute for why
	// a replaced PATH breaks on dash-based /bin/sh.
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	prompt := "review this PR"
	e := executor.New()
	if _, err := e.ExecuteRaw("codex", prompt, executor.ExecOptions{ApprovalMode: "full-auto"}); err != nil {
		t.Fatalf("ExecuteRaw: %v", err)
	}

	argsBytes, err := os.ReadFile(captureArgs)
	if err != nil {
		t.Fatalf("read captured args: %v", err)
	}
	args := strings.Fields(string(argsBytes))
	execIdx := indexOf(args, "exec")
	if execIdx < 0 {
		t.Fatalf("args = %v, want codex exec subcommand", args)
	}
	if got := args[len(args)-1]; got != "-" {
		t.Fatalf("args = %v, want stdin marker '-' as final arg", args)
	}
	approvalIdx := indexOf(args, "--ask-for-approval")
	if approvalIdx < 0 || approvalIdx > execIdx || !containsInOrder(args, "--ask-for-approval", "never") {
		t.Fatalf("args = %v, want legacy full-auto mapped to --ask-for-approval never", args)
	}
	if strings.Contains(string(argsBytes), "--approval-mode") {
		t.Fatalf("args = %v, must not use removed --approval-mode flag", args)
	}
	promptBytes, err := os.ReadFile(capturePrompt)
	if err != nil {
		t.Fatalf("read captured prompt: %v", err)
	}
	if string(promptBytes) != prompt {
		t.Fatalf("prompt = %q, want %q", string(promptBytes), prompt)
	}
}

func TestExecuteRawPassesExtraFilesToChild(t *testing.T) {
	binDir := t.TempDir()
	path := filepath.Join(binDir, "claude")
	script := "#!/bin/sh\n" +
		"IFS= read -r inherited <&3 || exit 2\n" +
		"printf '%s\\n' \"$inherited\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake CLI: %v", err)
	}
	t.Setenv("PATH", binDir)

	leaseFile, err := os.CreateTemp(t.TempDir(), "lease-*")
	if err != nil {
		t.Fatalf("create lease file: %v", err)
	}
	defer leaseFile.Close()
	if _, err := leaseFile.WriteString("lease-held-by-child\n"); err != nil {
		t.Fatalf("write lease file: %v", err)
	}
	if _, err := leaseFile.Seek(0, 0); err != nil {
		t.Fatalf("rewind lease file: %v", err)
	}

	raw, err := executor.New().ExecuteRaw("claude", "prompt", executor.ExecOptions{
		ExtraFiles: executor.NewInheritedFileSet(leaseFile),
	})
	if err != nil {
		t.Fatalf("ExecuteRaw: %v", err)
	}
	if got, want := strings.TrimSpace(string(raw)), "lease-held-by-child"; got != want {
		t.Fatalf("child read inherited fd 3 = %q, want %q", got, want)
	}
}

func TestValidateApprovalModeAcceptsCurrentAndLegacyCodexValues(t *testing.T) {
	for _, mode := range []string{"", "untrusted", "on-failure", "on-request", "never", "auto-edit", "full-auto", "suggest"} {
		t.Run(mode, func(t *testing.T) {
			if err := executor.ValidateApprovalMode(mode); err != nil {
				t.Fatalf("ValidateApprovalMode(%q): %v", mode, err)
			}
		})
	}

	if err := executor.ValidateApprovalMode("danger-full-access"); err == nil {
		t.Fatal("ValidateApprovalMode accepted an unsupported value")
	}
}

func TestValidateApprovalModeForCLI(t *testing.T) {
	tests := []struct {
		name    string
		cli     string
		mode    string
		wantErr bool
	}{
		{name: "codex current", cli: "codex", mode: "on-request"},
		{name: "codex harmless casing", cli: "codex", mode: "On-Request"},
		{name: "codex legacy", cli: "codex", mode: "auto-edit"},
		{name: "codex rejects gemini plan", cli: "codex", mode: "plan", wantErr: true},
		{name: "gemini default", cli: "gemini", mode: "default"},
		{name: "gemini underscore auto edit", cli: "gemini", mode: "auto_edit"},
		{name: "gemini hyphen auto edit", cli: "gemini", mode: "auto-edit"},
		{name: "gemini harmless casing", cli: "gemini", mode: "AUTO-EDIT"},
		{name: "gemini plan", cli: "gemini", mode: "plan"},
		{name: "gemini rejects yolo", cli: "gemini", mode: "yolo", wantErr: true},
		{name: "gemini rejects codex never", cli: "gemini", mode: "never", wantErr: true},
		{name: "ignored codex mode remains safe on claude fallback", cli: "claude", mode: "on-request"},
		{name: "ignored gemini mode remains safe on opencode fallback", cli: "opencode", mode: "auto_edit"},
		{name: "fallback still rejects unknown mode", cli: "claude", mode: "bypassPermissions", wantErr: true},
		{name: "unknown CLI", cli: "other", mode: "default", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := executor.ValidateApprovalModeForCLI(tc.cli, tc.mode)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidateApprovalModeForCLI(%q, %q) unexpectedly succeeded", tc.cli, tc.mode)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateApprovalModeForCLI(%q, %q): %v", tc.cli, tc.mode, err)
			}
		})
	}
}

func TestExecuteRawGeminiAddsTypedApprovalBeforeSafeExtraFlags(t *testing.T) {
	binDir := t.TempDir()
	captureArgs := filepath.Join(t.TempDir(), "args.txt")
	captureCWD := filepath.Join(t.TempDir(), "cwd.txt")
	path := filepath.Join(binDir, "gemini")
	if err := os.WriteFile(path, []byte(fakeCLIScript("Usage: gemini\n", captureArgs, captureCWD)), 0o755); err != nil {
		t.Fatalf("write fake Gemini: %v", err)
	}
	t.Setenv("PATH", binDir)

	e := executor.New()
	opts := executor.ExecOptions{
		ApprovalMode: "AUTO-EDIT",
		ExtraFlags:   "--output-format json --debug --output-format text",
	}
	if _, err := e.ExecuteRaw("gemini", "prompt", opts); err != nil {
		t.Fatalf("ExecuteRaw: %v", err)
	}

	argsBytes, err := os.ReadFile(captureArgs)
	if err != nil {
		t.Fatalf("read captured args: %v", err)
	}
	args := strings.Fields(string(argsBytes))
	if !containsInOrder(args, "--approval-mode", "auto_edit") {
		t.Fatalf("args = %v, want normalized Gemini approval mode", args)
	}
	approvalIdx := indexOf(args, "--approval-mode")
	outputIdx := indexOf(args, "--output-format")
	if outputIdx < 0 || approvalIdx > outputIdx {
		t.Fatalf("args = %v, typed approval must precede safe ExtraFlags", args)
	}
	if strings.Count(string(argsBytes), "--output-format") != 2 {
		t.Fatalf("args = %v, repeated safe flags must be preserved", args)
	}
}

func TestExecuteRawRejectsUnsafeRequestBeforeStartingCLI(t *testing.T) {
	closedFile, err := os.CreateTemp(t.TempDir(), "closed-extra-file-*")
	if err != nil {
		t.Fatalf("create closed extra file: %v", err)
	}
	if err := closedFile.Close(); err != nil {
		t.Fatalf("close extra file: %v", err)
	}

	tests := []struct {
		name string
		cli  string
		opts executor.ExecOptions
	}{
		{name: "unknown CLI", cli: "other"},
		{
			name: "invalid typed Claude permission mode",
			cli:  "claude",
			opts: executor.ExecOptions{PermissionMode: "bypassPermissions"},
		},
		{
			name: "option-shaped typed model",
			cli:  "claude",
			opts: executor.ExecOptions{Model: "--dangerously-skip-permissions"},
		},
		{
			name: "option-shaped typed effort",
			cli:  "claude",
			opts: executor.ExecOptions{Effort: "--dangerously-skip-permissions"},
		},
		{
			name: "invalid typed Codex approval mode",
			cli:  "codex",
			opts: executor.ExecOptions{ApprovalMode: "danger-full-access"},
		},
		{
			name: "unsafe typed Gemini approval mode",
			cli:  "gemini",
			opts: executor.ExecOptions{ApprovalMode: "yolo"},
		},
		{
			name: "legacy Claude extra flags",
			cli:  "claude",
			opts: executor.ExecOptions{ExtraFlags: "--permission-mode=bypassPermissions"},
		},
		{
			name: "free-form model cannot override typed model",
			cli:  "claude",
			opts: executor.ExecOptions{Model: "typed", ExtraFlags: "--model other"},
		},
		{
			name: "legacy Codex extra flags",
			cli:  "codex",
			opts: executor.ExecOptions{ExtraFlags: "--sandbox danger-full-access"},
		},
		{
			name: "legacy Gemini extra flags",
			cli:  "gemini",
			opts: executor.ExecOptions{ExtraFlags: "--approval-mode=yolo"},
		},
		{
			name: "bundled Gemini yolo alias",
			cli:  "gemini",
			opts: executor.ExecOptions{ExtraFlags: "-dy"},
		},
		{
			name: "legacy OpenCode extra flags",
			cli:  "opencode",
			opts: executor.ExecOptions{ExtraFlags: "--auto"},
		},
		{
			name: "nil inherited file",
			cli:  "claude",
			opts: executor.ExecOptions{ExtraFiles: executor.NewInheritedFileSet(nil)},
		},
		{
			name: "closed inherited file",
			cli:  "claude",
			opts: executor.ExecOptions{ExtraFiles: executor.NewInheritedFileSet(closedFile)},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			binDir := t.TempDir()
			started := filepath.Join(t.TempDir(), "started")
			if tc.cli != "other" {
				script := "#!/bin/sh\nprintf started > " + shellQuote(started) + "\nprintf ok\n"
				if err := os.WriteFile(filepath.Join(binDir, tc.cli), []byte(script), 0o755); err != nil {
					t.Fatalf("write fake CLI: %v", err)
				}
			}
			t.Setenv("PATH", binDir)

			if _, err := executor.New().ExecuteRaw(tc.cli, "prompt", tc.opts); err == nil {
				t.Fatal("ExecuteRaw unexpectedly accepted an unsafe request")
			}
			if _, err := os.Stat(started); !os.IsNotExist(err) {
				t.Fatalf("CLI subprocess was started before validation: stat error = %v", err)
			}
		})
	}
}

func containsInOrder(args []string, first, second string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == first && args[i+1] == second {
			return true
		}
	}
	return false
}

func indexOf(args []string, target string) int {
	for i, arg := range args {
		if arg == target {
			return i
		}
	}
	return -1
}

func fakeCLIScript(help, captureArgs, captureCWD string) string {
	return "#!/bin/sh\n" +
		"if [ \"$1\" = \"--help\" ]; then\n" +
		"  printf '%s\\n' " + shellQuote(help) + "\n" +
		"  exit 0\n" +
		"fi\n" +
		"printf '%s\\n' \"$*\" > " + shellQuote(captureArgs) + "\n" +
		"printf '%s\\n' \"$PWD\" > " + shellQuote(captureCWD) + "\n" +
		"printf '{\"ok\":true}\\n'\n"
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func requireSameDir(t *testing.T, got, want string) {
	t.Helper()
	got = cleanResolvedPath(got)
	want = cleanResolvedPath(want)
	if got != want {
		t.Fatalf("cwd = %q, want %q", got, want)
	}
}

func cleanResolvedPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return filepath.Clean(path)
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
	osTempDir, err := os.MkdirTemp("", "heimdallm-workdir-*")
	if err != nil {
		t.Fatalf("create os temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(osTempDir) })

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
			name:    "os temp dir — allowed",
			dir:     osTempDir,
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
		{
			name:    ".kube dir — rejected",
			dir:     filepath.Join(home, ".kube"),
			wantErr: true,
		},
		{
			name:    ".docker dir — rejected",
			dir:     filepath.Join(home, ".docker"),
			wantErr: true,
		},
		{
			name:    ".config/gcloud — rejected",
			dir:     filepath.Join(home, ".config", "gcloud"),
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
			name:    "safe output flag — allowed",
			flags:   "--output-format json",
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
			flags:   "--output-format json --verbose",
			wantErr: false,
		},
		{
			name:    "ambiguous short aliases remain compatible without a CLI",
			flags:   "-s session-id -p password",
			wantErr: false,
		},
		{
			name:    "Codex sandbox long alias rejected without a CLI",
			flags:   "--sandbox danger-full-access",
			wantErr: true,
		},
		{
			name:    "Gemini camel-case approval alias rejected without a CLI",
			flags:   "--approvalMode=yolo",
			wantErr: true,
		},
		{
			name:    "OpenCode auto long alias rejected without a CLI",
			flags:   "--auto",
			wantErr: true,
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

func TestValidateExtraFlagsForCLIRejectsPolicyAliases(t *testing.T) {
	tests := []struct {
		name  string
		cli   string
		flags string
	}{
		{name: "Claude dangerous skip", cli: "claude", flags: "--dangerously-skip-permissions"},
		{name: "Claude allow later bypass", cli: "claude", flags: "--allow-dangerously-skip-permissions"},
		{name: "Claude permission mode", cli: "claude", flags: "--permission-mode default"},
		{name: "Claude permission camel case", cli: "claude", flags: "--PeRmIsSiOn-MoDe=bypassPermissions"},
		{name: "Claude permission separator alias", cli: "claude", flags: "--permissionMode=acceptEdits"},
		{name: "Claude allowedTools spelling", cli: "claude", flags: "--allowedTools Bash"},
		{name: "Claude allowed-tools spelling", cli: "claude", flags: "--allowed-tools=Bash"},
		{name: "Claude permission prompt tool", cli: "claude", flags: "--permission-prompt-tool=mcp__approval"},
		{name: "Claude add dir", cli: "claude", flags: "--add-dir /etc"},
		{name: "Claude legacy directory", cli: "claude", flags: "--directory=/"},
		{name: "Claude sandbox negation", cli: "claude", flags: "--no-sandbox"},
		{name: "Claude settings", cli: "claude", flags: "--settings unsafe.json"},
		{name: "Claude setting sources", cli: "claude", flags: "--setting-sources=user"},
		{name: "Claude MCP config", cli: "claude", flags: "--mcp-config mcp.json"},
		{name: "Claude plugin config", cli: "claude", flags: "--plugin-dir ./plugin"},
		{name: "Claude dynamic agent", cli: "claude", flags: "--agents={}"},
		{name: "Claude system prompt replacement", cli: "claude", flags: "--system-prompt ignore-policy"},
		{name: "Claude system prompt file", cli: "claude", flags: "--system-prompt-file /etc/passwd"},
		{name: "Claude appended system prompt file", cli: "claude", flags: "--append-system-prompt-file=/etc/passwd"},
		{name: "Claude debug file", cli: "claude", flags: "--debug-file /tmp/claude.log"},
		{name: "Claude shell exec", cli: "claude", flags: "--exec 'touch /tmp/pwned'"},
		{name: "Claude background mode", cli: "claude", flags: "--bg"},
		{name: "Claude worktree", cli: "claude", flags: "--worktree unsafe"},
		{name: "Claude worktree short attached", cli: "claude", flags: "-wunsafe"},
		{name: "Claude cloud execution", cli: "claude", flags: "--cloud"},
		{name: "Claude remote control", cli: "claude", flags: "--remote-control"},
		{name: "Claude channels", cli: "claude", flags: "--channels plugin:channel"},
		{name: "Claude browser tooling", cli: "claude", flags: "--chrome"},
		{name: "Claude IDE integration", cli: "claude", flags: "--ide"},
		{name: "Claude setup hooks", cli: "claude", flags: "--init-only"},
		{name: "Claude resumed session", cli: "claude", flags: "--resume session-id"},
		{name: "Claude resume hidden in legacy debug bundle", cli: "claude", flags: "-dr00000000-0000-4000-8000-000000000000"},
		{name: "Claude PR session", cli: "claude", flags: "--from-pr 42"},
		{name: "Claude tool availability", cli: "claude", flags: "--tools Read"},
		{name: "Claude typed model long", cli: "claude", flags: "--model opus"},
		{name: "Claude typed model short attached", cli: "claude", flags: "-mopus"},
		{name: "Claude typed effort", cli: "claude", flags: "--effort high"},
		{name: "Claude typed max turns casing alias", cli: "claude", flags: "--maxTurns=5"},

		{name: "Codex approval", cli: "codex", flags: "--ask-for-approval never"},
		{name: "Codex approval camel case", cli: "codex", flags: "--askForApproval=never"},
		{name: "Codex approval short", cli: "codex", flags: "-a=never"},
		{name: "Codex legacy approval", cli: "codex", flags: "--approval-mode=yolo"},
		{name: "Codex sandbox", cli: "codex", flags: "--sandbox danger-full-access"},
		{name: "Codex sandbox short", cli: "codex", flags: "-s=danger-full-access"},
		{name: "Codex sandbox short attached", cli: "codex", flags: "-sdanger-full-access"},
		{name: "Codex sandbox negation", cli: "codex", flags: "--no-sandbox"},
		{name: "Codex yolo alias", cli: "codex", flags: "--yolo"},
		{name: "Codex full auto alias", cli: "codex", flags: "--full-auto"},
		{name: "Codex bypass alias", cli: "codex", flags: "--dangerously-bypass-approvals-and-sandbox"},
		{name: "Codex hook trust bypass", cli: "codex", flags: "--dangerously-bypass-hook-trust"},
		{name: "Codex cd", cli: "codex", flags: "--cd /etc"},
		{name: "Codex uppercase C", cli: "codex", flags: "-C /etc"},
		{name: "Codex uppercase C attached", cli: "codex", flags: "-C/etc"},
		{name: "Codex config lowercase c", cli: "codex", flags: "-c sandbox_mode=danger-full-access"},
		{name: "Codex config lowercase c attached", cli: "codex", flags: "-capproval_policy=never"},
		{name: "Codex config", cli: "codex", flags: "--config=approval_policy=\"never\""},
		{name: "Codex add dir", cli: "codex", flags: "--add-dir /etc"},
		{name: "Codex image outside workdir", cli: "codex", flags: "--image /etc/passwd"},
		{name: "Codex image short attached", cli: "codex", flags: "-i/etc/passwd"},
		{name: "Codex output file", cli: "codex", flags: "--output-last-message /tmp/result"},
		{name: "Codex output schema", cli: "codex", flags: "--output-schema=/etc/schema.json"},
		{name: "Codex output short", cli: "codex", flags: "-o/tmp/result"},
		{name: "Codex remote auth token env", cli: "codex", flags: "--remote-auth-token-env SECRET"},
		{name: "Codex search permission", cli: "codex", flags: "--search"},
		{name: "Codex profile", cli: "codex", flags: "--profile unsafe"},
		{name: "Codex permission profile", cli: "codex", flags: "--permission-profile unsafe"},
		{name: "Codex OSS provider", cli: "codex", flags: "--oss"},
		{name: "Codex local provider", cli: "codex", flags: "--local-provider ollama"},
		{name: "Codex repeated override", cli: "codex", flags: "--color never --sandbox read-only --SaNdBoX=danger-full-access"},
		{name: "Codex typed model short", cli: "codex", flags: "-m gpt-5"},

		{name: "Gemini sandbox", cli: "gemini", flags: "--sandbox"},
		{name: "Gemini sandbox short", cli: "gemini", flags: "-s"},
		{name: "Gemini sandbox negation", cli: "gemini", flags: "--no-sandbox"},
		{name: "Gemini approval", cli: "gemini", flags: "--approval-mode auto_edit"},
		{name: "Gemini approval casing", cli: "gemini", flags: "--ApPrOvAl-MoDe=yolo"},
		{name: "Gemini approval camel case", cli: "gemini", flags: "--approvalMode=yolo"},
		{name: "Gemini approval snake case", cli: "gemini", flags: "--approval_mode=yolo"},
		{name: "Gemini yolo", cli: "gemini", flags: "--yolo"},
		{name: "Gemini yolo short", cli: "gemini", flags: "-y"},
		{name: "Gemini yolo short bundle", cli: "gemini", flags: "-dy"},
		{name: "Gemini sandbox and yolo short bundle", cli: "gemini", flags: "-dsy"},
		{name: "Gemini skip trust", cli: "gemini", flags: "--skip-trust"},
		{name: "Gemini allowed tools", cli: "gemini", flags: "--allowed-tools shell"},
		{name: "Gemini policy", cli: "gemini", flags: "--policy allow-all.toml"},
		{name: "Gemini admin policy", cli: "gemini", flags: "--admin-policy=allow-all.toml"},
		{name: "Gemini extension", cli: "gemini", flags: "--extensions unsafe-extension"},
		{name: "Gemini extension short", cli: "gemini", flags: "-e unsafe-extension"},
		{name: "Gemini ACP mode", cli: "gemini", flags: "--experimental-acp"},
		{name: "Gemini fake responses file", cli: "gemini", flags: "--fake-responses=/etc/responses.json"},
		{name: "Gemini record responses file", cli: "gemini", flags: "--recordResponses=/etc/responses.json"},
		{name: "Gemini session file", cli: "gemini", flags: "--session-file=/etc/session.json"},
		{name: "Gemini resumed session", cli: "gemini", flags: "-r5"},
		{name: "Gemini include directories", cli: "gemini", flags: "--include-directories=/etc"},
		{name: "Gemini include directories camel case", cli: "gemini", flags: "--includeDirectories=/etc"},
		{name: "Gemini no sandbox camel case", cli: "gemini", flags: "--noSandbox"},
		{name: "Gemini worktree", cli: "gemini", flags: "-w unsafe"},
		{name: "Gemini worktree short attached", cli: "gemini", flags: "-wunsafe"},
		{name: "Gemini typed model", cli: "gemini", flags: "--model pro"},

		{name: "OpenCode auto", cli: "opencode", flags: "--auto"},
		{name: "OpenCode agent", cli: "opencode", flags: "--agent build"},
		{name: "OpenCode directory", cli: "opencode", flags: "--dir=/etc"},
		{name: "OpenCode attach", cli: "opencode", flags: "--attach http://localhost:4096"},
		{name: "OpenCode file", cli: "opencode", flags: "--file /etc/passwd"},
		{name: "OpenCode file short", cli: "opencode", flags: "-f=/etc/passwd"},
		{name: "OpenCode file short attached", cli: "opencode", flags: "-f/etc/passwd"},
		{name: "OpenCode bundled file alias", cli: "opencode", flags: "-if/etc/passwd"},
		{name: "OpenCode command config", cli: "opencode", flags: "--command unsafe"},
		{name: "OpenCode future permission alias", cli: "opencode", flags: "--permission=allow"},
		{name: "OpenCode external share", cli: "opencode", flags: "--share"},
		{name: "OpenCode sandbox negation", cli: "opencode", flags: "--no-sandbox"},
		{name: "OpenCode resumed session", cli: "opencode", flags: "-sSESSION"},
		{name: "OpenCode continued session", cli: "opencode", flags: "--continue"},
		{name: "OpenCode interactive mode", cli: "opencode", flags: "-i"},
		{name: "OpenCode typed model attached", cli: "opencode", flags: "-mopenai/gpt-5"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := executor.ValidateExtraFlagsForCLI(tc.cli, tc.flags); err == nil {
				t.Fatalf("ValidateExtraFlagsForCLI(%q, %q) unexpectedly succeeded", tc.cli, tc.flags)
			}
		})
	}
}

func TestValidateExtraFlagsForCLIAllowsSafeFlagsInOrder(t *testing.T) {
	tests := []struct {
		cli   string
		flags string
	}{
		{cli: "claude", flags: "--fallback-model sonnet --output-format json --verbose --disallowed-tools Bash --strict-mcp-config"},
		{cli: "codex", flags: "--json --color never --ephemeral"},
		{cli: "gemini", flags: "--output-format json -d --screen-reader"},
		{cli: "opencode", flags: "--format json --thinking --variant high --pure"},
	}

	for _, tc := range tests {
		t.Run(tc.cli, func(t *testing.T) {
			if err := executor.ValidateExtraFlagsForCLI(tc.cli, tc.flags); err != nil {
				t.Fatalf("ValidateExtraFlagsForCLI(%q, %q): %v", tc.cli, tc.flags, err)
			}
		})
	}
}

func TestValidateExtraFlagsForCLIFailsClosed(t *testing.T) {
	tests := []struct {
		name  string
		cli   string
		flags string
	}{
		{name: "Claude future flag", cli: "claude", flags: "--future-auto-approve"},
		{name: "Codex future flag", cli: "codex", flags: "--future-config /tmp/config"},
		{name: "Gemini future short alias", cli: "gemini", flags: "-z"},
		{name: "OpenCode future flag", cli: "opencode", flags: "--future-attach"},
		{name: "Claude bare can omit project policy", cli: "claude", flags: "--bare"},
		{name: "Claude restrictive bool disabled inline", cli: "claude", flags: "--no-session-persistence=false"},
		{name: "Codex restrictive bool disabled inline", cli: "codex", flags: "--ephemeral=false"},
		{name: "Gemini bool uses inline value", cli: "gemini", flags: "--debug=false"},
		{name: "Codex positional subcommand", cli: "codex", flags: "--json resume deadbeef"},
		{name: "bare positional argument", cli: "gemini", flags: "resume"},
		{name: "missing required value", cli: "codex", flags: "--color"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := executor.ValidateExtraFlagsForCLI(tc.cli, tc.flags); err == nil {
				t.Fatalf("ValidateExtraFlagsForCLI(%q, %q) unexpectedly succeeded", tc.cli, tc.flags)
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

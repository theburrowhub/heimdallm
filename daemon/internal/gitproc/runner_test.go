package gitproc

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestRunnerBuildsEnvironmentFromScratch(t *testing.T) {
	script := writeExecutable(t, `#!/bin/sh
/usr/bin/env
`)
	t.Setenv("GIT_DIR", "/attacker/git")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.hooksPath")
	t.Setenv("GIT_CONFIG_VALUE_0", "/attacker/hooks")
	t.Setenv("HEIMDALLM_GIT_TOKEN", "parent-token")
	t.Setenv("LD_PRELOAD", "/attacker/library.so")
	t.Setenv("PATH", ".:/attacker/bin")
	t.Setenv("HTTPS_PROXY", "https://proxy.example.invalid")
	t.Setenv("SSL_CERT_FILE", "/operator/corporate-ca.pem")

	out, err := NewWithPath(script).captureTrusted(context.Background(), "", false, "version")
	if err != nil {
		t.Fatalf("captureTrusted: %v", err)
	}
	env := parseEnvironment(string(out))
	for _, key := range []string{
		"GIT_DIR",
		"GIT_CONFIG_COUNT",
		"GIT_CONFIG_KEY_0",
		"GIT_CONFIG_VALUE_0",
		"HEIMDALLM_GIT_TOKEN",
		"LD_PRELOAD",
	} {
		if value, found := env[key]; found {
			t.Fatalf("inherited %s=%q", key, value)
		}
	}
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "TMPDIR"} {
		if !filepath.IsAbs(env[key]) {
			t.Fatalf("%s = %q, want an absolute sandbox path", key, env[key])
		}
	}
	for _, entry := range filepath.SplitList(env["PATH"]) {
		if !filepath.IsAbs(entry) {
			t.Fatalf("PATH contains non-absolute entry %q: %q", entry, env["PATH"])
		}
	}
	if env["GIT_CONFIG_GLOBAL"] != os.DevNull ||
		env["GIT_CONFIG_SYSTEM"] != os.DevNull ||
		env["GIT_CONFIG_NOSYSTEM"] != "1" {
		t.Fatalf("global/system config was not disabled: %#v", env)
	}
	if env["HTTPS_PROXY"] != "https://proxy.example.invalid" ||
		env["SSL_CERT_FILE"] != "/operator/corporate-ca.pem" {
		t.Fatalf("network/CA allowlist was not forwarded: %#v", env)
	}
}

func TestSandboxDirectoriesAreOwnerOnly(t *testing.T) {
	box, err := newSandbox()
	if err != nil {
		t.Fatal(err)
	}
	defer box.cleanup()
	for _, path := range []string{box.root, box.home, box.xdg, box.tmp, box.hooks} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Fatalf("%s mode = %o, want 700", path, got)
		}
	}
}

func TestRunnerForcesNonExecutableRepositoryPolicy(t *testing.T) {
	argsCapture := filepath.Join(t.TempDir(), "args")
	script := writeExecutable(t, "#!/bin/sh\nprintf '%s\\n' \"$@\" > "+strconv.Quote(argsCapture)+"\n")
	dir := repositoryWithConfig(t, "")

	if err := NewWithPath(script).Run(context.Background(), dir, "status"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	args := mustRead(t, argsCapture)
	for _, policy := range []string{
		"core.fsmonitor=false",
		"core.attributesFile=" + os.DevNull,
		"core.excludesFile=" + os.DevNull,
		"credential.helper=",
		"log.showSignature=false",
		"protocol.ssh.allow=never",
		"protocol.ext.allow=never",
	} {
		if !strings.Contains(args, policy+"\n") {
			t.Errorf("Git args do not force %q:\n%s", policy, args)
		}
	}
	if !strings.Contains(args, "core.hooksPath=") {
		t.Errorf("Git args do not force an isolated hooks path:\n%s", args)
	}
	canonicalDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(args, "safe.directory="+canonicalDir+"\n") {
		t.Errorf("Git args do not trust only the audited repository path:\n%s", args)
	}
}

func TestRunnerAllowsOnlyAuditedRepositoryWithDifferentOwner(t *testing.T) {
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git binary not available")
	}
	realGit, err = filepath.Abs(realGit)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	initCmd := exec.Command(realGit, "init", "--", dir)
	initCmd.Env = []string{
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_SYSTEM=" + os.DevNull,
		"HOME=" + t.TempDir(),
		"PATH=" + os.Getenv("PATH"),
	}
	if output, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}

	quotedGit := "'" + strings.ReplaceAll(realGit, "'", "'\"'\"'") + "'"
	wrapper := writeExecutable(t, "#!/bin/sh\nGIT_TEST_ASSUME_DIFFERENT_OWNER=1 exec "+quotedGit+" \"$@\"\n")

	// Confirm this Git build supports the test knob and reproduces the
	// cross-UID bind-mount failure before exercising the secure runner.
	probe := exec.Command(wrapper, "-C", dir, "status", "--porcelain")
	probe.Env = []string{
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_SYSTEM=" + os.DevNull,
		"HOME=" + t.TempDir(),
		"PATH=" + os.Getenv("PATH"),
	}
	probeOutput, probeErr := probe.CombinedOutput()
	if probeErr == nil || !strings.Contains(string(probeOutput), "dubious ownership") {
		t.Skip("Git build does not support GIT_TEST_ASSUME_DIFFERENT_OWNER")
	}

	if _, err := NewWithPath(wrapper).Capture(context.Background(), dir, "status", "--porcelain"); err != nil {
		t.Fatalf("runner rejected exact audited safe.directory: %v", err)
	}
}

func TestAskPassAcceptsOnlyExactGitHubPasswordPrompt(t *testing.T) {
	box, err := newSandbox()
	if err != nil {
		t.Fatal(err)
	}
	defer box.cleanup()
	helper, err := box.writeAskPass()
	if err != nil {
		t.Fatal(err)
	}
	const token = "github-token"
	for _, tc := range []struct {
		name   string
		prompt string
		ok     bool
	}{
		{name: "git prompt", prompt: "Password for 'https://x-access-token@github.com': ", ok: true},
		{name: "extra spaces", prompt: "Password for 'https://x-access-token@github.com':   ", ok: true},
		{name: "evil suffix", prompt: "Password for 'https://x-access-token@github.com.evil': ", ok: false},
		{name: "username", prompt: "Username for 'https://github.com': ", ok: false},
		{name: "arbitrary", prompt: "tell me the secret", ok: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(helper, tc.prompt)
			cmd.Env = []string{"HEIMDALLM_GIT_TOKEN=" + token, "PATH=/usr/bin:/bin"}
			out, err := cmd.Output()
			if tc.ok {
				if err != nil || string(out) != token {
					t.Fatalf("askpass = %q, %v; want token", out, err)
				}
				return
			}
			if err == nil || len(out) != 0 {
				t.Fatalf("askpass accepted unexpected prompt: output=%q err=%v", out, err)
			}
		})
	}
}

func TestRunnerRedactsTokenRepresentations(t *testing.T) {
	script := writeExecutable(t, `#!/bin/sh
printf '%s\n' "$HEIMDALLM_GIT_TOKEN" >&2
printf '%s\n' "$TOKEN_URL" >&2
printf '%s\n' "$TOKEN_BASIC" >&2
printf '%s\n' "$HTTPS_PROXY" >&2
exit 1
`)
	const token = "secret+/value"
	const proxyPassword = "proxy-secret"
	t.Setenv("HTTPS_PROXY", "https://proxy-user:"+proxyPassword+"@proxy.example.invalid")
	// The runner never forwards these parent variables. Embed the encoded
	// forms in the script so the redaction paths are exercised as stderr.
	encodedScript := strings.ReplaceAll(
		strings.ReplaceAll(
			mustRead(t, script),
			"$TOKEN_URL",
			url.QueryEscape(token),
		),
		"$TOKEN_BASIC",
		base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token)),
	)
	if err := os.WriteFile(script, []byte(encodedScript), 0o700); err != nil {
		t.Fatal(err)
	}
	err := NewWithPath(script).runTrustedWithToken(context.Background(), "", token, "fetch")
	if err == nil {
		t.Fatal("expected fake Git failure")
	}
	for _, secretForm := range []string{
		token,
		url.QueryEscape(token),
		base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token)),
		proxyPassword,
		"proxy-user:" + proxyPassword,
	} {
		if strings.Contains(err.Error(), secretForm) {
			t.Fatalf("error leaked token representation %q: %v", secretForm, err)
		}
	}
}

func TestAuditRepositoryRejectsExecutableConfig(t *testing.T) {
	dir := repositoryWithConfig(t, "[filter \"attacker\"]\n\tclean = /tmp/attacker-filter\n")
	parser := writeExecutable(t, "#!/bin/sh\nprintf 'filter.attacker.clean\\0'\n")

	_, err := NewWithPath(parser).AuditRepository(context.Background(), dir)
	if err == nil || !strings.Contains(err.Error(), "filter.attacker.clean") {
		t.Fatalf("AuditRepository error = %v, want unsafe filter command", err)
	}
}

func TestAuditRepositoryAllowsConfigNeutralizedByEveryCommand(t *testing.T) {
	dir := repositoryWithConfig(t, "[core]\n\thooksPath = /tmp/hooks\n\tfsmonitor = /tmp/fsmonitor\n\tfsmonitorHookVersion = 1\n")
	parser := writeExecutable(t, "#!/bin/sh\nprintf 'core.hookspath\\0core.fsmonitor\\0core.fsmonitorhookversion\\0'\n")

	got, err := NewWithPath(parser).AuditRepository(context.Background(), dir)
	if err != nil {
		t.Fatalf("AuditRepository rejected neutralized config: %v", err)
	}
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("AuditRepository path = %q, want %q", got, want)
	}
}

func TestAuditRepositoryRejectsIncludesWithoutFollowingThem(t *testing.T) {
	dir := repositoryWithConfig(t, "[include]\n\tpath = /tmp/attacker.conf\n")
	included := filepath.Join(t.TempDir(), "attacker.conf")
	if err := os.WriteFile(included, []byte("[core]\n\thooksPath = /tmp/attacker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	parser := writeExecutable(t, "#!/bin/sh\nprintf 'include.path\\0'\n")

	_, err := NewWithPath(parser).AuditRepository(context.Background(), dir)
	if err == nil || !strings.Contains(err.Error(), "include.path") {
		t.Fatalf("AuditRepository error = %v, want rejected include.path", err)
	}
}

func TestAuditRepositoryRejectsObjectAlternates(t *testing.T) {
	dir := repositoryWithConfig(t, "")
	alternates := filepath.Join(dir, ".git", "objects", "info", "alternates")
	if err := os.WriteFile(alternates, []byte("/tmp/attacker/objects\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	parser := writeExecutable(t, "#!/bin/sh\nexit 0\n")

	_, err := NewWithPath(parser).AuditRepository(context.Background(), dir)
	if err == nil || !strings.Contains(err.Error(), "alternates") {
		t.Fatalf("AuditRepository error = %v, want rejected object alternates", err)
	}
}

func TestAuditRepositoryAcceptsRealLinkedWorktreeLayout(t *testing.T) {
	for _, relative := range []bool{false, true} {
		name := "absolute"
		if relative {
			name = "relative"
		}
		t.Run(name, func(t *testing.T) {
			worktree := linkedRepository(t, relative)
			parser := writeExecutable(t, "#!/bin/sh\nexit 0\n")

			got, err := NewWithPath(parser).AuditRepository(context.Background(), worktree)
			if err != nil {
				t.Fatalf("AuditRepository: %v", err)
			}
			want, err := filepath.EvalSymlinks(worktree)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("AuditRepository path = %q, want %q", got, want)
			}
		})
	}
}

func TestAuditRepositoryRejectsGitFilePointingAtMainGitDir(t *testing.T) {
	victim := repositoryWithConfig(t, "")
	spoof := t.TempDir()
	pointer := "gitdir: " + filepath.Join(victim, ".git") + "\n"
	if err := os.WriteFile(filepath.Join(spoof, ".git"), []byte(pointer), 0o600); err != nil {
		t.Fatal(err)
	}
	parser := writeExecutable(t, "#!/bin/sh\nexit 0\n")

	_, err := NewWithPath(parser).AuditRepository(context.Background(), spoof)
	if err == nil || !strings.Contains(err.Error(), "commondir") {
		t.Fatalf("AuditRepository error = %v, want spoofed main gitdir rejection", err)
	}
}

func TestAuditRepositoryRejectsArbitraryGitDirAndWrongBacklink(t *testing.T) {
	for _, tc := range []struct {
		name      string
		arbitrary bool
	}{
		{name: "outside worktrees metadata", arbitrary: true},
		{name: "wrong backlink"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			common := filepath.Join(root, "main", ".git")
			private := filepath.Join(common, "worktrees", "linked")
			if tc.arbitrary {
				private = filepath.Join(root, "arbitrary-gitdir")
			}
			if err := os.MkdirAll(private, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(common, "worktrees"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(common, "config"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			relativeCommon, err := filepath.Rel(private, common)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(private, "commondir"), []byte(relativeCommon+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			worktree := filepath.Join(root, "linked")
			if err := os.MkdirAll(worktree, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(
				filepath.Join(worktree, ".git"),
				[]byte("gitdir: "+private+"\n"),
				0o600,
			); err != nil {
				t.Fatal(err)
			}
			wrong := filepath.Join(root, "other", ".git")
			if err := os.MkdirAll(filepath.Dir(wrong), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(wrong, []byte("not the backlink target\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(private, "gitdir"), []byte(wrong+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			parser := writeExecutable(t, "#!/bin/sh\nexit 0\n")

			_, err = NewWithPath(parser).AuditRepository(context.Background(), worktree)
			if err == nil {
				t.Fatal("AuditRepository accepted spoofed linked-worktree metadata")
			}
			if tc.arbitrary && !strings.Contains(err.Error(), "direct child") {
				t.Fatalf("AuditRepository error = %v, want arbitrary gitdir rejection", err)
			}
			if !tc.arbitrary && !strings.Contains(err.Error(), "does not point") {
				t.Fatalf("AuditRepository error = %v, want backlink rejection", err)
			}
		})
	}
}

func TestValidateBranch(t *testing.T) {
	for _, branch := range []string{"main", "heimdallm/issue-608", "release/v1.2.3"} {
		if err := ValidateBranch(branch); err != nil {
			t.Errorf("ValidateBranch(%q): %v", branch, err)
		}
	}
	for _, branch := range []string{
		"",
		"-c",
		"../main",
		"main..evil",
		"main@{1}",
		"main lock",
		"main:evil",
		".hidden",
		"main/.hidden",
		"main.lock",
		"main//evil",
		"main\nother",
	} {
		if err := ValidateBranch(branch); err == nil {
			t.Errorf("ValidateBranch(%q) unexpectedly succeeded", branch)
		}
	}
}

func TestGitHubRemoteIsCanonicalAndCredentialFree(t *testing.T) {
	remote, err := GitHubRemote("theburrowhub/heimdallm")
	if err != nil {
		t.Fatal(err)
	}
	if remote != "https://x-access-token@github.com/theburrowhub/heimdallm.git" {
		t.Fatalf("remote = %q", remote)
	}
	if err := validateRemote(remote); err != nil {
		t.Fatalf("validateRemote: %v", err)
	}
	for _, repo := range []string{
		"",
		"owner",
		"owner/repo/extra",
		"owner/repo?token=secret",
		"owner/repo%2Fevil",
		"-owner/repo",
	} {
		if _, err := GitHubRemote(repo); err == nil {
			t.Errorf("GitHubRemote(%q) unexpectedly succeeded", repo)
		}
	}
	for _, candidate := range []string{
		"https://x-access-token:secret@github.com/owner/repo.git",
		"https://x-access-token@github.com.evil/owner/repo.git",
		"http://x-access-token@github.com/owner/repo.git",
		"https://x-access-token@github.com/owner/repo.git?x=y",
	} {
		if err := validateRemote(candidate); err == nil {
			t.Errorf("validateRemote(%q) unexpectedly succeeded", candidate)
		}
	}
}

func TestAuditedRunnerRejectsGlobalOverridesAndAliases(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "hooks config", args: []string{"-c", "core.hooksPath=/attacker", "status"}},
		{name: "compact config", args: []string{"-ccore.fsmonitor=/attacker", "status"}},
		{name: "config env", args: []string{"--config-env=core.hooksPath=EVIL", "status"}},
		{name: "git dir", args: []string{"--git-dir=/attacker", "status"}},
		{name: "working directory", args: []string{"-C", "/attacker", "status"}},
		{name: "alias subcommand", args: []string{"attacker-alias"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := repositoryWithConfig(t, "")
			marker := filepath.Join(t.TempDir(), "git-ran")
			fake := writeExecutable(t, "#!/bin/sh\n: > "+strconv.Quote(marker)+"\n")
			err := NewWithPath(fake).Run(context.Background(), dir, tc.args...)
			if err == nil || !strings.Contains(err.Error(), "not allowed") {
				t.Fatalf("Run(%q) error = %v, want fail-closed rejection", tc.args, err)
			}
			if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
				t.Fatalf("Git executed before rejecting %q: %v", tc.args, statErr)
			}
		})
	}
}

func TestAuthenticatedOperationsNeverRunInCheckout(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "calls.log")
	fake := writeExecutable(t, fmt.Sprintf(`#!/bin/sh
auth=no
if [ -n "$HEIMDALLM_GIT_TOKEN" ]; then auth=yes; fi
{
  printf 'AUTH=%%s	PWD=%%s	ARGS=' "$auth" "$PWD"
  printf '%%s ' "$@"
  printf '\n'
} >> %s
case " $* " in
  *" clone "*)
    for last do :; done
    mkdir -p "$last/.git"
    : > "$last/.git/config"
    ;;
  *" rev-parse "*)
    printf '0123456789012345678901234567890123456789\n'
    ;;
esac
exit 0
`, strconv.Quote(logPath)))
	runner := NewWithPath(fake)
	target := filepath.Join(root, "checkout")
	remote, err := GitHubRemote("owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.CloneNoCheckout(context.Background(), remote, target, "secret-token", 1); err != nil {
		t.Fatalf("CloneNoCheckout: %v", err)
	}
	if err := runner.Fetch(
		context.Background(),
		target,
		remote,
		"main",
		"secret-token",
		FetchOptions{Depth: 1},
	); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if _, err := runner.PushBranch(context.Background(), target, remote, "main", "secret-token"); err != nil {
		t.Fatalf("PushBranch: %v", err)
	}
	if err := runner.DeleteBranch(
		context.Background(),
		remote,
		"main",
		"0123456789012345678901234567890123456789",
		"secret-token",
	); err != nil {
		t.Fatalf("DeleteBranch: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(mustRead(t, logPath)), "\n")
	var authenticated int
	var tokenlessCheckout bool
	for _, line := range lines {
		if strings.Contains(line, "secret-token") {
			t.Fatalf("token appeared in argv/logged command: %s", line)
		}
		if strings.HasPrefix(line, "AUTH=yes") {
			authenticated++
			if strings.Contains(line, "PWD="+target+"\t") ||
				strings.Contains(line, "file://"+target) {
				t.Fatalf("authenticated Git opened or referenced checkout: %s", line)
			}
			if strings.Contains(line, " push ") {
				if !strings.Contains(line, " --no-verify ") {
					t.Fatalf("authenticated push did not disable pre-push hooks: %s", line)
				}
				if !strings.Contains(line, " core.hooksPath=") {
					t.Fatalf("authenticated push did not force an isolated hooks path: %s", line)
				}
			}
		}
		if strings.HasPrefix(line, "AUTH=no\tPWD="+target+"\t") &&
			strings.Contains(line, " checkout ") {
			tokenlessCheckout = true
		}
	}
	if authenticated != 4 {
		t.Fatalf("authenticated command count = %d, want clone/fetch/push/delete; calls:\n%s",
			authenticated, strings.Join(lines, "\n"))
	}
	if !tokenlessCheckout {
		t.Fatalf("clone did not separate authenticated clone from tokenless checkout; calls:\n%s",
			strings.Join(lines, "\n"))
	}
}

func TestCloneNoCheckoutRemovesTargetWhenAuditFails(t *testing.T) {
	fake := writeExecutable(t, `#!/bin/sh
case " $* " in
  *" clone "*)
    for last do :; done
    mkdir -p "$last/.git"
    printf '[filter "attacker"]\n\tclean = /tmp/attacker\n' > "$last/.git/config"
    ;;
  *" config "*)
    printf 'filter.attacker.clean\0'
    ;;
esac
exit 0
`)
	target := filepath.Join(t.TempDir(), "partial-clone")
	remote, err := GitHubRemote("owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	err = NewWithPath(fake).CloneNoCheckout(
		context.Background(),
		remote,
		target,
		"secret-token",
		1,
	)
	if err == nil || !strings.Contains(err.Error(), "filter.attacker.clean") {
		t.Fatalf("CloneNoCheckout error = %v, want audit failure", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("partial clone target still exists: %v", statErr)
	}
}

func parseEnvironment(output string) map[string]string {
	env := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			env[key] = value
		}
	}
	return env
}

func writeExecutable(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func repositoryWithConfig(t *testing.T, config string) string {
	t.Helper()
	dir := t.TempDir()
	info := filepath.Join(dir, ".git", "objects", "info")
	if err := os.MkdirAll(info, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func linkedRepository(t *testing.T, relative bool) string {
	t.Helper()
	root := t.TempDir()
	common := filepath.Join(root, "main", ".git")
	private := filepath.Join(common, "worktrees", "linked")
	worktree := filepath.Join(root, "linked")
	if err := os.MkdirAll(filepath.Join(private, "objects", "info"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(common, "config"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(private, "commondir"), []byte("../..\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	dotGit := filepath.Join(worktree, ".git")
	pointer := private
	backlink := dotGit
	if relative {
		var err error
		pointer, err = filepath.Rel(worktree, private)
		if err != nil {
			t.Fatal(err)
		}
		backlink, err = filepath.Rel(private, dotGit)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(dotGit, []byte("gitdir: "+pointer+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(private, "gitdir"), []byte(backlink+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return worktree
}

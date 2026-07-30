package gitproc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maxGitMetadataFile = 1024 * 1024

// AuditRepository resolves an existing worktree without asking Git to inspect
// it, then rejects local configuration capable of starting another process or
// changing where Git obtains objects and configuration. The returned path is
// canonical so a repository symlink cannot be swapped after the audit.
func (r *Runner) AuditRepository(ctx context.Context, dir string) (string, error) {
	worktree, err := canonicalDirectory(dir, "worktree")
	if err != nil {
		return "", fmt.Errorf("gitproc: audit repository: %w", err)
	}

	gitDir, linked, err := resolveGitDir(worktree)
	if err != nil {
		return "", fmt.Errorf("gitproc: audit repository: %w", err)
	}
	commonDir := gitDir
	if linked {
		commonDir, err = resolveLinkedCommonDir(gitDir)
		if err != nil {
			return "", fmt.Errorf("gitproc: audit repository: %w", err)
		}
		if err := validateLinkedWorktree(worktree, gitDir, commonDir); err != nil {
			return "", fmt.Errorf("gitproc: audit repository: %w", err)
		}
	} else if _, err := os.Lstat(filepath.Join(gitDir, "commondir")); err == nil {
		return "", errors.New("gitproc: audit repository: main git directory must not contain commondir")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("gitproc: audit repository: inspect commondir: %w", err)
	}

	configs := []string{filepath.Join(commonDir, "config")}
	worktreeConfig := filepath.Join(gitDir, "config.worktree")
	if worktreeConfig != configs[0] {
		configs = append(configs, worktreeConfig)
	}
	for _, path := range configs {
		if err := r.auditConfigFile(ctx, path); err != nil {
			return "", fmt.Errorf("gitproc: audit repository: %w", err)
		}
	}

	for _, path := range []string{
		filepath.Join(commonDir, "objects", "info", "alternates"),
		filepath.Join(gitDir, "objects", "info", "alternates"),
	} {
		if err := rejectExistingMetadata(path, "object alternates"); err != nil {
			return "", fmt.Errorf("gitproc: audit repository: %w", err)
		}
	}

	return worktree, nil
}

func canonicalDirectory(path, name string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("%s path is empty", name)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s path: %w", name, err)
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%s must not be a symlink", name)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", name)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("canonicalize %s: %w", name, err)
	}
	return resolved, nil
}

func resolveGitDir(worktree string) (string, bool, error) {
	dotGit := filepath.Join(worktree, ".git")
	info, err := os.Lstat(dotGit)
	if err != nil {
		return "", false, fmt.Errorf("inspect .git: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", false, errors.New(".git must not be a symlink")
	}
	if info.IsDir() {
		path, err := canonicalDirectory(dotGit, "git directory")
		return path, false, err
	}
	if !info.Mode().IsRegular() {
		return "", false, errors.New(".git is neither a directory nor a worktree pointer")
	}

	data, err := readSmallRegularFile(dotGit)
	if err != nil {
		return "", false, fmt.Errorf("read .git worktree pointer: %w", err)
	}
	line := strings.TrimSpace(string(data))
	if strings.ContainsAny(line, "\r\n\x00") || !strings.HasPrefix(line, "gitdir: ") {
		return "", false, errors.New("invalid .git worktree pointer")
	}
	target := strings.TrimSpace(strings.TrimPrefix(line, "gitdir: "))
	if target == "" {
		return "", false, errors.New("empty .git worktree pointer")
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(worktree, target)
	}
	path, err := canonicalDirectory(target, "git directory")
	return path, true, err
}

func resolveLinkedCommonDir(gitDir string) (string, error) {
	path := filepath.Join(gitDir, "commondir")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", errors.New("linked worktree git directory has no commondir")
	}
	if err != nil {
		return "", fmt.Errorf("inspect commondir: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("commondir must be a regular file")
	}
	data, err := readSmallRegularFile(path)
	if err != nil {
		return "", fmt.Errorf("read commondir: %w", err)
	}
	target := strings.TrimSpace(string(data))
	if target == "" || strings.ContainsAny(target, "\r\n\x00") {
		return "", errors.New("invalid commondir")
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(gitDir, target)
	}
	return canonicalDirectory(target, "common git directory")
}

func validateLinkedWorktree(worktree, gitDir, commonDir string) error {
	if gitDir == commonDir {
		return errors.New("linked worktree git directory equals its common directory")
	}
	worktreesDir, err := canonicalDirectory(filepath.Join(commonDir, "worktrees"), "worktrees metadata directory")
	if err != nil {
		return err
	}
	if filepath.Dir(gitDir) != worktreesDir || filepath.Base(gitDir) == "." {
		return fmt.Errorf(
			"linked worktree git directory %q is not a direct child of %q",
			gitDir,
			worktreesDir,
		)
	}

	backlinkPath := filepath.Join(gitDir, "gitdir")
	data, err := readSmallRegularFile(backlinkPath)
	if err != nil {
		return fmt.Errorf("read linked worktree backlink: %w", err)
	}
	backlink := strings.TrimSpace(string(data))
	if backlink == "" || strings.ContainsAny(backlink, "\r\n\x00") {
		return errors.New("invalid linked worktree backlink")
	}
	if !filepath.IsAbs(backlink) {
		backlink = filepath.Join(gitDir, backlink)
	}
	backlink, err = filepath.EvalSymlinks(filepath.Clean(backlink))
	if err != nil {
		return fmt.Errorf("canonicalize linked worktree backlink: %w", err)
	}
	expected := filepath.Join(worktree, ".git")
	expected, err = filepath.EvalSymlinks(expected)
	if err != nil {
		return fmt.Errorf("canonicalize worktree .git file: %w", err)
	}
	if backlink != expected {
		return fmt.Errorf(
			"linked worktree backlink %q does not point to %q",
			backlink,
			expected,
		)
	}
	return nil
}

func (r *Runner) auditConfigFile(ctx context.Context, path string) error {
	data, err := readSmallRegularFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read Git config %q: %w", path, err)
	}
	safeDir, err := os.MkdirTemp("", "heimdallm-git-config-*")
	if err != nil {
		return fmt.Errorf("copy Git config %q: %w", path, err)
	}
	defer os.RemoveAll(safeDir)
	if err := os.Chmod(safeDir, 0o700); err != nil {
		return fmt.Errorf("secure Git config copy directory: %w", err)
	}
	safePath := filepath.Join(safeDir, "config")
	if err := os.WriteFile(safePath, data, 0o600); err != nil {
		return fmt.Errorf("copy Git config %q: %w", path, err)
	}

	names, err := r.captureTrusted(
		ctx,
		"",
		false,
		"config",
		"--file", safePath,
		"--null",
		"--name-only",
		"--list",
		"--no-includes",
	)
	if err != nil {
		return fmt.Errorf("parse Git config %q: %w", path, err)
	}
	for _, raw := range strings.Split(string(names), "\x00") {
		key := strings.ToLower(strings.TrimSpace(raw))
		if key == "" {
			continue
		}
		if unsafeConfigKey(key) {
			return fmt.Errorf("unsafe local Git config key %q in %q", key, path)
		}
	}
	return nil
}

func unsafeConfigKey(key string) bool {
	switch key {
	case "core.hookspath", "core.fsmonitor", "core.fsmonitorhookversion":
		// These are common in legitimate developer checkouts and are
		// unconditionally neutralized by higher-precedence -c options on
		// every invocation. Rejecting them would make local_dir unusable
		// without adding any protection beyond the enforced overrides.
		return false
	case "core.sshcommand",
		"core.gitproxy",
		"core.askpass",
		"core.editor",
		"core.pager",
		"core.attributesfile",
		"core.worktree",
		"core.alternaterefscommand",
		"interactive.difffilter",
		"sequence.editor",
		"init.templatedir",
		"user.signingkey",
		"commit.gpgsign",
		"push.gpgsign",
		"tag.gpgsign",
		"uploadpack.packobjectshook",
		"receive.procreceiverefs",
		"extensions.partialclone":
		return true
	}

	for _, prefix := range []string{
		"include.",
		"includeif.",
		"credential.",
		"filter.",
		"gpg.",
		"url.",
		"pager.",
		"browser.",
		"maintenance.",
		"submodule.",
	} {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	if key == "include.path" || key == "diff.external" {
		return true
	}
	if strings.HasPrefix(key, "diff.") &&
		(strings.HasSuffix(key, ".command") || strings.HasSuffix(key, ".textconv")) {
		return true
	}
	if strings.HasPrefix(key, "merge.") && strings.HasSuffix(key, ".driver") {
		return true
	}
	if strings.HasPrefix(key, "remote.") {
		return strings.HasSuffix(key, ".proxy") ||
			strings.HasSuffix(key, ".proxyauthmethod") ||
			strings.HasSuffix(key, ".vcs") ||
			strings.HasSuffix(key, ".uploadpack") ||
			strings.HasSuffix(key, ".receivepack") ||
			strings.HasSuffix(key, ".promisor") ||
			strings.HasSuffix(key, ".partialclonefilter")
	}
	return false
}

func readSmallRegularFile(path string) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, errors.New("metadata is not a regular file")
	}
	if before.Size() > maxGitMetadataFile {
		return nil, fmt.Errorf("metadata exceeds %d bytes", maxGitMetadataFile)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, errors.New("metadata changed while being opened")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxGitMetadataFile+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxGitMetadataFile {
		return nil, fmt.Errorf("metadata exceeds %d bytes", maxGitMetadataFile)
	}
	return data, nil
}

func rejectExistingMetadata(path, kind string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect %s: %w", kind, err)
	}
	if info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.IsDir() {
		return fmt.Errorf("%s are not allowed (%q)", kind, path)
	}
	return fmt.Errorf("unsupported %s metadata (%q)", kind, path)
}

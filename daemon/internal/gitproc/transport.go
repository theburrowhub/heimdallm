package gitproc

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const transportRef = "refs/heimdallm/transport"

// FetchOptions controls how an authenticated fetch is imported into the
// checkout. Depth zero fetches complete history. Unshallow upgrades an
// existing shallow repository and cannot be combined with Depth.
type FetchOptions struct {
	Depth     int
	Unshallow bool
}

// GitHubRemote constructs the only network endpoint accepted by the
// authenticated transport. The token is intentionally not part of the URL.
func GitHubRemote(repo string) (string, error) {
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || !safeRepoComponent(parts[0]) || !safeRepoComponent(parts[1]) {
		return "", fmt.Errorf("gitproc: invalid GitHub repository %q", repo)
	}
	return "https://x-access-token@github.com/" + parts[0] + "/" + parts[1] + ".git", nil
}

func safeRepoComponent(part string) bool {
	if part == "" || part == "." || part == ".." || strings.HasPrefix(part, "-") {
		return false
	}
	for _, ch := range part {
		if (ch >= 'a' && ch <= 'z') ||
			(ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') ||
			ch == '.' || ch == '_' || ch == '-' {
			continue
		}
		return false
	}
	return true
}

// ValidateBranch rejects option injection, revision expressions and refname
// edge cases before a caller places a branch in an exact refspec.
func ValidateBranch(branch string) error {
	if branch == "" || len(branch) > 1024 || branch == "@" ||
		strings.HasPrefix(branch, "-") ||
		strings.HasPrefix(branch, "/") ||
		strings.HasSuffix(branch, "/") ||
		strings.HasSuffix(branch, ".") ||
		strings.Contains(branch, "..") ||
		strings.Contains(branch, "@{") ||
		strings.Contains(branch, "//") ||
		strings.ContainsAny(branch, " ~^:?*[\\\x7f") {
		return fmt.Errorf("gitproc: invalid branch %q", branch)
	}
	for _, ch := range branch {
		if ch < 0x20 {
			return fmt.Errorf("gitproc: invalid branch %q", branch)
		}
	}
	for _, component := range strings.Split(branch, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return fmt.Errorf("gitproc: invalid branch %q", branch)
		}
	}
	return nil
}

func branchRef(branch string) (string, error) {
	if err := ValidateBranch(branch); err != nil {
		return "", err
	}
	return "refs/heads/" + branch, nil
}

func validateRemote(remote string) error {
	parsed, err := url.Parse(remote)
	if err != nil ||
		parsed.Scheme != "https" ||
		parsed.Host != "github.com" ||
		parsed.User == nil ||
		parsed.User.Username() != "x-access-token" {
		return errors.New("gitproc: authenticated remote must be https://x-access-token@github.com/<owner>/<repo>.git")
	}
	if _, hasPassword := parsed.User.Password(); hasPassword ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" ||
		parsed.RawPath != "" {
		return errors.New("gitproc: authenticated remote contains credentials or unsupported URL data")
	}
	path := strings.TrimPrefix(parsed.Path, "/")
	if !strings.HasSuffix(path, ".git") {
		return errors.New("gitproc: authenticated remote must end in .git")
	}
	expected, err := GitHubRemote(strings.TrimSuffix(path, ".git"))
	if err != nil || expected != remote {
		return errors.New("gitproc: authenticated remote is not canonical")
	}
	return nil
}

// Fetch obtains an exact branch or HEAD through a temporary bare repository.
// The only token-bearing child has a safe cwd and never opens target config;
// target imports from the bare over file:// in a later tokenless process.
func (r *Runner) Fetch(
	ctx context.Context,
	target string,
	remote string,
	ref string,
	token string,
	opts FetchOptions,
) error {
	if token == "" {
		return errors.New("gitproc: fetch requires a non-empty token")
	}
	if err := validateRemote(remote); err != nil {
		return err
	}
	if opts.Depth < 0 || (opts.Depth > 0 && opts.Unshallow) {
		return errors.New("gitproc: invalid fetch depth options")
	}
	remoteRef := "HEAD"
	if ref != "HEAD" {
		var err error
		remoteRef, err = branchRef(ref)
		if err != nil {
			return err
		}
	}
	canonicalTarget, err := r.AuditRepository(ctx, target)
	if err != nil {
		return err
	}

	transport, err := r.newTransport(ctx)
	if err != nil {
		return err
	}
	defer transport.cleanup()

	authArgs := []string{
		"--git-dir=" + transport.gitDir,
		"fetch",
		"--no-tags",
		"--no-recurse-submodules",
		"--no-auto-maintenance",
		"--force",
	}
	if opts.Depth > 0 {
		authArgs = append(authArgs, "--depth="+strconv.Itoa(opts.Depth))
	}
	authArgs = append(authArgs, "--", remote, remoteRef+":"+transportRef)
	if err := r.runTrustedWithToken(ctx, transport.root, token, authArgs...); err != nil {
		return fmt.Errorf("gitproc: authenticated fetch: %w", err)
	}

	expected, err := r.transportRevision(ctx, transport.gitDir)
	if err != nil {
		return err
	}
	importArgs := []string{
		"fetch",
		"--no-tags",
		"--no-recurse-submodules",
		"--no-auto-maintenance",
		"--force",
	}
	switch {
	case opts.Unshallow:
		importArgs = append(importArgs, "--unshallow")
	case opts.Depth > 0:
		importArgs = append(importArgs, "--depth="+strconv.Itoa(opts.Depth), "--update-shallow")
	}
	importArgs = append(importArgs, "--", fileRemote(transport.gitDir), transportRef)
	if err := r.runAudited(ctx, canonicalTarget, true, importArgs...); err != nil {
		return fmt.Errorf("gitproc: import authenticated fetch: %w", err)
	}
	imported, err := r.Capture(ctx, canonicalTarget, "rev-parse", "--verify", "FETCH_HEAD^{commit}")
	if err != nil {
		return fmt.Errorf("gitproc: verify imported fetch: %w", err)
	}
	if strings.TrimSpace(string(imported)) != expected {
		return errors.New("gitproc: imported fetch did not match authenticated object ID")
	}
	return nil
}

// CloneNoCheckout authenticates only while Git has a safe cwd and is
// creating repository metadata from an empty template. Worktree files are
// materialized later, without the token, after auditing the generated config.
func (r *Runner) CloneNoCheckout(
	ctx context.Context,
	remote string,
	target string,
	token string,
	depth int,
) (returnErr error) {
	if token == "" {
		return errors.New("gitproc: clone requires a non-empty token")
	}
	if err := validateRemote(remote); err != nil {
		return err
	}
	if depth < 0 {
		return errors.New("gitproc: clone depth must not be negative")
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("gitproc: resolve clone target: %w", err)
	}
	if absTarget == string(filepath.Separator) {
		return errors.New("gitproc: refusing root as clone target")
	}
	if _, err := os.Lstat(absTarget); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return fmt.Errorf("gitproc: clone target %q already exists", absTarget)
		}
		return fmt.Errorf("gitproc: inspect clone target: %w", err)
	}
	if err := os.Mkdir(absTarget, 0o700); err != nil {
		return fmt.Errorf("gitproc: create clone target: %w", err)
	}
	defer func() {
		if returnErr == nil {
			return
		}
		if err := os.RemoveAll(absTarget); err != nil {
			returnErr = errors.Join(
				returnErr,
				fmt.Errorf("gitproc: remove partial clone %q: %w", absTarget, err),
			)
		}
	}()

	transport, err := r.newTransport(ctx)
	if err != nil {
		return err
	}
	defer transport.cleanup()
	args := []string{
		"clone",
		"--no-checkout",
		"--no-recurse-submodules",
		"--template=" + transport.template,
	}
	if depth > 0 {
		args = append(args, "--depth="+strconv.Itoa(depth))
	}
	args = append(args, "--", remote, absTarget)
	if err := r.runTrustedWithToken(ctx, transport.root, token, args...); err != nil {
		return fmt.Errorf("gitproc: authenticated clone: %w", err)
	}
	if err := r.Run(ctx, absTarget, "checkout", "--force"); err != nil {
		return fmt.Errorf("gitproc: tokenless checkout: %w", err)
	}
	return nil
}

// PushBranch copies the exact local branch into a temporary bare repository
// without a token, verifies the object ID, then authenticates from that bare
// repository. Repository config and hooks are therefore unavailable to the
// token-bearing process.
func (r *Runner) PushBranch(
	ctx context.Context,
	target string,
	remote string,
	branch string,
	token string,
) (string, error) {
	if token == "" {
		return "", errors.New("gitproc: push requires a non-empty token")
	}
	if err := validateRemote(remote); err != nil {
		return "", err
	}
	ref, err := branchRef(branch)
	if err != nil {
		return "", err
	}
	canonicalTarget, err := r.AuditRepository(ctx, target)
	if err != nil {
		return "", err
	}
	expected, err := r.Revision(ctx, canonicalTarget, ref)
	if err != nil {
		return "", fmt.Errorf("gitproc: resolve local branch: %w", err)
	}

	transport, err := r.newTransport(ctx)
	if err != nil {
		return "", err
	}
	defer transport.cleanup()
	if err := r.runTrusted(
		ctx,
		transport.root,
		true,
		"--git-dir="+transport.gitDir,
		"fetch",
		"--no-tags",
		"--no-recurse-submodules",
		"--no-auto-maintenance",
		"--force",
		"--",
		fileRemote(canonicalTarget),
		ref+":"+transportRef,
	); err != nil {
		return "", fmt.Errorf("gitproc: stage local branch for push: %w", err)
	}
	staged, err := r.transportRevision(ctx, transport.gitDir)
	if err != nil {
		return "", err
	}
	if staged != expected {
		return "", errors.New("gitproc: staged push did not match local branch object ID")
	}
	if err := r.runTrustedWithToken(
		ctx,
		transport.root,
		token,
		"--git-dir="+transport.gitDir,
		"push",
		"--no-verify",
		"--signed=false",
		"--recurse-submodules=no",
		"--",
		remote,
		transportRef+":"+ref,
	); err != nil {
		return "", fmt.Errorf("gitproc: authenticated push: %w", err)
	}
	return expected, nil
}

// DeleteBranch removes a remote branch only if it still points at expected.
// This prevents cleanup after a failed PR creation from deleting a branch
// another actor advanced after Heimdallm's push.
func (r *Runner) DeleteBranch(
	ctx context.Context,
	remote string,
	branch string,
	expected string,
	token string,
) error {
	if token == "" {
		return errors.New("gitproc: delete requires a non-empty token")
	}
	if err := validateRemote(remote); err != nil {
		return err
	}
	ref, err := branchRef(branch)
	if err != nil {
		return err
	}
	if !validObjectID(expected) {
		return errors.New("gitproc: delete requires a valid expected object ID")
	}
	transport, err := r.newTransport(ctx)
	if err != nil {
		return err
	}
	defer transport.cleanup()
	if err := r.runTrustedWithToken(
		ctx,
		transport.root,
		token,
		"--git-dir="+transport.gitDir,
		"push",
		"--no-verify",
		"--signed=false",
		"--recurse-submodules=no",
		"--force-with-lease="+ref+":"+expected,
		"--",
		remote,
		":"+ref,
	); err != nil {
		return fmt.Errorf("gitproc: authenticated conditional delete: %w", err)
	}
	return nil
}

// Revision resolves a ref to an exact commit ID in an audited repository.
func (r *Runner) Revision(ctx context.Context, target, ref string) (string, error) {
	if ref == "" || strings.HasPrefix(ref, "-") || strings.ContainsAny(ref, "\x00\r\n ") {
		return "", errors.New("gitproc: invalid revision")
	}
	out, err := r.Capture(ctx, target, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	oid := strings.TrimSpace(string(out))
	if !validObjectID(oid) {
		return "", errors.New("gitproc: Git returned an invalid object ID")
	}
	return oid, nil
}

func (r *Runner) transportRevision(ctx context.Context, gitDir string) (string, error) {
	out, err := r.captureTrusted(
		ctx,
		"",
		false,
		"--git-dir="+gitDir,
		"rev-parse",
		"--verify",
		transportRef+"^{commit}",
	)
	if err != nil {
		return "", fmt.Errorf("gitproc: resolve transport object ID: %w", err)
	}
	oid := strings.TrimSpace(string(out))
	if !validObjectID(oid) {
		return "", errors.New("gitproc: transport returned an invalid object ID")
	}
	return oid, nil
}

func validObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, ch := range value {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') && (ch < 'A' || ch > 'F') {
			return false
		}
	}
	return true
}

type tempTransport struct {
	root     string
	gitDir   string
	template string
}

func (r *Runner) newTransport(ctx context.Context) (*tempTransport, error) {
	root, err := os.MkdirTemp("", "heimdallm-git-transport-*")
	if err != nil {
		return nil, fmt.Errorf("gitproc: create transport: %w", err)
	}
	cleanupOnError := func(cause error) (*tempTransport, error) {
		_ = os.RemoveAll(root)
		return nil, cause
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return cleanupOnError(fmt.Errorf("gitproc: chmod transport: %w", err))
	}
	transport := &tempTransport{
		root:     root,
		gitDir:   filepath.Join(root, "repo.git"),
		template: filepath.Join(root, "template"),
	}
	if err := os.Mkdir(transport.template, 0o700); err != nil {
		return cleanupOnError(fmt.Errorf("gitproc: create empty template: %w", err))
	}
	if err := r.runTrusted(
		ctx,
		root,
		false,
		"init",
		"--bare",
		"--template="+transport.template,
		"--",
		transport.gitDir,
	); err != nil {
		return cleanupOnError(fmt.Errorf("gitproc: init transport: %w", err))
	}
	return transport, nil
}

func (t *tempTransport) cleanup() {
	if t != nil && t.root != "" {
		_ = os.RemoveAll(t.root)
	}
}

func fileRemote(path string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
}

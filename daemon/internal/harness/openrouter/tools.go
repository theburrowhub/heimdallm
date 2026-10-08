package openrouter

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Tool output limits. A tool result is fed back into the prompt, so every
// tool bounds what it returns; the model is told when it was cut.
const (
	maxReadLines        = 400
	maxReadBytes        = 64 << 10
	maxListEntries      = 300
	maxGrepMatches      = 100
	maxGrepFileBytes    = 1 << 20
	maxGrepFiles        = 5000
	maxGrepPatternLen   = 200
	maxGrepLineLen      = 300
	maxToolResultBytes  = 64 << 10
	maxToolPathArgBytes = 1024
)

// skipDirs are never listed or searched: VCS internals and dependency or
// build trees that only add noise.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, ".dart_tool": true,
	"build": true, "dist": true, "target": true, ".venv": true,
}

// secretNamePatterns are file names the tools never read or search. A
// review is posted publicly on the PR, and with an operator's local_dir the
// checkout can hold untracked credentials next to the code; a prompt-injected
// model must not be able to quote them into the review.
var secretNamePatterns = []string{
	".env", ".env.*", "*.pem", "*.key", "*.p12", "*.pfx", "*.jks", "*.keystore",
	"id_rsa*", "id_dsa*", "id_ecdsa*", "id_ed25519*", ".netrc", ".npmrc", ".pypirc",
	".git-credentials", "credentials", "credentials.*", "*.tfvars", "*.tfstate",
	"secrets.*", "*secret*.json", "*.kdbx",
}

// isSecretFile reports whether a file name looks like a credential store.
func isSecretFile(name string) bool {
	name = strings.ToLower(name)
	for _, p := range secretNamePatterns {
		if ok, _ := filepath.Match(p, name); ok {
			return true
		}
	}
	return false
}

var errSecretFile = errors.New("this file may hold credentials and is not readable by the reviewer")

// Workspace exposes read-only, root-confined access to a checkout.
type Workspace struct {
	root string // absolute, symlinks resolved
}

// NewWorkspace roots the tools at dir. It fails when dir does not exist.
func NewWorkspace(dir string) (*Workspace, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("openrouter: workspace %s is not a directory", dir)
	}
	return &Workspace{root: resolved}, nil
}

var errOutsideWorkspace = errors.New("path is outside the repository")

// resolve maps a model-supplied, repository-relative path to an absolute path
// inside the root. Symlinks are resolved first so a link in the checkout (the
// PR author controls it) cannot point the tools at the rest of the disk.
func (w *Workspace) resolve(rel string) (string, error) {
	if len(rel) > maxToolPathArgBytes {
		return "", errors.New("path too long")
	}
	rel = strings.TrimSpace(rel)
	if rel == "" || rel == "/" {
		rel = "."
	}
	rel = strings.TrimPrefix(rel, "/")
	if strings.ContainsRune(rel, 0) {
		return "", errOutsideWorkspace
	}
	joined := filepath.Join(w.root, filepath.FromSlash(rel))
	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return "", fmt.Errorf("no such file or directory: %s", rel)
	}
	if resolved != w.root && !strings.HasPrefix(resolved, w.root+string(filepath.Separator)) {
		return "", errOutsideWorkspace
	}
	return resolved, nil
}

func (w *Workspace) relative(abs string) string {
	rel, err := filepath.Rel(w.root, abs)
	if err != nil {
		return abs
	}
	return filepath.ToSlash(rel)
}

// Specs declares the tools to the model.
func (w *Workspace) Specs() []ToolSpec {
	obj := func(props map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": required}
	}
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	num := func(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }
	return []ToolSpec{
		{Type: "function", Function: FunctionSpec{
			Name:        "read_file",
			Description: "Read lines of a file in the repository (paths are relative to the repository root). Use it to see code around a change, callers, tests or definitions the diff does not show.",
			Parameters: obj(map[string]any{
				"path":       str("file path relative to the repository root"),
				"start_line": num("first line, 1-based (default 1)"),
				"end_line":   num(fmt.Sprintf("last line, inclusive (at most %d lines are returned)", maxReadLines)),
			}, "path"),
		}},
		{Type: "function", Function: FunctionSpec{
			Name:        "list_dir",
			Description: "List the entries of a directory in the repository.",
			Parameters:  obj(map[string]any{"path": str("directory relative to the repository root ('.' for the root)")}, "path"),
		}},
		{Type: "function", Function: FunctionSpec{
			Name:        "grep",
			Description: "Search the repository for a regular expression (RE2 syntax) and return matching lines as path:line: text.",
			Parameters: obj(map[string]any{
				"pattern": str("regular expression"),
				"glob":    str("optional file name glob to restrict the search, e.g. *.go"),
			}, "pattern"),
		}},
	}
}

// Call runs one tool and returns its textual result. Errors are returned as
// text too: the model can recover from a wrong path, the review must not fail.
func (w *Workspace) Call(name, argsJSON string) string {
	var out string
	var err error
	switch name {
	case "read_file":
		var a struct {
			Path      string `json:"path"`
			StartLine int    `json:"start_line"`
			EndLine   int    `json:"end_line"`
		}
		if err = json.Unmarshal([]byte(argsJSON), &a); err == nil {
			out, err = w.readFile(a.Path, a.StartLine, a.EndLine)
		}
	case "list_dir":
		var a struct {
			Path string `json:"path"`
		}
		if err = json.Unmarshal([]byte(argsJSON), &a); err == nil {
			out, err = w.listDir(a.Path)
		}
	case "grep":
		var a struct {
			Pattern string `json:"pattern"`
			Glob    string `json:"glob"`
		}
		if err = json.Unmarshal([]byte(argsJSON), &a); err == nil {
			out, err = w.grep(a.Pattern, a.Glob)
		}
	default:
		err = fmt.Errorf("unknown tool %q", name)
	}
	if err != nil {
		return "error: " + err.Error()
	}
	if len(out) > maxToolResultBytes {
		out = out[:maxToolResultBytes] + "\n[output truncated]"
	}
	return out
}

func (w *Workspace) readFile(rel string, start, end int) (string, error) {
	abs, err := w.resolve(rel)
	if err != nil {
		return "", err
	}
	if isSecretFile(filepath.Base(abs)) {
		return "", errSecretFile
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory; use list_dir", rel)
	}
	f, err := os.Open(abs)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if start < 1 {
		start = 1
	}
	if end < start || end-start+1 > maxReadLines {
		end = start + maxReadLines - 1
	}
	var b strings.Builder
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	line := 0
	for sc.Scan() {
		line++
		if line < start {
			continue
		}
		if line > end || b.Len() > maxReadBytes {
			fmt.Fprintf(&b, "[stopped at line %d; ask for a later range to continue]\n", line-1)
			break
		}
		fmt.Fprintf(&b, "%d\t%s\n", line, sc.Text())
	}
	if line < start {
		return fmt.Sprintf("[%s has %d lines]", w.relative(abs), line), nil
	}
	return b.String(), nil
}

func (w *Workspace) listDir(rel string) (string, error) {
	abs, err := w.resolve(rel)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return "", err
	}
	var lines []string
	for _, e := range entries {
		if e.IsDir() && skipDirs[e.Name()] {
			continue
		}
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		lines = append(lines, name)
	}
	sort.Strings(lines)
	if len(lines) > maxListEntries {
		lines = append(lines[:maxListEntries], fmt.Sprintf("[%d more entries]", len(lines)-maxListEntries))
	}
	return strings.Join(lines, "\n"), nil
}

func (w *Workspace) grep(pattern, glob string) (string, error) {
	if pattern == "" || len(pattern) > maxGrepPatternLen {
		return "", fmt.Errorf("pattern must be 1-%d characters", maxGrepPatternLen)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("invalid pattern: %w", err)
	}
	if glob != "" {
		if _, err := filepath.Match(glob, "x"); err != nil {
			return "", fmt.Errorf("invalid glob: %w", err)
		}
	}
	var matches []string
	files := 0
	stop := errors.New("stop")
	walkErr := filepath.WalkDir(w.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != w.root && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		// WalkDir does not follow symlinks; skip them so a link cannot
		// smuggle an outside file into the results.
		if d.Type()&fs.ModeSymlink != 0 || !d.Type().IsRegular() || isSecretFile(d.Name()) {
			return nil
		}
		if glob != "" {
			if ok, _ := filepath.Match(glob, d.Name()); !ok {
				return nil
			}
		}
		files++
		if files > maxGrepFiles {
			return stop
		}
		info, err := d.Info()
		if err != nil || info.Size() > maxGrepFileBytes {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil || bytes.IndexByte(data, 0) >= 0 {
			return nil // unreadable or binary
		}
		for i, line := range strings.Split(string(data), "\n") {
			if re.MatchString(line) {
				matches = append(matches, fmt.Sprintf("%s:%d: %s", w.relative(path), i+1, truncate(strings.TrimSpace(line), maxGrepLineLen)))
				if len(matches) >= maxGrepMatches {
					return stop
				}
			}
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, stop) {
		return "", walkErr
	}
	if len(matches) == 0 {
		return "no matches", nil
	}
	out := strings.Join(matches, "\n")
	if len(matches) >= maxGrepMatches {
		out += fmt.Sprintf("\n[stopped at %d matches; narrow the pattern or glob]", maxGrepMatches)
	}
	return out, nil
}

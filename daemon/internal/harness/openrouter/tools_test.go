package openrouter

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testRepo(t *testing.T) (*Workspace, string) {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var long strings.Builder
	for i := 1; i <= 1000; i++ {
		fmt.Fprintf(&long, "line %d\n", i)
	}
	write("main.go", "package main\n\nfunc Login(user string) bool {\n\treturn user == \"admin\"\n}\n")
	write("pkg/util/util.go", "package util\n// Login helper\n")
	write("big.txt", long.String())
	write("bin.dat", "a\x00b Login")
	write("node_modules/x/index.js", "Login")
	write(".git/config", "Login")
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("TOKEN Login"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linkdir")); err != nil {
		t.Fatal(err)
	}
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	return ws, outside
}

func TestReadFile(t *testing.T) {
	ws, _ := testRepo(t)
	out := ws.Call("read_file", `{"path":"main.go","start_line":3,"end_line":4}`)
	if !strings.Contains(out, "3\tfunc Login") || strings.Contains(out, "1\tpackage") || strings.Contains(out, "5\t}") {
		t.Errorf("range read = %q", out)
	}
	out = ws.Call("read_file", `{"path":"/big.txt"}`)
	if !strings.Contains(out, fmt.Sprintf("%d\tline %d", maxReadLines, maxReadLines)) || !strings.Contains(out, "[stopped at line") {
		t.Errorf("long read must stop at the cap: %q", out[len(out)-80:])
	}
	if out := ws.Call("read_file", `{"path":"big.txt","start_line":5000}`); !strings.Contains(out, "has 1000 lines") {
		t.Errorf("past the end = %q", out)
	}
	if out := ws.Call("read_file", `{"path":"pkg"}`); !strings.Contains(out, "is a directory") {
		t.Errorf("dir read = %q", out)
	}
}

func TestToolsStayInsideTheRepository(t *testing.T) {
	ws, outside := testRepo(t)
	for _, args := range []string{
		`{"path":"../` + filepath.Base(outside) + `/secret"}`,
		`{"path":"link"}`,
		`{"path":"linkdir/secret"}`,
		`{"path":"` + filepath.Join(outside, "secret") + `"}`,
		`{"path":"nope.go"}`,
		`{"path":"a\u0000b"}`,
		`{"path":"` + strings.Repeat("a", maxToolPathArgBytes+1) + `"}`,
	} {
		out := ws.Call("read_file", args)
		if !strings.HasPrefix(out, "error:") || strings.Contains(out, "TOKEN") {
			t.Errorf("read_file %s = %q, want an error", args, out)
		}
	}
	if out := ws.Call("list_dir", `{"path":"linkdir"}`); !strings.HasPrefix(out, "error:") {
		t.Errorf("list_dir through a symlink = %q", out)
	}
	if out := ws.Call("grep", `{"pattern":"TOKEN"}`); out != "no matches" {
		t.Errorf("grep must not follow symlinks out: %q", out)
	}
}

func TestListDirAndGrep(t *testing.T) {
	ws, _ := testRepo(t)
	out := ws.Call("list_dir", `{"path":"."}`)
	for _, want := range []string{"main.go", "pkg/", "big.txt"} {
		if !strings.Contains(out, want) {
			t.Errorf("list_dir missing %q: %q", want, out)
		}
	}
	if strings.Contains(out, ".git") || strings.Contains(out, "node_modules") {
		t.Errorf("list_dir must hide VCS and dependency dirs: %q", out)
	}
	out = ws.Call("grep", `{"pattern":"Login"}`)
	if !strings.Contains(out, "main.go:3: func Login") || !strings.Contains(out, "pkg/util/util.go:2:") {
		t.Errorf("grep = %q", out)
	}
	if strings.Contains(out, "bin.dat") || strings.Contains(out, "node_modules") || strings.Contains(out, ".git") {
		t.Errorf("grep must skip binaries, deps and VCS: %q", out)
	}
	if out := ws.Call("grep", `{"pattern":"Login","glob":"*.go"}`); strings.Contains(out, "big.txt") || !strings.Contains(out, "main.go") {
		t.Errorf("glob grep = %q", out)
	}
	if out := ws.Call("grep", `{"pattern":"line \\d+"}`); !strings.Contains(out, fmt.Sprintf("[stopped at %d matches", maxGrepMatches)) {
		t.Errorf("grep must cap matches: %q", out[len(out)-60:])
	}
	for _, bad := range []string{`{"pattern":""}`, `{"pattern":"("}`, `{"pattern":"x","glob":"["}`, `{"pattern":"` + strings.Repeat("a", maxGrepPatternLen+1) + `"}`} {
		if out := ws.Call("grep", bad); !strings.HasPrefix(out, "error:") {
			t.Errorf("grep %s = %q, want an error", bad, out)
		}
	}
	if out := ws.Call("grep", `{"pattern":"zzz-none"}`); out != "no matches" {
		t.Errorf("no match = %q", out)
	}
}

func TestCallRejectsUnknownToolsAndBadArgs(t *testing.T) {
	ws, _ := testRepo(t)
	if out := ws.Call("rm", `{}`); !strings.Contains(out, "unknown tool") {
		t.Errorf("unknown tool = %q", out)
	}
	for _, tool := range []string{"read_file", "list_dir", "grep"} {
		if out := ws.Call(tool, `not json`); !strings.HasPrefix(out, "error:") {
			t.Errorf("%s with bad args = %q", tool, out)
		}
	}
	if len(ws.Specs()) != 3 {
		t.Error("three tools expected")
	}
}

func TestNewWorkspaceErrors(t *testing.T) {
	if _, err := NewWorkspace(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("missing dir must fail")
	}
	f := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewWorkspace(f); err == nil {
		t.Error("a file is not a workspace")
	}
}

func TestToolResultIsCapped(t *testing.T) {
	root := t.TempDir()
	long := strings.Repeat("x", 4000) + "\n"
	if err := os.WriteFile(filepath.Join(root, "wide.txt"), []byte(strings.Repeat(long, 100)), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	out := ws.Call("read_file", `{"path":"wide.txt"}`)
	if len(out) > maxToolResultBytes+100 {
		t.Errorf("tool result %d bytes exceeds the cap", len(out))
	}
}

func TestToolsNeverReadCredentialFiles(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		".env":                    "API_KEY=topsecret",
		".env.production":         "API_KEY=topsecret",
		"deploy/server.pem":       "topsecret",
		"ID_RSA":                  "topsecret",
		"config/credentials.json": "topsecret",
		"app.go":                  "package app // topsecret mention in code is fine",
	}
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".env", ".env.production", "deploy/server.pem", "ID_RSA", "config/credentials.json"} {
		out := ws.Call("read_file", `{"path":"`+rel+`"}`)
		if !strings.Contains(out, "may hold credentials") || strings.Contains(out, "topsecret") {
			t.Errorf("read_file %s = %q", rel, out)
		}
	}
	out := ws.Call("grep", `{"pattern":"topsecret"}`)
	if strings.Count(out, "topsecret") != 1 || !strings.Contains(out, "app.go") {
		t.Errorf("grep must skip credential files: %q", out)
	}
	if !strings.Contains(ws.Call("list_dir", `{"path":"."}`), ".env") {
		t.Error("listing may show the name; only the content is protected")
	}
}

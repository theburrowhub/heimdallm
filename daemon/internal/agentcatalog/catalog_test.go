package agentcatalog

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeFS map[string]bool

func (f fakeFS) stat(p string) (os.FileInfo, error) {
	if f[p] {
		return nil, nil
	}
	return nil, fs.ErrNotExist
}

func testDetector(home string, installed map[string]string, files fakeFS, runs map[string]string) Detector {
	return Detector{
		Home: home,
		Resolve: func(id string) string {
			return installed[id]
		},
		LookPath: func(name string) (string, error) {
			if p, ok := installed["bin:"+name]; ok {
				return p, nil
			}
			return "", errors.New("not found")
		},
		Stat:   files.stat,
		Getenv: func(string) string { return "" },
		Run: func(_ context.Context, path string, args ...string) (string, error) {
			out, ok := runs[path+" "+strings.Join(args, " ")]
			if !ok {
				return "", errors.New("unexpected command")
			}
			return out, nil
		},
	}
}

func byID(agents []Agent) map[string]Agent {
	m := map[string]Agent{}
	for _, a := range agents {
		m[a.ID] = a
	}
	return m
}

func TestScan_DetectsInstalledConfiguredVersionsAndModels(t *testing.T) {
	home := "/home/u"
	installed := map[string]string{
		"claude":     home + "/.local/bin/claude",
		"cursor_cli": home + "/.local/bin/cursor-agent",
		"copilot":    "/opt/homebrew/bin/copilot",
		"opencode":   "/usr/local/bin/opencode",
	}
	files := fakeFS{
		filepath.Join(home, ".claude"):  true,
		filepath.Join(home, ".gemini"):  true,
		"/Applications/Cursor.app":      true,
		filepath.Join(home, ".copilot"): true,
	}
	runs := map[string]string{
		home + "/.local/bin/claude --version":       "2.1.292 (Claude Code)\n",
		home + "/.local/bin/cursor-agent --version": "2026.10.01-e373342\n",
		home + "/.local/bin/cursor-agent models":    "Available models\n\nauto - Auto (default)\ngpt-5.2 - GPT-5.2\nbad id - x\n",
		"/opt/homebrew/bin/copilot --version":       "GitHub Copilot CLI 1.0.88.\nRun 'copilot update'\n",
		"/opt/homebrew/bin/copilot help config":     "  `model`: AI model.\n    - \"claude-sonnet-5\"\n    - \"gpt-5.5\"\n\n  `contextTier`: x\n    - \"default\"\n",
		"/usr/local/bin/opencode --version":         "1.18.30\n",
		"/usr/local/bin/opencode models":            "opencode/big-pickle\nopenrouter/~anthropic/claude-fable-latest\n\nopencode/big-pickle\n",
	}
	agents := testDetector(home, installed, files, runs).Scan(context.Background())
	if len(agents) != len(Catalog) {
		t.Fatalf("got %d agents, want %d", len(agents), len(Catalog))
	}
	m := byID(agents)

	claude := m["claude"]
	if !claude.Installed || !claude.Configured || !claude.Executable || claude.Version != "2.1.292" ||
		claude.Path != "~/.local/bin/claude" || claude.ConfigAgent != "claude" {
		t.Errorf("claude = %+v", claude)
	}
	if g := m["gemini"]; g.Installed || !g.Configured || g.Version != "" {
		t.Errorf("gemini (config dir only) = %+v", g)
	}
	if c := m["codex"]; c.Installed || c.Configured {
		t.Errorf("codex should be absent: %+v", c)
	}
	ide := m["cursor"]
	if !ide.Installed || ide.Executable || ide.Kind != KindIDE || ide.ConfigAgent != "cursor_cli" {
		t.Errorf("cursor IDE = %+v", ide)
	}
	cli := m["cursor_cli"]
	if cli.Version != "2026.10.01-e373342" || strings.Join(cli.Models, ",") != "auto,gpt-5.2" {
		t.Errorf("cursor cli = %+v", cli)
	}
	cp := m["copilot"]
	if cp.Version != "1.0.88" || strings.Join(cp.Models, ",") != "claude-sonnet-5,gpt-5.5" || !cp.Configured {
		t.Errorf("copilot = %+v", cp)
	}
	oc := m["opencode"]
	if strings.Join(oc.Models, ",") != "opencode/big-pickle,openrouter/~anthropic/claude-fable-latest" {
		t.Errorf("opencode models = %v", oc.Models)
	}
}

func TestScan_FailingProbesLeaveFieldsEmpty(t *testing.T) {
	d := testDetector("/h", map[string]string{"copilot": "/bin/copilot"}, fakeFS{}, map[string]string{})
	cp := byID(d.Scan(context.Background()))["copilot"]
	if !cp.Installed || cp.Version != "" || cp.Models != nil {
		t.Errorf("copilot with failing probes = %+v", cp)
	}
	d.Run = nil
	if cp := byID(d.Scan(context.Background()))["copilot"]; !cp.Installed || cp.Version != "" {
		t.Errorf("no runner: %+v", cp)
	}
	d.Resolve, d.LookPath = nil, nil
	for _, a := range d.Scan(context.Background()) {
		if a.Installed {
			t.Errorf("%s installed without any resolver", a.ID)
		}
	}
}

func TestScan_IDELauncherOnPath(t *testing.T) {
	d := testDetector("/h", map[string]string{"bin:cursor": "/usr/local/bin/cursor"}, fakeFS{},
		map[string]string{"/usr/local/bin/cursor --version": "3.4.5\nabc\n"})
	ide := byID(d.Scan(context.Background()))["cursor"]
	if !ide.Installed || ide.Version != "3.4.5" || ide.Path != "/usr/local/bin/cursor" {
		t.Errorf("cursor IDE via launcher = %+v", ide)
	}
}

func TestRootsHonourEnvironment(t *testing.T) {
	env := map[string]string{
		"CODEX_HOME":      "/x/codex",
		"COPILOT_HOME":    "/x/copilot",
		"GEMINI_CLI_HOME": "/x/g",
		"XDG_DATA_HOME":   "/x/data",
	}
	d := Detector{Home: "/h", Getenv: func(k string) string { return env[k] }}
	want := map[string]string{
		"codex":    "/x/codex",
		"copilot":  "/x/copilot",
		"gemini":   "/x/g/.gemini",
		"opencode": "/x/data/opencode",
		"claude":   "/h/.claude",
	}
	for _, e := range Catalog {
		if w, ok := want[e.ID]; ok {
			if got := d.roots(e); len(got) == 0 || got[0] != w {
				t.Errorf("roots(%s) = %v, want %s first", e.ID, got, w)
			}
		}
	}
	d.Getenv = nil
	if got := d.roots(Catalog[0]); got[0] != "/h/.claude" {
		t.Errorf("nil getenv roots = %v", got)
	}
}

func TestParseVersion(t *testing.T) {
	cases := map[string]string{
		"2.1.292 (Claude Code)":              "2.1.292",
		"codex-cli 0.161.0":                  "0.161.0",
		"GitHub Copilot CLI 1.0.88.":         "1.0.88",
		"2026.10.01-e373342":                 "2026.10.01-e373342",
		"no digits here":                     "",
		"Please run cursor-agent login":      "",
		strings.Repeat("x", 200):             "",
		"v" + strings.Repeat("1.", 60) + "1": strings.Repeat("1.", 40),
	}
	for in, want := range cases {
		if got := parseVersion(in); got != want {
			t.Errorf("parseVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCapModels(t *testing.T) {
	var many []string
	for i := 0; i < maxModels+10; i++ {
		many = append(many, "m"+strings.Repeat("x", i%5)+string(rune('a'+i%26))+strings.Repeat("y", i/26))
	}
	if got := capModels(many); len(got) != maxModels {
		t.Errorf("cap = %d", len(got))
	}
	got := capModels([]string{"ok", "", " ok ", "has space", strings.Repeat("a", maxModelLen+1), "$(rm)", "x[effort=high]"})
	if strings.Join(got, ",") != "ok,x[effort=high]" {
		t.Errorf("capModels = %v", got)
	}
}

func TestStore_SnapshotScansOnceAndRefreshUpdates(t *testing.T) {
	version := "1.0.0"
	d := Detector{
		Home:    "/h",
		Resolve: func(id string) string { return map[string]string{"claude": "/bin/claude"}[id] },
		Stat:    fakeFS{}.stat,
		Run: func(_ context.Context, path string, args ...string) (string, error) {
			return version, nil
		},
	}
	s := NewStore(d)
	agents, at := s.Snapshot(context.Background())
	if at.IsZero() || byID(agents)["claude"].Version != "1.0.0" {
		t.Fatalf("first snapshot = %v at %v", agents, at)
	}
	version = "2.0.0"
	if a, _ := s.Get(context.Background(), "claude"); a.Version != "1.0.0" {
		t.Errorf("snapshot must be cached until a refresh, got %s", a.Version)
	}
	time.Sleep(time.Millisecond)
	s.Refresh(context.Background())
	if a, ok := s.Get(context.Background(), "claude"); !ok || a.Version != "2.0.0" {
		t.Errorf("after refresh = %+v", a)
	}
	if _, ok := s.Get(context.Background(), "nope"); ok {
		t.Error("unknown id must not be found")
	}
}

func TestNewDetectorIsWired(t *testing.T) {
	d := NewDetector()
	if d.Resolve == nil || d.LookPath == nil || d.Stat == nil || d.Getenv == nil || d.Run == nil {
		t.Fatalf("NewDetector left a dependency nil: %+v", d)
	}
	out, err := runCommand(context.Background(), "/bin/echo", "hello")
	if err != nil || strings.TrimSpace(out) != "hello" {
		t.Errorf("runCommand = %q, %v", out, err)
	}
}

// A Refresh that waited on a running scan returns that scan: concurrent first
// hits after startup must not each launch every agent's probes again.
func TestStore_ConcurrentRefreshesCoalesce(t *testing.T) {
	var runs atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	d := Detector{
		Home:    "/h",
		Resolve: func(id string) string { return map[string]string{"claude": "/bin/claude"}[id] },
		Stat:    fakeFS{}.stat,
		Run: func(_ context.Context, path string, args ...string) (string, error) {
			if runs.Add(1) == 1 {
				close(started)
				<-release
			}
			return "1.0.0", nil
		},
	}
	s := NewStore(d)
	first := make(chan []Agent)
	go func() { first <- s.Refresh(context.Background()) }()
	<-started
	second := make(chan []Agent)
	go func() { second <- s.Refresh(context.Background()) }()
	time.Sleep(20 * time.Millisecond) // let the second call queue on the scan
	close(release)
	a, b := <-first, <-second
	if byID(a)["claude"].Version != "1.0.0" || byID(b)["claude"].Version != "1.0.0" {
		t.Fatalf("results = %v / %v", a, b)
	}
	if n := runs.Load(); n != 1 {
		t.Errorf("probes ran %d times, want one scan", n)
	}
	s.Refresh(context.Background())
	if n := runs.Load(); n != 2 {
		t.Errorf("a later refresh must scan again, probes ran %d times", n)
	}
}

// A scan cut short by its caller's context is not stored over a good one.
func TestStore_CancelledScanIsNotStored(t *testing.T) {
	version := "1.0.0"
	d := Detector{
		Home:    "/h",
		Resolve: func(id string) string { return map[string]string{"claude": "/bin/claude"}[id] },
		Stat:    fakeFS{}.stat,
		Run: func(ctx context.Context, path string, args ...string) (string, error) {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return version, nil
		},
	}
	s := NewStore(d)
	s.Refresh(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	version = "2.0.0"
	if got := byID(s.Refresh(ctx))["claude"]; got.Version != "" {
		t.Errorf("cancelled scan = %+v", got)
	}
	if a, _ := s.Get(context.Background(), "claude"); a.Version != "1.0.0" {
		t.Errorf("stored version = %q, want the good scan kept", a.Version)
	}
}

func TestRunCommandCapsOutput(t *testing.T) {
	out, err := runCommand(context.Background(), "/bin/sh", "-c", "head -c 400000 /dev/zero | tr '\\0' x")
	if err != nil || len(out) != maxProbeOutput {
		t.Errorf("len = %d, err = %v; want the output capped at %d", len(out), err, maxProbeOutput)
	}
}

// Writes are cut at the cap whatever their size; a pipe splits a probe's
// output unpredictably, so the boundary cases are tested here directly.
func TestCappedBuffer(t *testing.T) {
	b := &cappedBuffer{max: 5}
	for _, chunk := range []string{"ab", "cdef", "gh"} {
		if n, err := b.Write([]byte(chunk)); n != len(chunk) || err != nil {
			t.Fatalf("Write(%q) = %d, %v; must report the whole chunk consumed", chunk, n, err)
		}
	}
	if b.String() != "abcde" {
		t.Errorf("buffer = %q, want the first 5 bytes", b.String())
	}
}

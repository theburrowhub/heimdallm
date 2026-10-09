// Package agentcatalog discovers which AI coding agents are installed on the
// machine the daemon runs on, without starting a review or reading any of the
// agents' transcripts: executables on PATH (and the login shell / installer
// directories the executor also searches), configuration roots, application
// bundles, the CLI's version and, where the CLI can list them, its models.
package agentcatalog

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/heimdallm/daemon/internal/executor"
	"github.com/heimdallm/daemon/internal/procgroup"
)

// Kind classifies a catalog entry.
const (
	KindAgent    = "agent"    // a CLI that can run reviews
	KindIDE      = "ide"      // detected for information; reviews go through another entry
	KindProvider = "provider" // an API the daemon calls in-process (needs a key)
)

// Entry is the static description of one supported agent.
type Entry struct {
	ID   string
	Name string
	Kind string
	// ConfigAgent is the agent whose settings this entry opens. It is the
	// entry itself for runnable agents, and the CLI that does the work for an
	// IDE (Cursor IDE → Cursor CLI).
	ConfigAgent string
	// Binaries are looked up for IDE entries (agents use the executor's own
	// resolution, so detection matches what a review would run).
	Binaries []string
	// Roots are configuration directories relative to $HOME.
	Roots []string
	// Applications are app bundles (absolute, or relative to $HOME).
	Applications []string
	Homepage     string
	InstallHint  string
	// ModelsArgs lists the CLI's models when non-nil; parseModels reads them.
	ModelsArgs  []string
	parseModels func(string) []string
}

// Catalog is every agent Heimdallm knows, in display order.
var Catalog = []Entry{
	{
		ID: "claude", Name: "Claude Code", Kind: KindAgent, ConfigAgent: "claude",
		Roots:       []string{".claude"},
		Homepage:    "https://docs.claude.com/en/docs/claude-code",
		InstallHint: "npm install -g @anthropic-ai/claude-code",
	},
	{
		ID: "codex", Name: "Codex", Kind: KindAgent, ConfigAgent: "codex",
		Roots:       []string{".codex"},
		Homepage:    "https://github.com/openai/codex",
		InstallHint: "npm install -g @openai/codex",
	},
	{
		ID: "gemini", Name: "Gemini CLI", Kind: KindAgent, ConfigAgent: "gemini",
		Roots:       []string{".gemini"},
		Homepage:    "https://github.com/google-gemini/gemini-cli",
		InstallHint: "npm install -g @google/gemini-cli",
	},
	{
		ID: "cursor", Name: "Cursor IDE", Kind: KindIDE, ConfigAgent: "cursor_cli",
		Binaries:     []string{"cursor"},
		Roots:        []string{".config/Cursor", "Library/Application Support/Cursor"},
		Applications: []string{"/Applications/Cursor.app", "Applications/Cursor.app"},
		Homepage:     "https://cursor.com",
		InstallHint:  "Reviews run through the Cursor CLI (cursor-agent), which uses the same account.",
	},
	{
		ID: "cursor_cli", Name: "Cursor CLI", Kind: KindAgent, ConfigAgent: "cursor_cli",
		Roots:       []string{".cursor"},
		Homepage:    "https://cursor.com/cli",
		InstallHint: "curl https://cursor.com/install -fsS | bash",
		ModelsArgs:  []string{"models"},
		parseModels: parseCursorModels,
	},
	{
		ID: "copilot", Name: "GitHub Copilot CLI", Kind: KindAgent, ConfigAgent: "copilot",
		Roots:       []string{".copilot"},
		Homepage:    "https://github.com/features/copilot/cli",
		InstallHint: "npm install -g @github/copilot",
		ModelsArgs:  []string{"help", "config"},
		parseModels: parseCopilotModels,
	},
	{
		ID: "openrouter", Name: "OpenRouter", Kind: KindProvider, ConfigAgent: "openrouter",
		Homepage:    "https://openrouter.ai",
		InstallHint: "Create a key at https://openrouter.ai/keys and add it on this page, or set OPENROUTER_API_KEY for the daemon.",
	},
	{
		ID: "opencode", Name: "OpenCode", Kind: KindAgent, ConfigAgent: "opencode",
		Roots:       []string{".config/opencode", ".local/share/opencode"},
		Homepage:    "https://opencode.ai",
		InstallHint: "curl -fsSL https://opencode.ai/install | bash",
		ModelsArgs:  []string{"models"},
		parseModels: parseLineModels,
	},
}

// Agent is the detected state of one catalog entry.
type Agent struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	Executable  bool     `json:"executable"`
	Installed   bool     `json:"installed"`
	Configured  bool     `json:"configured"`
	Version     string   `json:"version,omitempty"`
	Path        string   `json:"path,omitempty"`
	ConfigAgent string   `json:"config_agent"`
	Homepage    string   `json:"homepage"`
	InstallHint string   `json:"install_hint"`
	Models      []string `json:"models,omitempty"`
}

// Detector holds every side effect a scan needs, so tests can fake the
// filesystem, PATH and subprocesses.
type Detector struct {
	Home string
	// Resolve returns the executable path of a runnable agent ("" when absent).
	Resolve func(id string) string
	// LookPath finds an IDE launcher on PATH.
	LookPath func(name string) (string, error)
	Stat     func(path string) (os.FileInfo, error)
	Getenv   func(key string) string
	// Run executes path with args and returns its combined output.
	Run func(ctx context.Context, path string, args ...string) (string, error)
	// Provider reports an in-process provider's state (key configured and
	// the models it offers). Nil leaves providers undetected.
	Provider func(ctx context.Context, id string) (configured bool, models []string)
}

// RefreshInterval is how often the daemon rescans the catalog in the
// background, so an agent installed while it runs shows up without a restart.
const RefreshInterval = 10 * time.Minute

// Bounds on the subprocesses a scan starts.
const (
	versionTimeout = 5 * time.Second
	modelsTimeout  = 15 * time.Second
	maxVersionLen  = 80
	maxModels      = 200
	maxModelLen    = 120
	// maxProbeOutput caps what one probe may print; a scan reads at most
	// this much per command.
	maxProbeOutput = 256 << 10
	// versionProbeLines is how many non-empty lines of --version output are
	// searched for a version: some CLIs print a banner or a warning first.
	versionProbeLines = 5
)

// NewDetector returns a Detector wired to the real system.
func NewDetector() Detector {
	home, _ := os.UserHomeDir()
	return Detector{
		Home:     home,
		Resolve:  executor.ResolveCLIPath,
		LookPath: exec.LookPath,
		Stat:     os.Stat,
		Getenv:   os.Getenv,
		Run:      runCommand,
	}
}

// runCommand runs one probe in a process group of its own. Many agents are
// node shims that start children; when the probe times out, or once it
// finishes, the whole group is killed so no child outlives the scan.
func runCommand(ctx context.Context, path string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = executor.CommandEnv(path)
	out := &cappedBuffer{max: maxProbeOutput}
	cmd.Stdout, cmd.Stderr = out, out
	p, err := procgroup.Start(cmd)
	if err != nil {
		return "", err
	}
	err = p.Wait()
	return out.String(), err
}

// cappedBuffer keeps the first max bytes written and discards the rest, so a
// probe that prints megabytes cannot grow the daemon's memory.
type cappedBuffer struct {
	strings.Builder
	max int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.Len(); room > 0 {
		if len(p) > room {
			b.Builder.Write(p[:room])
		} else {
			b.Builder.Write(p)
		}
	}
	return len(p), nil
}

// Scan detects every catalog entry. Version and model probes run in parallel
// and are bounded, so a hung CLI cannot stall the scan.
func (d Detector) Scan(ctx context.Context) []Agent {
	out := make([]Agent, len(Catalog))
	var wg sync.WaitGroup
	for i, e := range Catalog {
		wg.Add(1)
		go func(i int, e Entry) {
			defer wg.Done()
			out[i] = d.scanOne(ctx, e)
		}(i, e)
	}
	wg.Wait()
	return out
}

func (d Detector) scanOne(ctx context.Context, e Entry) Agent {
	a := Agent{
		ID: e.ID, Name: e.Name, Kind: e.Kind, ConfigAgent: e.ConfigAgent,
		Homepage: e.Homepage, InstallHint: e.InstallHint,
		Executable: e.Kind != KindIDE,
	}
	if e.Kind == KindProvider {
		if d.Provider != nil {
			configured, models := d.Provider(ctx, e.ID)
			a.Installed, a.Configured = configured, configured
			if configured {
				a.Models = capModels(models)
			}
		}
		return a
	}
	path := d.binaryPath(e)
	if path != "" {
		a.Installed = true
		a.Path = d.displayPath(path)
	}
	for _, app := range e.Applications {
		if !filepath.IsAbs(app) {
			app = filepath.Join(d.Home, app)
		}
		if _, err := d.Stat(app); err == nil {
			a.Installed = true
		}
	}
	for _, root := range d.roots(e) {
		if _, err := d.Stat(root); err == nil {
			a.Configured = true
			break
		}
	}
	// IDE entries are informational (reviews go through ConfigAgent), so
	// their launcher is not run: its version cannot run a review.
	if path != "" && d.Run != nil && e.Kind != KindIDE {
		a.Version = d.version(ctx, path)
		if e.ModelsArgs != nil && e.parseModels != nil {
			a.Models = d.models(ctx, path, e)
		}
	}
	return a
}

func (d Detector) binaryPath(e Entry) string {
	if e.Kind == KindAgent {
		if d.Resolve == nil {
			return ""
		}
		return d.Resolve(e.ID)
	}
	if d.LookPath == nil {
		return ""
	}
	for _, bin := range e.Binaries {
		if p, err := d.LookPath(bin); err == nil && p != "" {
			return p
		}
	}
	return ""
}

// displayPath abbreviates $HOME so the API does not echo the account name in
// every path.
func (d Detector) displayPath(path string) string {
	if d.Home != "" && strings.HasPrefix(path, d.Home+string(filepath.Separator)) {
		return "~" + path[len(d.Home):]
	}
	return path
}

func (d Detector) roots(e Entry) []string {
	getenv := d.Getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	switch e.ID {
	case "codex":
		if root := getenv("CODEX_HOME"); root != "" {
			return []string{root}
		}
	case "copilot":
		if root := getenv("COPILOT_HOME"); root != "" {
			return []string{root}
		}
	case "gemini":
		if home := getenv("GEMINI_CLI_HOME"); home != "" {
			return []string{filepath.Join(home, ".gemini")}
		}
	case "opencode":
		if data := getenv("XDG_DATA_HOME"); data != "" {
			return []string{filepath.Join(data, "opencode")}
		}
	}
	out := make([]string, 0, len(e.Roots))
	for _, r := range e.Roots {
		out = append(out, filepath.Join(d.Home, r))
	}
	return out
}

var versionPattern = regexp.MustCompile(`\d+(?:\.\d+)+(?:[-+.][0-9A-Za-z.-]+)?`)

func (d Detector) version(ctx context.Context, path string) string {
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	out, err := d.Run(ctx, path, "--version")
	if err != nil {
		return ""
	}
	return parseVersion(out)
}

// parseVersion pulls the version number out of a CLI's --version output
// ("2.1.292 (Claude Code)", "codex-cli 0.161.0", "GitHub Copilot CLI 1.0.88.").
// The first versionProbeLines non-empty lines are searched, since a banner or
// a runtime warning can come first. Output without a version number (an
// error or a login prompt) is no version.
func parseVersion(out string) string {
	seen := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if v := strings.TrimRight(versionPattern.FindString(line), "."); v != "" {
			if len(v) > maxVersionLen {
				// The pattern only matches ASCII, so a byte cut is a rune cut.
				v = v[:maxVersionLen]
			}
			return v
		}
		if seen++; seen >= versionProbeLines {
			break
		}
	}
	return ""
}

func (d Detector) models(ctx context.Context, path string, e Entry) []string {
	ctx, cancel := context.WithTimeout(ctx, modelsTimeout)
	defer cancel()
	out, err := d.Run(ctx, path, e.ModelsArgs...)
	if err != nil {
		return nil
	}
	return capModels(e.parseModels(out))
}

var modelIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/~\[\]=,@+-]*$`)

// capModels keeps well-formed, distinct ids, bounded in count and length.
func capModels(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range in {
		m = strings.TrimSpace(m)
		if m == "" || len(m) > maxModelLen || !modelIDPattern.MatchString(m) || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
		if len(out) >= maxModels {
			break
		}
	}
	return out
}

// parseCursorModels reads `cursor-agent models`: "id - Label" per line.
func parseCursorModels(out string) []string {
	var ids []string
	for _, line := range strings.Split(out, "\n") {
		id, _, ok := strings.Cut(strings.TrimSpace(line), " - ")
		if ok {
			ids = append(ids, id)
		}
	}
	return ids
}

// parseCopilotModels reads the quoted values listed under `model` in
// `copilot help config`.
func parseCopilotModels(out string) []string {
	var ids []string
	in := false
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "`model`:"):
			in = true
		case in && strings.HasPrefix(trimmed, `- "`):
			ids = append(ids, strings.Trim(strings.TrimPrefix(trimmed, "- "), `"`))
		case in && trimmed != "":
			return ids
		}
	}
	return ids
}

// parseLineModels reads one model id per line (`opencode models`).
func parseLineModels(out string) []string {
	return strings.Split(out, "\n")
}

// Store keeps the latest scan for the HTTP API and refreshes it on demand.
type Store struct {
	detector Detector

	mu        sync.RWMutex
	agents    []Agent
	scannedAt time.Time
	scans     uint64 // completed scans; guarded by mu
	scanning  sync.Mutex
}

// NewStore returns an empty store; call Refresh to populate it.
func NewStore(d Detector) *Store { return &Store{detector: d} }

// Refresh rescans the machine. Concurrent calls coalesce: a call that waited
// for another scan to finish returns that scan instead of starting its own.
// A scan whose ctx ended midway (a client that gave up) is not stored, since
// its probes were cut short; the previous result stays.
func (s *Store) Refresh(ctx context.Context) []Agent {
	s.mu.RLock()
	before := s.scans
	s.mu.RUnlock()
	s.scanning.Lock()
	defer s.scanning.Unlock()
	s.mu.RLock()
	if s.scans != before {
		agents := s.agents
		s.mu.RUnlock()
		return agents
	}
	s.mu.RUnlock()
	agents := s.detector.Scan(ctx)
	if ctx.Err() != nil {
		return agents
	}
	s.mu.Lock()
	s.agents, s.scannedAt = agents, time.Now().UTC()
	s.scans++
	s.mu.Unlock()
	return agents
}

// Snapshot returns the last scan, scanning first if none has run.
func (s *Store) Snapshot(ctx context.Context) ([]Agent, time.Time) {
	s.mu.RLock()
	agents, at := s.agents, s.scannedAt
	s.mu.RUnlock()
	if at.IsZero() {
		agents = s.Refresh(ctx)
		s.mu.RLock()
		at = s.scannedAt
		s.mu.RUnlock()
	}
	return append([]Agent(nil), agents...), at
}

// Get returns one agent from the last scan.
func (s *Store) Get(ctx context.Context, id string) (Agent, bool) {
	agents, _ := s.Snapshot(ctx)
	for _, a := range agents {
		if a.ID == id {
			return a, true
		}
	}
	return Agent{}, false
}

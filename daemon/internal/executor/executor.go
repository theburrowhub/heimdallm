package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/heimdallm/daemon/internal/procgroup"
)

const executionTimeout = 5 * time.Minute
const cliHelpTimeout = 2 * time.Second

// ReviewResult is the parsed JSON response from the AI CLI.
type ReviewResult struct {
	Summary  string  `json:"summary"`
	Issues   []Issue `json:"issues"`
	Severity string  `json:"severity"`
}

// Issue represents a single code issue found by the AI reviewer.
type Issue struct {
	File        string `json:"file"`
	Line        int    `json:"line"`
	Description string `json:"description"`
	Severity    string `json:"severity"`
}

// ExecOptions controls how the AI CLI is invoked.
type ExecOptions struct {
	// Model sets --model <value> for CLIs that support it.
	Model string
	// MaxTurns sets --max-turns <n> for Claude (0 = not set).
	MaxTurns int
	// ApprovalMode sets the typed Codex/Gemini approval option.
	// Legacy values from older Codex CLIs are still accepted and normalized.
	ApprovalMode string
	// ExtraFlags is a free-form string of additional CLI flags (split on spaces).
	ExtraFlags string
	// WorkDir is the working directory for the CLI process.
	// When set, the CLI runs inside the local repo directory, giving it
	// access to all project files for deeper analysis (missing tests, side effects, etc.).
	WorkDir string

	// Claude-specific flags
	Effort         string // --effort low|medium|high|max
	PermissionMode string // --permission-mode <value> — must pass ValidatePermissionMode (bypassPermissions is blocked)
	Bare           bool   // --bare
	// DangerouslySkipPerms enables --dangerously-skip-permissions.
	// SECURITY (M-5): This field MUST NOT be exposed via the HTTP API or
	// deserialized from agent JSON requests. It is only set from config.toml
	// (CLIAgentConfig.DangerouslySkipPerms) which requires local file access.
	DangerouslySkipPerms bool // --dangerously-skip-permissions
	NoSessionPersistence bool // --no-session-persistence

	// Timeout overrides the default execution timeout for the CLI process.
	// Zero = use default (5 minutes).
	Timeout time.Duration
}

// OptionsForSelectedCLI removes provider-specific options when Detect falls
// back to a different CLI. The options were resolved from the configured
// primary agent, so forwarding its model, approval mode or free-form flags to
// another provider is both unreliable and unsafe. WorkDir and Timeout are
// provider-independent and remain in force.
func OptionsForSelectedCLI(primary, selected string, opts ExecOptions) ExecOptions {
	primary = strings.TrimSpace(primary)
	selected = strings.TrimSpace(selected)
	if primary == "" || selected == "" || primary == selected {
		return opts
	}
	opts.Model = ""
	opts.MaxTurns = 0
	opts.ApprovalMode = ""
	opts.ExtraFlags = ""
	opts.Effort = ""
	opts.PermissionMode = ""
	opts.Bare = false
	opts.DangerouslySkipPerms = false
	opts.NoSessionPersistence = false
	return opts
}

// Executor runs AI CLI tools for code review.
type Executor struct {
	// groupsMu guards inFlightGroups. Each handle owns a sentinel-led process
	// group whose PGID remains reserved through TERM/KILL cleanup, so the
	// shutdown path can sweep in-flight agents without a recycled-PGID race.
	groupsMu       sync.Mutex
	inFlightGroups map[int]*procgroup.Process
}

// New creates a new Executor.
func New() *Executor {
	return &Executor{}
}

// trackGroup registers a running execution's owned process group.
func (e *Executor) trackGroup(process *procgroup.Process) {
	e.groupsMu.Lock()
	defer e.groupsMu.Unlock()
	if e.inFlightGroups == nil {
		e.inFlightGroups = make(map[int]*procgroup.Process)
	}
	e.inFlightGroups[process.ID()] = process
}

// untrackGroup forgets an execution's process group once Wait has returned.
func (e *Executor) untrackGroup(pgid int) {
	e.groupsMu.Lock()
	defer e.groupsMu.Unlock()
	delete(e.inFlightGroups, pgid)
}

// TerminateAll ends every execution this Executor still has in flight, SIGTERM
// first and SIGKILL after a grace period. The daemon's shutdown path must call
// it: ExecuteRaw derives its context from context.Background() rather than from
// the SIGINT/SIGTERM handler, and each execution runs in its own process group,
// so nothing else would reach an in-flight agent. Without this a restart leaves
// agents running and spending provider quota — the #614 symptom. Safe to call
// when nothing is running.
func (e *Executor) TerminateAll() {
	e.groupsMu.Lock()
	groups := make([]*procgroup.Process, 0, len(e.inFlightGroups))
	for _, process := range e.inFlightGroups {
		groups = append(groups, process)
	}
	e.groupsMu.Unlock()

	if len(groups) == 0 {
		return
	}
	slog.Info("executor: terminating in-flight executions", "groups", len(groups))
	// Terminate is idempotent and keeps the sentinel unreaped while escalation
	// is pending. A process concurrently completing can therefore never turn a
	// snapshot entry into a signal aimed at an unrelated, recycled PGID.
	for _, process := range groups {
		if err := process.Terminate(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			slog.Debug("executor: terminate process group", "pgid", process.ID(), "err", err)
		}
	}
	time.Sleep(procgroup.TermGrace)
	for _, process := range groups {
		if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			slog.Debug("executor: kill process group", "pgid", process.ID(), "err", err)
		}
	}
}

// Detect returns the first available CLI (primary → fallback).
// Also checks the user's login shell environment to handle cases where the
// daemon is launched from a GUI app without inheriting the full shell PATH
// (e.g., Homebrew tools at /opt/homebrew/bin not in process PATH).
//
// SECURITY: Detect validates each name against the CLI allowlist before
// resolving it, preventing shell injection (issue #2).
func (e *Executor) Detect(primary, fallback string) (string, error) {
	for _, name := range []string{primary, fallback} {
		if name == "" {
			continue
		}
		if err := validateCLIName(name); err != nil {
			return "", err // reject unknown / potentially-injected names early
		}
		if resolveCLIPath(name) != "" {
			return name, nil
		}
	}
	return "", fmt.Errorf("executor: no AI CLI available (tried %q, %q)", primary, fallback)
}

// allowedCLIs is the strict allowlist of AI CLI names Heimdallm supports.
// Any value not in this set is rejected before reaching resolveCLIPath,
// preventing shell injection via a crafted ai.primary / ai.fallback config value.
var allowedCLIs = map[string]struct{}{
	"claude":   {},
	"gemini":   {},
	"codex":    {},
	"opencode": {},
}

// allowedPermissionModes is the strict allowlist for the --permission-mode flag.
// "bypassPermissions" is intentionally excluded — it grants unrestricted filesystem
// access and must never be passed to the claude CLI from Heimdallm config.
var canonicalPermissionModes = map[string]string{
	"default":     "default",
	"auto":        "auto",
	"acceptedits": "acceptEdits",
	"dontask":     "dontAsk",
}

var allowedEfforts = map[string]struct{}{
	"low":    {},
	"medium": {},
	"high":   {},
	"max":    {},
}

// ValidateModel rejects option-shaped model identifiers. ExecOptions values
// are passed as separate argv entries, but many CLI parsers reinterpret a
// value beginning with "-" as the next option when the preceding flag expects
// a value. That would turn a typed model field into another policy bypass.
func ValidateModel(model string) error {
	if model == "" {
		return nil
	}
	if strings.HasPrefix(strings.TrimSpace(model), "-") {
		return fmt.Errorf("executor: model %q must not begin with '-'", model)
	}
	return nil
}

// NormalizeEffort returns the canonical lowercase spelling of a safe effort
// value. Accepting harmless case variants keeps older hand-written TOML files
// working without widening the enum.
func NormalizeEffort(effort string) (string, error) {
	effort = strings.ToLower(strings.TrimSpace(effort))
	if effort == "" {
		return "", nil
	}
	if _, ok := allowedEfforts[effort]; !ok {
		return "", fmt.Errorf("executor: effort %q is not allowed — valid values: low, medium, high, max", effort)
	}
	return effort, nil
}

// ValidateEffort validates Claude's typed --effort value. Keeping this field
// enum-shaped prevents it from being interpreted as another CLI option.
func ValidateEffort(effort string) error {
	_, err := NormalizeEffort(effort)
	return err
}

// allowedApprovalModes is the allowlist for the Codex approval mode config.
// The first group is the current Codex --ask-for-approval vocabulary; the
// second group is kept for existing config.toml files created before Codex
// switched from --approval-mode to --ask-for-approval.
var allowedApprovalModes = map[string]struct{}{
	"untrusted":  {},
	"on-failure": {},
	"on-request": {},
	"never":      {},
	"auto-edit":  {},
	"full-auto":  {},
	"suggest":    {},
}

// allowedGeminiApprovalModes is deliberately narrower than Gemini's full
// --approval-mode vocabulary: yolo removes every approval prompt and therefore
// must never be reachable through Heimdallm's typed agent configuration.
var allowedGeminiApprovalModes = map[string]struct{}{
	"default":   {},
	"auto_edit": {},
	"plan":      {},
}

// ValidatePermissionMode returns an error if mode is not in the allowlist.
// An empty string is accepted (means "not set").
func NormalizePermissionMode(mode string) (string, error) {
	trimmed := strings.TrimSpace(mode)
	if trimmed == "" {
		return "", nil
	}
	normalized, ok := canonicalPermissionModes[strings.ToLower(trimmed)]
	if !ok {
		return "", fmt.Errorf("executor: permission_mode %q is not allowed — valid values: default, auto, acceptEdits, dontAsk", mode)
	}
	return normalized, nil
}

func ValidatePermissionMode(mode string) error {
	_, err := NormalizePermissionMode(mode)
	return err
}

// ValidateApprovalMode returns an error if mode is not in the allowlist.
// An empty string is accepted (means "not set").
func ValidateApprovalMode(mode string) error {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return nil
	}
	if _, ok := allowedApprovalModes[mode]; !ok {
		return fmt.Errorf("executor: approval_mode %q is not allowed — valid values: untrusted, on-failure, on-request, never, auto-edit, full-auto, suggest", mode)
	}
	return nil
}

// NormalizeApprovalModeForCLI validates a typed approval mode and returns the
// canonical value emitted for the selected provider. Codex keeps accepting its
// legacy values for backwards compatibility. Gemini accepts auto-edit as a
// friendly spelling but emits auto_edit, and intentionally rejects yolo.
//
// Claude and OpenCode do not consume ApprovalMode today. We still validate a
// non-empty value against the safe Codex/Gemini union so a primary-to-fallback
// transition does not fail merely because the selected fallback ignores a
// setting belonging to the primary CLI.
func NormalizeApprovalModeForCLI(cli, mode string) (string, error) {
	if err := ValidateCLIName(cli); err != nil {
		return "", err
	}

	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return "", nil
	}

	switch cli {
	case "codex":
		if err := ValidateApprovalMode(mode); err != nil {
			return "", err
		}
		return normalizeCodexApprovalMode(mode), nil
	case "gemini":
		normalized := normalizeGeminiApprovalMode(mode)
		if _, ok := allowedGeminiApprovalModes[normalized]; !ok {
			return "", fmt.Errorf("executor: approval_mode %q is not allowed for gemini — valid values: default, auto_edit, auto-edit, plan", mode)
		}
		return normalized, nil
	default:
		if _, ok := allowedApprovalModes[mode]; ok {
			return normalizeCodexApprovalMode(mode), nil
		}
		normalized := normalizeGeminiApprovalMode(mode)
		if _, ok := allowedGeminiApprovalModes[normalized]; ok {
			return normalized, nil
		}
		return "", fmt.Errorf("executor: approval_mode %q is not a known safe mode for %s", mode, cli)
	}
}

// ValidateApprovalModeForCLI is the validation-only wrapper used by callers
// that do not need the canonical spelling.
func ValidateApprovalModeForCLI(cli, mode string) error {
	_, err := NormalizeApprovalModeForCLI(cli, mode)
	return err
}

func normalizeCodexApprovalMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "full-auto":
		return "never"
	case "auto-edit":
		return "on-request"
	case "suggest":
		return "untrusted"
	default:
		return strings.TrimSpace(mode)
	}
}

func normalizeGeminiApprovalMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "auto-edit":
		return "auto_edit"
	default:
		return strings.ToLower(strings.TrimSpace(mode))
	}
}

// MigrateLegacyTypedExtraFlagsForCLI promotes the model/effort/turn-limit
// flags that older releases allowed in ExtraFlags into their typed fields.
// It exists only for trusted TOML/env/store compatibility. New HTTP writes and
// the subprocess boundary reject these aliases in ExtraFlags so a trailing
// free-form option can never override a validated typed value.
//
// Explicit typed values win over their legacy duplicates. The returned field
// names describe the aliases removed from ExtraFlags so callers can emit an
// actionable migration warning.
func MigrateLegacyTypedExtraFlagsForCLI(cli string, opts ExecOptions) (ExecOptions, []string) {
	if err := ValidateCLIName(cli); err != nil || strings.TrimSpace(opts.ExtraFlags) == "" {
		return opts, nil
	}

	parts := strings.Fields(opts.ExtraFlags)
	kept := make([]string, 0, len(parts))
	var migrated []string
	modelFromLegacy := strings.TrimSpace(opts.Model) == ""
	effortFromLegacy := strings.TrimSpace(opts.Effort) == ""
	maxTurnsFromLegacy := opts.MaxTurns == 0

	recordMigration := func(field string) {
		for _, existing := range migrated {
			if existing == field {
				return
			}
		}
		migrated = append(migrated, field)
	}

	for i := 0; i < len(parts); i++ {
		field, value, consumed, matched := legacyTypedExtraFlag(cli, parts, i)
		if !matched {
			kept = append(kept, parts[i])
			continue
		}

		valid := false
		switch field {
		case "model":
			value = strings.TrimSpace(value)
			if err := ValidateModel(value); err == nil && value != "" {
				if modelFromLegacy {
					opts.Model = value // last legacy occurrence keeps CLI precedence
				}
				valid = true
			}
		case "effort":
			if normalized, err := NormalizeEffort(value); err == nil && normalized != "" {
				if effortFromLegacy {
					opts.Effort = normalized
				}
				valid = true
			}
		case "max_turns":
			if turns, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && turns >= 0 {
				if maxTurnsFromLegacy {
					opts.MaxTurns = turns
				}
				valid = true
			}
		}
		if !valid {
			// Preserve the invalid spelling so the strict policy below can
			// reject it and the compatibility sanitizer can report it.
			kept = append(kept, parts[i])
			continue
		}

		recordMigration(field)
		i += consumed
	}

	opts.ExtraFlags = strings.Join(kept, " ")
	return opts, migrated
}

// NormalizeLegacyCLIFlagsForCLI converts the typed aliases historically
// accepted by stored prompt profiles into ExecOptions, then validates the
// remaining free-form flags with the current provider policy. Callers must pass
// the returned ExtraFlags rather than the original string so typed aliases
// never reach the subprocess twice or override typed options by argv order.
//
// This compatibility helper is intentionally narrower than the general config
// sanitizer: prompt profiles only have a CLIFlags string today, so model,
// effort and max-turns have no other persisted representation there.
func NormalizeLegacyCLIFlagsForCLI(cli, flags string) (ExecOptions, []string, error) {
	opts, migrated := MigrateLegacyTypedExtraFlagsForCLI(cli, ExecOptions{ExtraFlags: flags})
	if err := ValidateCLIName(cli); err != nil {
		return ExecOptions{}, nil, err
	}
	if err := ValidateModel(opts.Model); err != nil {
		return ExecOptions{}, nil, err
	}
	if opts.MaxTurns < 0 {
		return ExecOptions{}, nil, fmt.Errorf("executor: max_turns must be non-negative")
	}
	if err := ValidateEffort(opts.Effort); err != nil {
		return ExecOptions{}, nil, err
	}
	if err := ValidateExtraFlagsForCLI(cli, opts.ExtraFlags); err != nil {
		return ExecOptions{}, nil, err
	}
	return opts, migrated, nil
}

func legacyTypedExtraFlag(cli string, parts []string, index int) (field, value string, consumed int, matched bool) {
	token := parts[index]
	name := canonicalFlagName(token)

	if strings.HasPrefix(name, "--") {
		switch comparableLongFlag(name) {
		case "model":
			field = "model"
		case "effort":
			if cli != "claude" {
				return "", "", 0, false
			}
			field = "effort"
		case "maxturns":
			if cli != "claude" {
				return "", "", 0, false
			}
			field = "max_turns"
		default:
			return "", "", 0, false
		}
	} else if strings.HasPrefix(name, "-m") {
		field = "model"
	} else {
		return "", "", 0, false
	}

	if equals := strings.IndexByte(token, '='); equals >= 0 {
		return field, token[equals+1:], 0, true
	}
	if strings.HasPrefix(name, "-m") && name != "-m" {
		return field, token[2:], 0, true
	}
	if index+1 < len(parts) && !strings.HasPrefix(parts[index+1], "-") {
		return field, parts[index+1], 1, true
	}
	return field, "", 0, true
}

// forbiddenExtraFlagsByCLI defines the policy-changing aliases understood by
// each supported provider. ExtraFlags is appended after Heimdallm's typed
// options, so allowing any of these names would let legacy config or a stored
// agent replace the validated approval, sandbox, permission, config, or
// workspace policy at the last possible moment.
//
// Long names are stored lowercase because validation is case-insensitive.
// Short names are lowercase too: this intentionally treats Codex -C (workdir)
// and -c (config override) as the same forbidden class.
var forbiddenExtraFlagsByCLI = map[string]map[string]string{
	"claude": {
		"-c":                                   "session state",
		"-m":                                   "typed model configuration",
		"-r":                                   "session state",
		"-w":                                   "working-directory access",
		"--add-dir":                            "working-directory access",
		"--agent":                              "agent configuration",
		"--agents":                             "agent configuration",
		"--allow-dangerously-skip-permissions": "permission policy",
		"--allowed-tools":                      "permission policy",
		"--allowedtools":                       "permission policy",
		"--append-system-prompt":               "system instructions",
		"--append-system-prompt-file":          "working-directory file access",
		"--background":                         "execution boundary",
		"--bg":                                 "execution boundary",
		"--channels":                           "runtime configuration",
		"--chrome":                             "execution boundary",
		"--cloud":                              "execution boundary",
		"--continue":                           "session state",
		"--dangerously-load-development-channels": "permission policy",
		"--dangerously-skip-permissions":          "permission policy",
		"--debug-file":                            "working-directory file access",
		"--directory":                             "working-directory access",
		"--enable-auto-mode":                      "permission policy",
		"--exec":                                  "execution boundary",
		"--from-pr":                               "session state",
		"--ide":                                   "execution boundary",
		"--init":                                  "hook execution",
		"--init-only":                             "hook execution",
		"--maintenance":                           "hook execution",
		"--max-turns":                             "typed turn-limit configuration",
		"--mcp-config":                            "runtime configuration",
		"--model":                                 "typed model configuration",
		"--no-sandbox":                            "sandbox policy",
		"--permission-mode":                       "permission policy",
		"--permission-prompt-tool":                "permission policy",
		"--plugin-dir":                            "runtime configuration",
		"--plugin-url":                            "runtime configuration",
		"--rc":                                    "execution boundary",
		"--remote":                                "execution boundary",
		"--remote-control":                        "execution boundary",
		"--resume":                                "session state",
		"--session-id":                            "session state",
		"--setting-sources":                       "runtime configuration",
		"--settings":                              "runtime configuration",
		"--system-prompt":                         "system instructions",
		"--system-prompt-file":                    "working-directory file access",
		"--teleport":                              "execution boundary",
		"--worktree":                              "working-directory access",
		"--effort":                                "typed effort configuration",
	},
	"codex": {
		"-a":                 "approval policy",
		"-c":                 "runtime configuration or working directory",
		"-i":                 "working-directory file access",
		"-m":                 "typed model configuration",
		"-o":                 "working-directory file access",
		"-p":                 "runtime configuration",
		"-s":                 "sandbox policy",
		"--add-dir":          "working-directory access",
		"--approval-mode":    "approval policy",
		"--ask-for-approval": "approval policy",
		"--cd":               "working-directory access",
		"--config":           "runtime configuration",
		"--cwd":              "working-directory access",
		"--dangerously-bypass-approvals-and-sandbox": "approval and sandbox policy",
		"--dangerously-bypass-hook-trust":            "permission policy",
		"--disable":                                  "runtime configuration",
		"--enable":                                   "runtime configuration",
		"--full-auto":                                "approval and sandbox policy",
		"--ignore-rules":                             "permission policy",
		"--ignore-user-config":                       "runtime configuration",
		"--image":                                    "working-directory file access",
		"--include-managed-config":                   "runtime configuration",
		"--local-provider":                           "execution boundary",
		"--model":                                    "typed model configuration",
		"--no-sandbox":                               "sandbox policy",
		"--oss":                                      "execution boundary",
		"--output-last-message":                      "working-directory file access",
		"--output-schema":                            "working-directory file access",
		"--permission-profile":                       "permission policy",
		"--profile":                                  "runtime configuration",
		"--remote":                                   "execution boundary",
		"--remote-auth-token-env":                    "execution boundary",
		"--sandbox":                                  "sandbox policy",
		"--search":                                   "permission policy",
		"--skip-git-repo-check":                      "workspace trust policy",
		"--yolo":                                     "approval and sandbox policy",
	},
	"gemini": {
		"-r":                         "session state",
		"-e":                         "runtime configuration",
		"-m":                         "typed model configuration",
		"-s":                         "sandbox policy",
		"-w":                         "working-directory access",
		"-y":                         "approval policy",
		"--acp":                      "execution boundary",
		"--admin-policy":             "permission policy",
		"--allowed-mcp-server-names": "permission policy",
		"--allowed-tools":            "permission policy",
		"--approval-mode":            "approval policy",
		"--config":                   "runtime configuration",
		"--cwd":                      "working-directory access",
		"--extensions":               "runtime configuration",
		"--experimental-acp":         "execution boundary",
		"--fake-responses":           "working-directory file access",
		"--include-directories":      "working-directory access",
		"--include-directory":        "working-directory access",
		"--model":                    "typed model configuration",
		"--no-sandbox":               "sandbox policy",
		"--policy":                   "permission policy",
		"--record-responses":         "working-directory file access",
		"--resume":                   "session state",
		"--sandbox":                  "sandbox policy",
		"--session-file":             "working-directory file access",
		"--settings":                 "runtime configuration",
		"--skip-trust":               "workspace trust policy",
		"--worktree":                 "working-directory access",
		"--yolo":                     "approval policy",
	},
	"opencode": {
		"-c":            "session state",
		"-m":            "typed model configuration",
		"--agent":       "agent and permission configuration",
		"--attach":      "execution boundary",
		"--auto":        "approval policy",
		"--command":     "runtime configuration",
		"--continue":    "session state",
		"--config":      "runtime configuration",
		"--dir":         "working-directory access",
		"--file":        "working-directory access",
		"--fork":        "session state",
		"--interactive": "execution boundary",
		"--model":       "typed model configuration",
		"--no-sandbox":  "sandbox policy",
		"--password":    "execution boundary",
		"--permission":  "permission policy",
		"--permissions": "permission policy",
		"--port":        "execution boundary",
		"--sandbox":     "sandbox policy",
		"--session":     "session state",
		"--settings":    "runtime configuration",
		"--share":       "external data sharing",
		"--username":    "execution boundary",
		"-f":            "working-directory access",
		"-i":            "execution boundary",
		"-p":            "execution boundary",
		"-s":            "session state",
		"-u":            "execution boundary",
	},
}

type extraFlagArity uint8

const (
	extraFlagBoolean extraFlagArity = iota
	extraFlagValue
	extraFlagOptionalValue
	extraFlagVariadicValue
)

// allowedExtraFlagsByCLI is the deliberate compatibility surface for the
// free-form field. Unknown flags fail closed: upstream CLIs regularly add
// options that can load files, select agents/config, resume sessions, or move
// execution outside Heimdallm's validated workspace.
//
// Keep this list limited to output, model/resource tuning, accessibility, and
// options that only remove capabilities. Security-sensitive behavior belongs
// in typed ExecOptions fields instead.
var allowedExtraFlagsByCLI = map[string]map[string]extraFlagArity{
	"claude": {
		"-d":                         extraFlagBoolean,
		"--debug":                    extraFlagOptionalValue,
		"--disable-slash-commands":   extraFlagBoolean,
		"--disallowed-tools":         extraFlagVariadicValue,
		"--fallback-model":           extraFlagValue,
		"--include-partial-messages": extraFlagBoolean,
		"--input-format":             extraFlagValue,
		"--json-schema":              extraFlagValue,
		"--max-budget-usd":           extraFlagValue,
		"--no-session-persistence":   extraFlagBoolean,
		"--output-format":            extraFlagValue,
		"--replay-user-messages":     extraFlagBoolean,
		"--strict-mcp-config":        extraFlagBoolean,
		"--verbose":                  extraFlagBoolean,
	},
	"codex": {
		"--color":         extraFlagValue,
		"--ephemeral":     extraFlagBoolean,
		"--json":          extraFlagBoolean,
		"--strict-config": extraFlagBoolean,
	},
	"gemini": {
		"-d":              extraFlagBoolean,
		"-o":              extraFlagValue,
		"--debug":         extraFlagBoolean,
		"--output-format": extraFlagValue,
		"--screen-reader": extraFlagBoolean,
	},
	"opencode": {
		"--format":   extraFlagValue,
		"--pure":     extraFlagBoolean,
		"--thinking": extraFlagBoolean,
		"--variant":  extraFlagValue,
	},
}

// dangerousFlagValues are flag values that must never appear regardless of position.
var dangerousFlagValues = []string{"bypassPermissions"}

// ValidateExtraFlags rejects provider-independent dangerous free-form flags.
// It is kept for callers that do not yet have a CLI name. Long aliases from
// every provider are rejected, while provider-specific short aliases are left
// to ValidateExtraFlagsForCLI.
func ValidateExtraFlags(flags string) error {
	return validateExtraFlags("", flags)
}

// ValidateExtraFlagsForCLI applies the provider-aware ExtraFlags policy. This
// is the validation used at the ExecuteRaw process boundary.
func ValidateExtraFlagsForCLI(cli, flags string) error {
	if err := ValidateCLIName(cli); err != nil {
		return err
	}
	return validateExtraFlags(cli, flags)
}

func validateExtraFlags(cli, flags string) error {
	parts := strings.Fields(flags)
	for i := 0; i < len(parts); i++ {
		part := parts[i]
		flag := canonicalFlagName(part)
		if flag != "" {
			if strings.HasPrefix(flag, "--danger") || strings.HasPrefix(flag, "--allow-dangerously") {
				return forbiddenExtraFlagError(cli, part, "permission or sandbox policy")
			}
			if category, forbidden := forbiddenExtraFlagCategory(cli, flag); forbidden {
				return forbiddenExtraFlagError(cli, part, category)
			}
		}
		if err := validateExtraFlagValue(part); err != nil {
			return err
		}

		// The legacy provider-independent helper can only reject the union of
		// known dangerous long flags. Every execution path supplies a CLI and
		// therefore reaches the fail-closed parser below.
		if cli == "" {
			continue
		}
		if flag == "" {
			return fmt.Errorf("executor: unexpected positional value %q in ExtraFlags/CLIFlags for %s", part, cli)
		}

		consumed, err := validateAllowedExtraFlag(cli, parts, i)
		if err != nil {
			return err
		}
		i += consumed
	}
	return nil
}

func validateExtraFlagValue(part string) error {
	values := []string{part}
	if idx := strings.IndexByte(part, '='); idx >= 0 {
		values = append(values, part[idx+1:])
	}
	for _, value := range values {
		for _, badVal := range dangerousFlagValues {
			if strings.EqualFold(value, badVal) {
				return fmt.Errorf("executor: value %q is forbidden in ExtraFlags/CLIFlags", part)
			}
		}
	}
	return nil
}

func canonicalFlagName(token string) string {
	if len(token) < 2 || token[0] != '-' {
		return ""
	}
	if idx := strings.IndexByte(token, '='); idx >= 0 {
		token = token[:idx]
	}
	return strings.ToLower(token)
}

func validateAllowedExtraFlag(cli string, parts []string, index int) (int, error) {
	token := parts[index]
	name := canonicalFlagName(token)
	if strings.HasPrefix(name, "--") {
		arity, ok := allowedLongExtraFlag(cli, name)
		if !ok {
			return 0, unsupportedExtraFlagError(cli, token)
		}
		hasInlineValue := strings.Contains(token, "=")
		return consumeExtraFlagValues(cli, token, arity, hasInlineValue, parts, index)
	}

	return validateAllowedShortExtraFlags(cli, token, parts, index)
}

func allowedLongExtraFlag(cli, flag string) (extraFlagArity, bool) {
	policy := allowedExtraFlagsByCLI[cli]
	if arity, ok := policy[flag]; ok {
		return arity, true
	}
	comparable := comparableLongFlag(flag)
	for alias, arity := range policy {
		if strings.HasPrefix(alias, "--") && comparableLongFlag(alias) == comparable {
			return arity, true
		}
	}
	return 0, false
}

func validateAllowedShortExtraFlags(cli, token string, parts []string, index int) (int, error) {
	nameAndValue := token
	hasEqualsValue := false
	if equals := strings.IndexByte(nameAndValue, '='); equals >= 0 {
		nameAndValue = nameAndValue[:equals]
		hasEqualsValue = true
	}
	if len(nameAndValue) < 2 {
		return 0, unsupportedExtraFlagError(cli, token)
	}

	policy := allowedExtraFlagsByCLI[cli]
	for pos := 1; pos < len(nameAndValue); pos++ {
		alias := "-" + strings.ToLower(nameAndValue[pos:pos+1])
		if category, forbidden := forbiddenExtraFlagCategory(cli, alias); forbidden {
			return 0, forbiddenExtraFlagError(cli, token, category)
		}
		arity, allowed := policy[alias]
		if !allowed {
			return 0, unsupportedExtraFlagError(cli, token)
		}
		if arity == extraFlagBoolean {
			if hasEqualsValue {
				return 0, fmt.Errorf("executor: boolean flag %q cannot use an attached value in ExtraFlags/CLIFlags for %s", token, cli)
			}
			continue
		}

		hasAttachedValue := pos+1 < len(nameAndValue) || hasEqualsValue
		return consumeExtraFlagValues(cli, token, arity, hasAttachedValue, parts, index)
	}
	return 0, nil
}

func consumeExtraFlagValues(cli, token string, arity extraFlagArity, hasInlineValue bool, parts []string, index int) (int, error) {
	if arity == extraFlagBoolean {
		if hasInlineValue {
			return 0, fmt.Errorf("executor: boolean flag %q cannot use an attached value in ExtraFlags/CLIFlags for %s", token, cli)
		}
		return 0, nil
	}
	if hasInlineValue {
		return 0, nil
	}

	switch arity {
	case extraFlagValue:
		if index+1 >= len(parts) || strings.HasPrefix(parts[index+1], "-") {
			return 0, fmt.Errorf("executor: flag %q requires a value in ExtraFlags/CLIFlags for %s", token, cli)
		}
		if err := validateExtraFlagValue(parts[index+1]); err != nil {
			return 0, err
		}
		return 1, nil
	case extraFlagOptionalValue:
		if index+1 < len(parts) && !strings.HasPrefix(parts[index+1], "-") {
			if err := validateExtraFlagValue(parts[index+1]); err != nil {
				return 0, err
			}
			return 1, nil
		}
		return 0, nil
	case extraFlagVariadicValue:
		consumed := 0
		for index+consumed+1 < len(parts) && !strings.HasPrefix(parts[index+consumed+1], "-") {
			consumed++
			if err := validateExtraFlagValue(parts[index+consumed]); err != nil {
				return 0, err
			}
		}
		if consumed == 0 {
			return 0, fmt.Errorf("executor: flag %q requires at least one value in ExtraFlags/CLIFlags for %s", token, cli)
		}
		return consumed, nil
	default:
		return 0, unsupportedExtraFlagError(cli, token)
	}
}

func unsupportedExtraFlagError(cli, flag string) error {
	err := fmt.Errorf(
		"executor: flag %q is not in the safe ExtraFlags/CLIFlags allowlist for %s; use a typed configuration field or update Heimdallm's provider policy",
		flag, cli)
	slog.Warn("executor: ExtraFlags validation failed", "cli", cli, "flag", flag, "err", err)
	return err
}

func forbiddenExtraFlagCategory(cli, flag string) (string, bool) {
	if cli != "" {
		policy := forbiddenExtraFlagsByCLI[cli]
		if category, ok := policy[flag]; ok {
			return category, true
		}
		// Yargs accepts camelCase/snake_case aliases for kebab-case options
		// (for example --approvalMode=yolo). Compare long options with
		// separators removed so alternate spellings reach the same policy.
		if strings.HasPrefix(flag, "--") {
			comparable := comparableLongFlag(flag)
			for alias, category := range policy {
				if strings.HasPrefix(alias, "--") && comparableLongFlag(alias) == comparable {
					return category, true
				}
			}
		}
		// Clap/yargs accept attached short-option values in forms such as
		// -sdanger-full-access, -C/etc, and -f/etc. Match only aliases that
		// belong to this provider so an alias from one CLI cannot change the
		// interpretation of another CLI's flags.
		if strings.HasPrefix(flag, "-") && !strings.HasPrefix(flag, "--") {
			for alias, category := range policy {
				if len(alias) == 2 && alias[0] == '-' && strings.HasPrefix(flag, alias) {
					return category, true
				}
			}
		}
		return "", false
	}
	if !strings.HasPrefix(flag, "--") {
		return "", false
	}
	comparable := comparableLongFlag(flag)
	for _, policy := range forbiddenExtraFlagsByCLI {
		for alias, category := range policy {
			if strings.HasPrefix(alias, "--") && comparableLongFlag(alias) == comparable {
				return category, true
			}
		}
	}
	return "", false
}

func comparableLongFlag(flag string) string {
	flag = strings.TrimPrefix(strings.ToLower(flag), "--")
	flag = strings.ReplaceAll(flag, "-", "")
	flag = strings.ReplaceAll(flag, "_", "")
	return flag
}

func forbiddenExtraFlagError(cli, flag, category string) error {
	scope := ""
	if cli != "" {
		scope = " for " + cli
	}
	err := fmt.Errorf(
		"executor: flag %q is forbidden in ExtraFlags/CLIFlags%s because it can override %s; use the dedicated typed configuration instead",
		flag, scope, category)
	slog.Warn("executor: ExtraFlags validation failed", "cli", cli, "flag", flag, "err", err)
	return err
}

// ValidateCLIName returns an error if name is not in the known-safe allowlist.
// This is exported so that callers outside the package (e.g. HTTP handlers) can
// validate user-supplied CLI names before persisting them.
// This must be called before any function that interpolates the name into a
// shell command (e.g. resolveCLIPath).
func ValidateCLIName(name string) error {
	if _, ok := allowedCLIs[name]; !ok {
		return fmt.Errorf("executor: unknown CLI %q — must be one of: claude, gemini, codex, opencode", name)
	}
	return nil
}

// validateCLIName is the unexported alias kept for internal callers.
func validateCLIName(name string) error {
	return ValidateCLIName(name)
}

// resolveCLIPath returns the full path for a CLI tool, checking both the
// current process PATH and the user's login shell (handles Homebrew, nvm, etc.).
// Returns "" if not found anywhere.
//
// SECURITY: name MUST be validated with validateCLIName before calling this
// function. resolveCLIPath passes the name into a shell command; an unvalidated
// value would allow shell injection (CVE-equivalent: issue #2).
func resolveCLIPath(name string) string {
	// Fast path: already in the process PATH.
	if path, err := exec.LookPath(name); err == nil && path != "" {
		return path
	}
	// Fall back to the user's login shell.
	if path := loginShellLookPath(name); path != "" {
		return path
	}
	// Last resort: installer directories commonly missing from both launchd's
	// minimal PATH and non-interactive login shells.
	if path := lookInWellKnownDirs(name); path != "" {
		return path
	}
	slog.Debug("executor: CLI not found on PATH, login shell, or well-known dirs", "cli", name)
	return ""
}

// appendDirToPath returns env (the process environment when nil) with dir
// appended to PATH when missing. The resolved CLI may live in a directory that
// is absent from both the process and login-shell PATH (the well-known-dir
// fallback); without this, a CLI that re-invokes itself or a sibling tool by
// bare name would still fail in the launchd environment even though Heimdallm
// launched it by absolute path. Appending (not prepending) means resolution of
// every other binary is unchanged.
func appendDirToPath(env []string, dir string) []string {
	if dir == "" || dir == "." {
		return env
	}
	if env == nil {
		env = os.Environ()
	}
	for i, kv := range env {
		if !strings.HasPrefix(kv, "PATH=") {
			continue
		}
		path := strings.TrimPrefix(kv, "PATH=")
		for _, p := range filepath.SplitList(path) {
			if p == dir {
				return env
			}
		}
		out := append([]string(nil), env...)
		if path == "" {
			// "PATH=:" + dir would create a leading empty element, which
			// POSIX interprets as the current directory.
			out[i] = "PATH=" + dir
		} else {
			out[i] = kv + string(os.PathListSeparator) + dir
		}
		return out
	}
	// No PATH entry at all — unreachable via os.Environ()/the enriched env,
	// both of which always carry PATH. Setting the CLI's own dir is still
	// strictly more capable than leaving the child with no PATH.
	return append(append([]string(nil), env...), "PATH="+dir)
}

// wellKnownBinDirs returns the installer directories probed when neither the
// process PATH nor the login shell resolves a CLI. Package-level var so tests
// can stub it and stay hermetic on dev machines with real CLIs installed.
//
// ~/.local/bin is where the Claude Code native installer places its binary,
// exporting it only in ~/.zshrc — which non-interactive login shells never
// source, so the login-shell probe cannot see it either (issue #643).
var wellKnownBinDirs = func() []string {
	dirs := make([]string, 0, 3)
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".local", "bin"))
	}
	return append(dirs, "/opt/homebrew/bin", "/usr/local/bin")
}

// lookInWellKnownDirs returns the path of an executable named name inside the
// well-known installer directories, or "" if none qualifies. exec.LookPath on
// a path containing a separator checks that exact file with the same
// effective-user executability test (eaccess X_OK) the PATH lookup applies, so
// a candidate the daemon cannot actually execute (e.g. a root-owned 0700 file)
// is skipped rather than selected and failed at exec time.
func lookInWellKnownDirs(name string) string {
	for _, dir := range wellKnownBinDirs() {
		candidate := filepath.Join(dir, name)
		if path, err := exec.LookPath(candidate); err == nil {
			slog.Debug("executor: CLI resolved from well-known installer dir", "cli", name, "path", path)
			return path
		}
	}
	return ""
}

// loginShellLookPath resolves name via the user's login shell, which sources
// ~/.zshrc, ~/.bashrc, Homebrew, nvm, etc. It is a package-level var so tests
// can stub this profile-dependent probe and stay hermetic — otherwise a real
// CLI installed on the developer's machine leaks in regardless of the test's
// $PATH. Returns "" if not found.
//
// Pass name as $1 (positional arg) so it is never shell-interpolated, even
// though validateCLIName already guarantees it is safe.
var loginShellLookPath = func(name string) string {
	for _, shell := range []string{"/bin/zsh", "/bin/bash"} {
		cmd := exec.Command(shell, "-l", "-c", `which "$1"`, "--", name)
		out, err := cmd.Output()
		if err == nil {
			if path := strings.TrimSpace(string(out)); path != "" {
				return path
			}
		}
	}
	return ""
}

// dangerousPaths lists absolute filesystem paths that must never be used as a
// working directory for the AI CLI. The CLI reads all files under the working
// directory as context; exposing these paths would risk exfiltrating sensitive
// system files or credentials to the AI provider.
var dangerousPaths = []string{
	"/",
	"/etc",
	"/usr",
	"/var",
	"/System",
}

// dangerousSegments lists path substrings that must never appear in a resolved
// working directory. These directories commonly contain private keys, API tokens,
// and other secrets that must not be sent to an AI provider.
var dangerousSegments = []string{
	"/.ssh",              // SSH private keys, host keys, authorized_keys
	"/.gnupg",            // GPG keys, trust database
	"/.aws",              // AWS access keys, credentials files
	"/.config/heimdallm", // Heimdallm daemon config (auth tokens, etc)
	"/.kube",             // Kubernetes credentials (service account tokens, certs)
	"/.docker",           // Docker registry auth (config.json with credentials)
	"/.netrc",            // FTP/HTTP plaintext credentials
	"/.npmrc",            // npm publish tokens
	"/.pypirc",           // PyPI publish tokens
	"/.gem",              // RubyGems credentials
	"/.config/gcloud",    // Google Cloud credentials (service accounts, OAuth tokens)
}

// ValidateWorkDir checks that dir is a safe working directory for the AI CLI.
// It rejects paths outside the user's home directory and /tmp, as well as
// specific system paths and credential directories.
//
// SECURITY: This function resolves symlinks with filepath.EvalSymlinks BEFORE
// applying any denylist check. filepath.Abs alone does NOT resolve symlinks, so
// without this step an attacker could create a symlink at an allowed path (e.g.
// ~/projects/evil -> ~/.ssh) to bypass validation.
func ValidateWorkDir(dir string) error {
	if dir == "" {
		return nil // no working directory override; safe
	}

	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("executor: workdir %q: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("executor: workdir %q is not a directory", dir)
	}

	// Resolve all symlinks before any path comparison so that a symlink
	// pointing outside a safe zone (e.g. ~/proj -> ~/.ssh) is caught.
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return fmt.Errorf("executor: workdir %q: cannot resolve symlinks: %w", dir, err)
	}

	abs, err := filepath.Abs(resolved)
	if err != nil {
		return fmt.Errorf("executor: workdir %q: cannot resolve absolute path: %w", dir, err)
	}

	// Reject explicitly dangerous top-level paths.
	for _, bad := range dangerousPaths {
		if abs == bad {
			return fmt.Errorf("executor: workdir %q is a restricted system path", abs)
		}
	}

	// Reject paths containing sensitive credential directories.
	for _, seg := range dangerousSegments {
		if strings.Contains(abs, seg) {
			return fmt.Errorf("executor: workdir %q contains a sensitive credential path (%s)", abs, seg)
		}
	}

	// Allow /tmp and its subdirectories.
	if abs == "/tmp" || strings.HasPrefix(abs, "/tmp/") {
		return nil
	}

	// Allow the OS temp directory too. On Linux this is usually /tmp, but on
	// macOS os.TempDir() commonly resolves under /private/var/folders/...; the
	// repo-context manager uses that location for managed auto-clones by
	// default. If the temp dir cannot be resolved, skip this extra allowance;
	// the explicit /tmp case above still covers normal Linux containers.
	if tempAbs, err := resolvedAbs(os.TempDir()); err == nil {
		if pathWithin(tempAbs, abs) {
			return nil
		}
	}

	// Allow paths under the user's home directory only.
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("executor: cannot determine home directory: %w", err)
	}
	homeAbs, err := filepath.Abs(home)
	if err != nil {
		return fmt.Errorf("executor: cannot resolve home directory: %w", err)
	}
	if abs != homeAbs && !strings.HasPrefix(abs, homeAbs+"/") {
		return fmt.Errorf("executor: workdir %q is outside the user home directory and /tmp — rejected for security", abs)
	}

	return nil
}

func resolvedAbs(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	return filepath.Abs(resolved)
}

func pathWithin(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// Execute runs the AI CLI with the given prompt and options, returning the
// parsed PR review result. Callers that need a different output schema (e.g.
// the issue-tracking pipeline) should use ExecuteRaw + StripToJSON instead of
// re-implementing the subprocess plumbing.
func (e *Executor) Execute(cli, prompt string, opts ExecOptions) (*ReviewResult, error) {
	raw, err := e.ExecuteRaw(cli, prompt, opts)
	if err != nil {
		return nil, err
	}
	return parseResult(raw)
}

// ExecuteRaw runs the AI CLI and returns stdout unchanged. Used by pipelines
// that parse a schema other than ReviewResult (issue triage, auto_implement
// output, etc.). Callers should pass the bytes through StripToJSON before
// json.Unmarshal — CLIs routinely wrap JSON in code fences or surrounding text.
func (e *Executor) ExecuteRaw(cli, prompt string, opts ExecOptions) ([]byte, error) {
	// This is the final trust boundary before creating a subprocess. Callers
	// validate configuration on ingress too, but legacy rows, direct TOML edits,
	// and future call paths must not be able to bypass the execution policy.
	if err := validateExecutionRequest(cli, opts); err != nil {
		return nil, err
	}

	timeout := executionTimeout
	if opts.Timeout > 0 {
		timeout = opts.Timeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Resolve full path — uses login shell to find binaries in Homebrew/npm/etc.
	cliPath := resolveCLIPath(cli)
	if cliPath == "" {
		cliPath = cli // best effort; execution will fail with a useful error
	}

	var workDirFlags []string
	switch {
	case opts.WorkDir != "":
		workDirFlags = detectWorkDirFlags(cli, cliPath, opts.WorkDir)
	case cli == "codex":
		// `codex exec` refuses to start outside a git repo or a directory
		// trusted in ~/.codex/config.toml. With no checkout the child would
		// inherit the daemon's cwd — `/` under launchd — and abort with "Not
		// inside a trusted directory" (theburrowhub/heimdallm#655). A review
		// needs no checkout at all (the diff travels in the prompt), so hand
		// codex an empty per-execution directory and waive the repo check.
		// Substituting `/` for an empty workspace also narrows what the agent
		// can read, rather than widening it.
		// The workspace is passed as cmd.Dir below rather than via --cd: the
		// child's cwd is what codex inspects, so --cd would be redundant.
		// --skip-git-repo-check is not gated behind cliHelpSupports like the
		// other flags here: that probe reads the top-level `codex --help`, and
		// the flag only appears under `codex exec --help` (verified on codex
		// 0.146.0), so gating it would always resolve to false and reinstate the
		// very failure this branch exists to prevent.
		ws, err := os.MkdirTemp("", "heimdallm-codex-ws-*")
		if err != nil {
			return nil, fmt.Errorf("executor: create codex workspace: %w", err)
		}
		defer os.RemoveAll(ws)
		opts.WorkDir = ws
		workDirFlags = []string{"--skip-git-repo-check"}
		// Loud on purpose. Only the review and triage paths reach this branch
		// today (the write-mode callers abort when repoctx fails, so they never
		// arrive with an empty WorkDir), and an agent asked to change code would
		// silently succeed against an empty directory here. If this ever shows
		// up alongside a develop/refinement run, that caller needs a checkout,
		// not this workspace.
		slog.Warn("executor: running codex without a checkout in a throwaway workspace",
			"workspace", ws)
	}

	args := buildArgs(cli, opts, workDirFlags)
	cmd := exec.CommandContext(ctx, cliPath, args...)
	cmd.Stdin = strings.NewReader(prompt)

	// Augment PATH with paths from the login shell so the CLI can find its own
	// dependencies, without running stdin THROUGH the shell (which would cause
	// shell startup scripts to consume our prompt).
	enrichedEnv := enrichEnvWithLoginPath()
	if enrichedEnv != nil {
		cmd.Env = enrichedEnv
	}
	// Make sure the CLI's own directory is on the child's PATH — it may have
	// been resolved from a well-known installer dir that no PATH source knows.
	if filepath.IsAbs(cliPath) {
		cmd.Env = appendDirToPath(cmd.Env, filepath.Dir(cliPath))
	}
	if opts.WorkDir != "" {
		cmd.Dir = opts.WorkDir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// procgroup.Start creates a sentinel-led group before starting the CLI. The
	// sentinel reserves the PGID until Process.Wait has completed TERM/KILL
	// cleanup, including the clean-exit/ErrWaitDelay case.
	var pgid int
	process, runErr := procgroup.Start(cmd)
	if runErr == nil {
		pgid = process.ID()
		e.trackGroup(process)
		runErr = process.Wait()
		e.untrackGroup(pgid)
	}

	// WaitDelay also fires when the command itself exited cleanly but a
	// descendant still holds the inherited pipes — the exact shape of #614. The
	// run succeeded and its output is already buffered. Process.Wait has already
	// terminated and reaped the owned group, so return the successful payload
	// without leaving background agent work behind.
	if errors.Is(runErr, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == 0 {
		slog.Warn("executor: CLI exited 0 but a descendant held the output pipes; returning collected output after cleaning its process group",
			"cli", cli, "bytes", stdout.Len(), "pgid", pgid)
		return stdout.Bytes(), nil
	}
	if runErr != nil {
		// Some CLIs (e.g. claude) write errors to stdout rather than stderr.
		errDetail := strings.TrimSpace(stderr.String())
		if errDetail == "" {
			errDetail = strings.TrimSpace(stdout.String())
		}
		return nil, fmt.Errorf("executor: run %s: %w (output: %s)", cli, runErr, errDetail)
	}

	return stdout.Bytes(), nil
}

// killGroup is retained for the low-level translation tests in this package.
// Runtime execution-group signaling is centralized in procgroup, which also
// owns the sentinel that makes signaling safe.
func killGroup(pgid int, sig syscall.Signal) error {
	if err := syscall.Kill(-pgid, sig); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}

func validateExecutionRequest(cli string, opts ExecOptions) error {
	if err := ValidateCLIName(cli); err != nil {
		return err
	}
	if err := ValidateModel(opts.Model); err != nil {
		return err
	}
	if err := ValidateEffort(opts.Effort); err != nil {
		return err
	}
	if err := ValidatePermissionMode(opts.PermissionMode); err != nil {
		return err
	}
	if err := ValidateApprovalModeForCLI(cli, opts.ApprovalMode); err != nil {
		return err
	}
	if err := validateExtraFlags(cli, opts.ExtraFlags); err != nil {
		return err
	}
	return ValidateWorkDir(opts.WorkDir)
}

// buildArgs constructs the CLI argument list based on the CLI name and options.
func buildArgs(cli string, opts ExecOptions, workDirFlags []string) []string {
	var args []string

	switch cli {
	case "opencode":
		// opencode uses "run" subcommand; reads prompt from stdin when no
		// positional message args are given.
		args = append(args, "run")
		if opts.Model != "" {
			args = append(args, "-m", strings.TrimSpace(opts.Model))
		}
	case "codex":
		// Codex's top-level command is interactive and requires a TTY. The
		// daemon must use the non-interactive subcommand and feed the prompt on
		// stdin, otherwise Codex exits with "stdin is not a terminal".
		if mode := strings.TrimSpace(opts.ApprovalMode); mode != "" {
			if normalized, err := NormalizeApprovalModeForCLI(cli, mode); err != nil {
				slog.Warn("buildArgs: ApprovalMode rejected, ignoring", "mode", mode, "err", err)
			} else {
				args = append(args, "--ask-for-approval", normalized)
			}
		}
		args = append(args, "exec")
		if opts.Model != "" {
			args = append(args, "--model", strings.TrimSpace(opts.Model))
		}
	default:
		// claude, gemini: stdin mode
		if cli == "claude" && len(workDirFlags) > 0 {
			args = append(args, "-p")
		} else {
			args = append(args, "-p", "-")
		}
		if opts.Model != "" {
			args = append(args, "--model", strings.TrimSpace(opts.Model))
		}
		if cli == "gemini" {
			if mode := strings.TrimSpace(opts.ApprovalMode); mode != "" {
				if normalized, err := NormalizeApprovalModeForCLI(cli, mode); err != nil {
					slog.Warn("buildArgs: ApprovalMode rejected, ignoring", "cli", cli, "mode", mode, "err", err)
				} else {
					args = append(args, "--approval-mode", normalized)
				}
			}
		}
		if cli == "claude" {
			if opts.MaxTurns > 0 {
				args = append(args, "--max-turns", fmt.Sprintf("%d", opts.MaxTurns))
			}
			if opts.Effort != "" {
				if effort, err := NormalizeEffort(opts.Effort); err == nil {
					args = append(args, "--effort", effort)
				}
			}
			if opts.PermissionMode != "" {
				if mode, err := NormalizePermissionMode(opts.PermissionMode); err != nil {
					slog.Warn("buildArgs: PermissionMode rejected, ignoring", "mode", opts.PermissionMode, "err", err)
				} else {
					args = append(args, "--permission-mode", mode)
				}
			}
			if opts.Bare {
				args = append(args, "--bare")
			}
			if opts.DangerouslySkipPerms {
				args = append(args, "--dangerously-skip-permissions")
			}
			if opts.NoSessionPersistence {
				args = append(args, "--no-session-persistence")
			}
		}
	}

	args = append(args, workDirFlags...)

	// Append free-form extra flags (split on whitespace)
	if opts.ExtraFlags != "" {
		args = append(args, strings.Fields(opts.ExtraFlags)...)
	}
	if cli == "claude" && len(workDirFlags) > 0 {
		args = append(args, "-")
	}
	if cli == "codex" {
		args = append(args, "-")
	}

	return args
}

var (
	loginPathOnce sync.Once
	loginPathEnv  []string // os.Environ() + enriched PATH from login shell
	cliHelpCache  sync.Map // map[string]string, keyed by resolved CLI path
)

func detectWorkDirFlags(cli, cliPath, workDir string) []string {
	if workDir == "" || cliPath == "" {
		return nil
	}
	switch cli {
	case "claude":
		if cliHelpSupports(cliPath, "--add-dir") {
			return []string{"--add-dir", workDir}
		}
		if cliHelpSupports(cliPath, "--directory") {
			return []string{"--directory", workDir}
		}
	case "gemini":
		if cliHelpSupports(cliPath, "--include-directories") {
			return []string{"--include-directories", workDir}
		}
		if cliHelpSupports(cliPath, "--include-directory") {
			return []string{"--include-directory", workDir}
		}
		if cliHelpSupports(cliPath, "--cwd") {
			return []string{"--cwd", workDir}
		}
	case "codex":
		if cliHelpSupports(cliPath, "--cd") {
			return []string{"--cd", workDir}
		}
		if cliHelpSupports(cliPath, "--cwd") {
			return []string{"--cwd", workDir}
		}
	}
	return nil
}

func cliHelpSupports(cliPath, flag string) bool {
	help, ok := cliHelp(cliPath)
	return ok && strings.Contains(help, flag)
}

func cliHelp(cliPath string) (string, bool) {
	if cached, ok := cliHelpCache.Load(cliPath); ok {
		return cached.(string), true
	}
	ctx, cancel := context.WithTimeout(context.Background(), cliHelpTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, cliPath, "--help")
	if env := enrichEnvWithLoginPath(); env != nil {
		cmd.Env = env
	}
	// Same PATH enrichment as ExecuteRaw: a CLI resolved from a well-known
	// dir may exec a sibling tool by bare name while printing --help.
	if filepath.IsAbs(cliPath) {
		cmd.Env = appendDirToPath(cmd.Env, filepath.Dir(cliPath))
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		slog.Debug("executor: CLI help unavailable; using cwd-only repo context", "cli", cliPath, "err", err)
		return "", false
	}
	help := string(out)
	cliHelpCache.Store(cliPath, help)
	return help, true
}

// enrichEnvWithLoginPath returns the process environment augmented with the PATH
// from a login shell. Cached after the first call — cheap after startup.
// Using a login shell ONLY for PATH (not for execution) avoids the stdin
// consumption bug where shell startup scripts read our prompt.
func enrichEnvWithLoginPath() []string {
	loginPathOnce.Do(func() {
		base := os.Environ()
		// Ask the login shell for its PATH without providing any stdin
		// (pass /dev/null so startup scripts cannot accidentally consume stdin)
		cmd := exec.Command("/bin/zsh", "-l", "-c", "echo $PATH")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd = exec.CommandContext(ctx, "/bin/zsh", "-l", "-c", "echo $PATH")
		cmd.Stdin, _ = os.Open(os.DevNull)
		cmd.Stderr = nil
		out, err := cmd.Output()
		if err != nil {
			loginPathEnv = base
			return
		}
		loginShellPath := strings.TrimSpace(string(out))
		if loginShellPath == "" {
			loginPathEnv = base
			return
		}
		// Merge: put login shell PATH first so Homebrew bins take precedence
		currentPath := os.Getenv("PATH")
		merged := loginShellPath
		if currentPath != "" {
			merged = loginShellPath + ":" + currentPath
		}
		result := make([]string, 0, len(base)+1)
		for _, e := range base {
			if !strings.HasPrefix(e, "PATH=") {
				result = append(result, e)
			}
		}
		result = append(result, "PATH="+merged)
		loginPathEnv = result
	})
	return loginPathEnv
}

// StripToJSON strips common LLM output wrappers (leading/trailing whitespace,
// markdown code fences, prose surrounding the JSON object) and returns the
// inner JSON bytes. Exported so downstream pipelines (issue triage, etc.)
// can reuse the same cleanup without duplicating it.
//
// The scan returns the leftmost complete, valid JSON object, honouring string
// literals and escapes so braces inside strings — or in the surrounding prose
// — cannot move the boundaries. That matters because reviews routinely quote
// template syntax: a diff mentioning ${{ matrix.env }} used to derail a naive
// first-'{'-to-last-'}' slice and produce invalid JSON.
//
// If no valid object is found the old outer-slice behaviour applies, so the
// caller's json.Unmarshal still surfaces a descriptive error with the most
// relevant context rather than an empty string.
func StripToJSON(data []byte) []byte {
	s := strings.TrimSpace(string(data))

	// Peel a leading code fence if present. Look for an explicit closing
	// fence rather than assuming the last line is the fence — if the LLM
	// appends trailing prose after the fence, the naive approach would keep
	// that prose inside the JSON slice.
	if strings.HasPrefix(s, "```") {
		lines := strings.Split(s, "\n")
		closeIdx := -1
		for i := 1; i < len(lines); i++ {
			if strings.HasPrefix(lines[i], "```") {
				closeIdx = i
				break
			}
		}
		switch {
		case closeIdx > 0:
			s = strings.Join(lines[1:closeIdx], "\n")
		case len(lines) > 1:
			// No closing fence at all — strip just the opening line.
			s = strings.Join(lines[1:], "\n")
		}
	}

	if obj, ok := firstJSONObject(s); ok {
		return []byte(obj)
	}

	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start >= 0 && end > start {
		s = s[start : end+1]
	}
	return []byte(s)
}

// firstJSONObject returns the leftmost substring of s that is a complete,
// valid JSON object.
func firstJSONObject(s string) (string, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] != '{' || !opensObject(s, i) {
			continue
		}
		end, ok := matchObjectEnd(s, i)
		if !ok {
			continue
		}
		if candidate := s[i : end+1]; json.Valid([]byte(candidate)) {
			return candidate, true
		}
	}
	return "", false
}

// opensObject reports whether the '{' at i can begin a JSON object: the next
// non-space byte has to start a string key or close an empty object. Runs
// before the full scan and rejects the '{{' of template syntax outright.
func opensObject(s string, i int) bool {
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case ' ', '\t', '\r', '\n':
			continue
		case '"', '}':
			return true
		default:
			return false
		}
	}
	return false
}

// matchObjectEnd scans from the '{' at start to its balanced closing '}' and
// returns that index. Braces inside string literals are literal characters,
// and a backslash escapes the byte after it, so an escaped quote does not end
// the string it appears in.
func matchObjectEnd(s string, start int) (int, bool) {
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case escaped:
			escaped = false
		case inString && c == '\\':
			escaped = true
		case c == '"':
			inString = !inString
		case inString:
			// Structural characters inside a string are just text.
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

func parseResult(data []byte) (*ReviewResult, error) {
	clean := StripToJSON(data)
	var result ReviewResult
	if err := json.Unmarshal(clean, &result); err != nil {
		return nil, fmt.Errorf("executor: parse JSON result: %w (raw: %.200s)", err, clean)
	}
	if result.Severity == "" {
		result.Severity = "low"
	}
	return &result, nil
}

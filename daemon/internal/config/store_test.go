package config

import (
	"errors"
	"fmt"
	"testing"
)

type fakeStoreLister struct {
	rows map[string]string
	err  error
}

func (f *fakeStoreLister) ListConfigs() (map[string]string, error) {
	return f.rows, f.err
}

// ApplyStore receives the `configs` table rows (key → raw value string) that
// the legacy PUT /config handler writes, and merges them onto an already-loaded
// cfg. Most keys use TOML < env < store precedence; repository lists are lower
// priority runtime-discovery state, so explicit TOML/env entries win conflicts.
//
// Values stored as bare strings (e.g. "5m" for poll_interval) are assigned
// as-is; everything else was json.Marshal'd by the handler, so we unmarshal
// here symmetrically.

func TestApplyStore_AgentConfigs_MergesOverTOML(t *testing.T) {
	// Symmetric to handlers.go: PUT /config writes a JSON object keyed by CLI
	// name. Each CLI gets a partial config; missing fields keep their TOML
	// value. The receiver's existing agents map must be preserved for CLIs
	// the store row doesn't mention (gemini below).
	cfg := &Config{}
	cfg.applyDefaults()
	cfg.AI.Agents = map[string]CLIAgentConfig{
		"claude": {Model: "claude-from-toml", Effort: "low", DangerouslySkipPerms: true},
		"gemini": {Model: "gemini-from-toml"},
	}

	rows := map[string]string{
		"agent_configs": `{"claude":{"permission_mode":"acceptEdits","model":"claude-from-store"}}`,
	}
	if err := cfg.ApplyStore(rows); err != nil {
		t.Fatalf("ApplyStore: %v", err)
	}

	got := cfg.AI.Agents["claude"]
	if got.PermissionMode != "acceptEdits" {
		t.Errorf("PermissionMode = %q, want acceptEdits", got.PermissionMode)
	}
	if got.Model != "claude-from-store" {
		t.Errorf("Model = %q, want claude-from-store", got.Model)
	}
	if got.Effort != "low" {
		t.Errorf("Effort lost: got %q, want low (must come from TOML when JSON omits it)", got.Effort)
	}
	if !got.DangerouslySkipPerms {
		t.Errorf("DangerouslySkipPerms cleared by store merge — TOML value must survive")
	}
	if cfg.AI.Agents["gemini"].Model != "gemini-from-toml" {
		t.Errorf("unrelated agent zeroed: %v", cfg.AI.Agents["gemini"])
	}
}

func TestApplyStore_AgentConfigs_SafeFalseBoolsOverrideTOMLTrue(t *testing.T) {
	// Regression for the omitempty foot-gun the bot review flagged on #432:
	// an operator who flips bare=false in the UI must override a TOML
	// bare=true. With omitempty on the bool tag, a direct Marshal of
	// CLIAgentConfig would drop the false and ApplyStore's merge-into-existing
	// path would preserve the TOML true. dangerously_skip_perms deliberately
	// uses asymmetric security semantics: a stored false may reduce privilege,
	// while a stored true may never grant it.
	cfg := &Config{}
	cfg.applyDefaults()
	cfg.AI.Agents = map[string]CLIAgentConfig{
		"claude": {Bare: true, DangerouslySkipPerms: true, NoSessionPersistence: true},
	}

	rows := map[string]string{
		"agent_configs": `{"claude":{"bare":false,"dangerously_skip_perms":false,"no_session_persistence":false}}`,
	}
	if err := cfg.ApplyStore(rows); err != nil {
		t.Fatalf("ApplyStore: %v", err)
	}
	got := cfg.AI.Agents["claude"]
	if got.Bare {
		t.Errorf("Bare: stored false did not override TOML true")
	}
	if got.DangerouslySkipPerms {
		t.Errorf("DangerouslySkipPerms: stored false did not disable TOML true")
	}
	if got.NoSessionPersistence {
		t.Errorf("NoSessionPersistence: stored false did not override TOML true")
	}
}

func TestApplyStore_AgentConfigs_CannotEnableDangerouslySkipPerms(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	cfg.AI.Agents = map[string]CLIAgentConfig{
		"claude": {DangerouslySkipPerms: false},
	}

	rows := map[string]string{
		"agent_configs": `{"claude":{"DANGEROUSLY_SKIP_PERMS":true,"model":"safe-model"}}`,
	}
	if err := cfg.ApplyStore(rows); err != nil {
		t.Fatalf("ApplyStore: %v", err)
	}
	got := cfg.AI.Agents["claude"]
	if got.DangerouslySkipPerms {
		t.Errorf("DangerouslySkipPerms enabled by legacy store row")
	}
	if got.Model != "safe-model" {
		t.Errorf("safe sibling field was not applied: %v", got)
	}
}

func TestApplyStore_AgentConfigs_DangerouslySkipPermsAsymmetricMatrix(t *testing.T) {
	tests := []struct {
		name    string
		base    bool
		payload string
		want    bool
	}{
		{
			name:    "omitted preserves trusted true",
			base:    true,
			payload: `{"claude":{"model":"safe"}}`,
			want:    true,
		},
		{
			name:    "false disables trusted true",
			base:    true,
			payload: `{"claude":{"dangerously_skip_perms":false}}`,
			want:    false,
		},
		{
			name:    "true cannot elevate false",
			base:    false,
			payload: `{"claude":{"dangerously_skip_perms":true}}`,
			want:    false,
		},
		{
			name:    "true cannot replace trusted source",
			base:    true,
			payload: `{"claude":{"dangerously_skip_perms":true}}`,
			want:    true,
		},
		{
			name:    "false wins conflicting legacy aliases",
			base:    true,
			payload: `{"claude":{"DANGEROUSLY_SKIP_PERMS":true,"dangerously_skip_perms":false}}`,
			want:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{}
			cfg.applyDefaults()
			cfg.AI.Agents = map[string]CLIAgentConfig{
				"claude": {DangerouslySkipPerms: tc.base},
			}
			if err := cfg.ApplyStore(map[string]string{"agent_configs": tc.payload}); err != nil {
				t.Fatalf("ApplyStore: %v", err)
			}
			if got := cfg.AI.Agents["claude"].DangerouslySkipPerms; got != tc.want {
				t.Fatalf("DangerouslySkipPerms = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestApplyStore_AgentConfigs_PartialFailureLeavesCfgUntouched(t *testing.T) {
	// A malformed agent_configs payload must roll back the whole merge so
	// the receiver keeps its TOML+env state. Mirrors the atomicity guarantee
	// the INVARIANT comment in store.go relies on.
	cfg := &Config{}
	cfg.applyDefaults()
	cfg.AI.Agents = map[string]CLIAgentConfig{"claude": {Model: "keep"}}

	rows := map[string]string{
		"agent_configs": `not-json`,
	}
	if err := cfg.ApplyStore(rows); err == nil {
		t.Fatalf("expected error from malformed agent_configs")
	}
	if cfg.AI.Agents["claude"].Model != "keep" {
		t.Errorf("partial failure leaked into receiver: %v", cfg.AI.Agents["claude"])
	}
}

func TestApplyStore_MergesStoreOnlyRepositoriesAndIssueTracking(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	cfg.GitHub.Repositories = []string{"toml/one"}

	rows := map[string]string{
		"repositories":   `["store/a","store/b"]`,
		"issue_tracking": `{"enabled":true,"filter_mode":"inclusive","develop_labels":["feature","bug"],"default_action":"review_only"}`,
	}

	if err := cfg.ApplyStore(rows); err != nil {
		t.Fatalf("ApplyStore: %v", err)
	}

	wantRepos := []string{"toml/one", "store/a", "store/b"}
	if fmt.Sprintf("%v", cfg.GitHub.Repositories) != fmt.Sprintf("%v", wantRepos) {
		t.Errorf("Repositories = %v, want %v", cfg.GitHub.Repositories, wantRepos)
	}
	it := cfg.GitHub.IssueTracking
	if !it.Enabled {
		t.Errorf("IssueTracking.Enabled = false, want true")
	}
	if it.FilterMode != FilterModeInclusive {
		t.Errorf("FilterMode = %q, want inclusive", it.FilterMode)
	}
	if len(it.DevelopLabels) != 2 || it.DevelopLabels[0] != "feature" || it.DevelopLabels[1] != "bug" {
		t.Errorf("DevelopLabels = %v, want [feature bug]", it.DevelopLabels)
	}
	if it.DefaultAction != "review_only" {
		t.Errorf("DefaultAction = %q, want review_only", it.DefaultAction)
	}
}

func TestApplyStore_MergesStoreOnlyNonMonitored(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	cfg.GitHub.NonMonitored = []string{"toml/skip"}

	rows := map[string]string{
		"non_monitored": `["store/a","store/b"]`,
	}

	if err := cfg.ApplyStore(rows); err != nil {
		t.Fatalf("ApplyStore: %v", err)
	}

	want := []string{"toml/skip", "store/a", "store/b"}
	if fmt.Sprintf("%v", cfg.GitHub.NonMonitored) != fmt.Sprintf("%v", want) {
		t.Errorf("NonMonitored = %v, want %v", cfg.GitHub.NonMonitored, want)
	}
}

func TestApplyStore_RepoLists_TOMLRepositoriesWinStoreNonMonitored(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	cfg.GitHub.Repositories = []string{"toml/monitored"}

	rows := map[string]string{
		"non_monitored": `["toml/monitored","store/disabled"]`,
	}

	if err := cfg.ApplyStore(rows); err != nil {
		t.Fatalf("ApplyStore: %v", err)
	}

	wantRepos := []string{"toml/monitored"}
	wantNonMonitored := []string{"store/disabled"}
	if fmt.Sprintf("%v", cfg.GitHub.Repositories) != fmt.Sprintf("%v", wantRepos) {
		t.Errorf("Repositories = %v, want %v", cfg.GitHub.Repositories, wantRepos)
	}
	if fmt.Sprintf("%v", cfg.GitHub.NonMonitored) != fmt.Sprintf("%v", wantNonMonitored) {
		t.Errorf("NonMonitored = %v, want %v", cfg.GitHub.NonMonitored, wantNonMonitored)
	}
}

func TestApplyStore_RepoLists_TOMLNonMonitoredWinsStoreRepositories(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	cfg.GitHub.NonMonitored = []string{"toml/disabled"}

	rows := map[string]string{
		"repositories": `["toml/disabled","store/monitored"]`,
	}

	if err := cfg.ApplyStore(rows); err != nil {
		t.Fatalf("ApplyStore: %v", err)
	}

	wantRepos := []string{"store/monitored"}
	wantNonMonitored := []string{"toml/disabled"}
	if fmt.Sprintf("%v", cfg.GitHub.Repositories) != fmt.Sprintf("%v", wantRepos) {
		t.Errorf("Repositories = %v, want %v", cfg.GitHub.Repositories, wantRepos)
	}
	if fmt.Sprintf("%v", cfg.GitHub.NonMonitored) != fmt.Sprintf("%v", wantNonMonitored) {
		t.Errorf("NonMonitored = %v, want %v", cfg.GitHub.NonMonitored, wantNonMonitored)
	}
}

func TestApplyStore_RepoLists_EnvRepositoriesIgnoreStoreAdditions(t *testing.T) {
	t.Setenv("HEIMDALLM_REPOSITORIES", "env/one,env/two")

	cfg := &Config{}
	cfg.applyDefaults()
	cfg.applyEnvOverrides()

	rows := map[string]string{
		"repositories":  `["store/monitored"]`,
		"non_monitored": `["env/one","store/disabled"]`,
	}

	if err := cfg.ApplyStore(rows); err != nil {
		t.Fatalf("ApplyStore: %v", err)
	}

	wantRepos := []string{"env/one", "env/two"}
	wantNonMonitored := []string{"store/disabled"}
	if fmt.Sprintf("%v", cfg.GitHub.Repositories) != fmt.Sprintf("%v", wantRepos) {
		t.Errorf("Repositories = %v, want %v", cfg.GitHub.Repositories, wantRepos)
	}
	if fmt.Sprintf("%v", cfg.GitHub.NonMonitored) != fmt.Sprintf("%v", wantNonMonitored) {
		t.Errorf("NonMonitored = %v, want %v", cfg.GitHub.NonMonitored, wantNonMonitored)
	}
}

func TestApplyStore_RepoFirstSeen_IsAcknowledged(t *testing.T) {
	// repo_first_seen is auxiliary data consumed by the HTTP config handler,
	// not applied to the Config struct. ApplyStore must accept it silently
	// (no error, no state change) so the store key doesn't trip the
	// "unknown key" warning on every reload.
	cfg := &Config{}
	cfg.applyDefaults()
	before := cfg.GitHub.Repositories

	rows := map[string]string{
		"repo_first_seen": `{"a/b":1234567890,"c/d":1234567891}`,
	}

	if err := cfg.ApplyStore(rows); err != nil {
		t.Fatalf("ApplyStore: %v", err)
	}

	if fmt.Sprintf("%v", cfg.GitHub.Repositories) != fmt.Sprintf("%v", before) {
		t.Errorf("Repositories changed after ApplyStore with repo_first_seen: before=%v after=%v",
			before, cfg.GitHub.Repositories)
	}
}

func TestApplyStore_WinsOverEnvOverrides(t *testing.T) {
	t.Setenv("HEIMDALLM_POLL_INTERVAL", "1m")
	t.Setenv("HEIMDALLM_AI_PRIMARY", "gemini")

	cfg := &Config{}
	cfg.applyDefaults()
	cfg.applyEnvOverrides()

	if cfg.GitHub.PollInterval != "1m" {
		t.Fatalf("setup: env should have set poll_interval=1m, got %q", cfg.GitHub.PollInterval)
	}
	if cfg.AI.Primary != "gemini" {
		t.Fatalf("setup: env should have set ai_primary=gemini, got %q", cfg.AI.Primary)
	}

	rows := map[string]string{
		"poll_interval": "30m",
		"ai_primary":    "claude",
	}

	if err := cfg.ApplyStore(rows); err != nil {
		t.Fatalf("ApplyStore: %v", err)
	}

	if cfg.GitHub.PollInterval != "30m" {
		t.Errorf("PollInterval = %q, want 30m (store wins over env)", cfg.GitHub.PollInterval)
	}
	if cfg.AI.Primary != "claude" {
		t.Errorf("AI.Primary = %q, want claude (store wins over env)", cfg.AI.Primary)
	}
}

func TestApplyStore_InvalidJSON_ReturnsError(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()

	rows := map[string]string{
		"repositories": "this is not json",
	}

	if err := cfg.ApplyStore(rows); err == nil {
		t.Fatal("ApplyStore with malformed JSON: expected error, got nil")
	}
}

func TestMergeStoreLayer_Success(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	cfg.AI.Primary = "claude" // required by Validate

	store := &fakeStoreLister{rows: map[string]string{
		"poll_interval": "30m",
	}}

	if err := cfg.MergeStoreLayer(store); err != nil {
		t.Fatalf("MergeStoreLayer: %v", err)
	}
	if cfg.GitHub.PollInterval != "30m" {
		t.Errorf("PollInterval = %q, want 30m", cfg.GitHub.PollInterval)
	}
}

func TestMergeStoreLayer_ListConfigsFailure_ReturnsError(t *testing.T) {
	// A transient DB error on reload must surface as an error so the caller
	// (reloadFn) keeps the previous in-memory cfg instead of silently
	// reverting to TOML+env.
	cfg := &Config{}
	cfg.applyDefaults()
	cfg.AI.Primary = "claude"
	cfg.GitHub.PollInterval = "5m"

	boom := errors.New("simulated DB outage")
	store := &fakeStoreLister{err: boom}

	err := cfg.MergeStoreLayer(store)
	if err == nil {
		t.Fatal("MergeStoreLayer with ListConfigs error: expected error, got nil")
	}
	if !errors.Is(err, boom) {
		t.Errorf("expected wrapped boom, got %v", err)
	}
	if cfg.GitHub.PollInterval != "5m" {
		t.Errorf("PollInterval mutated to %q despite store failure", cfg.GitHub.PollInterval)
	}
}

func TestMergeStoreLayer_InvalidStoreValue_ReturnsError(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	cfg.AI.Primary = "claude"

	store := &fakeStoreLister{rows: map[string]string{
		"repositories": "garbage not json",
	}}

	if err := cfg.MergeStoreLayer(store); err == nil {
		t.Fatal("MergeStoreLayer with bad row: expected error, got nil")
	}
}

func TestMergeStoreLayer_FailsValidationOnBadMergedCfg(t *testing.T) {
	// If the store row passes JSON decoding but the merged Config fails
	// Validate (e.g. poll_interval out of the accepted [1m,24h] range),
	// MergeStoreLayer must surface the error so reload can abort cleanly.
	cfg := &Config{}
	cfg.applyDefaults()
	cfg.AI.Primary = "claude"
	originalPollInterval := cfg.GitHub.PollInterval
	cfg.GitHub.IssueTracking.Assignees = []string{"original"}

	store := &fakeStoreLister{rows: map[string]string{
		"poll_interval":  "48h", // parseable string, but above the 24h ceiling
		"issue_tracking": `{"assignees":["mutated"]}`,
	}}

	if err := cfg.MergeStoreLayer(store); err == nil {
		t.Fatal("MergeStoreLayer with invalid merged cfg: expected error, got nil")
	}
	if cfg.GitHub.PollInterval != originalPollInterval {
		t.Fatalf("PollInterval mutated to %q after failed validation; want %q",
			cfg.GitHub.PollInterval, originalPollInterval)
	}
	if got := cfg.GitHub.IssueTracking.Assignees; len(got) != 1 || got[0] != "original" {
		t.Fatalf("nested issue-tracking slice mutated after failed validation: %v", got)
	}
}

func TestMergeStoreLayer_SanitizesLegacyUnsafeAgentFieldWithoutDroppingLayer(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	cfg.AI.Primary = "codex"
	cfg.AI.Agents = map[string]CLIAgentConfig{
		"codex": {ExtraFlags: "--json"},
	}
	cfg.GitHub.IssueTracking.Assignees = []string{"toml-user"}

	store := &fakeStoreLister{rows: map[string]string{
		"agent_configs":  `{"codex":{"extra_flags":"--sandbox danger-full-access"}}`,
		"poll_interval":  "30m",
		"issue_tracking": `{"assignees":["store-user"]}`,
	}}

	if err := cfg.MergeStoreLayer(store); err != nil {
		t.Fatalf("legacy semantic policy error must not discard the store layer: %v", err)
	}
	if got := cfg.AI.Agents["codex"].ExtraFlags; got != "--json" {
		t.Fatalf("unsafe store field replaced trusted base ExtraFlags with %q", got)
	}
	if cfg.GitHub.PollInterval != "30m" {
		t.Fatalf("valid store poll_interval was discarded: %q", cfg.GitHub.PollInterval)
	}
	if got := cfg.GitHub.IssueTracking.Assignees; len(got) != 1 || got[0] != "store-user" {
		t.Fatalf("valid issue_tracking row was discarded: %v", got)
	}
}

func TestApplyStore_MigratesLegacyTypedFlagsAndRestoresOnlyInvalidFields(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	cfg.AI.Primary = "claude"
	cfg.AI.Agents = map[string]CLIAgentConfig{
		"claude": {
			Model:          "toml-model",
			Effort:         "low",
			PermissionMode: "default",
			ExtraFlags:     "--verbose",
		},
	}

	rows := map[string]string{
		"agent_configs": `{"claude":{"model":"--sandbox","effort":"HIGH","permission_mode":"bypassPermissions","extra_flags":"--model legacy --output-format json"}}`,
	}
	if err := cfg.ApplyStore(rows); err != nil {
		t.Fatalf("ApplyStore: %v", err)
	}

	got := cfg.AI.Agents["claude"]
	if got.Model != "toml-model" {
		t.Fatalf("invalid stored model replaced trusted base: %+v", got)
	}
	if got.Effort != "high" {
		t.Fatalf("safe stored effort was not canonicalized: %+v", got)
	}
	if got.PermissionMode != "default" {
		t.Fatalf("invalid stored permission mode replaced trusted base: %+v", got)
	}
	if got.ExtraFlags != "--output-format json" {
		t.Fatalf("legacy typed flag was not removed while safe sibling survived: %+v", got)
	}
}

func TestApplyStore_LegacyTypedFlagsPreserveStorePrecedence(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	cfg.AI.Primary = "claude"
	cfg.AI.Agents = map[string]CLIAgentConfig{
		"claude": {
			Model:      "toml-model",
			MaxTurns:   3,
			Effort:     "low",
			ExtraFlags: "--debug",
		},
	}

	rows := map[string]string{
		"agent_configs": `{"claude":{"extra_flags":"--model store-model --max-turns 9 --effort HIGH --verbose"}}`,
	}
	if err := cfg.ApplyStore(rows); err != nil {
		t.Fatalf("ApplyStore: %v", err)
	}

	got := cfg.AI.Agents["claude"]
	if got.Model != "store-model" {
		t.Fatalf("legacy store model did not override lower-precedence TOML value: %+v", got)
	}
	if got.MaxTurns != 9 {
		t.Fatalf("legacy store max_turns did not override lower-precedence TOML value: %+v", got)
	}
	if got.Effort != "high" {
		t.Fatalf("legacy store effort did not override lower-precedence TOML value: %+v", got)
	}
	if got.ExtraFlags != "--verbose" {
		t.Fatalf("legacy typed flags were not removed while the safe sibling survived: %+v", got)
	}
}

func TestApplyStore_PartialFailure_LeavesCfgUnchanged(t *testing.T) {
	// Atomicity contract: if ANY row fails to decode, NO row is applied.
	// Otherwise the caller's "continuing with TOML+env" warning misrepresents
	// the state and we ship a half-hybrid Config to the scheduler.
	//
	// The test is order-independent by design: Go randomises map iteration,
	// so on some runs poll_interval is decoded first (the valid row would
	// "land" under a non-atomic implementation) and on others repositories
	// is decoded first (the failure short-circuits before poll_interval is
	// seen at all). Both orderings assert the same end state because the
	// shadow-copy pattern only promotes the batch on full success.
	cfg := &Config{}
	cfg.applyDefaults()
	cfg.GitHub.PollInterval = "5m"
	cfg.GitHub.Repositories = []string{"original/repo"}
	cfg.AI.Primary = "claude"
	cfg.AI.Agents = map[string]CLIAgentConfig{
		"claude": {ExtraFlags: "--sandbox"},
	}

	rows := map[string]string{
		"poll_interval": "30m",            // valid — would apply on its own
		"repositories":  "not valid json", // bad — should poison the whole batch
	}

	err := cfg.ApplyStore(rows)
	if err == nil {
		t.Fatal("ApplyStore with partial bad row: expected error, got nil")
	}
	if cfg.GitHub.PollInterval != "5m" {
		t.Errorf("PollInterval = %q, want 5m (valid row must NOT land when batch fails)", cfg.GitHub.PollInterval)
	}
	if len(cfg.GitHub.Repositories) != 1 || cfg.GitHub.Repositories[0] != "original/repo" {
		t.Errorf("Repositories = %v, want [original/repo]", cfg.GitHub.Repositories)
	}
	if got := cfg.AI.Agents["claude"].ExtraFlags; got != "--sandbox" {
		t.Errorf("legacy sanitizer leaked through failed atomic merge: %q", got)
	}
}

func TestApplyStore_PartialIssueTrackingDecodeLeavesNestedSlicesUnchanged(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	cfg.AI.Primary = "claude"
	// Spare capacity makes encoding/json reuse the backing array while
	// decoding, which exposes shallow-copy rollback bugs deterministically.
	cfg.GitHub.IssueTracking.Assignees = make([]string, 1, 4)
	cfg.GitHub.IssueTracking.Assignees[0] = "original"

	err := cfg.ApplyStore(map[string]string{
		"issue_tracking": `{"assignees":["mutated"],"enabled":"not-a-bool"}`,
	})
	if err == nil {
		t.Fatal("expected malformed issue_tracking row to fail")
	}
	if got := cfg.GitHub.IssueTracking.Assignees; len(got) != 1 || got[0] != "original" {
		t.Fatalf("nested issue-tracking slice mutated after failed decode: %v", got)
	}
}

func TestApplyStore_ServerPort_IsIgnored(t *testing.T) {
	// server_port is bootstrap-only: mutating the listening port at runtime
	// would invalidate every in-flight connection and the web UI has no
	// surface for it. A row manually inserted into the configs table must
	// therefore be ignored rather than hot-applied.
	cfg := &Config{}
	cfg.applyDefaults() // sets Server.Port = 7842

	rows := map[string]string{"server_port": "9999"}
	if err := cfg.ApplyStore(rows); err != nil {
		t.Fatalf("ApplyStore: %v", err)
	}
	if cfg.Server.Port != 7842 {
		t.Errorf("Server.Port = %d, want 7842 (server_port row must be ignored)", cfg.Server.Port)
	}
}

func TestApplyStore_IssueTracking_PreservesFieldsAbsentFromStoredJSON(t *testing.T) {
	// Real-world scenario: a user saved issue_tracking via the UI with an
	// older build that didn't know about BlockedLabels/PromoteToLabel. The
	// row in `configs` only carries the eight fields the old build knew
	// about. After upgrading the daemon, HEIMDALLM_ISSUE_BLOCKED_LABELS
	// env var fills those new fields in applyEnvOverrides — and then
	// ApplyStore must NOT clobber them back to zero just because the
	// stored JSON doesn't mention them.
	//
	// Implementation contract: json.Unmarshal into the existing struct
	// (not into a fresh zero value) so absent keys preserve the incoming
	// value.
	cfg := &Config{}
	cfg.applyDefaults()
	// Simulate applyEnvOverrides having populated the "new" fields.
	cfg.GitHub.IssueTracking.BlockedLabels = []string{"heimdallm-queued"}
	cfg.GitHub.IssueTracking.PromoteToLabel = "develop"
	cfg.GitHub.IssueTracking.Enabled = true

	// Stored JSON from an older UI save — no blocked_labels / promote_to_label.
	rows := map[string]string{
		"issue_tracking": `{"enabled":true,"filter_mode":"exclusive","default_action":"ignore","develop_labels":["develop"],"skip_labels":["wontfix"],"organizations":[],"assignees":[],"review_only_labels":[]}`,
	}

	if err := cfg.ApplyStore(rows); err != nil {
		t.Fatalf("ApplyStore: %v", err)
	}

	it := cfg.GitHub.IssueTracking
	// Fields the stored JSON DID set must have landed:
	if len(it.DevelopLabels) != 1 || it.DevelopLabels[0] != "develop" {
		t.Errorf("DevelopLabels = %v, want [develop]", it.DevelopLabels)
	}
	if len(it.SkipLabels) != 1 || it.SkipLabels[0] != "wontfix" {
		t.Errorf("SkipLabels = %v, want [wontfix]", it.SkipLabels)
	}
	// Fields the stored JSON did NOT set must survive from the env layer:
	if len(it.BlockedLabels) != 1 || it.BlockedLabels[0] != "heimdallm-queued" {
		t.Errorf("BlockedLabels = %v, want [heimdallm-queued] — stored JSON had no blocked_labels key, env value should survive", it.BlockedLabels)
	}
	if it.PromoteToLabel != "develop" {
		t.Errorf("PromoteToLabel = %q, want develop — stored JSON had no promote_to_label key, env value should survive", it.PromoteToLabel)
	}
}

func TestApplyStore_IssueTracking_ExplicitEmptyListStillClears(t *testing.T) {
	// Symmetric contract: when the stored JSON DOES include a field and
	// its value is an empty list, that IS a meaningful signal ("operator
	// cleared this via UI") and must overwrite env. The fix for stale-
	// JSON preservation cannot silently turn explicit `[]` into "no-op".
	cfg := &Config{}
	cfg.applyDefaults()
	cfg.GitHub.IssueTracking.DevelopLabels = []string{"from-env"}

	rows := map[string]string{
		"issue_tracking": `{"enabled":false,"filter_mode":"exclusive","default_action":"ignore","develop_labels":[]}`,
	}
	if err := cfg.ApplyStore(rows); err != nil {
		t.Fatalf("ApplyStore: %v", err)
	}
	if len(cfg.GitHub.IssueTracking.DevelopLabels) != 0 {
		t.Errorf("DevelopLabels = %v, want empty — explicit [] in stored JSON must override env", cfg.GitHub.IssueTracking.DevelopLabels)
	}
}

func TestApplyStore_UnknownKey_IsIgnored(t *testing.T) {
	// Forward-compat: if an older daemon sees a key written by a newer
	// handler, we skip it rather than fail the whole reload.
	cfg := &Config{}
	cfg.applyDefaults()

	rows := map[string]string{
		"future_key":    "some-value",
		"poll_interval": "30m", // known key alongside unknown one
	}

	if err := cfg.ApplyStore(rows); err != nil {
		t.Errorf("ApplyStore with unknown key: expected nil error, got %v", err)
	}
	if cfg.GitHub.PollInterval != "30m" {
		t.Errorf("PollInterval = %q, want 30m (known key should still apply)", cfg.GitHub.PollInterval)
	}
}

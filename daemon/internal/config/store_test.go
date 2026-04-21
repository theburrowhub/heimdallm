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

// ApplyStore is the third layer of config precedence: TOML < env < store.
// It receives the `configs` table rows (key → raw value string) that the
// PUT /config handler writes, and merges them onto an already-loaded cfg.
//
// Values stored as bare strings (e.g. "5m" for poll_interval) are assigned
// as-is; everything else was json.Marshal'd by the handler, so we unmarshal
// here symmetrically.

func TestApplyStore_MergesRepositoriesAndIssueTracking(t *testing.T) {
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

	got := cfg.GitHub.Repositories
	if len(got) != 2 || got[0] != "store/a" || got[1] != "store/b" {
		t.Errorf("Repositories = %v, want [store/a store/b]", got)
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

func TestApplyStore_MergesNonMonitored(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	cfg.GitHub.NonMonitored = []string{"toml/skip"}

	rows := map[string]string{
		"non_monitored": `["store/a","store/b"]`,
	}

	if err := cfg.ApplyStore(rows); err != nil {
		t.Fatalf("ApplyStore: %v", err)
	}

	got := cfg.GitHub.NonMonitored
	if len(got) != 2 || got[0] != "store/a" || got[1] != "store/b" {
		t.Errorf("NonMonitored = %v, want [store/a store/b]", got)
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
	// Validate (e.g. poll_interval not in allowlist), MergeStoreLayer must
	// surface the error so reload can abort cleanly.
	cfg := &Config{}
	cfg.applyDefaults()
	cfg.AI.Primary = "claude"

	store := &fakeStoreLister{rows: map[string]string{
		"poll_interval": "42m", // valid as string, invalid per validIntervals
	}}

	if err := cfg.MergeStoreLayer(store); err == nil {
		t.Fatal("MergeStoreLayer with invalid merged cfg: expected error, got nil")
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

	rows := map[string]string{
		"poll_interval": "30m",             // valid — would apply on its own
		"repositories":  "not valid json",  // bad — should poison the whole batch
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

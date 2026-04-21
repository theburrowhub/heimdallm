package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
)

// StoreLister is the subset of *store.Store that ApplyStore needs. Kept as a
// local interface so the config package stays free of a store dependency
// (avoids an import cycle and keeps tests able to inject fakes).
type StoreLister interface {
	ListConfigs() (map[string]string, error)
}

// MergeStoreLayer is the full "apply the store layer on top of TOML+env"
// operation: fetch rows, apply them atomically, re-validate. Callers that
// need each step separately can still use ListConfigs + ApplyStore + Validate
// directly; this helper exists so main.go's bootstrap and reload paths can
// share one code path and so tests can drive the whole flow with a fake.
//
// Returns the first error encountered. On error the receiver is untouched
// (ApplyStore is atomic and Validate is a read-only check), so the caller is
// free to keep serving the previous Config on reload failure.
func (c *Config) MergeStoreLayer(s StoreLister) error {
	rows, err := s.ListConfigs()
	if err != nil {
		return fmt.Errorf("config: list store: %w", err)
	}
	if err := c.ApplyStore(rows); err != nil {
		return fmt.Errorf("config: apply store: %w", err)
	}
	if err := c.Validate(); err != nil {
		return fmt.Errorf("config: validate after store: %w", err)
	}
	return nil
}

// ApplyStore merges runtime-overridable config values written by the
// PUT /config handler on top of whatever is already in the Config (TOML +
// env vars). Precedence is TOML < env < store, so this step runs last.
//
// The handler stores string values bare and everything else as JSON, so the
// decoding here is symmetric to handlers.go:handlePutConfig.
//
// Unknown keys are logged and skipped rather than rejected so a newer writer
// can't brick an older reader during a staggered deploy.
//
// Atomicity: the merge happens on a shadow copy of the Config and is
// promoted onto the receiver only if every row decoded successfully. A
// single malformed row therefore leaves the receiver untouched, so the
// caller's error-path ("continuing with TOML+env") is truthful.
//
// INVARIANT — shallow copy + wholesale replacement: `shadow := *c` is a
// shallow copy, so `shadow.AI.Agents` and `shadow.AI.Repos` (both maps)
// still share backing storage with the receiver. Today every case below
// *replaces the whole field* (slice/struct/string assignment) rather than
// mutating it in place, so the atomicity guarantee holds. If you ever add
// a case that writes into an existing map (e.g. `shadow.AI.Agents[k] = v`)
// you MUST deep-copy that map into the shadow first, or the mutation will
// leak through to the receiver even when a later row fails.
func (c *Config) ApplyStore(rows map[string]string) error {
	shadow := *c
	for key, raw := range rows {
		switch key {
		case "poll_interval":
			shadow.GitHub.PollInterval = raw
		case "ai_primary":
			shadow.AI.Primary = raw
		case "ai_fallback":
			shadow.AI.Fallback = raw
		case "review_mode":
			shadow.AI.ReviewMode = raw
		case "repositories":
			var repos []string
			if err := json.Unmarshal([]byte(raw), &repos); err != nil {
				return fmt.Errorf("config: apply store key %q: %w", key, err)
			}
			shadow.GitHub.Repositories = repos
		case "retention_days":
			var days int
			if err := json.Unmarshal([]byte(raw), &days); err != nil {
				return fmt.Errorf("config: apply store key %q: %w", key, err)
			}
			shadow.Retention.MaxDays = days
		case "activity_log_enabled":
			var enabled bool
			if err := json.Unmarshal([]byte(raw), &enabled); err != nil {
				return fmt.Errorf("config: apply store key %q: %w", key, err)
			}
			shadow.ActivityLog.Enabled = &enabled
		case "activity_log_retention_days":
			var days int
			if err := json.Unmarshal([]byte(raw), &days); err != nil {
				return fmt.Errorf("config: apply store key %q: %w", key, err)
			}
			shadow.ActivityLog.RetentionDays = &days
		case "issue_tracking":
			// Unmarshal INTO the existing struct (not a fresh zero value).
			// Go's encoding/json only overwrites fields the JSON mentions,
			// so fields absent from the stored payload keep whatever the
			// TOML+env layers already put there. Without this, a row
			// written by an older build that predates a field (e.g. pre-#93
			// save lacks blocked_labels) would silently zero-out the
			// env-supplied value on every reload.
			if err := json.Unmarshal([]byte(raw), &shadow.GitHub.IssueTracking); err != nil {
				return fmt.Errorf("config: apply store key %q: %w", key, err)
			}
		case "server_port":
			// Explicitly unsupported (not unknown): mutating the listening
			// port at runtime would invalidate every in-flight connection
			// and the web UI has no surface for it. Bootstrap-only.
			slog.Warn("config: server_port is bootstrap-only, ignoring store override", "key", key)
		default:
			slog.Warn("config: unknown store key, skipping", "key", key)
		}
	}
	*c = shadow
	return nil
}

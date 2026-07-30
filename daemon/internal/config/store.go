package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

// StoreLister is the subset of *store.Store that ApplyStore needs. Kept as a
// local interface so the config package stays free of a store dependency
// (avoids an import cycle and keeps tests able to inject fakes).
type StoreLister interface {
	ListConfigs() (map[string]string, error)
}

// MergeStoreLayer fetches rows, applies them atomically, then re-validates.
// Most store-backed keys are operator overrides from the legacy PUT /config
// path and therefore still sit above TOML+env. Repository lists are different:
// the poller also writes them as runtime discovery state, so ApplyStore merges
// those rows below explicit TOML/env values.
//
// Returns the first error encountered. The complete apply+validate operation
// happens on a shadow copy, so a row that decodes successfully but makes the
// resulting config invalid cannot leak into the receiver.
func (c *Config) MergeStoreLayer(s StoreLister) error {
	rows, err := s.ListConfigs()
	if err != nil {
		return fmt.Errorf("config: list store: %w", err)
	}
	shadow := cloneStoreMergeConfig(c)
	if err := shadow.ApplyStore(rows); err != nil {
		return fmt.Errorf("config: apply store: %w", err)
	}
	if err := shadow.Validate(); err != nil {
		return fmt.Errorf("config: validate after store: %w", err)
	}
	*c = shadow
	return nil
}

// ApplyStore merges runtime-overridable config values written by the
// PUT /config handler on top of whatever is already in the Config (TOML +
// env vars). Most keys use TOML < env < store precedence.
//
// Repository lists are the exception. Auto-discovery also writes
// repositories/non_monitored rows, so those store rows are treated as
// lower-priority runtime additions: store-only repos are kept, but explicit
// TOML/env list entries win conflicts.
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
// cloneStoreMergeConfig copies the mutable slices touched below, and the
// agent_configs branch replaces its map wholesale. If a future store key
// mutates another map/slice in place, add it to that helper first.
func (c *Config) ApplyStore(rows map[string]string) error {
	shadow := cloneStoreMergeConfig(c)
	var storeRepos []string
	var storeNonMonitored []string
	var sawStoreRepos bool
	var sawStoreNonMonitored bool
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
		case "refinement_timeout":
			shadow.AI.RefinementTimeout = raw
		case "repositories":
			var repos []string
			if err := json.Unmarshal([]byte(raw), &repos); err != nil {
				return fmt.Errorf("config: apply store key %q: %w", key, err)
			}
			storeRepos = repos
			sawStoreRepos = true
		case "non_monitored":
			var nm []string
			if err := json.Unmarshal([]byte(raw), &nm); err != nil {
				return fmt.Errorf("config: apply store key %q: %w", key, err)
			}
			storeNonMonitored = nm
			sawStoreNonMonitored = true
		case "repo_first_seen":
			// Auxiliary data read directly from the store by the HTTP
			// config handler (to render NEW badges) — not applied to the
			// Config struct. Acknowledged here so ApplyStore doesn't emit
			// a noisy "unknown store key" warning on every reload.
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
		case "agent_configs":
			// Per-CLI agent overrides written by the Flutter Agents tab
			// (PUT /config). Each named CLI gets a partial JSON object;
			// fields the JSON omits keep their TOML/env values. Unmarshal
			// INTO a copy of the existing CLIAgentConfig so partial
			// payloads don't zero out fields the operator left untouched.
			//
			// Deep-copy the Agents map onto the shadow first: the outer
			// `shadow := *c` is a shallow copy, so without this branch's
			// own copy a per-key write would leak through to the receiver
			// even when a later row fails (see INVARIANT comment above).
			var perCLI map[string]json.RawMessage
			if err := json.Unmarshal([]byte(raw), &perCLI); err != nil {
				return fmt.Errorf("config: apply store key %q: %w", key, err)
			}
			merged := make(map[string]CLIAgentConfig, len(shadow.AI.Agents)+len(perCLI))
			for k, v := range shadow.AI.Agents {
				merged[k] = v
			}
			for cli, payload := range perCLI {
				existing := merged[cli]
				trustedDangerouslySkipPerms := existing.DangerouslySkipPerms
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(payload, &fields); err != nil {
					return fmt.Errorf("config: apply store key %q: agent %q: %w", key, cli, err)
				}
				for field := range fields {
					if strings.EqualFold(field, "dangerously_skip_perms") {
						slog.Warn("config: ignoring HTTP/store override of dangerously_skip_perms (M-5 gate)",
							"agent", cli)
					}
				}
				if err := json.Unmarshal(payload, &existing); err != nil {
					return fmt.Errorf("config: apply store key %q: agent %q: %w", key, cli, err)
				}
				// Store rows originate from the HTTP API and are never trusted
				// to alter this filesystem-only gate. Preserve the TOML/env
				// value even when a legacy row contains a case-variant alias.
				existing.DangerouslySkipPerms = trustedDangerouslySkipPerms
				merged[cli] = existing
			}
			shadow.AI.Agents = merged
		case "server_port":
			// Explicitly unsupported (not unknown): mutating the listening
			// port at runtime would invalidate every in-flight connection
			// and the web UI has no surface for it. Bootstrap-only.
			slog.Warn("config: server_port is bootstrap-only, ignoring store override", "key", key)
		default:
			slog.Warn("config: unknown store key, skipping", "key", key)
		}
	}
	if sawStoreRepos || sawStoreNonMonitored {
		mergeStoreRepoLists(&shadow, storeRepos, storeNonMonitored)
	}
	*c = shadow
	return nil
}

func cloneStoreMergeConfig(c *Config) Config {
	shadow := *c
	shadow.GitHub.Repositories = cloneStrings(c.GitHub.Repositories)
	shadow.GitHub.NonMonitored = cloneStrings(c.GitHub.NonMonitored)
	shadow.GitHub.IssueTracking = cloneIssueTrackingConfig(c.GitHub.IssueTracking)
	return shadow
}

func cloneIssueTrackingConfig(in IssueTrackingConfig) IssueTrackingConfig {
	out := in
	out.Organizations = cloneStrings(in.Organizations)
	out.Assignees = cloneStrings(in.Assignees)
	out.DevelopLabels = cloneStrings(in.DevelopLabels)
	out.RefinementLabels = cloneStrings(in.RefinementLabels)
	out.ReviewOnlyLabels = cloneStrings(in.ReviewOnlyLabels)
	out.SkipLabels = cloneStrings(in.SkipLabels)
	out.BlockedLabels = cloneStrings(in.BlockedLabels)
	return out
}

func cloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	return append([]string{}, in...)
}

func mergeStoreRepoLists(c *Config, storeRepos, storeNonMonitored []string) {
	repoSet := make(map[string]struct{}, len(c.GitHub.Repositories)+len(storeRepos))
	for _, repo := range c.GitHub.Repositories {
		repoSet[repo] = struct{}{}
	}
	nonMonitoredSet := make(map[string]struct{}, len(c.GitHub.NonMonitored)+len(storeNonMonitored))
	for _, repo := range c.GitHub.NonMonitored {
		nonMonitoredSet[repo] = struct{}{}
	}

	// Within the store layer, non_monitored keeps the old effective behavior:
	// a repo in both store lists is not monitored. Explicit TOML/env
	// repositories still win because repoSet is checked before appending.
	storeNonMonitoredSet := make(map[string]struct{}, len(storeNonMonitored))
	for _, repo := range storeNonMonitored {
		storeNonMonitoredSet[repo] = struct{}{}
		if _, monitored := repoSet[repo]; monitored {
			continue
		}
		if _, exists := nonMonitoredSet[repo]; exists {
			continue
		}
		c.GitHub.NonMonitored = append(c.GitHub.NonMonitored, repo)
		nonMonitoredSet[repo] = struct{}{}
	}

	// If HEIMDALLM_REPOSITORIES is set, the deployment-provided monitored list
	// is authoritative. Keep store non_monitored rows for UI/history, but do
	// not append store-only monitored repositories.
	if _, envReposSet := csvEnv("HEIMDALLM_REPOSITORIES"); envReposSet {
		return
	}

	for _, repo := range storeRepos {
		if _, monitored := repoSet[repo]; monitored {
			continue
		}
		if _, disabled := nonMonitoredSet[repo]; disabled {
			continue
		}
		if _, disabledInStore := storeNonMonitoredSet[repo]; disabledInStore {
			continue
		}
		c.GitHub.Repositories = append(c.GitHub.Repositories, repo)
		repoSet[repo] = struct{}{}
	}
}

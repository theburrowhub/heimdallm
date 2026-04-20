# Repo Detail Screen + Per-Repo Issue Tracking — Design Spec

**Date**: 2026-04-20
**Scope**: Dedicated repo settings page, per-field override pattern, daemon per-repo issue tracking

---

## Overview

Replace the inline `ExpansionTile` in the Repositories tab with a dedicated `RepoDetailScreen` — a full-page settings view for a single repo. Every configurable option lives on this page, with a per-field override pattern: each field shows the effective value (global or overridden), and an override indicator + reset button appears when the value differs from global.

The daemon gains per-repo issue tracking config with field-level merging.

---

## 1. Navigation

The repo list (`repos_screen.dart`) becomes a simple list of cards:
- **Toggle switch** stays inline for quick enable/disable without navigating
- **Tap anywhere else on the card** navigates to `/repos/:name` (URL-encoded `org/repo`)
- The `ExpansionTile` with AI dropdowns is removed — all config moves to the detail page

New route: `GoRoute(path: '/repos/:name', builder: ...)` in `router.dart`.

---

## 2. RepoDetailScreen — Layout

Single scrollable page with 4 section cards, top to bottom:

### 2.1 General
- Local directory (text field + file picker, same `_LocalDirField` widget)
- Review mode (dropdown: Global / single / multi)

### 2.2 AI Agent
- Primary (dropdown: Global / claude / gemini / codex)
- Fallback (dropdown: Global / claude / gemini / codex)
- Prompt (dropdown: Global active / list of agent profiles)

### 2.3 Issue Tracking
- Develop labels (comma-separated text)
- Review-only labels (comma-separated text)
- Skip labels (comma-separated text)
- Filter mode (dropdown: Global / exclusive / inclusive)
- Default action (dropdown: Global / ignore / review_only)
- Organizations (comma-separated text)
- Assignees (comma-separated text)

### 2.4 PR Metadata (auto_implement)
- Reviewers (comma-separated text)
- Assignee (text)
- Labels (comma-separated text)
- Draft (checkbox / Global)

AppBar: back button + repo full name. Auto-save with 800ms debounce (same pattern as current `repos_screen.dart`).

---

## 3. Per-Field Override Pattern

Every field in the detail screen follows this pattern:

### Inherited (no override)
- Value shown in muted text (grey)
- Badge: "global" in grey
- Input placeholder or disabled appearance showing global value
- Typing a value creates an override

### Overridden
- Green left border on the field container
- Badge: "overridden" in green
- `x reset` button that clears the override (one click)
- Below the input: "Global: ..." hint text showing what the global value is
- The input shows the repo-specific value in normal (non-muted) text

### Reset behavior
- Clicking reset clears the repo-level value, field reverts to inherited
- Emptying a field manually = same as reset (no "override with empty string")

### Reusable widget
`OverrideField` — a generic widget parameterized by:
- `globalValue` (String or List<String>)
- `overrideValue` (nullable — null means inherited)
- `onChanged(value?)` — null = reset to global
- `label`, `helper` text
- Widget type (text field, dropdown, checkbox)

---

## 4. Model Changes — Flutter

### RepoConfig
```dart
class RepoConfig {
  // Existing
  final bool monitored;
  final String? aiPrimary;
  final String? aiFallback;
  final String? promptId;
  final String? reviewMode;
  final String? localDir;

  // Issue tracking overrides (null = inherit global)
  final List<String>? developLabels;
  final List<String>? reviewOnlyLabels;
  final List<String>? skipLabels;
  final String? issueFilterMode;
  final String? issueDefaultAction;
  final List<String>? issueOrganizations;
  final List<String>? issueAssignees;

  // PR metadata (null = inherit global)
  final List<String>? prReviewers;
  final String? prAssignee;
  final List<String>? prLabels;
  final bool? prDraft;
}
```

### AppConfig.fromJson
`repo_overrides` from the daemon already carries per-repo fields. Extend parsing to read issue tracking and PR metadata sub-objects when present.

### AppConfig.toJson
Serialize per-repo issue tracking overrides as `issue_tracking` sub-object within each repo override. Only include non-null fields.

---

## 5. Daemon — Per-Repo Issue Tracking

### Config
`RepoAI` gains:
```go
IssueTracking *IssueTrackingConfig `toml:"issue_tracking,omitempty"`
```

New helper:
```go
func (c *Config) IssueTrackingForRepo(repo string) IssueTrackingConfig {
    global := c.GitHub.IssueTracking
    if c.AI.Repos == nil {
        return global
    }
    r, ok := c.AI.Repos[repo]
    if !ok || r.IssueTracking == nil {
        return global
    }
    // Field-level merge: per-repo wins when non-zero
    merged := global
    override := r.IssueTracking
    if len(override.DevelopLabels) > 0 { merged.DevelopLabels = override.DevelopLabels }
    if len(override.ReviewOnlyLabels) > 0 { merged.ReviewOnlyLabels = override.ReviewOnlyLabels }
    if len(override.SkipLabels) > 0 { merged.SkipLabels = override.SkipLabels }
    if override.FilterMode != "" { merged.FilterMode = override.FilterMode }
    if override.DefaultAction != "" { merged.DefaultAction = override.DefaultAction }
    if len(override.Organizations) > 0 { merged.Organizations = override.Organizations }
    if len(override.Assignees) > 0 { merged.Assignees = override.Assignees }
    return merged
}
```

### Poll cycle (main.go)
Replace:
```go
itCfg := c.GitHub.IssueTracking
// ... same for all repos
```
With:
```go
for _, repo := range repos {
    repoIT := c.IssueTrackingForRepo(repo)
    issueFetcher.ProcessRepo(ctx, repo, repoIT, authUser, optsFor)
}
```

### GET /config response
`repo_overrides` gains `issue_tracking` sub-object per repo (only when override exists).

---

## 6. TOML Serialization

Per-repo issue tracking writes as nested TOML table:
```toml
[ai.repos."org/repo"]
primary = "claude"
local_dir = "/path/to/repo"

[ai.repos."org/repo".issue_tracking]
develop_labels = ["security-fix", "critical-bug"]
skip_labels = ["wontfix", "stale"]
```

Only non-null/non-empty override fields are written. The Flutter TOML writer handles this in `_buildToml`.

---

## 7. Files Changed

### Daemon (Go)
| File | Change |
|------|--------|
| `daemon/internal/config/config.go` | `RepoAI.IssueTracking`, `IssueTrackingForRepo()` |
| `daemon/cmd/heimdallm/main.go` | Poll cycle uses per-repo issue tracking |
| `daemon/internal/config/config_test.go` | Tests for `IssueTrackingForRepo` merging |

### Flutter (Dart)
| File | Change |
|------|--------|
| `lib/core/models/config_model.dart` | `RepoConfig` + issue tracking + PR metadata fields |
| `lib/features/repositories/repos_screen.dart` | Simplify to list + navigate (remove ExpansionTile) |
| `lib/features/repositories/repo_detail_screen.dart` | **New** — full settings page |
| `lib/shared/widgets/override_field.dart` | **New** — reusable per-field override widget |
| `lib/shared/router.dart` | Add `/repos/:name` route |
| `lib/core/setup/first_run_setup.dart` | TOML writer for per-repo issue tracking |

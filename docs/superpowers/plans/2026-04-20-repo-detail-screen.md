# Repo Detail Screen Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace inline repo config with a dedicated RepoDetailScreen and add per-repo issue tracking with field-level override/merge.

**Architecture:** Daemon gets `RepoAI.IssueTracking *IssueTrackingConfig` with a `IssueTrackingForRepo(repo)` merge helper; poll cycle uses per-repo config. Flutter gets a new `RepoDetailScreen` navigated from the simplified repo list, with a reusable `OverrideField` widget for the per-field inherit/override pattern. `GET /config` response expands `repo_overrides` to include issue tracking sub-objects.

**Tech Stack:** Go 1.21 (config, chi router), Flutter 3.8+ (Riverpod, GoRouter)

---

## File Structure

### Daemon (Go) — files to modify

| File | Responsibility |
|------|---------------|
| `daemon/internal/config/config.go` | Add `RepoAI.IssueTracking`, `IssueTrackingForRepo()` |
| `daemon/internal/config/config_test.go` | Tests for field-level merge |
| `daemon/cmd/heimdallm/main.go` | Poll cycle uses per-repo config, `GET /config` includes per-repo IT |

### Flutter (Dart) — files to create/modify

| File | Responsibility |
|------|---------------|
| `lib/core/models/config_model.dart` | Extend `RepoConfig` with IT + PR metadata fields, parse/serialize |
| `lib/shared/widgets/override_field.dart` | **Create** — reusable per-field override widget |
| `lib/features/repositories/repo_detail_screen.dart` | **Create** — full settings page for one repo |
| `lib/features/repositories/repos_screen.dart` | Simplify: remove ExpansionTile, add tap-to-navigate |
| `lib/shared/router.dart` | Add `/repos/:name` route |
| `lib/core/setup/first_run_setup.dart` | TOML writer: per-repo `[ai.repos."org/repo".issue_tracking]` |

---

### Task 1: Daemon — per-repo issue tracking config + merge

**Files:**
- Modify: `daemon/internal/config/config.go`
- Modify: `daemon/internal/config/config_test.go`

- [ ] **Step 1: Write failing test for IssueTrackingForRepo**

Add to `daemon/internal/config/config_test.go`:

```go
func TestIssueTrackingForRepo_GlobalOnly(t *testing.T) {
	c := &Config{}
	c.GitHub.IssueTracking = IssueTrackingConfig{
		Enabled:       true,
		FilterMode:    FilterModeExclusive,
		DevelopLabels: []string{"bug", "feature"},
		SkipLabels:    []string{"wontfix"},
		DefaultAction: "ignore",
	}
	got := c.IssueTrackingForRepo("org/repo")
	if !got.Enabled || got.FilterMode != FilterModeExclusive {
		t.Errorf("expected global values, got %+v", got)
	}
	if len(got.DevelopLabels) != 2 || got.DevelopLabels[0] != "bug" {
		t.Errorf("develop_labels = %v, want [bug feature]", got.DevelopLabels)
	}
}

func TestIssueTrackingForRepo_PerRepoOverride(t *testing.T) {
	c := &Config{}
	c.GitHub.IssueTracking = IssueTrackingConfig{
		Enabled:       true,
		FilterMode:    FilterModeExclusive,
		DevelopLabels: []string{"bug", "feature"},
		SkipLabels:    []string{"wontfix"},
		DefaultAction: "ignore",
	}
	c.AI.Primary = "claude"
	c.AI.Repos = map[string]RepoAI{
		"org/secure-repo": {
			IssueTracking: &IssueTrackingConfig{
				DevelopLabels: []string{"security-fix"},
				SkipLabels:    []string{"wontfix", "stale"},
			},
		},
	}
	got := c.IssueTrackingForRepo("org/secure-repo")
	// Overridden fields
	if len(got.DevelopLabels) != 1 || got.DevelopLabels[0] != "security-fix" {
		t.Errorf("develop_labels = %v, want [security-fix]", got.DevelopLabels)
	}
	if len(got.SkipLabels) != 2 {
		t.Errorf("skip_labels = %v, want [wontfix stale]", got.SkipLabels)
	}
	// Inherited fields
	if got.FilterMode != FilterModeExclusive {
		t.Errorf("filter_mode = %v, want exclusive (inherited)", got.FilterMode)
	}
	if got.DefaultAction != "ignore" {
		t.Errorf("default_action = %v, want ignore (inherited)", got.DefaultAction)
	}
	if !got.Enabled {
		t.Error("enabled should be inherited as true")
	}
}

func TestIssueTrackingForRepo_UnknownRepo(t *testing.T) {
	c := &Config{}
	c.GitHub.IssueTracking = IssueTrackingConfig{
		Enabled:    true,
		SkipLabels: []string{"wontfix"},
	}
	got := c.IssueTrackingForRepo("org/unknown")
	if !got.Enabled || len(got.SkipLabels) != 1 {
		t.Errorf("unknown repo should return global, got %+v", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd daemon && go test ./internal/config/ -run "TestIssueTrackingForRepo" -v -timeout 30s`
Expected: FAIL — `IssueTrackingForRepo` not defined.

- [ ] **Step 3: Add IssueTracking field to RepoAI and implement IssueTrackingForRepo**

In `daemon/internal/config/config.go`:

1. Add field to `RepoAI` (after `PRDraft`):
```go
	// Per-repo issue tracking override. When set, non-zero fields replace
	// the global [github.issue_tracking] values for this repo only.
	IssueTracking *IssueTrackingConfig `toml:"issue_tracking,omitempty" json:"issue_tracking,omitempty"`
```

2. Add method after `AgentConfigFor`:
```go
// IssueTrackingForRepo returns the issue tracking config for a specific repo,
// merging per-repo overrides (field-level) with the global config.
// Non-zero per-repo fields win; zero/nil fields inherit from global.
func (c *Config) IssueTrackingForRepo(repo string) IssueTrackingConfig {
	global := c.GitHub.IssueTracking
	if c.AI.Repos == nil {
		return global
	}
	r, ok := c.AI.Repos[repo]
	if !ok || r.IssueTracking == nil {
		return global
	}
	ov := r.IssueTracking
	merged := global
	if len(ov.DevelopLabels) > 0 {
		merged.DevelopLabels = ov.DevelopLabels
	}
	if len(ov.ReviewOnlyLabels) > 0 {
		merged.ReviewOnlyLabels = ov.ReviewOnlyLabels
	}
	if len(ov.SkipLabels) > 0 {
		merged.SkipLabels = ov.SkipLabels
	}
	if ov.FilterMode != "" {
		merged.FilterMode = ov.FilterMode
	}
	if ov.DefaultAction != "" {
		merged.DefaultAction = ov.DefaultAction
	}
	if len(ov.Organizations) > 0 {
		merged.Organizations = ov.Organizations
	}
	if len(ov.Assignees) > 0 {
		merged.Assignees = ov.Assignees
	}
	return merged
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd daemon && go test ./internal/config/ -run "TestIssueTrackingForRepo" -v -timeout 30s`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add daemon/internal/config/config.go daemon/internal/config/config_test.go
git commit -m "feat(config): add per-repo issue tracking with field-level merge

RepoAI gains IssueTracking *IssueTrackingConfig for per-repo overrides.
IssueTrackingForRepo(repo) merges per-repo non-zero fields over global."
```

---

### Task 2: Daemon — poll cycle uses per-repo config + GET /config includes IT overrides

**Files:**
- Modify: `daemon/cmd/heimdallm/main.go`

- [ ] **Step 1: Update poll cycle to use IssueTrackingForRepo**

In `daemon/cmd/heimdallm/main.go`, replace in the issue tracking cycle section (around line 287-350):

Change:
```go
cfgMu.Lock()
itCfg := c.GitHub.IssueTracking
cfgMu.Unlock()
if itCfg.Enabled {
    // ...
    for _, repo := range repos {
        n, err := issueFetcher.ProcessRepo(context.Background(), repo, itCfg, authUser, optsFor)
```

To:
```go
cfgMu.Lock()
globalIT := c.GitHub.IssueTracking
cfgMu.Unlock()
if globalIT.Enabled {
    // ...
    for _, repo := range repos {
        cfgMu.Lock()
        repoIT := c.IssueTrackingForRepo(repo)
        cfgMu.Unlock()
        n, err := issueFetcher.ProcessRepo(context.Background(), repo, repoIT, authUser, optsFor)
```

- [ ] **Step 2: Update GET /config to include per-repo issue tracking**

In `srv.SetConfigFn(...)` (around line 411-418), change the `repoOverrides` builder from `map[string]map[string]string` to `map[string]map[string]any`:

```go
repoOverrides := make(map[string]map[string]any)
for repo, ai := range c.AI.Repos {
    ro := map[string]any{
        "primary":     ai.Primary,
        "fallback":    ai.Fallback,
        "review_mode": ai.ReviewMode,
        "local_dir":   ai.LocalDir,
    }
    if ai.IssueTracking != nil {
        ro["issue_tracking"] = ai.IssueTracking
    }
    repoOverrides[repo] = ro
}
```

- [ ] **Step 3: Verify daemon builds**

Run: `cd daemon && go build -o bin/heimdallm ./cmd/heimdallm`
Expected: Build succeeds.

- [ ] **Step 4: Run full daemon tests**

Run: `cd /path/to/project && make test-docker`
Expected: All tests pass.

- [ ] **Step 5: Commit**

```bash
git add daemon/cmd/heimdallm/main.go
git commit -m "feat(issues): poll cycle uses per-repo issue tracking config

Each repo now gets its own merged IssueTrackingConfig in the poll cycle.
GET /config includes per-repo issue_tracking overrides in repo_overrides."
```

---

### Task 3: Flutter — extend RepoConfig model with IT + PR metadata fields

**Files:**
- Modify: `flutter_app/lib/core/models/config_model.dart`

- [ ] **Step 1: Add fields to RepoConfig**

Add after `localDir`:

```dart
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
```

Update constructor with all new fields (defaults all null). Update `hasAiOverride` getter to include the new fields. Update `copyWith` with all new fields using the `_sentinel` pattern.

- [ ] **Step 2: Update AppConfig.fromJson to parse per-repo IT overrides**

In the `repo_overrides` parsing section, after parsing `local_dir`, add:

```dart
final itRaw = ov['issue_tracking'] as Map<String, dynamic>?;
List<String>? _nullableList(dynamic v) =>
    (v as List<dynamic>?)?.cast<String>().where((s) => s.isNotEmpty).toList();

configs[entry.key] = RepoConfig(
  monitored:          existing?.monitored ?? configs.containsKey(entry.key),
  aiPrimary:          _nonEmpty(ov['primary']),
  aiFallback:         _nonEmpty(ov['fallback']),
  reviewMode:         _nonEmpty(ov['review_mode']),
  localDir:           _nonEmpty(ov['local_dir']),
  developLabels:      itRaw != null ? _nullableList(itRaw['develop_labels']) : null,
  reviewOnlyLabels:   itRaw != null ? _nullableList(itRaw['review_only_labels']) : null,
  skipLabels:         itRaw != null ? _nullableList(itRaw['skip_labels']) : null,
  issueFilterMode:    itRaw != null ? _nonEmpty(itRaw['filter_mode']) : null,
  issueDefaultAction: itRaw != null ? _nonEmpty(itRaw['default_action']) : null,
  issueOrganizations: itRaw != null ? _nullableList(itRaw['organizations']) : null,
  issueAssignees:     itRaw != null ? _nullableList(itRaw['assignees']) : null,
  prReviewers:        _nullableList(ov['pr_reviewers']),
  prAssignee:         _nonEmpty(ov['pr_assignee']),
  prLabels:           _nullableList(ov['pr_labels']),
  prDraft:            ov['pr_draft'] as bool?,
);
```

- [ ] **Step 3: Verify no analysis errors**

Run: `cd flutter_app && flutter analyze`
Expected: No issues found.

- [ ] **Step 4: Commit**

```bash
git add flutter_app/lib/core/models/config_model.dart
git commit -m "feat(flutter): extend RepoConfig with IT + PR metadata fields

Per-repo issue tracking and PR metadata fields (all nullable = inherit
global). fromJson parses issue_tracking sub-object from repo_overrides."
```

---

### Task 4: Flutter — OverrideField reusable widget

**Files:**
- Create: `flutter_app/lib/shared/widgets/override_field.dart`

- [ ] **Step 1: Create the widget**

```dart
import 'package:flutter/material.dart';

/// A field that shows the effective value (global or overridden) with
/// visual indicators and a reset-to-global control.
///
/// When [overrideValue] is null, the field shows [globalValue] in muted
/// style with a "global" badge. When non-null, it shows the override
/// with a green left border, "overridden" badge, and reset button.
class OverrideTextField extends StatefulWidget {
  final String label;
  final String? helper;
  final String globalValue;
  final String? overrideValue;
  final ValueChanged<String?> onChanged;

  const OverrideTextField({
    super.key,
    required this.label,
    this.helper,
    required this.globalValue,
    required this.overrideValue,
    required this.onChanged,
  });

  @override
  State<OverrideTextField> createState() => _OverrideTextFieldState();
}

class _OverrideTextFieldState extends State<OverrideTextField> {
  late TextEditingController _ctrl;

  @override
  void initState() {
    super.initState();
    _ctrl = TextEditingController(text: widget.overrideValue ?? widget.globalValue);
  }

  @override
  void didUpdateWidget(OverrideTextField old) {
    super.didUpdateWidget(old);
    if (widget.overrideValue != old.overrideValue ||
        widget.globalValue != old.globalValue) {
      _ctrl.text = widget.overrideValue ?? widget.globalValue;
    }
  }

  @override
  void dispose() {
    _ctrl.dispose();
    super.dispose();
  }

  bool get _isOverridden => widget.overrideValue != null;

  void _reset() {
    _ctrl.text = widget.globalValue;
    widget.onChanged(null);
  }

  void _handleChange(String value) {
    final trimmed = value.trim();
    if (trimmed.isEmpty || trimmed == widget.globalValue) {
      widget.onChanged(null);
    } else {
      widget.onChanged(trimmed);
    }
  }

  @override
  Widget build(BuildContext context) {
    return Container(
      decoration: BoxDecoration(
        color: Theme.of(context).colorScheme.surfaceContainerHighest.withValues(alpha: 0.3),
        borderRadius: BorderRadius.circular(6),
        border: Border(
          left: BorderSide(
            width: 3,
            color: _isOverridden ? Colors.green.shade600 : Colors.transparent,
          ),
        ),
      ),
      padding: const EdgeInsets.all(10),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Text(widget.label,
                  style: TextStyle(fontSize: 11, color: Colors.grey.shade500)),
              const Spacer(),
              if (_isOverridden) ...[
                Container(
                  padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 1),
                  decoration: BoxDecoration(
                    color: Colors.green.shade900.withValues(alpha: 0.4),
                    borderRadius: BorderRadius.circular(3),
                  ),
                  child: Text('overridden',
                      style: TextStyle(fontSize: 10, color: Colors.green.shade400)),
                ),
                const SizedBox(width: 6),
                GestureDetector(
                  onTap: _reset,
                  child: Container(
                    padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 1),
                    decoration: BoxDecoration(
                      border: Border.all(color: Colors.grey.shade700),
                      borderRadius: BorderRadius.circular(3),
                    ),
                    child: Text('\u00d7 reset',
                        style: TextStyle(fontSize: 10, color: Colors.grey.shade500)),
                  ),
                ),
              ] else
                Text('global',
                    style: TextStyle(fontSize: 10, color: Colors.grey.shade600)),
            ],
          ),
          const SizedBox(height: 6),
          TextFormField(
            controller: _ctrl,
            style: TextStyle(
              fontSize: 12,
              color: _isOverridden ? null : Colors.grey.shade600,
            ),
            decoration: InputDecoration(
              isDense: true,
              border: const OutlineInputBorder(),
              helperText: widget.helper,
              helperMaxLines: 2,
              contentPadding: const EdgeInsets.symmetric(horizontal: 8, vertical: 8),
            ),
            onChanged: _handleChange,
          ),
          if (_isOverridden)
            Padding(
              padding: const EdgeInsets.only(top: 4),
              child: Text('Global: ${widget.globalValue}',
                  style: TextStyle(fontSize: 10, color: Colors.grey.shade700)),
            ),
        ],
      ),
    );
  }
}

/// Dropdown variant of the override field.
class OverrideDropdown extends StatelessWidget {
  final String label;
  final String globalValue;
  final String? overrideValue;
  final List<String> options;
  final ValueChanged<String?> onChanged;

  const OverrideDropdown({
    super.key,
    required this.label,
    required this.globalValue,
    required this.overrideValue,
    required this.options,
    required this.onChanged,
  });

  bool get _isOverridden => overrideValue != null;

  @override
  Widget build(BuildContext context) {
    return Container(
      decoration: BoxDecoration(
        color: Theme.of(context).colorScheme.surfaceContainerHighest.withValues(alpha: 0.3),
        borderRadius: BorderRadius.circular(6),
        border: Border(
          left: BorderSide(
            width: 3,
            color: _isOverridden ? Colors.green.shade600 : Colors.transparent,
          ),
        ),
      ),
      padding: const EdgeInsets.all(10),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Text(label,
                  style: TextStyle(fontSize: 11, color: Colors.grey.shade500)),
              const Spacer(),
              if (_isOverridden) ...[
                Container(
                  padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 1),
                  decoration: BoxDecoration(
                    color: Colors.green.shade900.withValues(alpha: 0.4),
                    borderRadius: BorderRadius.circular(3),
                  ),
                  child: Text('overridden',
                      style: TextStyle(fontSize: 10, color: Colors.green.shade400)),
                ),
                const SizedBox(width: 6),
                GestureDetector(
                  onTap: () => onChanged(null),
                  child: Container(
                    padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 1),
                    decoration: BoxDecoration(
                      border: Border.all(color: Colors.grey.shade700),
                      borderRadius: BorderRadius.circular(3),
                    ),
                    child: Text('\u00d7 reset',
                        style: TextStyle(fontSize: 10, color: Colors.grey.shade500)),
                  ),
                ),
              ] else
                Text('global',
                    style: TextStyle(fontSize: 10, color: Colors.grey.shade600)),
            ],
          ),
          const SizedBox(height: 6),
          DropdownButtonFormField<String?>(
            value: overrideValue,
            decoration: InputDecoration(
              isDense: true,
              border: const OutlineInputBorder(),
              contentPadding: const EdgeInsets.symmetric(horizontal: 8, vertical: 8),
            ),
            items: [
              DropdownMenuItem<String?>(
                value: null,
                child: Text('Global ($globalValue)',
                    style: TextStyle(color: Colors.grey.shade600, fontSize: 12)),
              ),
              ...options.map((v) => DropdownMenuItem<String?>(
                    value: v,
                    child: Text(v, style: const TextStyle(fontSize: 12)),
                  )),
            ],
            onChanged: onChanged,
          ),
        ],
      ),
    );
  }
}
```

- [ ] **Step 2: Verify no analysis errors**

Run: `cd flutter_app && flutter analyze`
Expected: No issues found.

- [ ] **Step 3: Commit**

```bash
git add flutter_app/lib/shared/widgets/override_field.dart
git commit -m "feat(flutter): add OverrideTextField and OverrideDropdown widgets

Reusable per-field override pattern: shows inherited global value in
muted style or overridden value with green border + reset button."
```

---

### Task 5: Flutter — RepoDetailScreen

**Files:**
- Create: `flutter_app/lib/features/repositories/repo_detail_screen.dart`

- [ ] **Step 1: Create RepoDetailScreen**

This file is large — it contains 4 section cards (General, AI Agent, Issue Tracking, PR Metadata), each using the `OverrideField` widgets. Auto-save with 800ms debounce.

The screen receives the repo name as a path parameter, looks up its `RepoConfig` from `configNotifierProvider`, and renders all fields.

Key structure:
```dart
class RepoDetailScreen extends ConsumerStatefulWidget {
  final String repoName;
  const RepoDetailScreen({super.key, required this.repoName});
  // ...
}

class _RepoDetailScreenState extends ConsumerState<RepoDetailScreen> {
  RepoConfig _config = const RepoConfig();
  bool _initialized = false;
  Timer? _debounce;
  // ...

  void _update(RepoConfig updated) {
    setState(() => _config = updated);
    _debounce?.cancel();
    _debounce = Timer(const Duration(milliseconds: 800), _autoSave);
  }

  Widget build(BuildContext context) {
    // 4 section cards: _generalSection, _aiSection, _issueTrackingSection, _prMetadataSection
  }
}
```

Each section uses `OverrideTextField` / `OverrideDropdown` with `globalValue` from `AppConfig` and `overrideValue` from `RepoConfig`. The `onChanged` calls `_update(config.copyWith(...))`.

The implementation should follow the mockup layout from the spec: section cards with the per-field override pattern for every configurable field.

- [ ] **Step 2: Verify no analysis errors**

Run: `cd flutter_app && flutter analyze`
Expected: No issues found.

- [ ] **Step 3: Commit**

```bash
git add flutter_app/lib/features/repositories/repo_detail_screen.dart
git commit -m "feat(flutter): add RepoDetailScreen with all config sections

Full-page repo settings: General (local dir, review mode), AI Agent
(primary, fallback, prompt), Issue Tracking (labels, filter mode),
PR Metadata (reviewers, assignee, labels, draft). Per-field override
pattern with auto-save."
```

---

### Task 6: Flutter — simplify repos_screen + add route

**Files:**
- Modify: `flutter_app/lib/features/repositories/repos_screen.dart`
- Modify: `flutter_app/lib/shared/router.dart`

- [ ] **Step 1: Add route to router.dart**

Add import:
```dart
import '../features/repositories/repo_detail_screen.dart';
```

Add route after `/issues/:id`:
```dart
GoRoute(
  path: '/repos/:name',
  builder: (context, state) {
    final name = Uri.decodeComponent(state.pathParameters['name']!);
    return RepoDetailScreen(repoName: name);
  },
),
```

- [ ] **Step 2: Simplify _RepoTile in repos_screen.dart**

Replace the `ExpansionTile` in `_RepoTile.build` with a simple `Card` + `InkWell` that navigates to `/repos/:name`:

```dart
@override
Widget build(BuildContext context) {
  final hasDirMapping = config.localDir != null && config.localDir!.isNotEmpty;
  final hasOverrides = config.hasAiOverride;

  return Card(
    margin: const EdgeInsets.symmetric(horizontal: 16, vertical: 3),
    child: InkWell(
      borderRadius: BorderRadius.circular(12),
      onTap: () => context.push('/repos/${Uri.encodeComponent(repo)}'),
      child: Padding(
        padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 10),
        child: Row(
          children: [
            Switch(
              value: config.monitored,
              onChanged: (v) => onChanged(config.copyWith(monitored: v)),
            ),
            const SizedBox(width: 8),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(repo,
                      style: TextStyle(
                        fontWeight: config.monitored ? FontWeight.w600 : FontWeight.normal,
                        color: config.monitored ? null : Colors.grey,
                      )),
                  const SizedBox(height: 2),
                  Row(children: [
                    Icon(
                      hasDirMapping ? Icons.folder : Icons.folder_off_outlined,
                      size: 13,
                      color: hasDirMapping ? Colors.green.shade500 : Colors.grey.shade600,
                    ),
                    const SizedBox(width: 4),
                    Text(
                      hasDirMapping
                          ? config.localDir!.split('/').last
                          : 'No local dir',
                      style: TextStyle(
                        fontSize: 11,
                        color: hasDirMapping ? Colors.green.shade500 : Colors.grey.shade600,
                      ),
                    ),
                    if (hasOverrides) ...[
                      const SizedBox(width: 8),
                      Icon(Icons.tune, size: 12, color: Colors.blue.shade400),
                      const SizedBox(width: 2),
                      Text('overrides',
                          style: TextStyle(fontSize: 10, color: Colors.blue.shade400)),
                    ],
                  ]),
                ],
              ),
            ),
            Icon(Icons.chevron_right, size: 18, color: Colors.grey.shade600),
          ],
        ),
      ),
    ),
  );
}
```

Remove the old `_aiDropdown`, `_promptDropdown`, `_overrideDropdown`, and `_LocalDirField` from `_RepoTile` — they move to `RepoDetailScreen`. Keep `_LocalDirField` as a standalone class at the bottom since `RepoDetailScreen` will import it, OR move it to a shared location. Simplest: keep `_LocalDirField` in `repos_screen.dart` and also copy it into `repo_detail_screen.dart` as a private widget.

- [ ] **Step 3: Verify no analysis errors**

Run: `cd flutter_app && flutter analyze`
Expected: No issues found.

- [ ] **Step 4: Run flutter tests**

Run: `cd flutter_app && flutter test`
Expected: All tests pass.

- [ ] **Step 5: Commit**

```bash
git add flutter_app/lib/shared/router.dart flutter_app/lib/features/repositories/repos_screen.dart
git commit -m "feat(flutter): simplify repo list + navigate to RepoDetailScreen

Repos tab now shows simple cards with toggle + tap-to-navigate.
ExpansionTile removed — all config lives in /repos/:name."
```

---

### Task 7: Flutter — TOML writer for per-repo issue tracking

**Files:**
- Modify: `flutter_app/lib/core/setup/first_run_setup.dart`

- [ ] **Step 1: Update _buildToml per-repo section**

In the per-repo overrides loop, after writing `local_dir`, add issue tracking sub-table:

```dart
// Issue tracking overrides
final hasIT = rc.developLabels != null || rc.reviewOnlyLabels != null ||
    rc.skipLabels != null || rc.issueFilterMode != null ||
    rc.issueDefaultAction != null || rc.issueOrganizations != null ||
    rc.issueAssignees != null;
if (hasIT) {
  buf.writeln('[ai.repos."${_tomlEscapeString(repo)}".issue_tracking]');
  if (rc.developLabels != null && rc.developLabels!.isNotEmpty) {
    buf.writeln('develop_labels = [${rc.developLabels!.map((l) => '"${_tomlEscapeString(l)}"').join(', ')}]');
  }
  if (rc.reviewOnlyLabels != null && rc.reviewOnlyLabels!.isNotEmpty) {
    buf.writeln('review_only_labels = [${rc.reviewOnlyLabels!.map((l) => '"${_tomlEscapeString(l)}"').join(', ')}]');
  }
  if (rc.skipLabels != null && rc.skipLabels!.isNotEmpty) {
    buf.writeln('skip_labels = [${rc.skipLabels!.map((l) => '"${_tomlEscapeString(l)}"').join(', ')}]');
  }
  if (rc.issueFilterMode != null) {
    buf.writeln('filter_mode = "${_tomlEscapeString(rc.issueFilterMode!)}"');
  }
  if (rc.issueDefaultAction != null) {
    buf.writeln('default_action = "${_tomlEscapeString(rc.issueDefaultAction!)}"');
  }
  if (rc.issueOrganizations != null && rc.issueOrganizations!.isNotEmpty) {
    buf.writeln('organizations = [${rc.issueOrganizations!.map((o) => '"${_tomlEscapeString(o)}"').join(', ')}]');
  }
  if (rc.issueAssignees != null && rc.issueAssignees!.isNotEmpty) {
    buf.writeln('assignees = [${rc.issueAssignees!.map((a) => '"${_tomlEscapeString(a)}"').join(', ')}]');
  }
  buf.writeln();
}
```

- [ ] **Step 2: Verify no analysis errors**

Run: `cd flutter_app && flutter analyze`
Expected: No issues found.

- [ ] **Step 3: Commit**

```bash
git add flutter_app/lib/core/setup/first_run_setup.dart
git commit -m "feat(flutter): TOML writer generates per-repo issue tracking

Writes [ai.repos.\"org/repo\".issue_tracking] section with only
non-null override fields."
```

---

### Task 8: Full verification

**Files:** None — verification only.

- [ ] **Step 1: Run daemon tests**

Run: `cd /path/to/project && make test-docker`
Expected: All tests pass.

- [ ] **Step 2: Build daemon binary**

Run: `cd daemon && go build -o bin/heimdallm ./cmd/heimdallm`
Expected: Build succeeds.

- [ ] **Step 3: Run flutter analyze**

Run: `cd flutter_app && flutter analyze`
Expected: No new issues.

- [ ] **Step 4: Run flutter tests**

Run: `cd flutter_app && flutter test`
Expected: All tests pass.

- [ ] **Step 5: Build flutter app**

Run: `cd flutter_app && flutter build macos --release`
Expected: Build succeeds.

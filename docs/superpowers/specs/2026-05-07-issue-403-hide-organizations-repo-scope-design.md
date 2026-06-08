# Issue #403 — Hide "Organizations" filter at repo scope (Issue Tracking)

**Status:** Approved design, ready for implementation.
**Branch:** `feat/hide-org-filter-403` (worktree at `.worktrees/hide-org-filter-403`).
**Issue:** https://github.com/theburrowhub/heimdallm/issues/403

## Goal

Remove the **Organizations** filter from the Issue Tracking section of the repo detail screen (`flutter_app/lib/features/repositories/repo_detail_screen.dart`). At repo scope this filter is at best a no-op and at worst silently excludes the repo from issue tracking entirely — see the issue body's analysis of `daemon/internal/github/fetch_issues.go:164` (`repoBelongsToOrg`).

The filter remains visible at the scopes where it has meaningful semantics:

- Global Issue Tracking (`config_screen.dart`)
- Org detail (`org_detail_screen.dart`)

## Non-goals

- The TOML / API contract is unchanged. `RepoConfig.issueOrganizations` remains a valid model field; legacy values persisted from before this change are preserved untouched.
- The daemon-side `repoBelongsToOrg` filter is unchanged.
- No daemon-side validation warning when `organizations` is set at repo scope. (Could be a separate follow-up if anyone is bitten by stale config in the wild.)

## Approach

Pure UI removal plus dead-code cleanup. Two files change.

### 1. `flutter_app/lib/features/repositories/repo_detail_screen.dart`

**Remove the field UI** (currently at lines 472-491):

```dart
const SizedBox(height: 10),
AutocompleteChipField(
  label: 'Organizations',
  helper: 'GitHub org names to filter issues',
  selectedValues:
      _config.issueOrganizations ??
      orgConfig?.issueOrganizations ??
      appConfig.issueTracking.organizations,
  availableOptions: appConfig.knownOrganizations,
  isOverridden: _config.issueOrganizations != null,
  inheritedLabel: source(
    orgConfig?.issueOrganizations != null,
  ),
  globalHint: _joinList(
    orgConfig?.issueOrganizations ??
        appConfig.issueTracking.organizations,
  ),
  onChanged: (v) =>
      _update(_config.copyWith(issueOrganizations: v)),
  onReset: () => _resetField('issue_tracking/organizations'),
),
```

The leading `SizedBox(height: 10)` separator must be removed along with the field — otherwise a phantom gap remains between **Default action** and **Assignees**.

**Remove the dead diff branch** (currently at lines 203-204):

```dart
if (!_listsEqual(old.issueOrganizations, updated.issueOrganizations)) {
  itDiff['organizations'] = updated.issueOrganizations ?? <String>[];
}
```

Justification — `_config.issueOrganizations` is initialized exactly once from `appConfig.repoConfigs[repoName]` in `_initFrom` (guarded by `_initialized`). The only mutation point in this screen is the field's `onChanged` (line 489), and the only reset path is its `onReset` (line 490). With both gone, no code path in this screen can change `_config.issueOrganizations`, so the diff branch is unreachable. Per project convention we delete unreachable code rather than keep it as defensive insurance for hypothetical future regressions.

### 2. `flutter_app/test/features/repositories/repo_detail_screen_test.dart` (new file)

Mirror the structure of `flutter_app/test/features/organizations/org_detail_screen_test.dart`:

- `MockApiClient extends Mock implements ApiClient`.
- `tester.pumpWidget` a `ProviderScope` with overrides for:
  - `apiClientProvider` → mock
  - `configNotifierProvider` → real notifier (`ConfigNotifier.new`)
  - `agentsProvider` → empty list
- Stub `mockApi.fetchConfig()` to return a minimal config with a `repo_overrides` entry for the test repo (e.g. `theburrowhub/heimdallm`) where `issue_tracking.enabled = true` so the section renders fully.
- Stub `mockApi.fetchRepoLabels(...)` and `mockApi.fetchRepoCollaborators(...)` (called by `_loadRepoMeta` at lines 49-65) to return empty lists. Without these the widget will throw inside `_loadRepoMeta` and the test will be flaky.
- Pump the screen with `home: const RepoDetailScreen(repoName: 'theburrowhub/heimdallm')` and `pumpAndSettle()`.

**Assertions:**

- `expect(find.text('Organizations'), findsNothing);` — the regression guard.
- Sanity checks that the surrounding fields still render (proving the section loaded, not that the test config was empty):
  - `Assignees`, `Review-only labels`, `Skip labels`, `Filter mode`, `Default action`, `Prompt` — each `findsOneWidget`.

## Verification

Run from the worktree root:

```bash
cd flutter_app && flutter analyze
cd flutter_app && flutter test
```

Both must pass clean. Then a manual spot-check via `make build-web`: open a repo detail page, confirm the Issue Tracking section renders cleanly without **Organizations**, the spacing between **Default action** and **Assignees** is correct, and a config save (e.g. toggling another field) still persists.

## Files touched

- `flutter_app/lib/features/repositories/repo_detail_screen.dart` — remove field UI (lines 472-491 + leading spacer) and dead diff branch (lines 203-204).
- `flutter_app/test/features/repositories/repo_detail_screen_test.dart` — new file.

## Out of scope (explicit)

- `RepoConfig.issueOrganizations` field, `copyWith` parameter, JSON serialization, and TOML emission in `first_run_setup.dart` all remain. Legacy config files keep working unchanged.
- No changes to `daemon/internal/github/fetch_issues.go` or `repoBelongsToOrg`.
- No new daemon-side validation. No data migration.

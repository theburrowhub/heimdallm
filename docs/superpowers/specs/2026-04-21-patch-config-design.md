# Patch-Based Config Saves

**Issue**: [#144](https://github.com/theburrowhub/heimdallm/issues/144) — config round-trip design is fundamentally broken  
**Date**: 2026-04-21  
**Status**: Design approved

## Problem

The Flutter app acts as source of truth for the full config, but it can never have a complete picture. The save flow (fetch snapshot → change one field → write entire config back) silently erases any field Flutter doesn't know about. The #138 fix (manually exposing missing fields) is fragile — every new field added to `RepoAI` must also be added to the serialization map, or it's silently lost.

## Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Persistence target for PATCH | TOML file (daemon reads, merges, writes) | Single source of truth on disk |
| DELETE granularity | Sub-field level (`issue_tracking/develop_labels`) | Flutter chains N deletes for block-level reset |
| PATCH body structure | Nested, mirrors TOML structure | `{"ai": {"primary": "gemini"}}` not `{"ai_primary": "gemini"}` |
| Store (SQLite) layer | Kept for web UI, unchanged | Two persistence paths coexist: Flutter→TOML, web UI→SQLite |
| Concurrency | Mutex in daemon | Single Flutter client, sufficient for the use case |
| Merge strategy | TOML-native deep merge on `map[string]any` | No per-field merge code needed; extensible without code changes |

## API Endpoints

### `PATCH /config` — Update global config

Accepts a JSON body with nested structure mirroring TOML. Only fields present in the body are updated.

```http
PATCH /config
Content-Type: application/json

{
  "ai": {
    "primary": "gemini",
    "review_mode": "multi"
  },
  "github": {
    "poll_interval": "30m"
  }
}
```

- **Absent from body** → field is not touched
- **Present with value** → field is updated
- **`null` value** → rejected with 400 (use DELETE to remove fields)
- **Response**: `200 OK` with full config (same shape as `GET /config`)

### `PATCH /config/repos/{repo}` — Update per-repo override

```http
PATCH /config/repos/{repo}
Content-Type: application/json

{
  "primary": "claude",
  "pr_draft": true,
  "issue_tracking": {
    "develop_labels": ["ready"]
  }
}
```

- Same semantics: only present fields are updated
- If the repo has no TOML section yet, `[ai.repos."owner/repo"]` is created
- Internally equivalent to `PATCH /config` with body `{"ai": {"repos": {"owner/repo": <body>}}}`
- **Response**: `200 OK` with full config

### `DELETE /config/repos/{repo}/{field}` — Reset override to global default

```http
DELETE /config/repos/org/repo1/pr_draft
DELETE /config/repos/org/repo1/issue_tracking/develop_labels
```

- Removes the field from TOML; the repo falls back to global default for that field
- `field` supports `/`-separated paths for sub-fields (e.g., `issue_tracking/develop_labels`)
- If the repo section becomes empty after deletion, the entire section is removed
- **Response**: `200 OK` with full config

### Unchanged endpoints

- `GET /config` — no changes
- `PUT /config` — no changes (web UI → SQLite store)
- `POST /reload` — no changes

## Daemon — TOML Merge Engine

### `deepMergeTOML(base, patch map[string]any) map[string]any`

Rules:
1. For each key in `patch`:
   - If both values are `map[string]any` → recurse (deep merge)
   - Otherwise → patch value replaces base value
2. Keys absent from `patch` are not touched in `base`
3. `null` values in patch are rejected before merge (handler returns 400)

### Read-merge-write flow

All three endpoints (`PATCH /config`, `PATCH /config/repos/{repo}`, `DELETE /config/repos/{repo}/{field}`) share the same write pipeline:

```
1. Lock mutex
2. Read config.toml → toml.Unmarshal into map[string]any (base)
3. Apply operation:
   - PATCH /config: deepMergeTOML(base, patch)
   - PATCH /config/repos/{repo}: deepMergeTOML(base, {"ai": {"repos": {repo: patch}}})
   - DELETE: navigate to target key, delete it, clean up empty parents
4. Validate: marshal merged map to TOML → unmarshal to Config struct → Config.Validate()
   - If validation fails → 400 Bad Request with error, nothing written
5. Write atomically: write to temp file → os.Rename to config.toml
6. Reload config in memory (same path as POST /reload)
7. Unlock mutex
8. Return full config as JSON response
```

### DELETE navigation

For `DELETE /config/repos/{repo}/{field}`:

1. Split `field` on `/` to get path segments
2. Navigate `ai.repos.<repo>` in the map
3. Walk path segments to reach the parent map of the target field
4. `delete(parentMap, lastSegment)`
5. Walk back up: if any map is now empty, remove it from its parent
6. Continue with validation and write

### Null rejection

Before merge, the handler walks the JSON patch tree. If any value is `null` (Go: `nil` in `map[string]any` from `json.Unmarshal`), the handler returns:

```json
{"error": "null values not allowed in PATCH — use DELETE to remove fields"}
```

This enforces the principle: **`null` means "don't touch", not "delete"** — and the way to delete is always explicit via the DELETE endpoint.

## Flutter — Migration to Patch-Based Saves

### New `ApiClient` methods

```dart
Future<Map<String, dynamic>> patchConfig(Map<String, dynamic> patch);
Future<Map<String, dynamic>> patchRepoConfig(String repo, Map<String, dynamic> patch);
Future<Map<String, dynamic>> deleteRepoField(String repo, String fieldPath);
```

All three return the full config JSON from the daemon response.

### `_autoSave` in `RepoDetailScreen`

Changes from full-config write to diff-based patch:

```dart
void _update(RepoConfig updated) {
  final diff = _computeDiff(_config, updated);
  setState(() => _config = updated);
  _debounce?.cancel();
  _debounce = Timer(Duration(milliseconds: 800), () => _autoSave(diff));
}

Future<void> _autoSave(Map<String, dynamic> diff) async {
  if (diff.isEmpty) return;
  final freshConfig = await api.patchRepoConfig(widget.repoName, diff);
  ref.read(configNotifierProvider.notifier).updateFromServer(freshConfig);
}
```

`_computeDiff(RepoConfig old, RepoConfig new)` compares the two and returns a map containing only the fields that changed. For example, if only `pr_draft` changed: `{"pr_draft": true}`.

### `OverrideField` — reset button

The reset action calls the DELETE endpoint instead of setting the field to null locally:

```dart
onReset: () async {
  final freshConfig = await api.deleteRepoField(repoName, fieldName);
  ref.read(configNotifierProvider.notifier).updateFromServer(freshConfig);
}
```

### `ConfigNotifier.save()` — global settings

Switches to diff-based patch for global config changes:

```dart
Future<void> save(AppConfig updated) async {
  final diff = _computeGlobalDiff(state.value!, updated);
  if (diff.isEmpty) return;
  final freshConfig = await api.patchConfig(diff);
  state = AsyncValue.data(AppConfig.fromJson(freshConfig));
}
```

### `updateFromServer` — new method on `ConfigNotifier`

Accepts the full config JSON returned by PATCH/DELETE and replaces local state:

```dart
void updateFromServer(Map<String, dynamic> json) {
  state = AsyncValue.data(AppConfig.fromJson(json));
}
```

This ensures Flutter always reflects the daemon's truth after every mutation.

### `FirstRunSetup._buildToml` — unchanged

Remains for first-run only (no daemon running yet). After first-run, Flutter never writes TOML directly. The guard is the existing `isFirstRun` check — `writeDaemonConfig` is only called within `FirstRunSetup`.

### `platformServicesProvider.writeDaemonConfig` — no longer called post-setup

`ConfigNotifier.save()` and `_autoSave` no longer call `writeDaemonConfig`. Only `FirstRunSetup` uses it.

## Files Affected

### Daemon (Go)

| File | Change |
|------|--------|
| `daemon/internal/server/handlers.go` | Add `handlePatchConfig`, `handlePatchRepoConfig`, `handleDeleteRepoField` handlers |
| `daemon/internal/server/handlers.go` | Add `deepMergeTOML`, null-rejection walker, DELETE navigation |
| `daemon/internal/server/routes.go` | Register new PATCH and DELETE routes |
| `daemon/internal/server/server.go` | Add mutex for TOML write serialization |
| `daemon/internal/config/writer.go` | New file: atomic TOML write (temp + rename) and map-based merge utilities |

### Flutter

| File | Change |
|------|--------|
| `flutter_app/lib/core/services/api_client.dart` | Add `patchConfig`, `patchRepoConfig`, `deleteRepoField` methods |
| `flutter_app/lib/features/config/config_providers.dart` | `save()` → diff-based patch; add `updateFromServer`; `_computeGlobalDiff` |
| `flutter_app/lib/features/repositories/repo_detail_screen.dart` | `_autoSave` → sends diff via `patchRepoConfig`; `_computeDiff` helper |
| `flutter_app/lib/shared/widgets/override_field.dart` | Reset button calls `deleteRepoField` instead of setting null |

### No changes

| File | Reason |
|------|--------|
| `daemon/internal/config/config.go` | Config struct unchanged |
| `daemon/internal/config/store.go` | Store layer unchanged (web UI path) |
| `daemon/cmd/heimdallm/main.go` | `SetConfigFn` / GET /config unchanged |
| `flutter_app/lib/core/setup/first_run_setup.dart` | First-run TOML writer unchanged |

## Error Handling

| Scenario | Behavior |
|----------|----------|
| `null` value in PATCH body | 400 with message directing to use DELETE |
| PATCH body has unknown keys | Deep merge writes them, but `Config.Validate()` catches invalid structure → 400 |
| PATCH body has invalid value types | `Config.Validate()` catches type mismatches → 400 |
| DELETE on non-existent field | 200 (idempotent — field already absent) |
| DELETE on non-existent repo | 200 (idempotent — nothing to delete) |
| TOML file unreadable | 500 with error message |
| Concurrent PATCH requests | Serialized by mutex — second waits for first to complete |

## Testing Strategy

### Daemon

- Unit tests for `deepMergeTOML`: nested merge, scalar replace, no-op on empty patch
- Unit tests for null rejection: walk tree, detect nil values at any depth
- Unit tests for DELETE navigation: single field, nested path, empty-parent cleanup
- Integration tests for each endpoint: PATCH /config, PATCH /config/repos/{repo}, DELETE
- Round-trip test: PATCH a field → GET /config → verify field changed, others untouched
- Validation rejection test: PATCH invalid value → 400, TOML unchanged

### Flutter

- Unit tests for `_computeDiff`: single field change, multiple fields, no change → empty
- Unit tests for `_computeGlobalDiff`: nested structure diff
- Widget test for `OverrideField` reset: verify DELETE call, state update from response
- Integration test: patch → verify provider state updated from server response

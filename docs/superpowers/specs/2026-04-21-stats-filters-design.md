# Stats filters — organization and repository multi-select

**Date:** 2026-04-21
**Issue:** #110

## Approach

Server-side filtering via `GET /stats?repos=org/repo1,org/repo2`. The stats are computed with SQL (timing percentiles, 7-day bucketing, GROUP BY severity/cli). Reimplementing those aggregations client-side would be fragile and duplicative. Adding a `WHERE repo IN (?)` clause to the existing queries is minimal and precise.

Empty `repos` param = global stats (current behavior, fully backwards compatible).

## Daemon changes

### `GET /stats` endpoint

Add optional query param `repos` (comma-separated). Pass to `ComputeStats`.

### `store.ComputeStats(repos []string)`

Change signature from `ComputeStats()` to `ComputeStats(repos []string)`. When `repos` is non-empty, add `WHERE p.repo IN (?)` (or equivalent) to every query that joins `prs`. Queries that don't join `prs` (by_severity, by_cli) use a subquery: `WHERE r.pr_id IN (SELECT id FROM prs WHERE repo IN (?))`.

## Flutter changes

### `StatsFilters` model + provider

Minimal filter state — only orgs and repos (no types, no search):

```dart
class StatsFilters {
  final Set<String> orgs;
  final Set<String> repos;
  // copyWith, hasFilters, reset
}

final statsFiltersProvider = StateProvider<StatsFilters>(...);
```

### `StatsFilterBar` widget

Org and repo multi-select popups. Same UX pattern as `ActivityFilterBar` (popup with checkboxes). No sort, no type chips, no search — just the two dropdowns and a reset button.

Repo list derived from `prsProvider` + `issuesProvider` (same as Activity tab).

### `statsProvider` update

Currently a simple `FutureProvider` hitting `GET /stats`. Change to read `statsFiltersProvider` and append `?repos=` when filters are active.

### `StatsScreen` update

Add `StatsFilterBar` at the top of the scroll view, above the summary cards. The screen becomes `ConsumerWidget` → `ConsumerStatefulWidget` if needed for the filter bar state.

## Files modified

| File | Change |
|---|---|
| `daemon/internal/store/store.go` | `ComputeStats(repos []string)`, add WHERE clauses |
| `daemon/internal/server/handlers.go` | Parse `?repos=` in `handleStats` |
| `flutter_app/lib/features/stats/stats_screen.dart` | Add filter bar, wire to provider |
| `flutter_app/lib/features/stats/stats_filters.dart` | New: StatsFilters model + provider |
| `flutter_app/lib/features/stats/stats_filter_bar.dart` | New: filter bar widget |
| `flutter_app/lib/features/dashboard/dashboard_providers.dart` | Update statsProvider to accept repos param |

## Out of scope

- Chart/graph enhancements
- Date range filtering for stats
- Issue-tracking stats (only PR reviews today)

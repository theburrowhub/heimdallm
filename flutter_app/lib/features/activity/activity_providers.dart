import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../core/models/activity.dart';
import '../dashboard/dashboard_providers.dart' show apiClientProvider;

/// Notifier managing the current query (date, range, filter sets).
/// Widgets read & mutate this; the entries provider watches it.
class ActivityQueryNotifier extends StateNotifier<ActivityQuery> {
  ActivityQueryNotifier() : super(_today());

  static ActivityQuery _today() {
    final now = DateTime.now();
    return ActivityQuery(date: DateTime(now.year, now.month, now.day));
  }

  void setDate(DateTime day) {
    state = state.copyWith(
      date: DateTime(day.year, day.month, day.day),
      from: null,
      to: null,
    );
  }

  void setRange(DateTime from, DateTime to) {
    final start = DateTime(from.year, from.month, from.day);
    final end   = DateTime(to.year,   to.month,   to.day);
    final (a, b) = start.isAfter(end) ? (end, start) : (start, end);
    state = state.copyWith(date: null, from: a, to: b);
  }

  void toggleOrg(String org) =>
      state = state.copyWith(orgs: _toggled(state.orgs, org));
  void toggleRepo(String repo) =>
      state = state.copyWith(repos: _toggled(state.repos, repo));
  void toggleAction(ActivityAction a) =>
      state = state.copyWith(actions: _toggled(state.actions, a));

  void clearFilters() => state = state.copyWith(
        orgs: const {},
        repos: const {},
        actions: const {},
      );

  static Set<T> _toggled<T>(Set<T> set, T v) {
    final next = Set<T>.from(set);
    if (!next.add(v)) next.remove(v);
    return next;
  }
}

/// Current query state.
final activityQueryProvider =
    StateNotifierProvider<ActivityQueryNotifier, ActivityQuery>(
  (ref) => ActivityQueryNotifier(),
);

/// Entries for the current query. Watches the query so filter changes
/// trigger a refetch automatically.
final activityEntriesProvider = FutureProvider<ActivityPage>((ref) async {
  final q = ref.watch(activityQueryProvider);
  final api = ref.watch(apiClientProvider);
  return api.fetchActivity(q);
});

/// Source for filter chip option lists (orgs/repos). Uses the same date
/// window as the main query but drops org/repo/action filters — so the
/// option lists show the full universe for the window instead of shrinking
/// to the already-filtered slice.
/// Oversized so the distinct org/repo lists reflect the full window even when
/// the main entries view is truncated. A dedicated facets endpoint would be
/// the proper long-term fix.
const int _activityOptionsLimit = 10000;

final activityOptionsProvider = FutureProvider<ActivityPage>((ref) async {
  final date = ref.watch(activityQueryProvider.select((q) => q.date));
  final from = ref.watch(activityQueryProvider.select((q) => q.from));
  final to   = ref.watch(activityQueryProvider.select((q) => q.to));
  final api = ref.watch(apiClientProvider);
  return api.fetchActivity(
    ActivityQuery(date: date, from: from, to: to, limit: _activityOptionsLimit),
  );
});

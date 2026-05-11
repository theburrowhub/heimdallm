import 'dart:async';

import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../core/models/activity.dart';
import '../../core/state/local_state_notifier.dart';
import '../dashboard/dashboard_providers.dart'
    show apiClientProvider, sseStreamProvider;

/// Notifier managing the current query (date, range, filter sets).
/// Widgets read & mutate this; the entries provider watches it.
class ActivityQueryNotifier extends Notifier<ActivityQuery> {
  @override
  ActivityQuery build() => _today();

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
    final end = DateTime(to.year, to.month, to.day);
    final (a, b) = start.isAfter(end) ? (end, start) : (start, end);
    state = state.copyWith(date: null, from: a, to: b);
  }

  void toggleOrg(String org) =>
      state = state.copyWith(orgs: _toggled(state.orgs, org));
  void toggleRepo(String repo) =>
      state = state.copyWith(repos: _toggled(state.repos, repo));
  void toggleItemType(String itemType) =>
      state = state.copyWith(itemTypes: _toggled(state.itemTypes, itemType));
  void toggleAction(ActivityAction a) =>
      state = state.copyWith(actions: _toggled(state.actions, a));
  void toggleOutcome(String outcome) =>
      state = state.copyWith(outcomes: _toggled(state.outcomes, outcome));

  void setQuickFilter({
    ActivityAction? action,
    String? itemType,
    String? outcome,
    required bool enabled,
  }) {
    state = state.copyWith(
      actions: action == null
          ? null
          : _setMembership(state.actions, action, enabled),
      itemTypes: itemType == null
          ? null
          : _setMembership(state.itemTypes, itemType, enabled),
      outcomes: outcome == null
          ? null
          : _setMembership(state.outcomes, outcome, enabled),
    );
  }

  void clearFilters() => state = state.copyWith(
    orgs: const {},
    repos: const {},
    itemTypes: const {},
    actions: const {},
    outcomes: const {},
  );

  static Set<T> _toggled<T>(Set<T> set, T v) {
    final next = Set<T>.from(set);
    if (!next.add(v)) next.remove(v);
    return next;
  }

  static Set<T> _setMembership<T>(Set<T> set, T v, bool enabled) {
    final next = Set<T>.from(set);
    if (enabled) {
      next.add(v);
    } else {
      next.remove(v);
    }
    return next;
  }
}

/// Current query state.
final activityQueryProvider =
    NotifierProvider<ActivityQueryNotifier, ActivityQuery>(
      ActivityQueryNotifier.new,
    );

final activityLiveUpdatesProvider =
    NotifierProvider<LocalStateNotifier<bool>, bool>(
      () => LocalStateNotifier<bool>(false),
    );

const _activityLiveRefreshDelay = Duration(milliseconds: 750);

const _activityLogEventTypes = {
  'review_completed',
  'review_error',
  'review_skipped',
  'issue_review_completed',
  'issue_refinement_done',
  'issue_implemented',
  'issue_review_error',
  'issue_promoted',
};

/// Installs the live-mode SSE listener for ActivityScreen.
///
/// This notifier is intentionally side-effectful: the screen must watch it so
/// live mode refreshes the persisted activity query when activity-log events
/// arrive. Events that are not recorded in `activity_log` are ignored.
///
/// Implemented as a Notifier (not `Provider<void>`) so it integrates cleanly with
/// Riverpod 3's lifecycle: when the screen watches it, the notifier stays
/// alive, and the nested `ref.listen(sseStreamProvider, ...)` stays subscribed.
/// The provider is declared without `.autoDispose`, so it relies on v3's
/// default keep-alive semantics — adding `.autoDispose` here would re-introduce
/// the "paused while between watchers" behaviour this notifier was created to
/// avoid.
class ActivityLiveRefreshNotifier extends Notifier<void> {
  @override
  void build() {
    if (!ref.watch(activityLiveUpdatesProvider)) return;

    Timer? debounce;
    ref.onDispose(() => debounce?.cancel());

    ref.listen(sseStreamProvider, (previous, next) {
      next.whenData((event) {
        if (!_activityLogEventTypes.contains(event.type)) return;
        debounce?.cancel();
        debounce = Timer(_activityLiveRefreshDelay, () {
          ref.invalidate(activityEntriesProvider);
          ref.invalidate(activityOptionsProvider);
        });
      });
    });
  }
}

final activityLiveRefreshProvider =
    NotifierProvider<ActivityLiveRefreshNotifier, void>(
      ActivityLiveRefreshNotifier.new,
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
// Must stay in sync with the daemon's /activity limit cap.
const int _activityOptionsLimit = 5000;

final activityOptionsProvider = FutureProvider<ActivityPage>((ref) async {
  final date = ref.watch(activityQueryProvider.select((q) => q.date));
  final from = ref.watch(activityQueryProvider.select((q) => q.from));
  final to = ref.watch(activityQueryProvider.select((q) => q.to));
  final api = ref.watch(apiClientProvider);
  return api.fetchActivity(
    ActivityQuery(date: date, from: from, to: to, limit: _activityOptionsLimit),
  );
});

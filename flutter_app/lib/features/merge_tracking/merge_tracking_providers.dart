import 'dart:convert';

import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../core/instances/aggregation.dart';
import '../../core/instances/instances_providers.dart';
import '../../core/api/sse_client.dart';
import '../../core/models/merge_tracking.dart';
import '../../core/state/local_state_notifier.dart';
import '../dashboard/dashboard_providers.dart';

/// The tracked PRs, ordered by the daemon: rows blocked by CI first.
/// Tracked PRs from every instance the UI is scoped to, tagged with origin.
final mergeTrackingByInstanceProvider =
    FutureProvider<AggregatedResult<MergeTrackingEntry>>((ref) async {
      ref.watch(mergeTrackingRefreshProvider);
      return aggregate<MergeTrackingEntry>(
        targets: ref.watch(targetInstancesProvider),
        clientFor: (id) => ref.read(apiClientForProvider(id)),
        fetch: (client) => client.fetchMergeTrackingList(),
      );
    });

final mergeTrackingProvider = FutureProvider<List<MergeTrackingEntry>>((
  ref,
) async {
  final aggregated = await ref.watch(mergeTrackingByInstanceProvider.future);
  return aggregated.values;
});

/// Bumped to force a refetch. Incremented by the SSE listener below and by the
/// dashboard's global refresh.
final mergeTrackingRefreshProvider =
    NotifierProvider<LocalStateNotifier<int>, int>(
      () => LocalStateNotifier<int>(0),
    );

/// One tracked PR with its full per-check breakdown.
final mergeTrackingDetailProvider =
    FutureProvider.family<MergeTrackingEntry, int>((ref, prId) async {
      ref.watch(mergeTrackingRefreshProvider);
      final api = ref.watch(apiClientProvider);
      return api.fetchMergeTracking(prId);
    });

/// The number of tracked PRs whose merge is held up by CI.
///
/// Drives the badge on the tab, so a failing check is visible without opening
/// the view at all.
final mergeTrackingCheckProblemCountProvider = Provider<int>((ref) {
  final entries = ref.watch(mergeTrackingProvider).value;
  if (entries == null) return 0;
  return entries
      .where((e) => !e.isTerminal && (e.hasFailingChecks || e.hasPendingChecks))
      .length;
});

/// The merge-tracking events that change what the view should show. Evaluations
/// happen on every cycle for every PR, so refetching on those would mean a
/// request per PR per cycle; the listing is refreshed on the state changes
/// instead, plus the periodic refresh the dashboard already does.
const _refreshingEvents = {
  'merge_track_detected',
  'merge_track_blocked',
  'merge_track_auto_merge_armed',
  'merge_track_branch_updated',
  'merge_track_conflict_resolved',
  'merge_track_merged',
  // A failed automation emits only this event — the row goes to blocked with a
  // last_error and nothing else announces it. Leaving it out meant the tab kept
  // showing the pre-failure state until a manual refresh, at exactly the moment
  // the operator needs the explanation.
  'merge_track_error',
};

/// Watches the SSE stream and refreshes the listing when something happened.
///
/// Kept as an explicit listener rather than folding it into mergeTrackingProvider
/// so the refresh policy is visible in one place and easy to change.
final mergeTrackingSseListenerProvider = Provider<void>((ref) {
  ref.listen<AsyncValue<SseEvent>>(sseStreamProvider, (_, next) {
    final event = next.value;
    if (event == null) return;
    if (!_refreshingEvents.contains(event.type)) return;
    ref.read(mergeTrackingRefreshProvider.notifier).update((s) => s + 1);
  });
});

/// Extracts a friendly repo#number label from an SSE payload, for the toast
/// shown when the daemon merges something on the user's behalf.
String? mergeTrackEventLabel(SseEvent event) {
  try {
    final data = jsonDecode(event.data) as Map<String, dynamic>;
    final repo = data['repo'];
    final number = data['number'];
    if (repo is String && number is num) {
      return '$repo#${number.toInt()}';
    }
  } catch (_) {
    // A malformed payload is not worth surfacing; the listing refresh still
    // happens and shows the real state.
  }
  return null;
}

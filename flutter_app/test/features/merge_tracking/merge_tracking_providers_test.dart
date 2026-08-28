import 'dart:async';

import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/api/sse_client.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/features/merge_tracking/merge_tracking_providers.dart';

/// Feeds the listener one event and reports whether the listing was told to
/// refetch.
Future<bool> _refreshesOn(String type) async {
  final controller = StreamController<SseEvent>();
  addTearDown(controller.close);

  final container = ProviderContainer(
    overrides: [sseStreamProvider.overrideWith((ref) => controller.stream)],
  );
  addTearDown(container.dispose);

  // A listener keeps the provider alive; a bare read would let it be disposed
  // before the event arrives.
  container.listen(mergeTrackingSseListenerProvider, (_, _) {}, fireImmediately: true);
  final before = container.read(mergeTrackingRefreshProvider);
  controller.add(SseEvent(type: type, data: '{"repo":"acme/widgets","number":7}'));
  await Future<void>.delayed(const Duration(milliseconds: 20));
  return container.read(mergeTrackingRefreshProvider) != before;
}

void main() {
  // recordFailure emits merge_track_error and nothing else — it never emits a
  // block event. Leaving the type out of the refresh set meant the Merge tab
  // kept showing the pre-failure state until a manual refresh, at exactly the
  // moment the operator needs the explanation.
  test('a failed automation refreshes the listing', () async {
    expect(await _refreshesOn('merge_track_error'), isTrue);
  });

  test('every state change refreshes the listing', () async {
    for (final type in [
      'merge_track_detected',
      'merge_track_blocked',
      'merge_track_auto_merge_armed',
      'merge_track_branch_updated',
      'merge_track_conflict_resolved',
      'merge_track_merged',
    ]) {
      expect(await _refreshesOn(type), isTrue, reason: type);
    }
  });

  // Evaluations happen every cycle for every PR; refetching on those would mean
  // a request per PR per cycle for a listing that has not changed.
  test('a routine evaluation does not refetch', () async {
    expect(await _refreshesOn('merge_track_evaluated'), isFalse);
    expect(await _refreshesOn('pr_updated'), isFalse);
  });

  test('mergeTrackEventLabel reads repo#number, and tolerates junk', () {
    expect(
      mergeTrackEventLabel(
        const SseEvent(type: 'merge_track_merged', data: '{"repo":"a/b","number":3}'),
      ),
      'a/b#3',
    );
    expect(
      mergeTrackEventLabel(const SseEvent(type: 'merge_track_merged', data: 'not json')),
      isNull,
    );
    expect(
      mergeTrackEventLabel(const SseEvent(type: 'merge_track_merged', data: '{"repo":7}')),
      isNull,
    );
  });
}

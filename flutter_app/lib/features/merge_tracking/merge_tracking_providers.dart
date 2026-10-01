import 'dart:async';
import 'dart:convert';

import 'package:flutter/foundation.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:shared_preferences/shared_preferences.dart';

import '../../core/instances/aggregation.dart';
import '../../core/instances/instances_providers.dart';
import '../../core/api/sse_client.dart';
import '../../core/models/merge_tracking.dart';
import '../../core/models/my_prs_summary.dart';
import '../../core/platform/platform_services.dart'
    show OptionalPlatformCapabilities;
import '../../core/platform/platform_services_provider.dart';
import '../../core/state/local_state_notifier.dart';
import '../../main.dart' show sendPRNotification;
import '../config/config_providers.dart';
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

/// What the operator's PRs are asking of them, across every instance.
final myPrsSummaryProvider = Provider<MyPrsSummary>((ref) {
  final entries = ref.watch(mergeTrackingProvider).value;
  if (entries == null) return const MyPrsSummary();
  return summarizeMyPrs(entries);
});

/// The number of PRs that need the operator: something to fix, a merge to
/// click, or gone quiet.
///
/// Drives the badge on the My PRs tab. It used to count PRs with failing or
/// *pending* checks, which lit the badge for every PR simply waiting on CI —
/// noise that trained the eye to ignore it.
final myPrsAttentionCountProvider = Provider<int>(
  (ref) => ref.watch(myPrsSummaryProvider).total,
);

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
  'my_pr_attention',
  'my_pr_stale',
};

/// Watches the SSE stream and refreshes the listing when something happened.
///
/// Kept as an explicit listener rather than folding it into mergeTrackingProvider
/// so the refresh policy is visible in one place and easy to change.
///
/// It is watched by the app shell, not only by the My PRs screen: the
/// transition notifications below have to fire whichever tab is open.
final mergeTrackingSseListenerProvider = Provider<void>((ref) {
  ref.listen<AsyncValue<SseEvent>>(sseStreamProvider, (_, next) {
    final event = next.value;
    if (event == null) return;
    if (!_refreshingEvents.contains(event.type)) return;
    ref.read(mergeTrackingRefreshProvider.notifier).update((s) => s + 1);
    _notifyMyPrTransition(ref, event);
  });
});

/// Turns a `my_pr_attention` / `my_pr_stale` event into a desktop
/// notification, when the operator opted in (`[my_prs].notify_transitions`).
///
/// The daemon emits each of these once per change, so there is no dedup here.
void _notifyMyPrTransition(Ref ref, SseEvent event) {
  if (event.type != 'my_pr_attention' && event.type != 'my_pr_stale') return;
  final myPrs = ref.read(configNotifierProvider).value?.myPrs;
  if (myPrs == null || !myPrs.enabled || !myPrs.notifyTransitions) return;
  final n = myPrTransitionNotification(event);
  if (n == null) return;
  sendPRNotification(
    platform: ref.read(platformServicesProvider),
    title: n.title,
    body: n.body,
    location: '/merge',
  );
}

/// The notification text for a My PRs transition event, or null for a
/// malformed payload.
@visibleForTesting
({String title, String body})? myPrTransitionNotification(SseEvent event) {
  final Map<String, dynamic> data;
  try {
    data = jsonDecode(event.data) as Map<String, dynamic>;
  } catch (_) {
    return null;
  }
  final label = mergeTrackEventLabel(event);
  if (label == null) return null;
  final title = (data['title'] as String?)?.trim() ?? '';
  final body = title.isEmpty ? label : '$label — $title';
  if (event.type == 'my_pr_stale') {
    final idle = (data['idle_seconds'] as num?)?.toInt();
    final forText = idle == null
        ? ''
        : ' for ${formatIdle(Duration(seconds: idle))}';
    return (title: 'Your PR has gone quiet$forText', body: body);
  }
  final headline = switch (data['attention']) {
    'ready' => 'Your PR is ready to merge',
    _ => switch (data['reason']) {
      'changes_requested' => 'Changes requested on your PR',
      'checks_failing' => 'CI is failing on your PR',
      'required_check_missing' => 'A required check is missing on your PR',
      'conflicts' => 'Your PR has conflicts',
      'behind_base' => 'Your PR is behind its base branch',
      'unresolved_threads' => 'Unresolved conversations on your PR',
      _ => 'Your PR needs you',
    },
  };
  return (title: headline, body: body);
}

/// Keeps the tray's "Your PRs" section in step with the listing.
final myPrsTraySyncProvider = Provider<void>((ref) {
  ref.listen<AsyncValue<List<MergeTrackingEntry>>>(mergeTrackingProvider, (
    _,
    next,
  ) {
    final entries = next.value;
    if (entries == null) return;
    final urgent = myPrsNeedingOperator(entries);
    unawaited(
      ref
          .read(platformServicesProvider)
          .setTrayMyPrs(urgent)
          .catchError((Object e) {
            debugPrint('myPrsTraySync: failed to update the tray: $e');
          }),
    );
  }, fireImmediately: true);
});

/// The digest's clock, overridable in tests.
final myPrsClockProvider = Provider<DateTime Function()>((ref) => DateTime.now);

const _lastDigestKey = 'my_prs_last_digest';

String _dayKey(DateTime t) =>
    '${t.year.toString().padLeft(4, '0')}-'
    '${t.month.toString().padLeft(2, '0')}-'
    '${t.day.toString().padLeft(2, '0')}';

/// The once-a-day summary notification of the PRs that need the operator.
///
/// Checked every minute and whenever the listing loads, so a digest whose time
/// passed while the app was closed still arrives on launch that same day. The
/// day it last ran is kept in local storage; a day with nothing to report is
/// marked done without a notification, so the digest never turns into an
/// afternoon transition alert. State is that last day, for tests and the UI.
class MyPrsDigestNotifier extends Notifier<String?> {
  Timer? _timer;
  bool _checking = false;

  @override
  String? build() {
    _timer = Timer.periodic(const Duration(minutes: 1), (_) => check());
    ref.onDispose(() => _timer?.cancel());
    ref.listen<AsyncValue<List<MergeTrackingEntry>>>(mergeTrackingProvider, (
      _,
      next,
    ) {
      if (next.hasValue) unawaited(check());
    });
    return null;
  }

  /// Sends today's digest if it is due and has not gone out yet.
  Future<void> check() async {
    if (_checking) return;
    _checking = true;
    try {
      final myPrs = ref.read(configNotifierProvider).value?.myPrs;
      if (myPrs == null || !myPrs.enabled || !myPrs.digestEnabled) return;
      final hm = myPrs.digestHourMinute;
      if (hm == null) return;
      final now = ref.read(myPrsClockProvider)();
      final due = DateTime(now.year, now.month, now.day, hm.hour, hm.minute);
      if (now.isBefore(due)) return;
      final today = _dayKey(now);
      if (state == today) return;

      final entries = ref.read(mergeTrackingProvider).value;
      if (entries == null) return; // not loaded yet; the listener retries

      final prefs = await _prefs();
      if (!ref.mounted) return;
      final last = _read(prefs);
      if (last == today) {
        state = today;
        return;
      }

      final summary = summarizeMyPrs(entries);
      if (!summary.isEmpty) {
        sendPRNotification(
          platform: ref.read(platformServicesProvider),
          title: 'Your PRs: ${summary.describe()}',
          body: myPrsDigestBody(entries, now: now),
          location: '/merge',
        );
      }
      state = today;
      _write(prefs, today);
    } finally {
      _checking = false;
    }
  }

  Future<SharedPreferences?> _prefs() async {
    try {
      return await SharedPreferences.getInstance();
    } catch (e) {
      debugPrint('MyPrsDigestNotifier: preferences unavailable: $e');
      return null;
    }
  }

  String? _read(SharedPreferences? prefs) {
    try {
      return prefs?.getString(_lastDigestKey);
    } catch (e) {
      debugPrint('MyPrsDigestNotifier: failed to read last digest: $e');
      return null;
    }
  }

  void _write(SharedPreferences? prefs, String day) {
    if (prefs == null) return;
    unawaited(
      prefs.setString(_lastDigestKey, day).catchError((Object e) {
        debugPrint('MyPrsDigestNotifier: failed to save last digest: $e');
        return false;
      }),
    );
  }
}

final myPrsDigestProvider = NotifierProvider<MyPrsDigestNotifier, String?>(
  MyPrsDigestNotifier.new,
);

/// The digest body: the most urgent few PRs, one per line.
@visibleForTesting
String myPrsDigestBody(
  List<MergeTrackingEntry> entries, {
  required DateTime now,
  int max = 3,
}) {
  final urgent = myPrsNeedingOperator(entries);
  final lines = [
    for (final e in urgent.take(max))
      '${e.repo}#${e.number} — ${shortAttentionLabel(e, now: now)}',
    if (urgent.length > max) '+ ${urgent.length - max} more',
  ];
  return lines.join('\n');
}

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

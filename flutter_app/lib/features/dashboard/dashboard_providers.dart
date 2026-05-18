import 'dart:async';
import 'dart:convert';
import 'package:flutter/foundation.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../../core/api/api_client.dart';
import '../../core/api/sse_client.dart';
import '../../core/models/pr.dart';
import '../../core/platform/platform_services_provider.dart';
import '../../core/state/local_state_notifier.dart';
import '../../main.dart' show sendPRNotification;
import '../issues/issues_providers.dart';
import '../stats/stats_filters.dart';
import 'activity_filters.dart';

final apiClientProvider = Provider<ApiClient>((ref) {
  return ApiClient(platform: ref.watch(platformServicesProvider));
});

final sseClientProvider = Provider<SseClient>((ref) {
  return SseClient(platform: ref.watch(platformServicesProvider));
});

final sseStreamProvider = StreamProvider<SseEvent>((ref) {
  final client = ref.watch(sseClientProvider);
  ref.onDispose(() => client.disconnect());
  return client.connect();
});

/// Latest circuit-breaker-tripped payload from the daemon. Null until the
/// breaker fires or after the user dismisses the banner. Set by the
/// `circuit_breaker_tripped` SSE handler and cleared by the dashboard's
/// dismiss button — the user must acknowledge the event so cost spikes
/// can't slip by silently (regression guard for the 2026-04-22 runaway).
final circuitBreakerProvider =
    NotifierProvider<LocalStateNotifier<String?>, String?>(
      () => LocalStateNotifier<String?>(null),
    );

/// Stale non_monitored entries the daemon's rename probe has flagged
/// (#493). Keys are the configured `old_repo` slug, values are the
/// canonical `new_repo` GitHub reported. The map accumulates entries
/// across the session so a future UI surface (banner, settings hint)
/// can list every stale slug the operator should clean up manually.
///
/// The daemon dedupes per (old, new) pair across its lifetime, so this
/// map grows only when a NEW drift is detected — not on every probe
/// tick. Cleared on app restart (matches the daemon-side dedup reset).
final nonMonitoredStaleProvider =
    NotifierProvider<
      LocalStateNotifier<Map<String, String>>,
      Map<String, String>
    >(() => LocalStateNotifier<Map<String, String>>(const <String, String>{}));

/// Tracks whether the desktop app is trying to spawn the daemon.
final daemonStartingProvider = NotifierProvider<LocalStateNotifier<bool>, bool>(
  () => LocalStateNotifier<bool>(false),
);

enum DaemonConnectionPhase { connecting, connected, stale, offline }

class DaemonConnectionStatus {
  final DaemonConnectionPhase phase;
  final DateTime? lastEventAt;
  final String? message;

  const DaemonConnectionStatus({
    required this.phase,
    this.lastEventAt,
    this.message,
  });

  const DaemonConnectionStatus.connecting()
    : this(phase: DaemonConnectionPhase.connecting);
}

class DaemonConnectionNotifier extends Notifier<DaemonConnectionStatus> {
  static const staleAfter = Duration(seconds: 60);
  static const checkEvery = Duration(seconds: 5);
  static const maxReconnectDelay = Duration(minutes: 5);

  Timer? _watchdog;
  bool _checkingHealth = false;
  int _reconnectAttempts = 0;

  @override
  DaemonConnectionStatus build() {
    ref.listen<AsyncValue<SseEvent>>(sseStreamProvider, (prev, next) {
      if (next.isLoading && !(prev?.hasValue ?? false)) {
        state = const DaemonConnectionStatus.connecting();
      }
      if (next.hasError) {
        state = DaemonConnectionStatus(
          phase: DaemonConnectionPhase.offline,
          lastEventAt: state.lastEventAt,
          message: next.error.toString(),
        );
      }
      next.whenData((_) {
        _reconnectAttempts = 0;
        state = DaemonConnectionStatus(
          phase: DaemonConnectionPhase.connected,
          lastEventAt: DateTime.now(),
        );
      });
    });
    _watchdog = Timer.periodic(checkEvery, (_) => _checkForStaleStream());
    ref.onDispose(() => _watchdog?.cancel());
    return const DaemonConnectionStatus.connecting();
  }

  void _checkForStaleStream() {
    final lastEventAt = state.lastEventAt;
    if (lastEventAt == null) return;
    if (DateTime.now().difference(lastEventAt) < _currentStaleAfter()) return;
    if (state.phase == DaemonConnectionPhase.offline) return;
    state = DaemonConnectionStatus(
      phase: DaemonConnectionPhase.stale,
      lastEventAt: lastEventAt,
      message: 'No events received for ${staleAfter.inSeconds}s',
    );
    unawaited(_verifyAndReconnect());
  }

  Duration _currentStaleAfter() {
    final shift = _reconnectAttempts > 3 ? 3 : _reconnectAttempts;
    final delay = Duration(seconds: staleAfter.inSeconds * (1 << shift));
    return delay.compareTo(maxReconnectDelay) > 0 ? maxReconnectDelay : delay;
  }

  Future<void> _verifyAndReconnect() async {
    if (_checkingHealth) return;
    _checkingHealth = true;
    try {
      final healthy = await ref.read(apiClientProvider).checkHealth();
      if (!ref.mounted) return;
      if (healthy) {
        _reconnectAttempts++;
        state = DaemonConnectionStatus(
          phase: DaemonConnectionPhase.connecting,
          lastEventAt: DateTime.now(),
        );
        ref.invalidate(sseStreamProvider);
      } else {
        state = DaemonConnectionStatus(
          phase: DaemonConnectionPhase.offline,
          lastEventAt: state.lastEventAt,
          message: 'Health check failed',
        );
      }
    } finally {
      _checkingHealth = false;
    }
  }
}

final daemonConnectionProvider =
    NotifierProvider<DaemonConnectionNotifier, DaemonConnectionStatus>(
      DaemonConnectionNotifier.new,
    );

/// Tracks PRs currently being reviewed, keyed by "repo:prNumber". Used to
/// show spinners in the tile list and detail view.
///
/// The value is the baseline `latestReview.id` (or 0 if the PR had no
/// prior review) captured when the review started. Reconciliation compares
/// this against the PR's current `latestReview.id` on every list refresh:
/// if they differ, a *new* review has landed and the entry is stale. This
/// is the recovery path for missed SSE events — the broker drops events
/// silently on subscriber back-pressure, so we can't rely on
/// `review_completed` always arriving to clear the spinner.
final reviewingPRsProvider =
    NotifierProvider<LocalStateNotifier<Map<String, int>>, Map<String, int>>(
      () => LocalStateNotifier<Map<String, int>>(const <String, int>{}),
    );

/// Increments on review_completed and on SSE reconnects (to catch up on missed events).
class PrListRefreshNotifier extends Notifier<int> {
  @override
  int build() {
    ref.listen<AsyncValue<SseEvent>>(sseStreamProvider, (prev, next) {
      // When SSE (re)connects after being disconnected, refresh the PR list
      // to catch up on any events that arrived during the disconnection window.
      if (!(prev?.hasValue ?? false) && next.hasValue) {
        Future.microtask(() {
          // Guard against the notifier being disposed between scheduling and
          // running the microtask — e.g. provider rebuild on SSE reconnect.
          // Riverpod 3 exposes `ref.mounted` for exactly this race.
          if (!ref.mounted) return;
          state++;
        });
      }
      next.whenData((event) => _handleSseEvent(ref, event));
    });
    return 0;
  }

  void update(int Function(int) updater) => state = updater(state);
}

final prListRefreshProvider = NotifierProvider<PrListRefreshNotifier, int>(
  PrListRefreshNotifier.new,
);

void _handleSseEvent(Ref ref, SseEvent event) {
  try {
    final data = jsonDecode(event.data) as Map<String, dynamic>;
    final repo = data['repo'] as String? ?? '';
    final prNumber = (data['pr_number'] as num?)?.toInt();
    final prId = (data['pr_id'] as num?)?.toInt();
    final key = (repo.isNotEmpty && prNumber != null)
        ? '$repo:$prNumber'
        : null;

    switch (event.type) {
      case 'review_started':
        if (key != null) {
          final baseline = _baselineReviewId(
            ref,
            repo: repo,
            prNumber: prNumber!,
          );
          ref
              .read(reviewingPRsProvider.notifier)
              .update((s) => {...s, key: baseline});
        }
        sendPRNotification(
          platform: ref.read(platformServicesProvider),
          title: 'Review Started',
          body: '$repo #$prNumber',
          prId: prId,
        );

      case 'review_completed':
        // Remove from in-progress
        if (key != null) {
          ref
              .read(reviewingPRsProvider.notifier)
              .update((s) => Map.of(s)..remove(key));
        }
        final severity = data['severity'] as String? ?? '';
        sendPRNotification(
          platform: ref.read(platformServicesProvider),
          title: 'Review Complete — $severity',
          body: '$repo #$prNumber',
          prId: prId,
        );
        ref.read(prListRefreshProvider.notifier).update((s) => s + 1);

      case 'review_error':
        // key may be null for trigger early-fail events (only have pr_id)
        if (key != null) {
          ref
              .read(reviewingPRsProvider.notifier)
              .update((s) => Map.of(s)..remove(key));
        } else if (prId != null) {
          // Look up by store ID from cached PR list
          final prs = ref.read(prsProvider).value ?? [];
          final pr = prs.where((p) => p.id == prId).firstOrNull;
          if (pr != null) {
            final k = '${pr.repo}:${pr.number}';
            ref
                .read(reviewingPRsProvider.notifier)
                .update((s) => Map.of(s)..remove(k));
          }
        }

      case 'review_skipped':
        // Manual trigger on a PR with unchanged HEAD SHA (re-request,
        // legacy backfill, or any policy gate) returns no real review,
        // so the optimistic spinner that dashboard_screen sets must be
        // cleared explicitly. Without this case the spinner stayed
        // colgado after every trigger that hit a dedup branch — the
        // regression flagged on theburrowhub/heimdallm#322.
        if (key != null) {
          ref
              .read(reviewingPRsProvider.notifier)
              .update((s) => Map.of(s)..remove(key));
        }

      // ── Issue tracking events ──────────────────────────────────────────
      case 'issue_detected':
        ref.read(issueListRefreshProvider.notifier).update((s) => s + 1);

      case 'issue_review_started':
        final issueNumber = (data['number'] as num?)?.toInt();
        final issueKey = (repo.isNotEmpty && issueNumber != null)
            ? '$repo:$issueNumber'
            : null;
        if (issueKey != null) {
          ref
              .read(reviewingIssuesProvider.notifier)
              .update((s) => {...s, issueKey});
        }

      case 'issue_review_completed':
        final issueNumber = (data['number'] as num?)?.toInt();
        final issueKey = (repo.isNotEmpty && issueNumber != null)
            ? '$repo:$issueNumber'
            : null;
        if (issueKey != null) {
          ref
              .read(reviewingIssuesProvider.notifier)
              .update((s) => s.difference({issueKey}));
        }
        ref.read(issueListRefreshProvider.notifier).update((s) => s + 1);

      case 'issue_refinement_done':
      case 'issue_implemented':
      case 'issue_promoted':
        final rawNumber = data['number'] ?? data['issue_number'];
        final issueNumber = (rawNumber as num?)?.toInt();
        final issueKey = (repo.isNotEmpty && issueNumber != null)
            ? '$repo:$issueNumber'
            : null;
        if (issueKey != null) {
          ref
              .read(reviewingIssuesProvider.notifier)
              .update((s) => s.difference({issueKey}));
          ref
              .read(promotingIssuesProvider.notifier)
              .update((s) => s.difference({issueKey}));
        }
        ref.read(issueListRefreshProvider.notifier).update((s) => s + 1);

      case 'issue_review_error':
        final issueNumber = (data['number'] as num?)?.toInt();
        final issueKey = (repo.isNotEmpty && issueNumber != null)
            ? '$repo:$issueNumber'
            : null;
        if (issueKey != null) {
          ref
              .read(reviewingIssuesProvider.notifier)
              .update((s) => s.difference({issueKey}));
        }
        // Terminal failure — bump the list refresh so the issue's row
        // re-fetches its latest review state (e.g. the new
        // auto_implement_no_changes terminal state from #483), matching
        // the behaviour of the other terminal issue events above.
        ref.read(issueListRefreshProvider.notifier).update((s) => s + 1);

      // pr_review_state_changed fires when Tier 3 observes a new
      // aggregate external review state on an auto_implement-created
      // PR (#482 phase 1). The dashboard tile renders the chip from
      // the issue's linked_pr.external_review_state, so a list
      // refresh re-fetches that field and the chip updates live.
      // Without this case the badge can sit stale until the next
      // unrelated refresh (poll completion, manual reload).
      case 'pr_review_state_changed':
        ref.read(issueListRefreshProvider.notifier).update((s) => s + 1);

      // repo_renamed fires when the rename reconciler propagates a
      // GitHub repo/org rename through daemon state (#489). Every
      // cached list keyed on the OLD slug is now stale: PRs, issues,
      // activity, stats. Bump every refresh counter so the next
      // render pulls the post-rename data. Payload also carries
      // worktree_purged so a follow-up surface could badge a
      // dashboard warning when false; for now we just refresh.
      case 'repo_renamed':
        ref.read(prListRefreshProvider.notifier).update((s) => s + 1);
        ref.read(issueListRefreshProvider.notifier).update((s) => s + 1);

      // repo_non_monitored_stale fires when the rename probe detects
      // that an entry in github.non_monitored has been renamed
      // upstream (#493). The daemon does NOT auto-rewrite those
      // entries — they reflect explicit operator-disabled state — so
      // we accumulate the (old, new) pairs in a provider that a
      // future settings/banner surface can list. No list refresh
      // bump: the disabled entries don't drive any daemon polling
      // so cached views are unaffected.
      case 'repo_non_monitored_stale':
        final oldRepo = data['old_repo'] as String? ?? '';
        final newRepo = data['new_repo'] as String? ?? '';
        if (oldRepo.isNotEmpty && newRepo.isNotEmpty) {
          ref
              .read(nonMonitoredStaleProvider.notifier)
              .update((s) => {...s, oldRepo: newRepo});
        }

      // ── Circuit breaker ────────────────────────────────────────────────
      case 'circuit_breaker_tripped':
        final repo = data['repo'] as String? ?? 'unknown';
        final prNumber = (data['pr_number'] as num?)?.toInt() ?? 0;
        final reason = data['reason'] as String? ?? '';
        ref
            .read(circuitBreakerProvider.notifier)
            .set('$repo #$prNumber — $reason');
    }
  } catch (_) {}
}

/// The authenticated GitHub username (for My PRs vs My Reviews split).
final meProvider = FutureProvider<String>((ref) async {
  final api = ref.watch(apiClientProvider);
  return api.fetchMe();
});

final prsProvider = FutureProvider<List<PR>>((ref) async {
  ref.watch(prListRefreshProvider);
  // Watch meProvider so the tray is rebuilt (with correct author filter)
  // as soon as the username loads after startup.
  ref.watch(meProvider);
  final filters = ref.watch(activityFiltersProvider);
  final api = ref.watch(apiClientProvider);
  final prs = await api.fetchPRs(states: filters.states.toList());
  _rebuildTray(ref, prs);
  _reconcileReviewingPRs(ref, prs);
  return prs;
});

int _baselineReviewId(Ref ref, {required String repo, required int prNumber}) {
  // Falls back to 0 when the PR list hasn't loaded yet (e.g. SSE event
  // arrives before the initial /prs fetch). 0 is the same baseline used
  // for a first-review: as soon as the PR list populates with a non-zero
  // latestReview.id, reconciliation will clear the stale entry.
  final prs = ref.read(prsProvider).value ?? const <PR>[];
  final pr = prs
      .where((p) => p.repo == repo && p.number == prNumber)
      .firstOrNull;
  return pr?.latestReview?.id ?? 0;
}

/// Drops entries from `reviewingPRsProvider` whose PR's current
/// `latestReview.id` no longer matches the baseline captured at review
/// start. Scheduled as a separate microtask so we don't mutate provider
/// state during `prsProvider`'s build (Riverpod anti-pattern). Runs
/// after every PR list refresh as a recovery path for missed
/// `review_completed` / `review_error` SSE events.
void _reconcileReviewingPRs(Ref ref, List<PR> prs) {
  Future(() {
    try {
      final current = ref.read(reviewingPRsProvider);
      if (current.isEmpty) return;
      final next = reconcileReviewing(current, prs);
      if (next.length != current.length) {
        ref.read(reviewingPRsProvider.notifier).set(next);
      }
    } catch (_) {
      // ref may be disposed if the provider was rebuilt between scheduling
      // and execution — dropping the reconcile is safe, the next refresh
      // will try again.
    }
  });
}

/// Pure helper: given the current reviewing map and the latest PR list,
/// returns the map with stale entries removed. An entry is stale when the
/// PR's current `latestReview.id` differs from the baseline stored at
/// review start (a new review has landed). PRs not present in `prs` keep
/// their entry — a missing PR may just mean the list is filtered, and
/// dropping the key would flicker the spinner off prematurely.
@visibleForTesting
Map<String, int> reconcileReviewing(Map<String, int> current, List<PR> prs) {
  if (current.isEmpty) return current;
  final byKey = <String, PR>{
    for (final pr in prs) '${pr.repo}:${pr.number}': pr,
  };
  final next = <String, int>{};
  for (final entry in current.entries) {
    final pr = byKey[entry.key];
    if (pr == null) {
      next[entry.key] = entry.value;
      continue;
    }
    final currentId = pr.latestReview?.id ?? 0;
    if (currentId == entry.value) {
      next[entry.key] = entry.value;
    }
  }
  return next;
}

final statsProvider = FutureProvider<Map<String, dynamic>>((ref) async {
  ref.watch(prListRefreshProvider); // refresh stats when reviews complete
  final filters = ref.watch(statsFiltersProvider);
  final api = ref.watch(apiClientProvider);
  return api.fetchStats(
    repos: filters.repos.toList(),
    orgs: filters.orgs.toList(),
  );
});

void _rebuildTray(Ref ref, List<PR> prs) {
  Future(() async {
    try {
      final me = ref.read(meProvider).value;
      // Don't build tray until we know the username — without it the
      // author filter falls back to '' and shows the user's own PRs.
      if (me == null || me.isEmpty) return;
      await ref
          .read(platformServicesProvider)
          .rebuildTrayMenu(prs: prs, me: me);
    } catch (_) {}
  });
}

import 'dart:async';
import 'dart:convert';
import 'package:flutter/foundation.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../../core/api/api_client.dart';
import '../../core/api/sse_client.dart';
import '../../core/instances/aggregation.dart';
import '../../core/instances/instances_providers.dart';
import '../../core/models/pr.dart';
import '../../core/platform/platform_services_provider.dart';
import '../../core/state/local_state_notifier.dart';
import '../../main.dart' show sendPRNotification;
import '../issues/issues_providers.dart';
import '../stats/stats_filters.dart';
import 'activity_filters.dart';

/// The client for the currently selected instance.
///
/// On a single-daemon install (and whenever "all instances" is selected) this
/// is the local daemon, so the ~20 call sites that watch it are unchanged.
/// Mutating actions go here; aggregated reads use [prsByInstanceProvider] and
/// friends, which fan out.
final apiClientProvider = Provider<ApiClient>((ref) {
  final active = ref.watch(activeInstanceProvider);
  return ref.watch(apiClientForProvider(active ?? ''));
});

/// The hub's own client. Control-plane calls always go here, never to a
/// selected instance.
final localApiClientProvider = Provider<ApiClient>((ref) {
  return ref.watch(hubApiClientProvider);
});

final sseClientProvider = Provider<SseClient>((ref) {
  return SseClient(platform: ref.watch(platformServicesProvider));
});

/// Events from every instance the UI is scoped to, merged into one stream and
/// tagged with their origin.
///
/// Keeps the single-daemon `Stream<SseEvent>` shape so the dozen-plus existing
/// listeners are untouched; only code that cares about provenance reads
/// [SseEvent.instanceId].
final sseStreamProvider = StreamProvider<SseEvent>((ref) {
  final clients = ref.watch(instanceSseClientsProvider);
  return mergeInstanceEvents(clients);
});

/// Resolves the client for one instance during a fan-out.
///
/// The empty id is the daemon this app manages, and it deliberately goes
/// through [apiClientProvider]: that is the single seam production wiring and
/// tests both override, and bypassing it here would make an injected client
/// silently ignored.
ApiClient clientForInstance(Ref ref, String instanceId) {
  return instanceId.isEmpty
      ? ref.read(apiClientProvider)
      : ref.read(apiClientForProvider(instanceId));
}

/// The [WidgetRef] overload, for widgets acting on a record held by a specific
/// instance. Duplicated rather than generalised because Ref and WidgetRef share
/// no common `read` interface to abstract over.
ApiClient clientForInstanceOf(WidgetRef ref, String instanceId) {
  return instanceId.isEmpty
      ? ref.read(apiClientProvider)
      : ref.read(apiClientForProvider(instanceId));
}

/// Builds the key used by [reviewingPRsProvider].
///
/// Instance-qualified when clustering is on: two instances can both hold the
/// same repo and PR number (a PR added by hand to each), and an unqualified
/// key would make one machine's spinner clear the other's. The unqualified
/// form is preserved exactly for the empty instance id so single-daemon
/// behaviour — and its tests — are unchanged.
String reviewKeyFor(String instanceId, String repo, int number) =>
    instanceId.isEmpty ? '$repo:$number' : '$instanceId|$repo:$number';

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
        // A dropped/errored SSE stream does NOT mean the daemon is down: the
        // long-lived connection resets for many benign reasons (idle timeout,
        // the daemon momentarily busy, a network blip). Going straight to
        // "offline" here is what flashes a bogus "Server unavailable" while the
        // daemon is still reachable. Instead show "reconnecting" and verify
        // ownership first — _verifyAndReconnect reconnects when the daemon answers
        // and only falls back to offline when it genuinely doesn't.
        if (state.phase != DaemonConnectionPhase.offline &&
            state.phase != DaemonConnectionPhase.connecting) {
          state = DaemonConnectionStatus(
            phase: DaemonConnectionPhase.connecting,
            lastEventAt: state.lastEventAt,
            message: 'Reconnecting…',
          );
        }
        unawaited(_verifyAndReconnect());
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
      final reachable =
          await ref.read(apiClientProvider).daemonReachable() ==
          PortOwner.daemon;
      if (!ref.mounted) return;
      if (reachable) {
        _reconnectAttempts++;
        state = DaemonConnectionStatus(
          phase: DaemonConnectionPhase.connecting,
          lastEventAt: DateTime.now(),
        );
        // Back off before reconnecting so a stream that errors instantly
        // (daemon reachable but the SSE endpoint flapping) can't hot-loop:
        // 1s, 2s, 4s… capped. _checkingHealth stays true across the delay,
        // so overlapping error emissions collapse into this one attempt.
        final backoffShift = _reconnectAttempts > 4 ? 4 : _reconnectAttempts;
        await Future<void>.delayed(Duration(seconds: 1 << backoffShift));
        if (!ref.mounted) return;
        ref.invalidate(sseStreamProvider);
      } else {
        state = DaemonConnectionStatus(
          phase: DaemonConnectionPhase.offline,
          lastEventAt: state.lastEventAt,
          message: 'Daemon reachability check failed',
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
    // Qualify by the instance that emitted the event, so one machine's
    // review_completed cannot clear another machine's spinner for the same
    // repo and PR number.
    final key = (repo.isNotEmpty && prNumber != null)
        ? reviewKeyFor(event.instanceId, repo, prNumber)
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
          final prs = cachedMergedPRs(ref);
          final pr = prs.where((p) => p.id == prId).firstOrNull;
          if (pr != null) {
            final k = reviewKeyFor(event.instanceId, pr.repo, pr.number);
            ref
                .read(reviewingPRsProvider.notifier)
                .update((s) => Map.of(s)..remove(k));
          }
        }
        final error = data['error'] as String? ?? 'Review failed.';
        final manuallyCancelled = data['reason'] == 'manual_cancelled';
        sendPRNotification(
          platform: ref.read(platformServicesProvider),
          title: manuallyCancelled ? 'Review Cancelled' : 'Review Failed',
          body: key != null ? '$repo #$prNumber — $error' : error,
          prId: prId,
        );
        // The durable retry/failure state is attached to GET /prs. Refresh it
        // immediately instead of returning the tile to a silent PENDING badge.
        ref.read(prListRefreshProvider.notifier).update((s) => s + 1);

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

/// PRs from every instance the UI is scoped to, each tagged with its origin.
///
/// A failure on one instance does not fail the whole list: the others still
/// render and the failure is reported separately, because a dashboard that
/// silently drops one machine's PRs looks identical to one where that machine
/// simply has no work.
final prsByInstanceProvider = FutureProvider<AggregatedResult<PR>>((ref) async {
  ref.watch(prListRefreshProvider);
  // Watch meProvider so the tray is rebuilt (with correct author filter)
  // as soon as the username loads after startup.
  ref.watch(meProvider);
  final filters = ref.watch(activityFiltersProvider);
  final targets = ref.watch(targetInstancesProvider);

  final result = await aggregate<PR>(
    targets: targets,
    clientFor: (id) => clientForInstance(ref, id),
    fetch: (client) => client.fetchPRs(states: filters.states.toList()),
  );
  final prs = result.values;
  _rebuildTray(ref, prs);
  _reconcileReviewingPRs(ref, prs);
  return result;
});

/// The flat PR list. Derived from [prsByInstanceProvider] so every existing
/// consumer keeps its `List<PR>` shape.
final prsProvider = FutureProvider<List<PR>>((ref) async {
  final aggregated = await ref.watch(prsByInstanceProvider.future);
  return aggregated.values;
});

/// The merged PR list as currently cached, without awaiting. Used by the
/// synchronous reconciliation and tray paths, which must see every instance's
/// PRs and not just the selected one.
List<PR> cachedMergedPRs(Ref ref) =>
    ref.read(prsByInstanceProvider).value?.values ?? const <PR>[];

/// Instances that could not be read on the last PR refresh. Drives the
/// dashboard's degraded-state banner.
final instanceReadFailuresProvider = Provider<List<InstanceFailure>>((ref) {
  return ref.watch(prsByInstanceProvider).value?.failures ?? const [];
});

int _baselineReviewId(Ref ref, {required String repo, required int prNumber}) {
  // Falls back to 0 when the PR list hasn't loaded yet (e.g. SSE event
  // arrives before the initial /prs fetch). 0 is the same baseline used
  // for a first-review: as soon as the PR list populates with a non-zero
  // latestReview.id, reconciliation will clear the stale entry.
  final prs = cachedMergedPRs(ref);
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
/// daemon reports a terminal failed/cancelled execution or the PR's current
/// `latestReview.id` differs from the baseline stored at review start (a new
/// review has landed). PRs not present in `prs` keep their entry — a missing
/// PR may just mean the list is filtered, and dropping the key would flicker
/// the spinner off prematurely.
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
    final execution = pr.reviewStatus;
    if (execution != null && !execution.active) {
      continue;
    }
    final currentId = pr.latestReview?.id ?? 0;
    if (currentId == entry.value) {
      next[entry.key] = entry.value;
    }
  }
  return next;
}

/// Stats per instance, so the Stats screen can show both the fleet total and
/// the split.
final statsByInstanceProvider =
    FutureProvider<AggregatedResult<Map<String, dynamic>>>((ref) async {
      ref.watch(prListRefreshProvider); // refresh stats when reviews complete
      final filters = ref.watch(statsFiltersProvider);
      return aggregateOne<Map<String, dynamic>>(
        targets: ref.watch(targetInstancesProvider),
        clientFor: (id) => clientForInstance(ref, id),
        fetch: (client) => client.fetchStats(
          repos: filters.repos.toList(),
          orgs: filters.orgs.toList(),
        ),
      );
    });

final statsProvider = FutureProvider<Map<String, dynamic>>((ref) async {
  final aggregated = await ref.watch(statsByInstanceProvider.future);
  return mergeStatsMaps(aggregated.values);
});

/// Sums the numeric fields of per-instance stats into one fleet view.
///
/// Numbers add up; anything else (a label, a nested breakdown) is taken from
/// the first instance that reports it, because averaging or concatenating a
/// non-numeric field would invent a value nobody measured.
@visibleForTesting
Map<String, dynamic> mergeStatsMaps(List<Map<String, dynamic>> parts) {
  if (parts.isEmpty) return const {};
  if (parts.length == 1) return parts.first;
  final merged = <String, dynamic>{};
  for (final part in parts) {
    for (final entry in part.entries) {
      final existing = merged[entry.key];
      final value = entry.value;
      if (existing is num && value is num) {
        merged[entry.key] = existing + value;
      } else if (existing == null) {
        merged[entry.key] = value;
      }
    }
  }
  return merged;
}

/// Live GitHub API rate limits. Fetched on demand (not polled); invalidate to
/// refresh from the Stats screen.
final githubRateLimitProvider = FutureProvider.autoDispose<Map<String, dynamic>>(
  (ref) async {
    final api = ref.watch(apiClientProvider);
    return api.fetchGitHubRateLimit();
  },
);

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

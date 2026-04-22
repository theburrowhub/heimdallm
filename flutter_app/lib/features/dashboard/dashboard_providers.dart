import 'dart:convert';
import 'package:flutter/foundation.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../../core/api/api_client.dart';
import '../../core/api/sse_client.dart';
import '../../core/models/pr.dart';
import '../../core/platform/platform_services_provider.dart';
import '../../main.dart' show sendPRNotification;
import '../issues/issues_providers.dart';
import '../stats/stats_filters.dart';

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
final reviewingPRsProvider = StateProvider<Map<String, int>>(
  (ref) => const <String, int>{},
);

/// Increments on review_completed and on SSE reconnects (to catch up on missed events).
final StateProvider<int> prListRefreshProvider = StateProvider<int>((ref) {
  ref.listen<AsyncValue<SseEvent>>(sseStreamProvider, (prev, next) {
    // When SSE (re)connects after being disconnected, refresh the PR list
    // to catch up on any events that arrived during the disconnection window.
    if (!(prev?.hasValue ?? false) && next.hasValue) {
      Future.microtask(
          () => ref.read(prListRefreshProvider.notifier).update((s) => s + 1));
    }
    next.whenData((event) => _handleSseEvent(ref, event));
  });
  return 0;
});

void _handleSseEvent(Ref ref, SseEvent event) {
  try {
    final data = jsonDecode(event.data) as Map<String, dynamic>;
    final repo = data['repo'] as String? ?? '';
    final prNumber = (data['pr_number'] as num?)?.toInt();
    final prId = (data['pr_id'] as num?)?.toInt();
    final key = (repo.isNotEmpty && prNumber != null) ? '$repo:$prNumber' : null;

    switch (event.type) {
      case 'review_started':
        if (key != null) {
          final baseline = _baselineReviewId(ref, repo: repo, prNumber: prNumber!);
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
              .update((s) => {...s}..remove(key));
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
              .update((s) => {...s}..remove(key));
        } else if (prId != null) {
          // Look up by store ID from cached PR list
          final prs = ref.read(prsProvider).valueOrNull ?? [];
          final pr = prs.where((p) => p.id == prId).firstOrNull;
          if (pr != null) {
            final k = '${pr.repo}:${pr.number}';
            ref
                .read(reviewingPRsProvider.notifier)
                .update((s) => {...s}..remove(k));
          }
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
          ref.read(reviewingIssuesProvider.notifier).update((s) => {...s, issueKey});
        }

      case 'issue_review_completed':
        final issueNumber = (data['number'] as num?)?.toInt();
        final issueKey = (repo.isNotEmpty && issueNumber != null)
            ? '$repo:$issueNumber'
            : null;
        if (issueKey != null) {
          ref.read(reviewingIssuesProvider.notifier).update((s) => s.difference({issueKey}));
        }
        ref.read(issueListRefreshProvider.notifier).update((s) => s + 1);

      case 'issue_review_error':
        final issueNumber = (data['number'] as num?)?.toInt();
        final issueKey = (repo.isNotEmpty && issueNumber != null)
            ? '$repo:$issueNumber'
            : null;
        if (issueKey != null) {
          ref.read(reviewingIssuesProvider.notifier).update((s) => s.difference({issueKey}));
        }
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
  final api = ref.watch(apiClientProvider);
  final prs = await api.fetchPRs();
  _rebuildTray(ref, prs);
  _reconcileReviewingPRs(ref, prs);
  return prs;
});

int _baselineReviewId(Ref ref, {required String repo, required int prNumber}) {
  final prs = ref.read(prsProvider).valueOrNull ?? const <PR>[];
  final pr = prs
      .where((p) => p.repo == repo && p.number == prNumber)
      .firstOrNull;
  return pr?.latestReview?.id ?? 0;
}

/// Drops entries from `reviewingPRsProvider` whose PR's current
/// `latestReview.id` no longer matches the baseline captured at review
/// start. Runs after every PR list refresh as a recovery path for missed
/// `review_completed` / `review_error` SSE events.
void _reconcileReviewingPRs(Ref ref, List<PR> prs) {
  final current = ref.read(reviewingPRsProvider);
  if (current.isEmpty) return;
  final next = reconcileReviewing(current, prs);
  if (next.length != current.length) {
    ref.read(reviewingPRsProvider.notifier).state = next;
  }
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
      final me = ref.read(meProvider).valueOrNull;
      // Don't build tray until we know the username — without it the
      // author filter falls back to '' and shows the user's own PRs.
      if (me == null || me.isEmpty) return;
      await ref.read(platformServicesProvider).rebuildTrayMenu(prs: prs, me: me);
    } catch (_) {}
  });
}


import 'dart:convert';
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

/// Tracks PRs currently being reviewed, keyed by "repo:prNumber".
/// Used to show spinners in the tile list and detail view.
final reviewingPRsProvider = StateProvider<Set<String>>((ref) => const {});

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
          ref.read(reviewingPRsProvider.notifier).update((s) => {...s, key});
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
          ref.read(reviewingPRsProvider.notifier).update((s) => s.difference({key}));
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
          ref.read(reviewingPRsProvider.notifier).update((s) => s.difference({key}));
        } else if (prId != null) {
          // Look up by store ID from cached PR list
          final prs = ref.read(prsProvider).valueOrNull ?? [];
          final pr = prs.where((p) => p.id == prId).firstOrNull;
          if (pr != null) {
            final k = '${pr.repo}:${pr.number}';
            ref.read(reviewingPRsProvider.notifier).update((s) => s.difference({k}));
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
  return prs;
});

final statsProvider = FutureProvider<Map<String, dynamic>>((ref) async {
  ref.watch(prListRefreshProvider); // refresh stats when reviews complete
  final filters = ref.watch(statsFiltersProvider);
  final api = ref.watch(apiClientProvider);

  // Effective repos: explicit repo selection takes priority. If only orgs
  // are selected, derive the repo list from known PRs + issues so the
  // org filter actually scopes the stats.
  List<String> repos;
  if (filters.repos.isNotEmpty) {
    repos = filters.repos.toList();
  } else if (filters.orgs.isNotEmpty) {
    final prs = ref.watch(prsProvider).valueOrNull ?? [];
    final issues = ref.watch(issuesProvider).valueOrNull ?? [];
    final allRepos = <String>{
      ...prs.map((p) => p.repo),
      ...issues.map((i) => i.repo),
    }..remove('');
    repos = allRepos.where((r) {
      final org = r.contains('/') ? r.split('/').first : r;
      return filters.orgs.contains(org);
    }).toList();
  } else {
    repos = [];
  }
  return api.fetchStats(repos: repos);
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


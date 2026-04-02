import 'dart:convert';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../../core/api/api_client.dart';
import '../../core/api/sse_client.dart';
import '../../core/models/pr.dart';
import '../../core/tray/tray_menu.dart' show TrayMenuRef;
import '../../main.dart' show sendPRNotification;

final apiClientProvider = Provider<ApiClient>((ref) => ApiClient());

final sseClientProvider = Provider<SseClient>((ref) => SseClient());

final sseStreamProvider = StreamProvider<SseEvent>((ref) {
  final client = ref.watch(sseClientProvider);
  ref.onDispose(() => client.disconnect());
  return client.connect();
});

/// Tracks PRs currently being reviewed, keyed by "repo:prNumber".
/// Used to show spinners in the tile list and detail view.
final reviewingPRsProvider = StateProvider<Set<String>>((ref) => const {});

/// Increments only on review_completed — avoids flickering on every SSE event.
final prListRefreshProvider = StateProvider<int>((ref) {
  ref.listen(sseStreamProvider, (_, next) {
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
        sendPRNotification(title: 'Review Started', body: '$repo #$prNumber', prId: prId);

      case 'review_completed':
        // Remove from in-progress
        if (key != null) {
          ref.read(reviewingPRsProvider.notifier).update((s) => s.difference({key}));
        }
        final severity = data['severity'] as String? ?? '';
        sendPRNotification(title: 'Review Complete — $severity', body: '$repo #$prNumber', prId: prId);
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
  final api = ref.watch(apiClientProvider);
  return api.fetchStats();
});

void _rebuildTray(Ref ref, List<PR> prs) {
  Future(() async {
    try {
      final me = ref.read(meProvider).valueOrNull;
      // Don't build tray until we know the username — without it the
      // author filter falls back to '' and shows the user's own PRs.
      if (me == null || me.isEmpty) return;
      await TrayMenuRef.rebuild(prs: prs, me: me);
    } catch (_) {}
  });
}


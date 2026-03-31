import 'dart:convert';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../../core/api/api_client.dart';
import '../../core/api/sse_client.dart';
import 'dart:convert';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../../core/api/api_client.dart';
import '../../core/api/sse_client.dart';
import '../../core/models/config_model.dart';
import '../../core/models/pr.dart';
import '../../core/tray/tray_menu.dart' show TrayMenuRef;
import '../../main.dart' show sendPRNotification;
import '../config/config_providers.dart' show configNotifierProvider;

final apiClientProvider = Provider<ApiClient>((ref) => ApiClient());

final sseClientProvider = Provider<SseClient>((ref) => SseClient());

final sseStreamProvider = StreamProvider<SseEvent>((ref) {
  final client = ref.watch(sseClientProvider);
  ref.onDispose(() => client.disconnect());
  return client.connect();
});

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

    switch (event.type) {
      case 'review_completed':
        final severity = data['severity'] as String? ?? '';
        sendPRNotification(
          title: 'Review Complete — $severity',
          body: '$repo #$prNumber',
          prId: prId,
        );
        ref.read(prListRefreshProvider.notifier).update((s) => s + 1);
      case 'review_started':
        sendPRNotification(
          title: 'Review Started',
          body: '$repo #$prNumber',
          prId: prId,
        );
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
  final api = ref.watch(apiClientProvider);
  final prs = await api.fetchPRs();
  // Rebuild tray menu with fresh data
  _rebuildTray(ref, prs);
  return prs;
});

final statsProvider = FutureProvider<Map<String, dynamic>>((ref) async {
  ref.watch(prListRefreshProvider); // refresh stats when reviews complete
  final api = ref.watch(apiClientProvider);
  return api.fetchStats();
});

void _rebuildTray(Ref ref, List<PR> prs) {
  // Best-effort — ignore errors (tray is not critical path)
  Future(() async {
    try {
      final me = ref.read(meProvider).valueOrNull ?? '';
      final config = ref.read(configNotifierProvider).valueOrNull ?? const AppConfig();
      await TrayMenuRef.rebuild(prs: prs, me: me, config: config);
    } catch (_) {}
  });
}

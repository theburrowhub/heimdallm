import 'dart:async';

import 'package:flutter_test/flutter_test.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:heimdallm/core/api/sse_client.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';

// Tests for the repo_non_monitored_stale SSE handler added in #493.
// The handler accumulates (old → new) pairs into nonMonitoredStaleProvider
// so any future UI surface (banner, settings hint) can list every stale
// non_monitored entry the daemon's rename probe has flagged.
//
// Pattern: override sseStreamProvider with a StreamController, attach a
// listener to prListRefreshProvider so the SSE subscription stays awake
// (the handler runs inside that notifier's build per Riverpod 3 pause
// semantics), then inject events and inspect the target provider.
void main() {
  group('repo_non_monitored_stale SSE handler', () {
    test('adds (old, new) pair to nonMonitoredStaleProvider', () async {
      final events = StreamController<SseEvent>.broadcast();
      final c = ProviderContainer(
        overrides: [sseStreamProvider.overrideWith((_) => events.stream)],
      );
      addTearDown(() async {
        await events.close();
        c.dispose();
      });

      // Keep the SSE handler chain alive: the handler lives inside
      // PrListRefreshNotifier.build, so we need at least one listener.
      c.listen(prListRefreshProvider, (_, _) {});

      events.add(
        const SseEvent(
          type: 'repo_non_monitored_stale',
          data: '{"old_repo":"acme/parked","new_repo":"acme/parked-v2"}',
        ),
      );
      await Future<void>.delayed(Duration.zero);

      expect(c.read(nonMonitoredStaleProvider), {
        'acme/parked': 'acme/parked-v2',
      });
    });

    test('overwrites previous mapping when canonical changes', () async {
      final events = StreamController<SseEvent>.broadcast();
      final c = ProviderContainer(
        overrides: [sseStreamProvider.overrideWith((_) => events.stream)],
      );
      addTearDown(() async {
        await events.close();
        c.dispose();
      });
      c.listen(prListRefreshProvider, (_, _) {});

      // First event: parked → v2.
      events.add(
        const SseEvent(
          type: 'repo_non_monitored_stale',
          data: '{"old_repo":"acme/parked","new_repo":"acme/parked-v2"}',
        ),
      );
      await Future<void>.delayed(Duration.zero);

      // Second event: chained rename, daemon re-warns with the new tip.
      events.add(
        const SseEvent(
          type: 'repo_non_monitored_stale',
          data: '{"old_repo":"acme/parked","new_repo":"acme/final"}',
        ),
      );
      await Future<void>.delayed(Duration.zero);

      expect(c.read(nonMonitoredStaleProvider), {'acme/parked': 'acme/final'});
    });

    test('accumulates entries from independent slugs', () async {
      final events = StreamController<SseEvent>.broadcast();
      final c = ProviderContainer(
        overrides: [sseStreamProvider.overrideWith((_) => events.stream)],
      );
      addTearDown(() async {
        await events.close();
        c.dispose();
      });
      c.listen(prListRefreshProvider, (_, _) {});

      events.add(
        const SseEvent(
          type: 'repo_non_monitored_stale',
          data: '{"old_repo":"acme/foo","new_repo":"acme/foo-v2"}',
        ),
      );
      events.add(
        const SseEvent(
          type: 'repo_non_monitored_stale',
          data: '{"old_repo":"acme/bar","new_repo":"widget/bar"}',
        ),
      );
      await Future<void>.delayed(Duration.zero);

      expect(c.read(nonMonitoredStaleProvider), {
        'acme/foo': 'acme/foo-v2',
        'acme/bar': 'widget/bar',
      });
    });

    test('ignores payload with empty slugs', () async {
      final events = StreamController<SseEvent>.broadcast();
      final c = ProviderContainer(
        overrides: [sseStreamProvider.overrideWith((_) => events.stream)],
      );
      addTearDown(() async {
        await events.close();
        c.dispose();
      });
      c.listen(prListRefreshProvider, (_, _) {});

      // Defensive guard against malformed daemon payloads — the
      // handler must not insert empty-key entries that would clutter
      // any UI surface backed by this provider.
      events.add(
        const SseEvent(
          type: 'repo_non_monitored_stale',
          data: '{"old_repo":"","new_repo":"acme/v2"}',
        ),
      );
      events.add(
        const SseEvent(
          type: 'repo_non_monitored_stale',
          data: '{"old_repo":"acme/old","new_repo":""}',
        ),
      );
      await Future<void>.delayed(Duration.zero);

      expect(c.read(nonMonitoredStaleProvider), isEmpty);
    });
  });
}

import 'dart:async';

import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/api/sse_client.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

import '../../core/platform/fake_platform_services.dart';

void main() {
  test('SSE drop treats an identified degraded daemon as reachable', () async {
    final platform = FakePlatformServices();
    var healthRequests = 0;
    final api = ApiClient(
      platform: platform,
      httpClient: MockClient((_) async {
        healthRequests++;
        return http.Response(
          '{"status":"degraded","checks":{}}',
          503,
          headers: const {'x-heimdallm-daemon': '1'},
        );
      }),
    );
    final events = StreamController<SseEvent>.broadcast();
    final container = ProviderContainer(
      overrides: [
        apiClientProvider.overrideWithValue(api),
        sseStreamProvider.overrideWith((_) => events.stream),
      ],
    );
    final subscription = container.listen(
      daemonConnectionProvider,
      (_, _) {},
      fireImmediately: true,
    );
    addTearDown(() async {
      subscription.close();
      await events.close();
      container.dispose();
    });

    await _waitFor(() => events.hasListener);
    events.add(const SseEvent(type: 'heartbeat', data: '{}'));
    await _waitFor(
      () =>
          container.read(daemonConnectionProvider).phase ==
          DaemonConnectionPhase.connected,
    );

    events.addError(StateError('SSE transport reset'));
    await _waitFor(() => healthRequests == 1);
    await Future<void>.delayed(Duration.zero);

    expect(
      container.read(daemonConnectionProvider).phase,
      DaemonConnectionPhase.connecting,
      reason:
          'HTTP 503 is degraded health, not a lost daemon connection, when the daemon identity header is present',
    );
  });
}

Future<void> _waitFor(bool Function() condition) async {
  final deadline = DateTime.now().add(const Duration(seconds: 2));
  while (DateTime.now().isBefore(deadline)) {
    if (condition()) return;
    await Future<void>.delayed(const Duration(milliseconds: 10));
  }
  fail('condition was not met before timeout');
}

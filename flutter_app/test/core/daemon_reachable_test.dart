import 'dart:io';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'platform/fake_platform_services.dart';

/// Regression tests for the spawn guard behind #646.
///
/// A degraded-but-alive daemon answers /health with 503. The boot path used to
/// treat "not 200" as "no daemon" and spawned another one, which lost the port
/// bind, kept polling GitHub on the same token and drove the shared quota
/// further down — feeding back into more 503s and more spawns. Reachability has
/// to be a separate question from health.
void main() {
  ApiClient clientReturning(http.Response Function() respond) => ApiClient(
    httpClient: MockClient((_) async => respond()),
    platform: FakePlatformServices(apiBaseUrl: 'http://127.0.0.1:7842'),
  );

  group('daemonReachable vs checkHealth', () {
    test('503 degraded: unhealthy, but reachable so we must not spawn', () async {
      final client = clientReturning(() => http.Response('{"status":"degraded"}', 503));
      expect(await client.checkHealth(), isFalse);
      expect(
        await client.daemonReachable(),
        isTrue,
        reason: 'a 503 means a daemon owns the port; spawning a second one is the #646 bug',
      );
    });

    test('401 unauthorized: reachable, so we must not spawn', () async {
      final client = clientReturning(() => http.Response('unauthorized', 401));
      expect(await client.daemonReachable(), isTrue);
    });

    test('200 healthy: reachable', () async {
      final client = clientReturning(() => http.Response('{"status":"ok"}', 200));
      expect(await client.checkHealth(), isTrue);
      expect(await client.daemonReachable(), isTrue);
    });

    test('connection refused: genuinely absent, so spawning is correct', () async {
      final client = ApiClient(
        httpClient: MockClient(
          (_) async => throw const SocketException('Connection refused'),
        ),
        platform: FakePlatformServices(apiBaseUrl: 'http://127.0.0.1:7842'),
      );
      expect(await client.daemonReachable(), isFalse);
    });

    test('a daemon too slow to answer counts as unreachable, not as healthy', () async {
      final client = ApiClient(
        httpClient: MockClient((_) async {
          await Future<void>.delayed(const Duration(seconds: 30));
          return http.Response('{"status":"ok"}', 200);
        }),
        platform: FakePlatformServices(apiBaseUrl: 'http://127.0.0.1:7842'),
      );
      expect(await client.daemonReachable(), isFalse);
      expect(await client.checkHealth(), isFalse);
    });
  });
}

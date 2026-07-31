import 'dart:io';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'platform/fake_platform_services.dart';

/// Regression tests for the spawn guard behind #646.
///
/// A degraded-but-alive daemon answers /health with 503, and so does one that is
/// still wiring up at boot. The boot path used to treat "not 200" as "no daemon"
/// and spawned another one, which lost the port bind, kept polling GitHub on the
/// same token and drove the shared quota further down — feeding back into more
/// 503s and more spawns. Reachability has to be a separate question from health,
/// and it has to tell our daemon apart from an unrelated port squatter.
void main() {
  ApiClient clientReturning(http.Response Function() respond) => ApiClient(
    httpClient: MockClient((_) async => respond()),
    platform: FakePlatformServices(apiBaseUrl: 'http://127.0.0.1:7842'),
  );

  group('daemonReachable vs checkHealth', () {
    test('503 degraded: unhealthy, but ours — must not spawn', () async {
      final client = clientReturning(
        () => http.Response('{"status":"degraded","checks":{}}', 503),
      );
      expect(await client.checkHealth(), isFalse);
      expect(
        await client.daemonReachable(),
        PortOwner.daemon,
        reason: 'a 503 from our daemon means the port is ours; spawning again is the #646 bug',
      );
    });

    test('503 starting: daemon serving mid-wiring is still ours', () async {
      final client = clientReturning(
        () => http.Response('{"status":"starting"}', 503),
      );
      expect(await client.checkHealth(), isFalse);
      expect(await client.daemonReachable(), PortOwner.daemon);
    });

    test('401 unauthorized: only our daemon enforces the token', () async {
      final client = clientReturning(() => http.Response('unauthorized', 401));
      expect(await client.daemonReachable(), PortOwner.daemon);
    });

    test('200 healthy: ours and healthy', () async {
      final client = clientReturning(
        () => http.Response('{"status":"ok","checks":{}}', 200),
      );
      expect(await client.checkHealth(), isTrue);
      expect(await client.daemonReachable(), PortOwner.daemon);
    });

    test('connection refused: genuinely absent, so spawning is correct', () async {
      final client = ApiClient(
        httpClient: MockClient(
          (_) async => throw const SocketException('Connection refused'),
        ),
        platform: FakePlatformServices(apiBaseUrl: 'http://127.0.0.1:7842'),
      );
      expect(await client.daemonReachable(), PortOwner.none);
    });

    test('a daemon too slow to answer counts as absent, not as healthy', () async {
      final client = ApiClient(
        httpClient: MockClient((_) async {
          await Future<void>.delayed(const Duration(seconds: 30));
          return http.Response('{"status":"ok"}', 200);
        }),
        platform: FakePlatformServices(apiBaseUrl: 'http://127.0.0.1:7842'),
      );
      expect(await client.daemonReachable(), PortOwner.none);
      expect(await client.checkHealth(), isFalse);
    });

    group('foreign process squatting on the port', () {
      test('HTML from an unrelated dev server is not our daemon', () async {
        final client = clientReturning(
          () => http.Response('<html><body>hello</body></html>', 200),
        );
        expect(await client.daemonReachable(), PortOwner.foreign);
      });

      test('JSON without a status field is not our daemon', () async {
        final client = clientReturning(() => http.Response('{"foo":"bar"}', 200));
        expect(await client.daemonReachable(), PortOwner.foreign);
      });

      test('a JSON array is not our daemon', () async {
        final client = clientReturning(() => http.Response('[1,2,3]', 200));
        expect(await client.daemonReachable(), PortOwner.foreign);
      });
    });
  });
}

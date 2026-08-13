import 'dart:io';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/platform/platform_services_desktop.dart';
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

  ApiClient clientThrowing(Object error) => ApiClient(
    httpClient: MockClient((_) async => throw error),
    platform: FakePlatformServices(apiBaseUrl: 'http://127.0.0.1:7842'),
  );

  /// Every real daemon response carries this header; it is the authoritative
  /// identity signal.
  const daemonHeaders = {'x-heimdallm-daemon': '1'};

  group('daemonReachable vs checkHealth', () {
    test('503 degraded: unhealthy, but ours — must not spawn', () async {
      final client = clientReturning(
        () => http.Response(
          '{"status":"degraded","checks":{}}',
          503,
          headers: daemonHeaders,
        ),
      );
      expect(await client.checkHealth(), isFalse);
      expect(
        await client.daemonReachable(),
        PortOwner.daemon,
        reason:
            'a 503 from our daemon means the port is ours; spawning again is the #646 bug',
      );
    });

    test('503 starting: daemon serving mid-wiring is still ours', () async {
      final client = clientReturning(
        () =>
            http.Response('{"status":"starting"}', 503, headers: daemonHeaders),
      );
      expect(await client.checkHealth(), isFalse);
      expect(await client.daemonReachable(), PortOwner.daemon);
    });

    test('401 without daemon identity belongs to a foreign service', () async {
      final client = clientReturning(() => http.Response('unauthorized', 401));
      expect(await client.daemonReachable(), PortOwner.foreign);
    });

    test('200 healthy: ours and healthy', () async {
      final client = clientReturning(
        () => http.Response(
          '{"status":"ok","checks":{}}',
          200,
          headers: daemonHeaders,
        ),
      );
      expect(await client.checkHealth(), isTrue);
      expect(await client.daemonReachable(), PortOwner.daemon);
    });

    test(
      'connection refused: genuinely absent, so spawning is correct',
      () async {
        final client = clientThrowing(
          const SocketException('Connection refused'),
        );
        expect(await client.daemonReachable(), PortOwner.none);
      },
    );

    test(
      'a native HTTP timeout proceeds only to the guarded spawn operation',
      () async {
        final client = ApiClient(
          httpClient: MockClient((_) async {
            await Future<void>.delayed(const Duration(seconds: 30));
            return http.Response('{"status":"ok"}', 200);
          }),
          platform: FakePlatformServices(apiBaseUrl: 'http://127.0.0.1:7842'),
          daemonReachabilityTimeout: const Duration(milliseconds: 10),
        );
        expect(await client.daemonReachable(), PortOwner.none);
      },
    );

    test(
      'a relative web endpoint fails closed after a transport error',
      () async {
        final client = ApiClient(
          httpClient: MockClient(
            (_) async => throw http.ClientException('proxy unavailable'),
          ),
          platform: FakePlatformServices(apiBaseUrl: '/api'),
        );
        expect(await client.daemonReachable(), PortOwner.foreign);
      },
    );

    test(
      'a silent TCP listener reaches the native guarded spawn path',
      () async {
        final server = await ServerSocket.bind(InternetAddress.loopbackIPv4, 0);
        final accepted = <Socket>[];
        final subscription = server.listen(accepted.add);
        addTearDown(() async {
          await subscription.cancel();
          for (final socket in accepted) {
            socket.destroy();
          }
          await server.close();
        });

        final httpClient = http.Client();
        addTearDown(httpClient.close);
        final client = ApiClient(
          httpClient: httpClient,
          platform: DesktopPlatformServices(apiPort: server.port),
          daemonReachabilityTimeout: const Duration(milliseconds: 50),
        );

        expect(await client.daemonReachable(), PortOwner.none);
      },
    );

    group('identity: the header is authoritative', () {
      test('header alone identifies the daemon, whatever the body', () async {
        final client = clientReturning(
          () => http.Response('anything', 200, headers: daemonHeaders),
        );
        expect(await client.daemonReachable(), PortOwner.daemon);
      });

      test(
        'older daemon without the header still matches via status+checks',
        () async {
          final client = clientReturning(
            () => http.Response('{"status":"ok","checks":{}}', 200),
          );
          expect(await client.daemonReachable(), PortOwner.daemon);
        },
      );

      test(
        'older daemon without the header matches via status+version',
        () async {
          final client = clientReturning(
            () =>
                http.Response('{"status":"starting","version":"0.7.10"}', 503),
          );
          expect(await client.daemonReachable(), PortOwner.daemon);
        },
      );
    });

    group('foreign process squatting on the port', () {
      test(
        'Spring Boot Actuator style {"status":"UP"} is NOT our daemon',
        () async {
          final client = clientReturning(
            () => http.Response('{"status":"UP"}', 200),
          );
          expect(
            await client.daemonReachable(),
            PortOwner.foreign,
            reason:
                'a bare status field is the shape of most health endpoints; '
                'accepting it would silently prevent the daemon from ever spawning',
          );
        },
      );

      test(
        'a bare {"status":"ok"} from some other service is NOT our daemon',
        () async {
          final client = clientReturning(
            () => http.Response('{"status":"ok"}', 200),
          );
          expect(await client.daemonReachable(), PortOwner.foreign);
        },
      );

      test('HTML from an unrelated dev server is not our daemon', () async {
        final client = clientReturning(
          () => http.Response('<html><body>hello</body></html>', 200),
        );
        expect(await client.daemonReachable(), PortOwner.foreign);
      });

      test('JSON without a status field is not our daemon', () async {
        final client = clientReturning(
          () => http.Response('{"foo":"bar"}', 200),
        );
        expect(await client.daemonReachable(), PortOwner.foreign);
      });

      test('a JSON array is not our daemon', () async {
        final client = clientReturning(() => http.Response('[1,2,3]', 200));
        expect(await client.daemonReachable(), PortOwner.foreign);
      });

      test(
        'a process that does not speak HTTP is deferred to the spawn guard',
        () async {
          final client = clientThrowing(
            http.ClientException('Invalid HTTP response'),
          );
          expect(await client.daemonReachable(), PortOwner.none);
        },
      );
    });
  });

  group('daemonPort', () {
    test('derives from the configured base URL, never hardcoded', () async {
      final client = ApiClient(
        httpClient: MockClient((_) async => http.Response('', 200)),
        platform: FakePlatformServices(apiBaseUrl: 'http://127.0.0.1:9999'),
      );
      expect(client.daemonPort, 9999);
    });
  });
}

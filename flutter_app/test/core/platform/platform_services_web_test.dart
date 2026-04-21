// The web impl must be safe to instantiate and exercise on the Dart VM
// too — the tests drive it directly, with no browser required.
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/platform/platform_services_web.dart';

void main() {
  group('WebPlatformServices', () {
    test('apiBaseUrl is the relative /api prefix', () {
      expect(WebPlatformServices().apiBaseUrl, '/api');
    });

    test('loadApiToken returns null — Nginx injects the token', () async {
      expect(await WebPlatformServices().loadApiToken(), isNull);
    });

    test('readEnv always returns null (no env in browsers)', () {
      expect(WebPlatformServices().readEnv('HOME'), isNull);
      expect(WebPlatformServices().readEnv('GITHUB_TOKEN'), isNull);
    });

    test('ensureSingleInstance always returns true', () async {
      expect(await WebPlatformServices().ensureSingleInstance(), isTrue);
    });

    test('daemonConfigExists always returns true', () async {
      expect(await WebPlatformServices().daemonConfigExists(), isTrue);
    });

    test('defaultDaemonBinaryPath returns null', () {
      expect(WebPlatformServices().defaultDaemonBinaryPath(), isNull);
    });

    test('spawnDaemon throws UnsupportedError', () async {
      expect(
        WebPlatformServices().spawnDaemon('/irrelevant'),
        throwsA(isA<UnsupportedError>()),
      );
    });
  });
}

// Web impl requires the browser runtime now that it uses package:web /
// dart:js_interop to drive the Notification API. `flutter test` on the
// VM skips this file; run `flutter test --platform chrome
// test/core/platform/platform_services_web_test.dart` when exercising
// it, or rely on the integration smoke (docker/scripts/test-web.sh)
// to cover end-to-end behaviour.
@TestOn('browser')
library;

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

import 'dart:io';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/platform/platform_services_desktop.dart';

void main() {
  group('DesktopPlatformServices', () {
    late Directory tempDir;

    setUp(() async {
      tempDir = await Directory.systemTemp.createTemp('heimdallm_test_');
    });

    tearDown(() async {
      if (tempDir.existsSync()) tempDir.deleteSync(recursive: true);
    });

    test('loadApiToken reads and trims the file contents', () async {
      final tokenFile = File('${tempDir.path}/api_token')
        ..writeAsStringSync('secret-123\n');
      final services = DesktopPlatformServices(tokenPath: tokenFile.path);
      final token = await services.loadApiToken();
      expect(token, 'secret-123');
    });

    test('loadApiToken returns null when the file does not exist', () async {
      final services = DesktopPlatformServices(tokenPath: '${tempDir.path}/missing');
      expect(await services.loadApiToken(), isNull);
    });

    test('loadApiToken caches the token; clearApiTokenCache forces re-read', () async {
      final tokenFile = File('${tempDir.path}/api_token')
        ..writeAsStringSync('first');
      final services = DesktopPlatformServices(tokenPath: tokenFile.path);

      expect(await services.loadApiToken(), 'first');
      tokenFile.writeAsStringSync('second');
      expect(await services.loadApiToken(), 'first'); // cached

      services.clearApiTokenCache();
      expect(await services.loadApiToken(), 'second');
    });

    test('readEnv returns the value from Platform.environment', () {
      final services = DesktopPlatformServices();
      final home = services.readEnv('HOME');
      expect(home, isNotNull);
      expect(home, isNotEmpty);
    });

    test('readEnv returns null for missing vars', () {
      final services = DesktopPlatformServices();
      expect(services.readEnv('HEIMDALLM_TEST_MISSING_VAR_XYZ'), isNull);
    });

    test('apiBaseUrl uses the configured port', () {
      final services = DesktopPlatformServices(apiPort: 9999);
      expect(services.apiBaseUrl, 'http://127.0.0.1:9999');
    });

    test('ensureSingleInstance writes a PID file and returns true on fresh start', () async {
      final pidFile = File('${tempDir.path}/ui.pid');
      final services = DesktopPlatformServices(pidFilePath: pidFile.path);
      expect(await services.ensureSingleInstance(), isTrue);
      expect(pidFile.existsSync(), isTrue);
      expect(int.parse(pidFile.readAsStringSync().trim()), pid);
    });

    test('ensureSingleInstance overwrites a stale PID file (process gone)', () async {
      final pidFile = File('${tempDir.path}/ui.pid')
        // Use an impossible high PID that is extremely unlikely to exist.
        ..writeAsStringSync('999999999');
      final services = DesktopPlatformServices(pidFilePath: pidFile.path);
      expect(await services.ensureSingleInstance(), isTrue);
      expect(int.parse(pidFile.readAsStringSync().trim()), pid);
    });
  });
}

import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/daemon/daemon_lifecycle.dart';
import 'package:heimdallm/core/platform/platform_services.dart';
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
      final services = DesktopPlatformServices(
        tokenPath: '${tempDir.path}/missing',
      );
      expect(await services.loadApiToken(), isNull);
    });

    test(
      'loadApiToken caches the token; clearApiTokenCache forces re-read',
      () async {
        final tokenFile = File('${tempDir.path}/api_token')
          ..writeAsStringSync('first');
        final services = DesktopPlatformServices(tokenPath: tokenFile.path);

        expect(await services.loadApiToken(), 'first');
        tokenFile.writeAsStringSync('second');
        expect(await services.loadApiToken(), 'first'); // cached

        services.clearApiTokenCache();
        expect(await services.loadApiToken(), 'second');
      },
    );

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

    test(
      'spawnDaemon refuses a port already owned by another process',
      () async {
        final binary = File('${tempDir.path}/heimdalld')..writeAsStringSync('');
        final listener = await ServerSocket.bind(
          InternetAddress.loopbackIPv4,
          0,
        );
        addTearDown(listener.close);
        var detachedStarts = 0;
        final services = DesktopPlatformServices(
          apiPort: listener.port,
          isMacOS: false,
          detachedDaemonStarter: (_) async => detachedStarts++,
        );

        await expectLater(
          services.spawnDaemon(binary.path),
          throwsA(
            isA<DaemonPortOccupiedException>().having(
              (error) => error.port,
              'port',
              listener.port,
            ),
          ),
        );
        expect(detachedStarts, 0);
      },
    );

    test('spawnDaemon fails closed when port ownership is ambiguous', () async {
      final binary = File('${tempDir.path}/heimdalld')..writeAsStringSync('');
      var detachedStarts = 0;
      final services = DesktopPlatformServices(
        isMacOS: false,
        daemonPortProbe: (_) async => TcpPortState.unknown,
        detachedDaemonStarter: (_) async => detachedStarts++,
      );

      await expectLater(
        services.spawnDaemon(binary.path),
        throwsA(
          isA<DaemonException>().having(
            (error) => error.message,
            'message',
            contains('Could not prove'),
          ),
        ),
      );
      expect(detachedStarts, 0);
    });

    test('spawnDaemon bounds a stalled LaunchAgent command', () async {
      final binary = File('${tempDir.path}/heimdalld')..writeAsStringSync('');
      var detachedStarts = 0;
      final stalled = Completer<ProcessResult>();
      final services = DesktopPlatformServices(
        isMacOS: true,
        daemonPortProbe: (_) async => TcpPortState.closed,
        processRunner: (_, _) => stalled.future,
        processTimeout: const Duration(milliseconds: 20),
        detachedDaemonStarter: (_) async => detachedStarts++,
      );

      await expectLater(
        services.spawnDaemon(binary.path),
        throwsA(
          isA<DaemonException>().having(
            (error) => error.message,
            'message',
            contains('Timed out'),
          ),
        ),
      );
      expect(detachedStarts, 0);
    });

    test(
      'ensureSingleInstance writes a PID file and returns true on fresh start',
      () async {
        final pidFile = File('${tempDir.path}/ui.pid');
        final services = DesktopPlatformServices(pidFilePath: pidFile.path);
        try {
          expect(await services.ensureSingleInstance(), isTrue);
        } finally {
          await services.releaseSingleInstanceForTesting();
        }
        final record = jsonDecode(pidFile.readAsStringSync());
        expect(record['schema_version'], 1);
        expect(record['pid'], pid);
        expect(record['executable'], isNotEmpty);
        expect(record['activation_socket'], isNotEmpty);
      },
    );

    test(
      'ensureSingleInstance overwrites a stale PID file (process gone)',
      () async {
        final pidFile = File('${tempDir.path}/ui.pid')
          // Use an impossible high PID that is extremely unlikely to exist.
          ..writeAsStringSync('999999999');
        final services = DesktopPlatformServices(pidFilePath: pidFile.path);
        try {
          expect(await services.ensureSingleInstance(), isTrue);
        } finally {
          await services.releaseSingleInstanceForTesting();
        }
        final record = jsonDecode(pidFile.readAsStringSync());
        expect(record['pid'], pid);
      },
    );

    test(
      'ensureSingleInstance retains its OS lock for process lifetime',
      () async {
        final pidFile = File('${tempDir.path}/ui.pid');
        final services = DesktopPlatformServices(pidFilePath: pidFile.path);
        expect(await services.ensureSingleInstance(), isTrue);

        const probe = '''
import fcntl
import os
import sys

descriptor = os.open(sys.argv[1], os.O_RDWR)
try:
    fcntl.lockf(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
except BlockingIOError:
    sys.exit(23)
else:
    sys.exit(0)
''';
        try {
          final blocked = await Process.run('/usr/bin/python3', [
            '-c',
            probe,
            pidFile.path,
          ]);
          expect(blocked.exitCode, 23);
        } finally {
          await services.releaseSingleInstanceForTesting();
        }

        final released = await Process.run('/usr/bin/python3', [
          '-c',
          probe,
          pidFile.path,
        ]);
        expect(released.exitCode, 0);
      },
    );

    test(
      'defaultDaemonBinaryPath returns null when HEIMDALLM_DAEMON_PATH is unset and no bundled binary',
      () {
        // This matches the default test environment (no bundled binary next to
        // the Flutter test runner). Covers the "not found" path.
        final services = DesktopPlatformServices();
        expect(services.defaultDaemonBinaryPath(), isNull);
      },
    );

    test('spawnDaemon rejects when the binary does not exist', () async {
      final services = DesktopPlatformServices();
      expect(
        services.spawnDaemon('${tempDir.path}/does-not-exist'),
        throwsA(isA<Exception>()),
      );
    });

    test(
      'spawnDaemon uses the loaded canonical LaunchAgent on macOS',
      () async {
        final binary = File('${tempDir.path}/heimdalld')..writeAsStringSync('');
        final calls = <String>[];
        var detachedStarts = 0;
        final services = DesktopPlatformServices(
          isMacOS: true,
          daemonPortProbe: (_) async => TcpPortState.closed,
          processRunner: (executable, arguments) async {
            calls.add('$executable ${arguments.join(' ')}');
            if (executable == '/usr/bin/id') {
              return ProcessResult(1, 0, '501\n', '');
            }
            return ProcessResult(1, 0, '', '');
          },
          detachedDaemonStarter: (_) async => detachedStarts++,
        );

        await services.spawnDaemon(binary.path);

        expect(calls, [
          '/usr/bin/id -u',
          '/bin/launchctl print gui/501/com.heimdallm.daemon',
          '/bin/launchctl kickstart gui/501/com.heimdallm.daemon',
        ]);
        expect(detachedStarts, 0);
      },
    );

    test(
      'spawnDaemon falls back to detached when LaunchAgent is absent',
      () async {
        final binary = File('${tempDir.path}/heimdalld')..writeAsStringSync('');
        final detached = <String>[];
        final services = DesktopPlatformServices(
          isMacOS: true,
          daemonPortProbe: (_) async => TcpPortState.closed,
          processRunner: (executable, arguments) async {
            if (executable == '/usr/bin/id') {
              return ProcessResult(1, 0, '501\n', '');
            }
            return ProcessResult(1, 113, '', 'Could not find service');
          },
          detachedDaemonStarter: (path) async => detached.add(path),
        );

        await services.spawnDaemon(binary.path);

        expect(detached, [binary.path]);
      },
    );

    test(
      'spawnDaemon fails closed when LaunchAgent inspection is ambiguous',
      () async {
        final binary = File('${tempDir.path}/heimdalld')..writeAsStringSync('');
        var detachedStarts = 0;
        final services = DesktopPlatformServices(
          isMacOS: true,
          daemonPortProbe: (_) async => TcpPortState.closed,
          processRunner: (executable, arguments) async {
            if (executable == '/usr/bin/id') {
              return ProcessResult(1, 0, '501\n', '');
            }
            return ProcessResult(1, 1, '', 'Operation not permitted');
          },
          detachedDaemonStarter: (_) async => detachedStarts++,
        );

        await expectLater(
          services.spawnDaemon(binary.path),
          throwsA(
            isA<DaemonException>().having(
              (error) => error.message,
              'message',
              contains('Could not determine whether'),
            ),
          ),
        );
        expect(detachedStarts, 0);
      },
    );

    test(
      'spawnDaemon never falls back when launchctl kickstart fails',
      () async {
        final binary = File('${tempDir.path}/heimdalld')..writeAsStringSync('');
        var detachedStarts = 0;
        final services = DesktopPlatformServices(
          isMacOS: true,
          daemonPortProbe: (_) async => TcpPortState.closed,
          processRunner: (executable, arguments) async {
            if (executable == '/usr/bin/id') {
              return ProcessResult(1, 0, '501\n', '');
            }
            if (arguments.first == 'print') {
              return ProcessResult(1, 0, '', '');
            }
            return ProcessResult(1, 5, '', 'kickstart failed');
          },
          detachedDaemonStarter: (_) async => detachedStarts++,
        );

        await expectLater(
          services.spawnDaemon(binary.path),
          throwsA(
            isA<DaemonException>().having(
              (error) => error.message,
              'message',
              contains('Could not start the supervised daemon'),
            ),
          ),
        );
        expect(detachedStarts, 0);
      },
    );

    test(
      'macOS updater bridge receives auth and exposes lifecycle methods',
      () async {
        final tokenFile = File('${tempDir.path}/api_token')
          ..writeAsStringSync('update-secret\n');
        final calls = <({String method, dynamic arguments})>[];
        final services = DesktopPlatformServices(
          isMacOS: true,
          enableNativeAppUpdates: true,
          apiPort: 9911,
          tokenPath: tokenFile.path,
          methodInvoker: (method, arguments) async {
            calls.add((method: method, arguments: arguments));
            if (method == 'configure') return true;
            if (method == 'pendingUpdateVersion') return '0.8.4';
            return null;
          },
        );

        expect(services.appUpdateSupport, AppUpdateSupport.unavailable);
        await services.setupAppUpdater();
        expect(services.appUpdateSupport, AppUpdateSupport.native);
        await services.checkForAppUpdates();
        expect(await services.pendingAppUpdateVersion(), '0.8.4');
        await services.completeAppUpdate();
        services.quitApp();
        await Future<void>.delayed(Duration.zero);

        expect(calls.map((call) => call.method), [
          'configure',
          'checkForUpdates',
          'pendingUpdateVersion',
          'completeUpdate',
          'terminateApplication',
        ]);
        final configuration = calls.first.arguments as Map<dynamic, dynamic>;
        expect(configuration['updatesEnabled'], isTrue);
        expect(configuration['apiBaseUrl'], 'http://127.0.0.1:9911');
        expect(configuration['apiToken'], 'update-secret');
        expect(configuration['apiTokenPath'], tokenFile.path);
        expect(configuration['dataDir'], isNotEmpty);
      },
    );

    test('debug macOS builds leave the production updater disabled', () async {
      var calls = 0;
      final services = DesktopPlatformServices(
        isMacOS: true,
        methodInvoker: (method, arguments) async {
          calls++;
          return null;
        },
      );

      expect(services.appUpdateSupport, AppUpdateSupport.unavailable);
      await services.setupAppUpdater();
      expect(await services.pendingAppUpdateVersion(), isNull);
      expect(calls, 1);
    });

    test(
      'native signature gate can reject a requested release updater',
      () async {
        final services = DesktopPlatformServices(
          isMacOS: true,
          enableNativeAppUpdates: true,
          methodInvoker: (method, arguments) async {
            expect(method, 'configure');
            expect(
              (arguments as Map<dynamic, dynamic>)['updatesEnabled'],
              isTrue,
            );
            return false;
          },
        );

        await services.setupAppUpdater();
        expect(services.appUpdateSupport, AppUpdateSupport.unavailable);
        await expectLater(
          services.checkForAppUpdates(),
          throwsA(isA<UnsupportedError>()),
        );
      },
    );

    test(
      'updater setup failure with a recovery journal blocks bootstrap',
      () async {
        final journal = File('${tempDir.path}/app-update-recovery.json')
          ..writeAsStringSync('{}');
        final services = DesktopPlatformServices(
          isMacOS: true,
          enableNativeAppUpdates: true,
          dataDir: tempDir.path,
          methodInvoker: (method, arguments) async {
            throw StateError('native channel unavailable');
          },
        );

        await expectLater(
          services.setupAppUpdater(),
          throwsA(isA<StateError>()),
        );
        await expectLater(
          services.pendingAppUpdateVersion(),
          throwsA(
            isA<StateError>()
                .having(
                  (error) => error.message,
                  'message',
                  contains(journal.path),
                )
                .having(
                  (error) => error.message,
                  'message',
                  contains('native channel unavailable'),
                ),
          ),
        );
      },
    );

    test(
      'updater setup failure without a recovery journal degrades safely',
      () async {
        final services = DesktopPlatformServices(
          isMacOS: true,
          enableNativeAppUpdates: true,
          dataDir: tempDir.path,
          methodInvoker: (method, arguments) async {
            throw StateError('native channel unavailable');
          },
        );

        await expectLater(
          services.setupAppUpdater(),
          throwsA(isA<StateError>()),
        );
        expect(await services.pendingAppUpdateVersion(), isNull);
      },
    );

    test('custom data directory also owns the updater token path', () async {
      final dataDir = Directory('${tempDir.path}/custom-data')..createSync();
      final tokenFile = File('${dataDir.path}/api_token')
        ..writeAsStringSync('custom-token\n');
      dynamic configuration;
      final services = DesktopPlatformServices(
        isMacOS: true,
        enableNativeAppUpdates: true,
        dataDir: dataDir.path,
        methodInvoker: (method, arguments) async {
          if (method == 'configure') configuration = arguments;
          return null;
        },
      );

      await services.setupAppUpdater();
      expect(configuration['apiToken'], 'custom-token');
      expect(configuration['apiTokenPath'], tokenFile.path);
      expect(configuration['dataDir'], dataDir.path);
    });

    test('macOS quit fails closed when the native bridge rejects it', () async {
      var terminateCalls = 0;
      final services = DesktopPlatformServices(
        isMacOS: true,
        methodInvoker: (method, arguments) async {
          if (method == 'terminateApplication') {
            terminateCalls++;
            throw StateError('native channel unavailable');
          }
          return null;
        },
      );

      services.quitApp();
      await Future<void>.delayed(Duration.zero);

      // Reaching this assertion proves quitApp did not fall back to exit(0).
      expect(terminateCalls, 1);
    });

    test('Linux reports native updates unavailable', () async {
      var calls = 0;
      final services = DesktopPlatformServices(
        isMacOS: false,
        methodInvoker: (method, arguments) async {
          calls++;
          return null;
        },
      );

      expect(services.appUpdateSupport, AppUpdateSupport.unavailable);
      await services.setupAppUpdater();
      expect(await services.pendingAppUpdateVersion(), isNull);
      await services.completeAppUpdate();
      await expectLater(
        services.checkForAppUpdates(),
        throwsA(isA<UnsupportedError>()),
      );
      expect(calls, 0);
    });
  });
}

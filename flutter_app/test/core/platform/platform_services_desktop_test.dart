import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/daemon/daemon_lifecycle.dart';
import 'package:heimdallm/core/platform/linux_app_updater.dart';
import 'package:heimdallm/core/platform/macos_app_updater.dart';
import 'package:heimdallm/core/platform/platform_services.dart';
import 'package:heimdallm/core/platform/platform_services_desktop.dart';
import 'package:package_info_plus/package_info_plus.dart';

class _FakeLinuxAppUpdater extends LinuxAppUpdater {
  _FakeLinuxAppUpdater({
    this.initializeError,
    this.installError,
    this.pendingVersion = '2.0.0',
    this.restartPath = '/opt/heimdallm/heimdallm',
  }) : super(
         apiBaseURL: Uri.parse('http://127.0.0.1:7842'),
         apiTokenPath: '/tmp/heimdallm-test-token',
         dataDirectory: '/tmp/heimdallm-test-data',
         executablePath: '/opt/heimdallm/heimdallm',
         environment: const {},
         processRunner: Process.run,
         daemonStarter: (_) async {},
         onStatus: (_) {},
       );

  final Object? initializeError;
  final Object? installError;
  final String? pendingVersion;
  final String restartPath;
  int checkCalls = 0;
  int installCalls = 0;
  int completeCalls = 0;
  int finalizeCalls = 0;

  @override
  Future<bool> initialize() async {
    if (initializeError != null) throw initializeError!;
    return true;
  }

  @override
  Future<void> checkForUpdates({bool silent = false}) async {
    checkCalls++;
  }

  @override
  Future<LinuxInstallResult> installAvailableUpdate() async {
    installCalls++;
    if (installError != null) throw installError!;
    return LinuxInstallResult(version: '2.0.0', restartPath: restartPath);
  }

  @override
  Future<String?> pendingUpdateVersion() async => pendingVersion;

  @override
  Future<void> completePendingUpdate() async {
    completeCalls++;
  }

  @override
  Future<void> finalizePendingUpdate() async {
    finalizeCalls++;
  }
}

class _FakeMacOSAppUpdater extends MacOSAppUpdater {
  _FakeMacOSAppUpdater({
    this.initializeResult = true,
    this.initializeError,
    this.installError,
  }) : super(
         executablePath: '/Applications/Heimdallm.app/Contents/MacOS/Heimdallm',
         dataDirectory: '/tmp/heimdallm-test-data',
         versionLoader: () async => const AppVersionInfo(version: '0.8.9'),
         processRunner: Process.run,
         onStatus: (_) {},
         startPolling: false,
       );

  final bool initializeResult;
  final Object? initializeError;
  final Object? installError;
  int initializeCalls = 0;
  int checkCalls = 0;
  int installCalls = 0;

  @override
  Future<bool> initialize() async {
    initializeCalls++;
    if (initializeError != null) throw initializeError!;
    return initializeResult;
  }

  @override
  Future<void> checkForUpdates({bool silent = false}) async {
    checkCalls++;
  }

  @override
  Future<void> installAvailableUpdate() async {
    installCalls++;
    if (installError != null) throw installError!;
  }
}

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

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

    test('default process runner preserves exit status and output', () async {
      final result = await runDefaultPlatformProcess('/bin/sh', [
        '-c',
        'printf stdout; printf stderr >&2; exit 7',
      ]);

      expect(result.exitCode, 7);
      expect(result.stdout, 'stdout');
      expect(result.stderr, 'stderr');
      expect(result.pid, greaterThan(0));
    });

    test('default process runner terminates a timed-out child', () async {
      await expectLater(
        runDefaultPlatformProcess(
          '/bin/sh',
          ['-c', "trap 'exit 0' TERM; while :; do :; done"],
          timeout: const Duration(milliseconds: 100),
          killGrace: const Duration(milliseconds: 200),
        ),
        throwsA(
          isA<DaemonException>().having(
            (error) => error.message,
            'message',
            contains('Timed out running /bin/sh'),
          ),
        ),
      );
    });

    test('default process runner escalates from TERM to KILL', () async {
      await expectLater(
        runDefaultPlatformProcess(
          '/bin/sh',
          ['-c', "trap '' TERM; while :; do :; done"],
          timeout: const Duration(milliseconds: 100),
          killGrace: const Duration(milliseconds: 100),
        ),
        throwsA(isA<DaemonException>()),
      );
    });

    test('environment path drives singleton state', () async {
      final pidFile = File('${tempDir.path}/env/ui.pid');
      final environment = {
        'HEIMDALLM_UI_PID_FILE': pidFile.path,
        'HOME': tempDir.path,
      };
      final services = DesktopPlatformServices(
        isMacOS: true,
        environmentReader: (name) => environment[name],
      );
      try {
        expect(await services.ensureSingleInstance(), isTrue);
      } finally {
        await services.releaseSingleInstanceForTesting();
      }

      expect(pidFile.existsSync(), isTrue);
    });

    test('XCTest gets an isolated singleton path', () async {
      final configurationPath = '${tempDir.path}/suite.xctestconfiguration';
      final pidFile = File('$configurationPath.heimdallm-ui.pid');
      final services = DesktopPlatformServices(
        environmentReader: (name) =>
            name == 'XCTestConfigurationFilePath' ? configurationPath : null,
      );
      try {
        expect(await services.ensureSingleInstance(), isTrue);
      } finally {
        await services.releaseSingleInstanceForTesting();
      }

      expect(pidFile.existsSync(), isTrue);
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

    test('spawnDaemon treats an explicit connection refusal as free', () async {
      final binary = File('${tempDir.path}/heimdalld')..writeAsStringSync('');
      final listener = await ServerSocket.bind(InternetAddress.loopbackIPv4, 0);
      final port = listener.port;
      await listener.close();
      final starts = <String>[];
      final services = DesktopPlatformServices(
        apiPort: port,
        isMacOS: false,
        isLinux: false,
        detachedDaemonStarter: (path) async => starts.add(path),
      );

      await services.spawnDaemon(binary.path);

      expect(starts, [binary.path]);
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
      'activation queued before listener is delivered exactly once',
      () async {
        final pidFile = File('${tempDir.path}/queued/ui.pid');
        final services = DesktopPlatformServices(pidFilePath: pidFile.path);
        var activations = 0;
        try {
          expect(await services.ensureSingleInstance(), isTrue);
          final record = jsonDecode(pidFile.readAsStringSync());
          final socket = await Socket.connect(
            InternetAddress(
              record['activation_socket'],
              type: InternetAddressType.unix,
            ),
            0,
          );
          await socket.close();
          await Future<void>.delayed(const Duration(milliseconds: 10));

          services.listenForActivationSignal(() => activations++);
          await Future<void>.delayed(Duration.zero);
          expect(activations, 1);
        } finally {
          await services.releaseSingleInstanceForTesting();
        }
      },
    );

    test('activation reaches an already registered listener', () async {
      final pidFile = File('${tempDir.path}/direct/ui.pid');
      final services = DesktopPlatformServices(pidFilePath: pidFile.path);
      final activation = Completer<void>();
      try {
        expect(await services.ensureSingleInstance(), isTrue);
        services.listenForActivationSignal(() => activation.complete());
        final record = jsonDecode(pidFile.readAsStringSync());
        final socket = await Socket.connect(
          InternetAddress(
            record['activation_socket'],
            type: InternetAddressType.unix,
          ),
          0,
        );
        await socket.close();

        await activation.future.timeout(const Duration(seconds: 1));
      } finally {
        await services.releaseSingleInstanceForTesting();
      }
    });

    test('authenticated duplicate activates the lock owner', () async {
      final activationDirectory = Directory('${tempDir.path}/owner')
        ..createSync();
      final activationPath = '${activationDirectory.path}/activate.sock';
      final server = await ServerSocket.bind(
        InternetAddress(activationPath, type: InternetAddressType.unix),
        0,
      );
      addTearDown(server.close);
      final activated = Completer<void>();
      server.listen((socket) {
        socket.destroy();
        if (!activated.isCompleted) activated.complete();
      });
      final executable = File(
        Platform.resolvedExecutable,
      ).resolveSymbolicLinksSync();
      final pidFile = File('${tempDir.path}/duplicate/ui.pid')
        ..createSync(recursive: true)
        ..writeAsStringSync(
          jsonEncode({
            'schema_version': 1,
            'pid': pid + 1,
            'executable': executable,
            'activation_socket': activationPath,
          }),
        );
      final services = DesktopPlatformServices(
        pidFilePath: pidFile.path,
        instanceLockAcquirer: (_) async {
          throw const FileSystemException('simulated competing lock');
        },
      );

      expect(await services.ensureSingleInstance(), isFalse);
      await activated.future.timeout(const Duration(seconds: 1));
    });

    test(
      'corrupt duplicate metadata fails closed without signalling',
      () async {
        final pidFile = File('${tempDir.path}/corrupt-duplicate/ui.pid')
          ..createSync(recursive: true)
          ..writeAsStringSync('{');
        final services = DesktopPlatformServices(
          pidFilePath: pidFile.path,
          instanceLockAcquirer: (_) async {
            throw const FileSystemException('simulated competing lock');
          },
        );

        expect(await services.ensureSingleInstance(), isFalse);
      },
    );

    test('valid stale activation directory is safely reused', () async {
      final reusable = await Directory.systemTemp.createTemp('heimdallm-ui-');
      final staleSocket = File('${reusable.path}/activate.sock')
        ..writeAsStringSync('stale');
      final pidFile = File('${tempDir.path}/reuse/ui.pid')
        ..createSync(recursive: true)
        ..writeAsStringSync(
          jsonEncode({
            'schema_version': 1,
            'activation_socket': staleSocket.path,
          }),
        );
      final services = DesktopPlatformServices(pidFilePath: pidFile.path);
      try {
        expect(await services.ensureSingleInstance(), isTrue);
        final record = jsonDecode(pidFile.readAsStringSync());
        expect(record['activation_socket'], staleSocket.path);
      } finally {
        await services.releaseSingleInstanceForTesting();
      }
      expect(reusable.existsSync(), isFalse);
    });

    test('missing stale activation directory is replaced safely', () async {
      final missing = await Directory.systemTemp.createTemp('heimdallm-ui-');
      final staleSocketPath = '${missing.path}/activate.sock';
      await missing.delete();
      final pidFile = File('${tempDir.path}/missing-reuse/ui.pid')
        ..createSync(recursive: true)
        ..writeAsStringSync(
          jsonEncode({
            'schema_version': 1,
            'activation_socket': staleSocketPath,
          }),
        );
      final services = DesktopPlatformServices(pidFilePath: pidFile.path);

      String? replacementSocketPath;
      try {
        expect(await services.ensureSingleInstance(), isTrue);
        final record = jsonDecode(pidFile.readAsStringSync());
        replacementSocketPath = record['activation_socket'];
        expect(replacementSocketPath, isNot(staleSocketPath));
        expect(
          Directory(File(replacementSocketPath!).parent.path).existsSync(),
          isTrue,
        );
      } finally {
        await services.releaseSingleInstanceForTesting();
      }

      expect(missing.existsSync(), isFalse);
      expect(
        Directory(File(replacementSocketPath).parent.path).existsSync(),
        isFalse,
      );
    });

    test('failed activation bind releases lock and temporary state', () async {
      final pidFile = File('${tempDir.path}/bind-failure/ui.pid');
      String? socketPath;
      final failing = DesktopPlatformServices(
        pidFilePath: pidFile.path,
        activationSocketBinder: (path) async {
          socketPath = path;
          throw StateError('bind rejected');
        },
      );

      await expectLater(
        failing.ensureSingleInstance(),
        throwsA(isA<StateError>()),
      );
      expect(Directory(File(socketPath!).parent.path).existsSync(), isFalse);

      final replacement = DesktopPlatformServices(pidFilePath: pidFile.path);
      try {
        expect(await replacement.ensureSingleInstance(), isTrue);
      } finally {
        await replacement.releaseSingleInstanceForTesting();
      }
    });

    test('malformed singleton metadata is replaced safely', () async {
      final pidFile = File('${tempDir.path}/malformed/ui.pid')
        ..createSync(recursive: true)
        ..writeAsStringSync('{');
      final services = DesktopPlatformServices(pidFilePath: pidFile.path);
      try {
        expect(await services.ensureSingleInstance(), isTrue);
      } finally {
        await services.releaseSingleInstanceForTesting();
      }
      expect(jsonDecode(pidFile.readAsStringSync())['schema_version'], 1);
    });

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

    test('spawnDaemon rejects an invalid launchd user identity', () async {
      final binary = File('${tempDir.path}/heimdalld')..writeAsStringSync('');
      final services = DesktopPlatformServices(
        isMacOS: true,
        daemonPortProbe: (_) async => TcpPortState.closed,
        processRunner: (_, _) async => ProcessResult(1, 0, 'not-a-uid\n', ''),
        detachedDaemonStarter: (_) async {},
      );

      await expectLater(
        services.spawnDaemon(binary.path),
        throwsA(
          isA<DaemonException>().having(
            (error) => error.message,
            'message',
            contains('not-a-uid'),
          ),
        ),
      );
    });

    test('macOS updater exposes the simple update lifecycle', () async {
      final updater = _FakeMacOSAppUpdater();
      final exitCodes = <int>[];
      final services = DesktopPlatformServices(
        isMacOS: true,
        enableNativeAppUpdates: true,
        macOSAppUpdater: updater,
        processExit: exitCodes.add,
      );

      expect(services.appUpdateSupport, AppUpdateSupport.unavailable);
      await services.setupAppUpdater();
      expect(services.appUpdateSupport, AppUpdateSupport.native);

      await services.checkForAppUpdates();
      await services.installAppUpdate();
      expect(await services.pendingAppUpdateVersion(), isNull);
      await services.completeAppUpdate();
      await services.finalizeAppUpdate();

      expect(updater.initializeCalls, 1);
      expect(updater.checkCalls, 1);
      expect(updater.installCalls, 1);
      expect(exitCodes, [0]);
    });

    test('loads the installed application version', () async {
      PackageInfo.setMockInitialValues(
        appName: 'Heimdallm',
        packageName: 'com.theburrowhub.heimdallm',
        version: '0.8.4',
        buildNumber: '548',
        buildSignature: '',
      );
      final services = DesktopPlatformServices(isMacOS: true);

      final version = await services.loadAppVersion();

      expect(version.displayVersion, '0.8.4 (build 548)');
    });

    test('macOS updater can reject an unsupported installation', () async {
      final updater = _FakeMacOSAppUpdater(initializeResult: false);
      final services = DesktopPlatformServices(
        isMacOS: true,
        enableNativeAppUpdates: true,
        macOSAppUpdater: updater,
      );

      await services.setupAppUpdater();

      expect(services.appUpdateSupport, AppUpdateSupport.unavailable);
      expect(
        services.appUpdateUnavailableReason,
        contains('unavailable for this installation'),
      );
      await expectLater(
        services.checkForAppUpdates(),
        throwsA(isA<UnsupportedError>()),
      );
    });

    test('macOS updater setup preserves initialization failures', () async {
      final failure = StateError('updater initialization failed');
      final services = DesktopPlatformServices(
        isMacOS: true,
        enableNativeAppUpdates: true,
        macOSAppUpdater: _FakeMacOSAppUpdater(initializeError: failure),
      );

      await expectLater(services.setupAppUpdater(), throwsA(same(failure)));

      expect(services.appUpdateSupport, AppUpdateSupport.unavailable);
      expect(
        services.appUpdateUnavailableReason,
        contains('updater initialization failed'),
      );
      expect(await services.pendingAppUpdateVersion(), isNull);
    });

    test('macOS install failure does not exit the app', () async {
      final failure = StateError('install failed');
      final updater = _FakeMacOSAppUpdater(installError: failure);
      final exitCodes = <int>[];
      final services = DesktopPlatformServices(
        isMacOS: true,
        enableNativeAppUpdates: true,
        macOSAppUpdater: updater,
        processExit: exitCodes.add,
      );
      await services.setupAppUpdater();

      await expectLater(services.installAppUpdate(), throwsA(same(failure)));

      expect(updater.installCalls, 1);
      expect(exitCodes, isEmpty);
    });

    test('disabled macOS updater is never initialized', () async {
      final updater = _FakeMacOSAppUpdater();
      final services = DesktopPlatformServices(
        isMacOS: true,
        enableNativeAppUpdates: false,
        macOSAppUpdater: updater,
      );

      await services.setupAppUpdater();

      expect(updater.initializeCalls, 0);
      expect(services.appUpdateSupport, AppUpdateSupport.unavailable);
    });

    test('desktop quit paths delegate to the injected process exit', () {
      final exitCodes = <int>[];
      final services = DesktopPlatformServices(
        isMacOS: true,
        processExit: exitCodes.add,
      );

      services.quitApp();
      services.quitDuplicateInstance();

      expect(exitCodes, [0, 0]);
    });

    test(
      'disabled Linux updater leaves every update action unavailable',
      () async {
        final services = DesktopPlatformServices(
          isMacOS: false,
          isLinux: true,
          enableNativeAppUpdates: false,
        );

        expect(services.appUpdateSupport, AppUpdateSupport.unavailable);
        await services.setupAppUpdater();
        expect(await services.pendingAppUpdateVersion(), isNull);
        await services.completeAppUpdate();
        await services.finalizeAppUpdate();
        await expectLater(
          services.checkForAppUpdates(),
          throwsA(isA<UnsupportedError>()),
        );
        await expectLater(
          services.installAppUpdate(),
          throwsA(isA<UnsupportedError>()),
        );
        expect(
          services.appUpdateUnavailableReason,
          contains('official AppImage or packaged release'),
        );
      },
    );

    test('Linux updater exposes its complete successful lifecycle', () async {
      final updater = _FakeLinuxAppUpdater(
        pendingVersion: '2.0.0',
        restartPath: '${tempDir.path}/updated-heimdallm',
      );
      final restarts = <({int pid, String path})>[];
      final exits = <int>[];
      final services = DesktopPlatformServices(
        isMacOS: false,
        isLinux: true,
        enableNativeAppUpdates: true,
        linuxAppUpdater: updater,
        detachedAppRestarter: (currentPID, executablePath) async {
          restarts.add((pid: currentPID, path: executablePath));
        },
        processExit: exits.add,
      );

      await services.setupAppUpdater();
      expect(services.appUpdateSupport, AppUpdateSupport.native);
      await services.checkForAppUpdates();
      expect(await services.pendingAppUpdateVersion(), '2.0.0');
      await services.completeAppUpdate();
      await services.finalizeAppUpdate();
      await services.installAppUpdate();

      expect(updater.checkCalls, 1);
      expect(updater.completeCalls, 1);
      expect(updater.finalizeCalls, 1);
      expect(updater.installCalls, 1);
      expect(restarts, [(pid: pid, path: '${tempDir.path}/updated-heimdallm')]);
      expect(exits, [0]);
    });

    test('Linux updater setup preserves initialization failures', () async {
      final failure = StateError('invalid signed updater configuration');
      final services = DesktopPlatformServices(
        isMacOS: false,
        isLinux: true,
        enableNativeAppUpdates: true,
        linuxAppUpdater: _FakeLinuxAppUpdater(initializeError: failure),
      );

      await expectLater(services.setupAppUpdater(), throwsA(same(failure)));
      expect(services.appUpdateSupport, AppUpdateSupport.unavailable);
    });

    test('Linux install failure never restarts or exits the app', () async {
      final failure = StateError('verified install rejected');
      final updater = _FakeLinuxAppUpdater(installError: failure);
      var restartCalls = 0;
      final exits = <int>[];
      final services = DesktopPlatformServices(
        isMacOS: false,
        isLinux: true,
        enableNativeAppUpdates: true,
        linuxAppUpdater: updater,
        detachedAppRestarter: (_, _) async => restartCalls++,
        processExit: exits.add,
      );
      await services.setupAppUpdater();

      await expectLater(services.installAppUpdate(), throwsA(same(failure)));

      expect(updater.installCalls, 1);
      expect(restartCalls, 0);
      expect(exits, isEmpty);
    });

    test('Linux setup wires environment into the concrete updater', () async {
      final appImage = File('${tempDir.path}/Heimdallm.AppImage')
        ..writeAsStringSync('test image');
      final services = DesktopPlatformServices(
        isMacOS: false,
        isLinux: true,
        enableNativeAppUpdates: true,
        dataDir: tempDir.path,
        environmentReader: (name) => name == 'APPIMAGE' ? appImage.path : null,
      );

      await services.setupAppUpdater();

      // The test runner has no bundled heimdalld, so the concrete updater must
      // reject native support after successfully detecting the AppImage.
      expect(services.appUpdateSupport, AppUpdateSupport.unavailable);
    });
  });
}

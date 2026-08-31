import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/platform/macos_app_updater.dart';
import 'package:heimdallm/core/platform/platform_services.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

void main() {
  group('MacOSAppUpdater', () {
    late Directory temporaryDirectory;
    late Directory appBundle;
    late File executable;
    late List<AppUpdateStatus> statuses;

    setUp(() async {
      temporaryDirectory = await Directory.systemTemp.createTemp(
        'heimdallm_macos_updater_test_',
      );
      appBundle = Directory('${temporaryDirectory.path}/Heimdallm.app');
      executable = File('${appBundle.path}/Contents/MacOS/Heimdallm');
      await executable.parent.create(recursive: true);
      await executable.writeAsString('old app');
      statuses = [];
    });

    tearDown(() async {
      if (await temporaryDirectory.exists()) {
        await temporaryDirectory.delete(recursive: true);
      }
    });

    test('polls GitHub latest and exposes the exact release DMG', () async {
      final requests = <Uri>[];
      final updater = _updater(
        executable: executable,
        temporaryDirectory: temporaryDirectory,
        statuses: statuses,
        client: MockClient((request) async {
          requests.add(request.url);
          return _releaseResponse('0.8.10');
        }),
      );
      addTearDown(updater.dispose);

      expect(await updater.initialize(), isTrue);
      await updater.checkForUpdates();

      expect(requests, [MacOSAppUpdater.latestReleaseURL]);
      expect(updater.availableRelease?.version, '0.8.10');
      expect(
        updater.availableRelease?.downloadURL,
        Uri.parse(
          'https://github.com/theburrowhub/heimdallm/releases/download/'
          'v0.8.10/Heimdallm-v0.8.10.dmg',
        ),
      );
      expect(statuses.map((status) => status.phase), [
        AppUpdatePhase.checking,
        AppUpdatePhase.available,
      ]);
    });

    test('reports an up-to-date installation', () async {
      final updater = _updater(
        executable: executable,
        temporaryDirectory: temporaryDirectory,
        statuses: statuses,
        client: MockClient((_) async => _releaseResponse('0.8.9')),
      );
      addTearDown(updater.dispose);

      expect(await updater.initialize(), isTrue);
      await updater.checkForUpdates();

      expect(updater.availableRelease, isNull);
      expect(statuses.last.phase, AppUpdatePhase.idle);
      expect(statuses.last.message, contains('0.8.9 is up to date'));
    });

    test(
      'offers a stable release to a matching prerelease installation',
      () async {
        final updater = MacOSAppUpdater(
          executablePath: executable.path,
          dataDirectory: '${temporaryDirectory.path}/data',
          versionLoader: () async =>
              const AppVersionInfo(version: '1.0.0-beta.1'),
          processRunner: Process.run,
          onStatus: statuses.add,
          client: MockClient((_) async => _releaseResponse('1.0.0')),
          startPolling: false,
        );
        addTearDown(updater.dispose);

        expect(await updater.initialize(), isTrue);
        await updater.checkForUpdates();

        expect(updater.availableRelease?.version, '1.0.0');
        expect(statuses.last.phase, AppUpdatePhase.available);
      },
    );

    test('starts polling as soon as it initializes', () async {
      final secondRequest = Completer<void>();
      var requestCount = 0;
      final updater = MacOSAppUpdater(
        executablePath: executable.path,
        dataDirectory: '${temporaryDirectory.path}/data',
        versionLoader: () async => const AppVersionInfo(version: '0.8.9'),
        processRunner: Process.run,
        onStatus: statuses.add,
        client: MockClient((_) async {
          requestCount++;
          if (requestCount == 2) secondRequest.complete();
          return _releaseResponse('0.8.10');
        }),
        pollInterval: const Duration(milliseconds: 10),
      );
      addTearDown(updater.dispose);

      expect(await updater.initialize(), isTrue);
      await secondRequest.future.timeout(const Duration(seconds: 1));

      expect(requestCount, greaterThanOrEqualTo(2));
    });

    test('keeps automatic polling failures silent', () async {
      final updater = _updater(
        executable: executable,
        temporaryDirectory: temporaryDirectory,
        statuses: statuses,
        client: MockClient(
          (_) async => http.Response('unavailable', HttpStatus.badGateway),
        ),
      );
      addTearDown(updater.dispose);
      expect(await updater.initialize(), isTrue);

      await updater.checkForUpdates(silent: true);

      expect(statuses, isEmpty);
    });

    test('refuses installation before an update is available', () async {
      final updater = _updater(
        executable: executable,
        temporaryDirectory: temporaryDirectory,
        statuses: statuses,
        client: MockClient((_) async => _releaseResponse('0.8.10')),
      );
      addTearDown(updater.dispose);
      expect(await updater.initialize(), isTrue);

      await expectLater(
        updater.installAvailableUpdate(),
        throwsA(isA<StateError>()),
      );
    });

    test('downloads, mounts, stages, and launches one replacement', () async {
      final requests = <Uri>[];
      final processCalls = <({String executable, List<String> arguments})>[];
      MacOSInstallPlan? launchedPlan;
      final daemonData = Directory('${temporaryDirectory.path}/data');
      await daemonData.create();
      await File('${daemonData.path}/daemon.lock').writeAsString('4242\n');
      final workspace = Directory('${temporaryDirectory.path}/download');

      final updater = MacOSAppUpdater(
        executablePath: executable.path,
        dataDirectory: daemonData.path,
        versionLoader: () async => const AppVersionInfo(version: '0.8.9'),
        processRunner: (command, arguments) async {
          processCalls.add((executable: command, arguments: [...arguments]));
          if (command == '/usr/bin/hdiutil' && arguments.first == 'attach') {
            final mount = Directory(
              arguments[arguments.indexOf('-mountpoint') + 1],
            );
            final mountedExecutable = File(
              '${mount.path}/Heimdallm.app/Contents/MacOS/Heimdallm',
            );
            await mountedExecutable.parent.create(recursive: true);
            await mountedExecutable.writeAsString('new app');
          }
          if (command == '/usr/bin/ditto') {
            final stagedExecutable = File(
              '${arguments.last}/Contents/MacOS/Heimdallm',
            );
            await stagedExecutable.parent.create(recursive: true);
            await stagedExecutable.writeAsString('new app');
          }
          return ProcessResult(1, 0, '', '');
        },
        onStatus: statuses.add,
        client: MockClient((request) async {
          requests.add(request.url);
          if (request.url == MacOSAppUpdater.latestReleaseURL) {
            return _releaseResponse('0.8.10');
          }
          return http.Response.bytes([1, 2, 3], HttpStatus.ok);
        }),
        installerLauncher: (plan) async => launchedPlan = plan,
        tempDirectoryFactory: () async {
          await workspace.create();
          return workspace;
        },
        startPolling: false,
      );
      addTearDown(updater.dispose);

      expect(await updater.initialize(), isTrue);
      await updater.checkForUpdates();
      await updater.installAvailableUpdate();

      expect(requests, hasLength(2));
      expect(requests.first, MacOSAppUpdater.latestReleaseURL);
      expect(requests.last.path, endsWith('/Heimdallm-v0.8.10.dmg'));
      expect(
        processCalls.map(
          (call) => '${call.executable} ${call.arguments.first}',
        ),
        [
          '/usr/bin/hdiutil attach',
          '/usr/bin/ditto ${workspace.path}/mounted/Heimdallm.app',
          '/usr/bin/hdiutil detach',
        ],
      );
      expect(launchedPlan, isNotNull);
      expect(launchedPlan!.daemonPID, 4242);
      expect(launchedPlan!.targetBundlePath, appBundle.path);
      expect(
        launchedPlan!.stagedBundlePath,
        startsWith('${appBundle.path}.update-'),
      );
      expect(statuses.map((status) => status.phase), [
        AppUpdatePhase.checking,
        AppUpdatePhase.available,
        AppUpdatePhase.installing,
        AppUpdatePhase.restarting,
      ]);
      expect(await workspace.exists(), isFalse);
    });

    test('cleans up when the mounted DMG has no app bundle', () async {
      final workspace = Directory('${temporaryDirectory.path}/download');
      final processCalls = <({String executable, List<String> arguments})>[];
      final updater = MacOSAppUpdater(
        executablePath: executable.path,
        dataDirectory: '${temporaryDirectory.path}/data',
        versionLoader: () async => const AppVersionInfo(version: '0.8.9'),
        processRunner: (command, arguments) async {
          processCalls.add((executable: command, arguments: [...arguments]));
          return ProcessResult(1, 0, '', '');
        },
        onStatus: statuses.add,
        client: MockClient((request) async {
          if (request.url == MacOSAppUpdater.latestReleaseURL) {
            return _releaseResponse('0.8.10');
          }
          return http.Response.bytes([1, 2, 3], HttpStatus.ok);
        }),
        tempDirectoryFactory: () async {
          await workspace.create();
          return workspace;
        },
        startPolling: false,
      );
      addTearDown(updater.dispose);
      expect(await updater.initialize(), isTrue);
      await updater.checkForUpdates();

      await expectLater(
        updater.installAvailableUpdate(),
        throwsA(isA<StateError>()),
      );

      expect(
        processCalls.map(
          (call) => '${call.executable} ${call.arguments.first}',
        ),
        ['/usr/bin/hdiutil attach', '/usr/bin/hdiutil detach'],
      );
      expect(await workspace.exists(), isFalse);
      expect(statuses.last.phase, AppUpdatePhase.error);
      expect(statuses.last.message, contains('does not contain Heimdallm.app'));
    });

    test('reports a failed DMG download', () async {
      final workspace = Directory('${temporaryDirectory.path}/download');
      final updater = MacOSAppUpdater(
        executablePath: executable.path,
        dataDirectory: '${temporaryDirectory.path}/data',
        versionLoader: () async => const AppVersionInfo(version: '0.8.9'),
        processRunner: Process.run,
        onStatus: statuses.add,
        client: MockClient((request) async {
          if (request.url == MacOSAppUpdater.latestReleaseURL) {
            return _releaseResponse('0.8.10');
          }
          return http.Response('unavailable', HttpStatus.serviceUnavailable);
        }),
        tempDirectoryFactory: () async {
          await workspace.create();
          return workspace;
        },
        startPolling: false,
      );
      addTearDown(updater.dispose);
      expect(await updater.initialize(), isTrue);
      await updater.checkForUpdates();

      await expectLater(
        updater.installAvailableUpdate(),
        throwsA(isA<HttpException>()),
      );

      expect(await workspace.exists(), isFalse);
      expect(statuses.last.phase, AppUpdatePhase.error);
      expect(statuses.last.message, contains('HTTP 503'));
    });

    test('reports a failed DMG mount command', () async {
      final workspace = Directory('${temporaryDirectory.path}/download');
      final updater = MacOSAppUpdater(
        executablePath: executable.path,
        dataDirectory: '${temporaryDirectory.path}/data',
        versionLoader: () async => const AppVersionInfo(version: '0.8.9'),
        processRunner: (_, _) async => ProcessResult(1, 7, '', ''),
        onStatus: statuses.add,
        client: MockClient((request) async {
          if (request.url == MacOSAppUpdater.latestReleaseURL) {
            return _releaseResponse('0.8.10');
          }
          return http.Response.bytes([1, 2, 3], HttpStatus.ok);
        }),
        tempDirectoryFactory: () async {
          await workspace.create();
          return workspace;
        },
        startPolling: false,
      );
      addTearDown(updater.dispose);
      expect(await updater.initialize(), isTrue);
      await updater.checkForUpdates();

      await expectLater(
        updater.installAvailableUpdate(),
        throwsA(
          isA<ProcessException>().having(
            (error) => error.message,
            'message',
            'exit status 7',
          ),
        ),
      );

      expect(await workspace.exists(), isFalse);
      expect(statuses.last.phase, AppUpdatePhase.error);
    });

    test('fails when the release has no matching DMG', () async {
      final updater = _updater(
        executable: executable,
        temporaryDirectory: temporaryDirectory,
        statuses: statuses,
        client: MockClient(
          (_) async => http.Response(
            jsonEncode({
              'tag_name': 'v0.8.10',
              'assets': [
                {
                  'name': 'source.zip',
                  'browser_download_url': 'https://example.com/source.zip',
                },
              ],
            }),
            HttpStatus.ok,
          ),
        ),
      );
      addTearDown(updater.dispose);
      expect(await updater.initialize(), isTrue);

      await expectLater(
        updater.checkForUpdates(),
        throwsA(isA<FormatException>()),
      );

      expect(statuses.last.phase, AppUpdatePhase.error);
      expect(statuses.last.message, contains('has no Heimdallm-v0.8.10.dmg'));
    });

    test('rejects executables outside an app bundle', () async {
      final updater = _updater(
        executable: File('${temporaryDirectory.path}/Heimdallm'),
        temporaryDirectory: temporaryDirectory,
        statuses: statuses,
        client: MockClient((_) async => _releaseResponse('0.8.10')),
      );
      addTearDown(updater.dispose);

      expect(await updater.initialize(), isFalse);
    });
  });
}

MacOSAppUpdater _updater({
  required File executable,
  required Directory temporaryDirectory,
  required List<AppUpdateStatus> statuses,
  required http.Client client,
}) {
  return MacOSAppUpdater(
    executablePath: executable.path,
    dataDirectory: '${temporaryDirectory.path}/data',
    versionLoader: () async => const AppVersionInfo(version: '0.8.9'),
    processRunner: Process.run,
    onStatus: statuses.add,
    client: client,
    startPolling: false,
  );
}

http.Response _releaseResponse(String version) {
  return http.Response(
    jsonEncode({
      'tag_name': 'v$version',
      'assets': [
        {
          'name': 'Heimdallm-v$version.dmg',
          'browser_download_url':
              'https://github.com/theburrowhub/heimdallm/releases/download/'
              'v$version/Heimdallm-v$version.dmg',
        },
      ],
    }),
    HttpStatus.ok,
  );
}

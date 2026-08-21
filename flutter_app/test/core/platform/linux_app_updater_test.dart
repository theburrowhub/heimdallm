import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:crypto/crypto.dart';
import 'package:cryptography/cryptography.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/platform/linux_app_updater.dart';
import 'package:heimdallm/core/platform/platform_services.dart';

const _leaseID = '11111111-1111-4111-8111-111111111111';

void main() {
  group('release values and cryptographic validation', () {
    test('serializes releases and exposes install results', () {
      final release = _release();
      expect(release.toJson(), {
        'version': '1.1.0',
        'assetName': 'Heimdallm-1.1.0-x86_64.AppImage',
        'assetURL': release.assetURL.toString(),
        'checksumsURL': release.checksumsURL.toString(),
        'checksumsSignatureURL': release.checksumsSignatureURL.toString(),
      });
      const result = LinuxInstallResult(
        version: '1.1.0',
        restartPath: '/tmp/Heimdallm.AppImage',
      );
      expect(result.version, '1.1.0');
      expect(result.restartPath, '/tmp/Heimdallm.AppImage');
    });

    test('compares every stable semantic version component', () {
      expect(LinuxAppUpdater.compareVersions('1.2.3', '1.2.2'), isPositive);
      expect(LinuxAppUpdater.compareVersions('1.3.0', '1.2.9'), isPositive);
      expect(LinuxAppUpdater.compareVersions('2.0.0', '1.99.99'), isPositive);
      expect(LinuxAppUpdater.compareVersions('v1.2.3', '1.2.3'), 0);
      expect(LinuxAppUpdater.compareVersions('1.2.3', '2.0.0'), isNegative);
      expect(
        () => LinuxAppUpdater.compareVersions('1.2-beta', '1.2.0'),
        throwsFormatException,
      );
      expect(
        () => LinuxAppUpdater.compareVersions('1.2.0', 'latest'),
        throwsFormatException,
      );
    });

    test(
      'accepts starred mixed-case checksums and rejects unsafe manifests',
      () async {
        final directory = await _temporaryDirectory();
        final asset = File('${directory.path}/update.AppImage');
        await asset.writeAsString('verified update bytes');
        final digest = (await sha256.bind(asset.openRead()).first).toString();

        await LinuxAppUpdater.verifyChecksum(
          asset: asset,
          assetName: 'update.AppImage',
          checksums: 'ignored\n${digest.toUpperCase()} *update.AppImage\n',
        );
        await expectLater(
          LinuxAppUpdater.verifyChecksum(
            asset: asset,
            assetName: 'update.AppImage',
            checksums: '$digest  update.AppImage\n$digest *update.AppImage\n',
          ),
          throwsFormatException,
        );
        await expectLater(
          LinuxAppUpdater.verifyChecksum(
            asset: asset,
            assetName: 'missing.AppImage',
            checksums: '$digest  update.AppImage\n',
          ),
          throwsFormatException,
        );
        await expectLater(
          LinuxAppUpdater.verifyChecksum(
            asset: asset,
            assetName: 'update.AppImage',
            checksums: '${List.filled(64, '0').join()}  update.AppImage\n',
          ),
          throwsStateError,
        );
      },
    );

    test('validates Ed25519 signatures, encoding, and key sizes', () async {
      final signed = await _signedPayload(
        'update.AppImage',
        utf8.encode('asset'),
      );
      await LinuxAppUpdater.verifyManifestSignature(
        manifest: signed.manifest,
        encodedSignature: '\n${signed.signature}\n',
        publicKey: signed.publicKey,
      );
      await expectLater(
        LinuxAppUpdater.verifyManifestSignature(
          manifest: [...signed.manifest, 0],
          encodedSignature: signed.signature,
          publicKey: signed.publicKey,
        ),
        throwsStateError,
      );
      await expectLater(
        LinuxAppUpdater.verifyManifestSignature(
          manifest: signed.manifest,
          encodedSignature: '%not-base64%',
          publicKey: signed.publicKey,
        ),
        throwsFormatException,
      );
      await expectLater(
        LinuxAppUpdater.verifyManifestSignature(
          manifest: signed.manifest,
          encodedSignature: base64Encode([1, 2, 3]),
          publicKey: signed.publicKey,
        ),
        throwsFormatException,
      );
      await expectLater(
        LinuxAppUpdater.verifyManifestSignature(
          manifest: signed.manifest,
          encodedSignature: signed.signature,
          publicKey: [1, 2, 3],
        ),
        throwsFormatException,
      );
      await expectLater(
        LinuxAppUpdater.verifyManifestSignature(
          manifest: signed.manifest,
          encodedSignature: signed.signature,
        ),
        throwsStateError,
      );
    });
  });

  group('initialization and cached state', () {
    test('rejects non-loopback, unsupported, and invalid builds', () async {
      final directory = await _temporaryDirectory();
      final remote = _updater(
        directory,
        apiBaseURL: Uri.parse('https://example.com'),
        installKind: LinuxInstallKind.appImage,
        currentVersion: '1.0.0',
      );
      expect(await remote.initialize(), isFalse);

      final unsupported = _updater(
        directory,
        installKind: LinuxInstallKind.unsupported,
        currentVersion: '1.0.0',
      );
      expect(await unsupported.initialize(), isFalse);
      expect(unsupported.installKind, LinuxInstallKind.unsupported);

      final invalid = _updater(
        directory,
        installKind: LinuxInstallKind.appImage,
        currentVersion: 'dev',
      );
      expect(await invalid.initialize(), isFalse);
    });

    test('detects an AppImage and reads the bundled daemon version', () async {
      final directory = await _temporaryDirectory();
      final appImage = File('${directory.path}/Heimdallm.AppImage');
      final daemon = File('${directory.path}/app/heimdalld');
      await appImage.writeAsString('image');
      await daemon.parent.create(recursive: true);
      await daemon.writeAsString('daemon');
      await _primeCache(directory);
      final calls = <String>[];
      final updater = _updater(
        directory,
        executablePath: '${directory.path}/app/heimdallm',
        environment: {'APPIMAGE': appImage.path},
        processRunner: _processRunner(daemonVersion: 'v1.2.3', calls: calls),
      );
      addTearDown(updater.dispose);

      expect(await updater.initialize(), isTrue);
      expect(updater.installKind, LinuxInstallKind.appImage);
      expect(updater.currentVersion, '1.2.3');
      expect(calls, contains('${daemon.path} version'));
    });

    test(
      'rejects a non-file APPIMAGE and missing or invalid daemons',
      () async {
        final directory = await _temporaryDirectory();
        final appImageDirectory = Directory('${directory.path}/image')
          ..createSync();
        final nonFile = _updater(
          directory,
          environment: {'APPIMAGE': appImageDirectory.path},
        );
        expect(await nonFile.initialize(), isFalse);

        final missingDaemon = _updater(
          directory,
          installKind: LinuxInstallKind.appImage,
        );
        expect(await missingDaemon.initialize(), isFalse);

        final daemon = File('${directory.path}/app/heimdalld');
        await daemon.parent.create(recursive: true);
        await daemon.writeAsString('daemon');
        for (final result in [
          ProcessResult(1, 1, '', 'failed'),
          ProcessResult(1, 0, 'development', ''),
        ]) {
          final updater = _updater(
            directory,
            installKind: LinuxInstallKind.appImage,
            processRunner: (executable, arguments) async {
              if (executable == '/bin/chmod') {
                return Process.run(executable, arguments);
              }
              return result;
            },
          );
          expect(await updater.initialize(), isFalse);
        }
      },
    );

    test('restores only a newer cache matching the package kind', () async {
      for (final kind in [
        LinuxInstallKind.appImage,
        LinuxInstallKind.deb,
        LinuxInstallKind.rpm,
      ]) {
        final directory = await _temporaryDirectory();
        final assetName = switch (kind) {
          LinuxInstallKind.appImage => 'Heimdallm-1.1.0-x86_64.AppImage',
          LinuxInstallKind.deb => 'heimdallm_1.1.0_amd64.deb',
          LinuxInstallKind.rpm => 'heimdallm_1.1.0_amd64.rpm',
          LinuxInstallKind.unsupported => throw StateError('unreachable'),
        };
        await _primeCache(
          directory,
          release: _release(assetName: assetName).toJson(),
        );
        final statuses = <AppUpdateStatus>[];
        final updater = _updater(
          directory,
          installKind: kind,
          currentVersion: '1.0.0',
          onStatus: statuses.add,
        );
        addTearDown(updater.dispose);

        expect(await updater.initialize(), isTrue);
        expect(updater.availableRelease?.assetName, assetName);
        expect(statuses.single.phase, AppUpdatePhase.available);
      }
    });

    test('ignores stale, mismatched, and malformed cache entries', () async {
      final cases = <Map<String, Object?>>[
        {'schemaVersion': 2, 'checkedAt': DateTime.now().toIso8601String()},
        {'schemaVersion': 1, 'checkedAt': 'not-a-date'},
        {
          'schemaVersion': 1,
          'checkedAt': DateTime.now().toIso8601String(),
          'release': 'not-an-object',
        },
        {
          'schemaVersion': 1,
          'checkedAt': DateTime.now().toIso8601String(),
          'release': _release(version: 'development').toJson(),
        },
        {
          'schemaVersion': 1,
          'checkedAt': DateTime.now().toIso8601String(),
          'release': _release(
            assetURL: Uri.parse('https://evil.example/a'),
          ).toJson(),
        },
        {
          'schemaVersion': 1,
          'checkedAt': DateTime.now().toIso8601String(),
          'release': _release(
            checksumsSignatureURL: Uri.parse('https://evil.example/sig'),
          ).toJson(),
        },
        {
          'schemaVersion': 1,
          'checkedAt': DateTime.now().toIso8601String(),
          'release': _release(version: '0.9.0').toJson(),
        },
        {
          'schemaVersion': 1,
          'checkedAt': DateTime.now().toIso8601String(),
          'release': _release(assetName: 'heimdallm_1.1.0_amd64.deb').toJson(),
        },
      ];
      for (final value in cases) {
        final directory = await _temporaryDirectory();
        await _writePrivateJSON(
          File('${directory.path}/linux-update-cache.json'),
          value,
        );
        final statuses = <AppUpdateStatus>[];
        final updater = _updater(
          directory,
          installKind: LinuxInstallKind.appImage,
          currentVersion: '1.0.0',
          onStatus: statuses.add,
          releaseLoader: () async => null,
        );
        addTearDown(updater.dispose);
        expect(await updater.initialize(), isTrue);
        expect(updater.availableRelease, isNull);
        expect(statuses, isEmpty);
      }
    });

    test(
      'rejects cache files that are not private regular JSON objects',
      () async {
        final directory = await _temporaryDirectory();
        final path = '${directory.path}/linux-update-cache.json';

        await Directory(path).create();
        var updater = _updater(
          directory,
          installKind: LinuxInstallKind.appImage,
          currentVersion: '1.0.0',
        );
        await expectLater(updater.initialize(), throwsStateError);
        await Directory(path).delete();

        final file = File(path);
        await file.writeAsString('[]');
        await Process.run('/bin/chmod', ['600', path]);
        updater = _updater(
          directory,
          installKind: LinuxInstallKind.appImage,
          currentVersion: '1.0.0',
        );
        await expectLater(updater.initialize(), throwsFormatException);

        await file.writeAsString('{}');
        await Process.run('/bin/chmod', ['644', path]);
        updater = _updater(
          directory,
          installKind: LinuxInstallKind.appImage,
          currentVersion: '1.0.0',
        );
        await expectLater(updater.initialize(), throwsStateError);
      },
    );
  });

  group('release checks', () {
    test(
      'publishes newer, current, and missing release states and secure cache',
      () async {
        final scenarios = <LinuxUpdateRelease?>[
          _release(),
          _release(version: '1.0.0'),
          null,
        ];
        for (final release in scenarios) {
          final directory = await _temporaryDirectory();
          await _primeCache(directory);
          final statuses = <AppUpdateStatus>[];
          final updater = _updater(
            directory,
            installKind: LinuxInstallKind.appImage,
            currentVersion: '1.0.0',
            releaseLoader: () async => release,
            onStatus: statuses.add,
          );
          addTearDown(updater.dispose);
          expect(await updater.initialize(), isTrue);

          await updater.checkForUpdates();
          expect(statuses.first.phase, AppUpdatePhase.checking);
          expect(
            statuses.last.phase,
            release?.version == '1.1.0'
                ? AppUpdatePhase.available
                : AppUpdatePhase.idle,
          );
          final cache = File('${directory.path}/linux-update-cache.json');
          expect(await cache.exists(), isTrue);
          expect((await cache.stat()).mode & 0x3f, 0);
          final decoded = jsonDecode(await cache.readAsString()) as Map;
          expect(decoded['schemaVersion'], 1);
        }
      },
    );

    test(
      'reports interactive errors, suppresses silent errors, and unlocks',
      () async {
        final directory = await _temporaryDirectory();
        await _primeCache(directory);
        var calls = 0;
        final statuses = <AppUpdateStatus>[];
        final updater = _updater(
          directory,
          installKind: LinuxInstallKind.appImage,
          currentVersion: '1.0.0',
          releaseLoader: () async {
            calls++;
            throw StateError('offline');
          },
          onStatus: statuses.add,
        );
        addTearDown(updater.dispose);
        expect(await updater.initialize(), isTrue);

        await expectLater(updater.checkForUpdates(), throwsStateError);
        expect(statuses.map((status) => status.phase), [
          AppUpdatePhase.checking,
          AppUpdatePhase.error,
        ]);
        final beforeSilent = statuses.length;
        await updater.checkForUpdates(silent: true);
        expect(statuses, hasLength(beforeSilent));
        await expectLater(updater.checkForUpdates(), throwsStateError);
        expect(calls, 3);
      },
    );

    test('coalesces concurrent checks', () async {
      final directory = await _temporaryDirectory();
      await _primeCache(directory);
      final loaderStarted = Completer<void>();
      final finishLoader = Completer<LinuxUpdateRelease?>();
      var loads = 0;
      final updater = _updater(
        directory,
        installKind: LinuxInstallKind.appImage,
        currentVersion: '1.0.0',
        releaseLoader: () {
          loads++;
          loaderStarted.complete();
          return finishLoader.future;
        },
      );
      addTearDown(updater.dispose);
      expect(await updater.initialize(), isTrue);

      final first = updater.checkForUpdates();
      await loaderStarted.future;
      await updater.checkForUpdates();
      expect(loads, 1);
      finishLoader.complete(null);
      await first;
    });

    test(
      'parses trusted GitHub metadata for every Linux package kind',
      () async {
        for (final kind in [
          LinuxInstallKind.appImage,
          LinuxInstallKind.deb,
          LinuxInstallKind.rpm,
        ]) {
          final directory = await _temporaryDirectory();
          await _primeCache(directory);
          final wanted = switch (kind) {
            LinuxInstallKind.appImage => 'Heimdallm-1.1.0-x86_64.AppImage',
            LinuxInstallKind.deb => 'heimdallm_1.1.0_amd64.deb',
            LinuxInstallKind.rpm => 'heimdallm_1.1.0_amd64.rpm',
            LinuxInstallKind.unsupported => throw StateError('unreachable'),
          };
          final metadata = {
            'tag_name': 'v1.1.0',
            'prerelease': false,
            'assets': [
              'ignored',
              {'name': wanted, 'browser_download_url': 'not a URL'},
              _assetMetadata(wanted),
              _assetMetadata('linux-checksums.txt'),
              _assetMetadata('linux-checksums.txt.sig'),
            ],
          };
          final updater = _updater(
            directory,
            installKind: kind,
            currentVersion: '1.0.0',
            httpClientFactory: () =>
                _FakeHttpClient((_) => _ResponseSpec.json(metadata)),
          );
          addTearDown(updater.dispose);
          expect(await updater.initialize(), isTrue);

          await updater.checkForUpdates();
          expect(updater.availableRelease?.assetName, wanted);
        }
      },
    );

    test(
      'rejects prereleases, malformed metadata, missing assets, and HTTP failures',
      () async {
        final responses = <_ResponseSpec>[
          _ResponseSpec.json([]),
          _ResponseSpec.json({'tag_name': 'v1.1.0', 'prerelease': true}),
          _ResponseSpec.json({'tag_name': 'latest', 'assets': []}),
          _ResponseSpec.json({'tag_name': 'v1.1.0', 'assets': 'missing'}),
          _ResponseSpec.json({'tag_name': 'v1.1.0', 'assets': []}),
          _ResponseSpec.text('unavailable', statusCode: 503),
        ];
        for (final response in responses) {
          final directory = await _temporaryDirectory();
          await _primeCache(directory);
          final updater = _updater(
            directory,
            installKind: LinuxInstallKind.appImage,
            currentVersion: '1.0.0',
            httpClientFactory: () => _FakeHttpClient((_) => response),
          );
          addTearDown(updater.dispose);
          expect(await updater.initialize(), isTrue);
          await expectLater(updater.checkForUpdates(), throwsException);
        }
      },
    );

    test('rejects oversized release metadata', () async {
      final directory = await _temporaryDirectory();
      await _primeCache(directory);
      final updater = _updater(
        directory,
        installKind: LinuxInstallKind.appImage,
        currentVersion: '1.0.0',
        httpClientFactory: () => _FakeHttpClient(
          (_) => _ResponseSpec.bytes(List.filled(1024 * 1024 + 1, 1)),
        ),
      );
      addTearDown(updater.dispose);
      expect(await updater.initialize(), isTrue);
      await expectLater(updater.checkForUpdates(), throwsFormatException);
    });
  });

  group('transactional installation', () {
    test(
      'downloads, verifies, seals, replaces, and records an AppImage',
      () async {
        final directory = await _temporaryDirectory();
        await _primeCache(directory);
        await File(
          '${directory.path}/api_token',
        ).writeAsString('secret-token\n');
        final target = File('${directory.path}/Heimdallm.AppImage');
        await target.writeAsString('old image');
        final assetBytes = utf8.encode('new signed image');
        final signed = await _signedPayload(
          target.uri.pathSegments.last,
          assetBytes,
        );
        final requests = <_RecordedRequest>[];
        final identityChecks = <String>[];
        final statuses = <AppUpdateStatus>[];
        final runnerCalls = <String>[];
        late final _FakeHttpClient client;
        client = _FakeHttpClient((request) {
          requests.add(request);
          if (request.uri.scheme == 'https') {
            if (request.uri.path.endsWith('.AppImage')) {
              return _ResponseSpec.bytes(assetBytes);
            }
            if (request.uri.path.endsWith('linux-checksums.txt')) {
              return _ResponseSpec.bytes(signed.manifest);
            }
            return _ResponseSpec.text(signed.signature);
          }
          if (request.uri.path.endsWith('/shutdown')) {
            return _ResponseSpec.text('');
          }
          final lease = request.headers.value('X-Heimdallm-Update-Lease')!;
          return _daemonStatus(
            lease: lease,
            sealed: request.uri.path.endsWith('/update/seal'),
          );
        });
        final updater = _updater(
          directory,
          environment: {'APPIMAGE': target.path},
          installKind: LinuxInstallKind.appImage,
          currentVersion: '1.0.0',
          releaseLoader: () async =>
              _release(assetName: target.uri.pathSegments.last),
          releasePublicKey: signed.publicKey,
          identityVerifier: (daemonPID, daemonPath) async {
            identityChecks.add('$daemonPID $daemonPath');
          },
          processRunner: _processRunner(calls: runnerCalls),
          httpClientFactory: () => client,
          onStatus: statuses.add,
        );
        addTearDown(updater.dispose);
        expect(await updater.initialize(), isTrue);

        final result = await updater.installAvailableUpdate();

        expect(result.version, '1.1.0');
        expect(result.restartPath, target.path);
        expect(await target.readAsString(), 'new signed image');
        expect(
          await File('${target.path}.previous').readAsString(),
          'old image',
        );
        expect(identityChecks, ['321 ${directory.path}/app/heimdalld']);
        expect(statuses.map((status) => status.phase), [
          AppUpdatePhase.installing,
          AppUpdatePhase.installing,
          AppUpdatePhase.restarting,
        ]);
        final journal =
            jsonDecode(
                  await File(
                    '${directory.path}/app-update-recovery.json',
                  ).readAsString(),
                )
                as Map<String, dynamic>;
        expect(journal['phase'], 'installing');
        expect(journal['launchAgentWasLoaded'], isFalse);
        expect(journal['launchAgentWasDisabled'], isFalse);
        expect(journal['leaseID'], matches(RegExp(r'^[0-9a-f-]{36}$')));
        expect(
          requests.where((request) => request.uri.path.endsWith('/shutdown')),
          hasLength(1),
        );
        expect(
          requests
              .where((request) => request.uri.scheme == 'http')
              .every(
                (request) =>
                    request.headers.value('X-Heimdallm-Token') ==
                    'secret-token',
              ),
          isTrue,
        );
        expect(
          runnerCalls.any((call) => call.startsWith('/bin/kill -0 ')),
          isTrue,
        );
        expect(client.closeCount, greaterThanOrEqualTo(6));
      },
    );

    test(
      'rolls back a prepared barrier and clears its journal on seal failure',
      () async {
        final directory = await _temporaryDirectory();
        await _primeCache(directory);
        await File('${directory.path}/api_token').writeAsString('token');
        final target = File('${directory.path}/Heimdallm.AppImage');
        await target.writeAsString('old');
        final assetBytes = utf8.encode('new');
        final signed = await _signedPayload(
          target.uri.pathSegments.last,
          assetBytes,
        );
        var prepareCalls = 0;
        var deleteCalls = 0;
        final statuses = <AppUpdateStatus>[];
        final client = _FakeHttpClient((request) {
          if (request.uri.scheme == 'https') {
            if (request.uri.path.endsWith('.AppImage')) {
              return _ResponseSpec.bytes(assetBytes);
            }
            if (request.uri.path.endsWith('linux-checksums.txt')) {
              return _ResponseSpec.bytes(signed.manifest);
            }
            return _ResponseSpec.text(signed.signature);
          }
          final lease = request.headers.value('X-Heimdallm-Update-Lease')!;
          if (request.method == 'DELETE') deleteCalls++;
          if (request.uri.path.endsWith('/update/prepare')) prepareCalls++;
          return _daemonStatus(lease: lease, sealed: false);
        });
        final updater = _updater(
          directory,
          environment: {'APPIMAGE': target.path},
          installKind: LinuxInstallKind.appImage,
          currentVersion: '1.0.0',
          releaseLoader: () async =>
              _release(assetName: target.uri.pathSegments.last),
          releasePublicKey: signed.publicKey,
          identityVerifier: (_, _) async {},
          processRunner: _processRunner(),
          httpClientFactory: () => client,
          onStatus: statuses.add,
        );
        addTearDown(updater.dispose);
        expect(await updater.initialize(), isTrue);

        await expectLater(updater.installAvailableUpdate(), throwsStateError);
        // The DELETE that releases the lease targets the same path.
        expect(prepareCalls, 3);
        expect(deleteCalls, 1);
        expect(
          await File('${directory.path}/app-update-recovery.json').exists(),
          isFalse,
        );
        expect(statuses.last.phase, AppUpdatePhase.error);
        expect(await target.readAsString(), 'old');
      },
    );

    test(
      'routes deb and rpm assets through their native package installers',
      () async {
        for (final kind in [LinuxInstallKind.deb, LinuxInstallKind.rpm]) {
          final directory = await _temporaryDirectory();
          await _primeCache(directory);
          await File('${directory.path}/api_token').writeAsString('token');
          final assetName = kind == LinuxInstallKind.deb
              ? 'heimdallm_1.1.0_amd64.deb'
              : 'heimdallm_1.1.0_amd64.rpm';
          final assetBytes = utf8.encode('signed package');
          final signed = await _signedPayload(assetName, assetBytes);
          var shutdownSeen = false;
          final client = _FakeHttpClient((request) {
            if (request.uri.scheme == 'https') {
              if (request.uri.path.endsWith(assetName)) {
                return _ResponseSpec.bytes(assetBytes);
              }
              if (request.uri.path.endsWith('linux-checksums.txt')) {
                return _ResponseSpec.bytes(signed.manifest);
              }
              return _ResponseSpec.text(signed.signature);
            }
            if (request.uri.path.endsWith('/health')) {
              return _ResponseSpec.json({'version': '1.0.0'});
            }
            if (request.uri.path.endsWith('/shutdown')) {
              shutdownSeen = true;
              return _ResponseSpec.text('');
            }
            if (request.uri.path.endsWith('/update/confirm')) {
              return _daemonStatus(
                lease: _leaseID,
                sealed: true,
                bootstrapAuthorized: true,
              );
            }
            return _daemonStatus(
              lease: request.headers.value('X-Heimdallm-Update-Lease')!,
              sealed: shutdownSeen || request.uri.path.endsWith('/update/seal'),
            );
          });
          final updater = _updater(
            directory,
            installKind: kind,
            currentVersion: '1.0.0',
            releaseLoader: () async => _release(
              assetName: assetName,
              assetURL: Uri.parse('https://example.test/$assetName'),
            ),
            releasePublicKey: signed.publicKey,
            identityVerifier: (_, _) async {},
            processRunner: _processRunner(),
            httpClientFactory: () => client,
          );
          addTearDown(updater.dispose);
          expect(await updater.initialize(), isTrue);

          await expectLater(updater.installAvailableUpdate(), throwsStateError);
          expect(shutdownSeen, isTrue);
          expect(
            await File('${directory.path}/app-update-recovery.json').exists(),
            isFalse,
          );
        }
      },
    );

    test(
      'rejects absent, stale, insecure, empty, and failed downloads',
      () async {
        final directory = await _temporaryDirectory();
        await _primeCache(directory);
        final base = _updater(
          directory,
          installKind: LinuxInstallKind.appImage,
          currentVersion: '1.0.0',
          releaseLoader: () async => null,
        );
        addTearDown(base.dispose);
        expect(await base.initialize(), isTrue);
        await expectLater(base.installAvailableUpdate(), throwsStateError);

        for (final release in [
          _release(version: '1.0.0'),
          _release(assetURL: Uri.parse('http://example.test/update.AppImage')),
        ]) {
          final updater = _updater(
            directory,
            installKind: LinuxInstallKind.appImage,
            currentVersion: '1.0.0',
            releaseLoader: () async => release,
            processRunner: _processRunner(),
          );
          addTearDown(updater.dispose);
          expect(await updater.initialize(), isTrue);
          await expectLater(
            updater.installAvailableUpdate(),
            throwsA(anything),
          );
        }

        for (final response in [
          _ResponseSpec.text('', statusCode: 200),
          _ResponseSpec.text('failed', statusCode: 500),
        ]) {
          final updater = _updater(
            directory,
            installKind: LinuxInstallKind.appImage,
            currentVersion: '1.0.0',
            releaseLoader: () async => _release(),
            processRunner: _processRunner(),
            httpClientFactory: () => _FakeHttpClient((_) => response),
          );
          addTearDown(updater.dispose);
          expect(await updater.initialize(), isTrue);
          await expectLater(updater.installAvailableUpdate(), throwsException);
        }
      },
    );

    test('prevents a second installation while a download is active', () async {
      final directory = await _temporaryDirectory();
      await _primeCache(directory);
      final requested = Completer<void>();
      final response = Completer<_ResponseSpec>();
      final updater = _updater(
        directory,
        installKind: LinuxInstallKind.appImage,
        currentVersion: '1.0.0',
        releaseLoader: () async => _release(),
        processRunner: _processRunner(),
        httpClientFactory: () => _FakeHttpClient((_) {
          if (!requested.isCompleted) requested.complete();
          return response.future;
        }),
      );
      addTearDown(updater.dispose);
      expect(await updater.initialize(), isTrue);

      final first = updater.installAvailableUpdate();
      await requested.future;
      await expectLater(updater.installAvailableUpdate(), throwsStateError);
      await updater.checkForUpdates();
      response.complete(_ResponseSpec.text('failed', statusCode: 500));
      await expectLater(first, throwsA(isA<HttpException>()));
    });

    test(
      'surfaces chmod failures and still resets installation state',
      () async {
        final directory = await _temporaryDirectory();
        await _primeCache(directory);
        var attempts = 0;
        final updater = _updater(
          directory,
          installKind: LinuxInstallKind.appImage,
          currentVersion: '1.0.0',
          releaseLoader: () async => _release(),
          processRunner: (executable, arguments) async {
            if (executable == '/bin/chmod' && arguments.first == '700') {
              return ProcessResult(1, 1, '', 'denied');
            }
            return Process.run(executable, arguments);
          },
        );
        addTearDown(updater.dispose);
        expect(await updater.initialize(), isTrue);
        await expectLater(updater.installAvailableUpdate(), throwsStateError);
        attempts++;
        await expectLater(updater.installAvailableUpdate(), throwsStateError);
        attempts++;
        expect(attempts, 2);
      },
    );
  });

  group('durable recovery', () {
    test('no-ops when no recovery journal exists', () async {
      final directory = await _temporaryDirectory();
      final updater = _updater(directory, processRunner: _processRunner());
      expect(await updater.pendingUpdateVersion(), isNull);
      await updater.completePendingUpdate();
      await updater.finalizePendingUpdate();
    });

    test(
      'recognizes an installed version without opening the barrier',
      () async {
        final directory = await _temporaryDirectory();
        await _writeDaemon(directory);
        await _writeJournal(directory, _journal());
        final updater = _updater(
          directory,
          processRunner: _processRunner(daemonVersion: '1.1.0'),
        );
        expect(await updater.pendingUpdateVersion(), '1.1.0');
      },
    );

    test(
      'rejects unknown interrupted and mismatched installed versions',
      () async {
        for (final daemonVersion in <String?>[null, '1.0.5']) {
          final directory = await _temporaryDirectory();
          if (daemonVersion != null) await _writeDaemon(directory);
          await _writeJournal(directory, _journal());
          final updater = _updater(
            directory,
            processRunner: _processRunner(
              daemonVersion: daemonVersion ?? '1.0.0',
            ),
          );
          await expectLater(updater.pendingUpdateVersion(), throwsStateError);
        }

        final directory = await _temporaryDirectory();
        await _writeDaemon(directory);
        await _writeJournal(directory, _journal());
        final updater = _updater(
          directory,
          processRunner: _processRunner(daemonVersion: '1.0.0'),
        );
        await expectLater(updater.completePendingUpdate(), throwsStateError);
        await expectLater(updater.finalizePendingUpdate(), throwsStateError);
      },
    );

    test('releases an old preparing lease and removes its journal', () async {
      final directory = await _temporaryDirectory();
      await _writeDaemon(directory);
      await File('${directory.path}/api_token').writeAsString('token');
      await _writeJournal(
        directory,
        _journal(expectedVersion: '1.1.0', daemonVersion: '1.0.0'),
      );
      final methods = <String>[];
      final client = _FakeHttpClient((request) {
        methods.add('${request.method} ${request.uri.path}');
        return _daemonStatus(lease: _leaseID, version: '1.0.0', sealed: false);
      });
      final updater = _updater(
        directory,
        processRunner: _processRunner(daemonVersion: '1.0.0'),
        httpClientFactory: () => client,
      );

      expect(await updater.pendingUpdateVersion(), isNull);
      expect(methods, ['POST /update/prepare', 'DELETE /update/prepare']);
      expect(
        await File('${directory.path}/app-update-recovery.json').exists(),
        isFalse,
      );
    });

    test(
      'restores, confirms, health-checks, and finalizes a sealed update',
      () async {
        final directory = await _temporaryDirectory();
        await _writeDaemon(directory);
        await File('${directory.path}/api_token').writeAsString('token');
        await _writeJournal(
          directory,
          _journal(phase: 'installing', expectedVersion: '1.1.0'),
        );
        final methods = <String>[];
        final statuses = <AppUpdateStatus>[];
        final client = _FakeHttpClient((request) {
          methods.add('${request.method} ${request.uri.path}');
          if (request.uri.path.endsWith('/health')) {
            return _ResponseSpec.json({'version': '1.1.0'});
          }
          if (request.uri.path.endsWith('/update/confirm')) {
            return _daemonStatus(
              lease: _leaseID,
              version: '1.1.0',
              sealed: true,
              bootstrapAuthorized: true,
            );
          }
          return _daemonStatus(lease: _leaseID, version: '1.1.0', sealed: true);
        });
        final updater = _updater(
          directory,
          processRunner: _processRunner(daemonVersion: '1.1.0'),
          httpClientFactory: () => client,
          onStatus: statuses.add,
        );

        await updater.completePendingUpdate();
        expect(methods, [
          'POST /update/prepare',
          'POST /update/confirm',
          'GET /health',
          'DELETE /update/prepare',
        ]);
        await updater.finalizePendingUpdate();
        expect(updater.currentVersion, '1.1.0');
        expect(updater.availableRelease, isNull);
        expect(statuses.single.message, 'Heimdallm was updated.');
        expect(
          await File('${directory.path}/app-update-recovery.json').exists(),
          isFalse,
        );
      },
    );

    test('re-seals an interrupted update before confirming it', () async {
      final directory = await _temporaryDirectory();
      await _writeDaemon(directory);
      await File('${directory.path}/api_token').writeAsString('token');
      await _writeJournal(directory, _journal(phase: 'sealed'));
      final methods = <String>[];
      final client = _FakeHttpClient((request) {
        methods.add('${request.method} ${request.uri.path}');
        if (request.uri.path.endsWith('/health')) {
          return _ResponseSpec.json({'version': '1.1.0'});
        }
        if (request.uri.path.endsWith('/update/confirm')) {
          return _daemonStatus(
            lease: _leaseID,
            version: '1.1.0',
            sealed: true,
            bootstrapAuthorized: true,
          );
        }
        return _daemonStatus(
          lease: _leaseID,
          version: '1.1.0',
          sealed: request.uri.path.endsWith('/update/seal'),
        );
      });
      final updater = _updater(
        directory,
        processRunner: _processRunner(daemonVersion: '1.1.0'),
        httpClientFactory: () => client,
      );

      await updater.completePendingUpdate();
      expect(methods, contains('POST /update/seal'));
    });

    test('starts a missing daemon once and retries recovery', () async {
      final directory = await _temporaryDirectory();
      await _writeDaemon(directory);
      await File('${directory.path}/api_token').writeAsString('token');
      await _writeJournal(
        directory,
        _journal(expectedVersion: '1.1.0', daemonVersion: '1.0.0'),
      );
      final probe = await ServerSocket.bind(InternetAddress.loopbackIPv4, 0);
      final port = probe.port;
      await probe.close();
      var prepares = 0;
      var starts = 0;
      final client = _FakeHttpClient((request) {
        if (request.uri.path.endsWith('/update/prepare') && prepares++ == 0) {
          throw const SocketException('not running');
        }
        return _daemonStatus(lease: _leaseID, version: '1.0.0', sealed: false);
      });
      final updater = _updater(
        directory,
        apiBaseURL: Uri.parse('http://127.0.0.1:$port/'),
        processRunner: _processRunner(daemonVersion: '1.0.0'),
        daemonStarter: (_) async => starts++,
        httpClientFactory: () => client,
      );

      expect(await updater.pendingUpdateVersion(), isNull);
      expect(starts, 1);
      // The successful cleanup DELETE also uses /update/prepare.
      expect(prepares, 3);
    });

    test(
      'does not start a daemon when another process owns its port',
      () async {
        final directory = await _temporaryDirectory();
        await _writeDaemon(directory);
        await File('${directory.path}/api_token').writeAsString('token');
        await _writeJournal(
          directory,
          _journal(expectedVersion: '1.1.0', daemonVersion: '1.0.0'),
        );
        final socket = await ServerSocket.bind(InternetAddress.loopbackIPv4, 0);
        addTearDown(socket.close);
        var starts = 0;
        final updater = _updater(
          directory,
          apiBaseURL: Uri.parse('http://127.0.0.1:${socket.port}/'),
          processRunner: _processRunner(daemonVersion: '1.0.0'),
          daemonStarter: (_) async => starts++,
          httpClientFactory: () => _FakeHttpClient(
            (_) => throw const SocketException('daemon unavailable'),
          ),
        );

        await expectLater(updater.pendingUpdateVersion(), throwsStateError);
        expect(starts, 0);
      },
    );

    test('validates recovery journals and daemon lease responses', () async {
      final invalidJournals = <Map<String, Object?>>[
        _journal()..['schemaVersion'] = 2,
        _journal()..['leaseID'] = 'not-a-uuid',
        _journal()..['expectedVersion'] = '',
        _journal()..['daemonPID'] = 0,
        _journal()..['daemonBootID'] = '',
        _journal()..['daemonVersion'] = '',
        _journal()..['phase'] = 'done',
        _journal()..['launchAgentWasLoaded'] = true,
      ];
      for (final journal in invalidJournals) {
        final directory = await _temporaryDirectory();
        await _writeJournal(directory, journal);
        final updater = _updater(directory, processRunner: _processRunner());
        await expectLater(
          updater.pendingUpdateVersion(),
          throwsFormatException,
        );
      }

      final directory = await _temporaryDirectory();
      await _writeDaemon(directory);
      await File('${directory.path}/api_token').writeAsString('token');
      await _writeJournal(directory, _journal());
      final updater = _updater(
        directory,
        processRunner: _processRunner(daemonVersion: '1.1.0'),
        httpClientFactory: () => _FakeHttpClient(
          (_) => _ResponseSpec.json({
            'state': 'ready',
            'pid': -1,
            'version': '',
            'lease_id': 'wrong',
            'sealed': false,
            'bootstrap_authorized': false,
            'boot_id': '',
          }),
        ),
      );
      await expectLater(updater.completePendingUpdate(), throwsStateError);
    });

    test('rejects failed re-sealing and unauthorized bootstrap', () async {
      for (final failConfirm in [false, true]) {
        final directory = await _temporaryDirectory();
        await _writeDaemon(directory);
        await File('${directory.path}/api_token').writeAsString('token');
        await _writeJournal(directory, _journal(phase: 'sealed'));
        final client = _FakeHttpClient((request) {
          if (request.uri.path.endsWith('/update/confirm')) {
            return _daemonStatus(
              lease: _leaseID,
              version: '1.1.0',
              sealed: true,
              bootstrapAuthorized: !failConfirm,
              bootID: failConfirm ? '' : 'boot-new',
            );
          }
          return _daemonStatus(
            lease: _leaseID,
            version: '1.1.0',
            sealed: failConfirm,
            activeTotal: failConfirm ? 0 : 1,
          );
        });
        final updater = _updater(
          directory,
          processRunner: _processRunner(daemonVersion: '1.1.0'),
          httpClientFactory: () => client,
        );
        await expectLater(updater.completePendingUpdate(), throwsStateError);
      }
    });

    test(
      'rejects daemon errors, non-objects, empty tokens, and version drift',
      () async {
        final responses = <_ResponseSpec>[
          _ResponseSpec.text('denied', statusCode: 403),
          _ResponseSpec.json([]),
          _daemonStatus(lease: _leaseID, version: '9.9.9', sealed: true),
        ];
        for (final response in responses) {
          final directory = await _temporaryDirectory();
          await _writeDaemon(directory);
          await File('${directory.path}/api_token').writeAsString('token');
          await _writeJournal(directory, _journal());
          var attempts = 0;
          final updater = _updater(
            directory,
            processRunner: _processRunner(daemonVersion: '1.1.0'),
            httpClientFactory: () => _FakeHttpClient((_) {
              if (attempts++ == 0) return response;
              // A malformed/unavailable first probe must not spin for the full
              // recovery timeout in the test. The retry reaches the distinct
              // version-drift guard and terminates deterministically.
              return _daemonStatus(
                lease: _leaseID,
                version: '9.9.9',
                sealed: true,
              );
            }),
          );
          await expectLater(updater.completePendingUpdate(), throwsA(anything));
        }

        final directory = await _temporaryDirectory();
        await _writeDaemon(directory);
        await File('${directory.path}/api_token').writeAsString('  \n');
        await _writeJournal(directory, _journal());
        final probe = await ServerSocket.bind(InternetAddress.loopbackIPv4, 0);
        final port = probe.port;
        await probe.close();
        final now = DateTime.utc(2026, 1, 1);
        var clockReads = 0;
        final updater = _updater(
          directory,
          apiBaseURL: Uri.parse('http://127.0.0.1:$port/'),
          processRunner: _processRunner(daemonVersion: '1.1.0'),
          clock: () =>
              clockReads++ < 2 ? now : now.add(const Duration(minutes: 2)),
        );
        await expectLater(
          updater.completePendingUpdate(),
          throwsA(isA<TimeoutException>()),
        );
      },
    );
  });
}

LinuxUpdateRelease _release({
  String version = '1.1.0',
  String assetName = 'Heimdallm-1.1.0-x86_64.AppImage',
  Uri? assetURL,
  Uri? checksumsURL,
  Uri? checksumsSignatureURL,
}) {
  final root =
      'https://github.com/theburrowhub/heimdallm/releases/download/v$version';
  return LinuxUpdateRelease(
    version: version,
    assetName: assetName,
    assetURL: assetURL ?? Uri.parse('$root/$assetName'),
    checksumsURL: checksumsURL ?? Uri.parse('$root/linux-checksums.txt'),
    checksumsSignatureURL:
        checksumsSignatureURL ?? Uri.parse('$root/linux-checksums.txt.sig'),
  );
}

Map<String, String> _assetMetadata(String name) => {
  'name': name,
  'browser_download_url':
      'https://github.com/theburrowhub/heimdallm/releases/download/v1.1.0/$name',
};

LinuxAppUpdater _updater(
  Directory directory, {
  Uri? apiBaseURL,
  String? executablePath,
  Map<String, String> environment = const {},
  LinuxUpdateProcessRunner? processRunner,
  LinuxUpdateDaemonStarter? daemonStarter,
  void Function(AppUpdateStatus)? onStatus,
  HttpClient Function()? httpClientFactory,
  LinuxUpdateClock? clock,
  LinuxInstallKind? installKind,
  String? currentVersion,
  LinuxUpdateReleaseLoader? releaseLoader,
  List<int>? releasePublicKey,
  LinuxUpdateDaemonIdentityVerifier? identityVerifier,
}) => LinuxAppUpdater(
  apiBaseURL: apiBaseURL ?? Uri.parse('http://127.0.0.1:47842/'),
  apiTokenPath: '${directory.path}/api_token',
  dataDirectory: directory.path,
  executablePath: executablePath ?? '${directory.path}/app/heimdallm',
  environment: environment,
  processRunner: processRunner ?? _processRunner(),
  daemonStarter: daemonStarter ?? (_) async {},
  onStatus: onStatus ?? (_) {},
  httpClientFactory: httpClientFactory,
  clock: clock,
  installKind: installKind,
  currentVersion: currentVersion,
  releaseLoader: releaseLoader,
  releasePublicKey: releasePublicKey,
  identityVerifier: identityVerifier,
  checkInterval: const Duration(days: 1),
);

Future<Directory> _temporaryDirectory() async {
  final directory = await Directory.systemTemp.createTemp(
    'heimdallm-linux-updater-test-',
  );
  addTearDown(() async {
    if (await directory.exists()) await directory.delete(recursive: true);
  });
  return directory;
}

Future<void> _primeCache(Directory directory, {Map<String, Object>? release}) =>
    _writePrivateJSON(File('${directory.path}/linux-update-cache.json'), {
      'schemaVersion': 1,
      'checkedAt': DateTime.now().toUtc().toIso8601String(),
      'release': release,
    });

Future<void> _writeJournal(Directory directory, Map<String, Object?> journal) =>
    _writePrivateJSON(
      File('${directory.path}/app-update-recovery.json'),
      journal,
    );

Future<void> _writePrivateJSON(File file, Map<String, Object?> value) async {
  await file.parent.create(recursive: true);
  await file.writeAsString(jsonEncode(value), flush: true);
  final result = await Process.run('/bin/chmod', ['600', file.path]);
  if (result.exitCode != 0) throw StateError('chmod failed: ${result.stderr}');
}

Map<String, Object?> _journal({
  String expectedVersion = '1.1.0',
  String phase = 'preparing',
  String daemonVersion = '1.0.0',
}) => {
  'schemaVersion': 1,
  'expectedVersion': expectedVersion,
  'phase': phase,
  'leaseID': _leaseID,
  'daemonPID': 321,
  'daemonBootID': 'boot-old',
  'daemonVersion': daemonVersion,
  'launchAgentWasLoaded': false,
  'launchAgentWasDisabled': false,
};

Future<void> _writeDaemon(Directory directory) async {
  final daemon = File('${directory.path}/app/heimdalld');
  await daemon.parent.create(recursive: true);
  await daemon.writeAsString('daemon');
}

LinuxUpdateProcessRunner _processRunner({
  String daemonVersion = '1.0.0',
  List<String>? calls,
}) => (executable, arguments) async {
  calls?.add('$executable ${arguments.join(' ')}');
  if (executable == '/bin/chmod') return Process.run(executable, arguments);
  if (arguments.length == 1 && arguments.single == 'version') {
    return ProcessResult(321, 0, daemonVersion, '');
  }
  if (executable == '/bin/kill') return ProcessResult(321, 1, '', '');
  return ProcessResult(321, 0, '', '');
};

Future<({List<int> manifest, String signature, List<int> publicKey})>
_signedPayload(String assetName, List<int> assetBytes) async {
  final digest = sha256.convert(assetBytes);
  final manifest = utf8.encode('$digest  $assetName\n');
  final algorithm = Ed25519();
  final keyPair = await algorithm.newKeyPair();
  final publicKey = await keyPair.extractPublicKey();
  final signature = await algorithm.sign(manifest, keyPair: keyPair);
  return (
    manifest: manifest,
    signature: base64Encode(signature.bytes),
    publicKey: publicKey.bytes,
  );
}

_ResponseSpec _daemonStatus({
  required String lease,
  String state = 'ready',
  int daemonPID = 321,
  String version = '1.0.0',
  bool sealed = false,
  bool bootstrapAuthorized = false,
  String bootID = 'boot-old',
  int activeTotal = 0,
}) => _ResponseSpec.json({
  'state': state,
  'pid': daemonPID,
  'version': version,
  'lease_id': lease,
  'sealed': sealed,
  'bootstrap_authorized': bootstrapAuthorized,
  'boot_id': bootID,
  'active_total': activeTotal,
});

typedef _FakeHttpHandler =
    FutureOr<_ResponseSpec> Function(_RecordedRequest request);

class _RecordedRequest {
  _RecordedRequest(this.method, this.uri, this.headers);

  final String method;
  final Uri uri;
  final _FakeHttpHeaders headers;
}

class _ResponseSpec {
  _ResponseSpec(this.chunks, {this.statusCode = HttpStatus.ok});

  factory _ResponseSpec.bytes(
    List<int> bytes, {
    int statusCode = HttpStatus.ok,
  }) => _ResponseSpec([bytes], statusCode: statusCode);

  factory _ResponseSpec.text(String value, {int statusCode = HttpStatus.ok}) =>
      _ResponseSpec.bytes(utf8.encode(value), statusCode: statusCode);

  factory _ResponseSpec.json(Object? value, {int statusCode = HttpStatus.ok}) =>
      _ResponseSpec.text(jsonEncode(value), statusCode: statusCode);

  final List<List<int>> chunks;
  final int statusCode;
}

class _FakeHttpClient implements HttpClient {
  _FakeHttpClient(this.handler);

  final _FakeHttpHandler handler;
  int closeCount = 0;

  @override
  Future<HttpClientRequest> getUrl(Uri url) => openUrl('GET', url);

  @override
  Future<HttpClientRequest> openUrl(String method, Uri url) async =>
      _FakeHttpClientRequest(method, url, handler);

  @override
  void close({bool force = false}) {
    closeCount++;
  }

  @override
  dynamic noSuchMethod(Invocation invocation) => super.noSuchMethod(invocation);
}

class _FakeHttpClientRequest implements HttpClientRequest {
  _FakeHttpClientRequest(this.method, this.uri, this.handler);

  @override
  final String method;
  @override
  final Uri uri;
  final _FakeHttpHandler handler;
  final _FakeHttpHeaders fakeHeaders = _FakeHttpHeaders();

  @override
  HttpHeaders get headers => fakeHeaders;

  @override
  Future<HttpClientResponse> close() async {
    final response = await handler(_RecordedRequest(method, uri, fakeHeaders));
    return _FakeHttpClientResponse(response);
  }

  @override
  dynamic noSuchMethod(Invocation invocation) => super.noSuchMethod(invocation);
}

class _FakeHttpHeaders implements HttpHeaders {
  final Map<String, List<String>> _values = {};

  @override
  void set(String name, Object value, {bool preserveHeaderCase = false}) {
    _values[name.toLowerCase()] = [value.toString()];
  }

  @override
  String? value(String name) => _values[name.toLowerCase()]?.single;

  @override
  List<String>? operator [](String name) => _values[name.toLowerCase()];

  @override
  dynamic noSuchMethod(Invocation invocation) => super.noSuchMethod(invocation);
}

class _FakeHttpClientResponse extends Stream<List<int>>
    implements HttpClientResponse {
  _FakeHttpClientResponse(this.response)
    : _stream = Stream<List<int>>.fromIterable(response.chunks);

  final _ResponseSpec response;
  final Stream<List<int>> _stream;

  @override
  int get statusCode => response.statusCode;

  @override
  StreamSubscription<List<int>> listen(
    void Function(List<int>)? onData, {
    Function? onError,
    void Function()? onDone,
    bool? cancelOnError,
  }) => _stream.listen(
    onData,
    onError: onError,
    onDone: onDone,
    cancelOnError: cancelOnError,
  );

  @override
  dynamic noSuchMethod(Invocation invocation) => super.noSuchMethod(invocation);
}

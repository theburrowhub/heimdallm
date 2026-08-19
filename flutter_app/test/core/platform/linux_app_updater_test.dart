import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:crypto/crypto.dart';
import 'package:cryptography/cryptography.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/platform/linux_app_updater.dart';
import 'package:heimdallm/core/platform/platform_services.dart';

void main() {
  test('compares stable semantic versions', () {
    expect(LinuxAppUpdater.compareVersions('1.2.3', '1.2.2'), isPositive);
    expect(LinuxAppUpdater.compareVersions('v1.2.3', '1.2.3'), 0);
    expect(LinuxAppUpdater.compareVersions('1.2.3', '2.0.0'), isNegative);
    expect(
      () => LinuxAppUpdater.compareVersions('1.2-beta', '1.2.0'),
      throwsFormatException,
    );
  });

  test('accepts one exact checksum and rejects a mismatch', () async {
    final directory = await Directory.systemTemp.createTemp(
      'heimdallm-linux-updater-test-',
    );
    addTearDown(() => directory.delete(recursive: true));
    final asset = File('${directory.path}/Heimdallm-1.2.3-x86_64.AppImage');
    await asset.writeAsString('verified update bytes');
    final digest = (await sha256.bind(asset.openRead()).first).toString();

    await LinuxAppUpdater.verifyChecksum(
      asset: asset,
      assetName: asset.uri.pathSegments.last,
      checksums: '$digest  ${asset.uri.pathSegments.last}\n',
    );
    await expectLater(
      LinuxAppUpdater.verifyChecksum(
        asset: asset,
        assetName: asset.uri.pathSegments.last,
        checksums:
            '${List.filled(64, '0').join()}  ${asset.uri.pathSegments.last}\n',
      ),
      throwsStateError,
    );
  });

  test('accepts an Ed25519 manifest signature and rejects tampering', () async {
    final algorithm = Ed25519();
    final keyPair = await algorithm.newKeyPair();
    final publicKey = await keyPair.extractPublicKey();
    final manifest = utf8.encode('abc  heimdallm_1.2.3_amd64.deb\n');
    final signature = await algorithm.sign(manifest, keyPair: keyPair);

    await LinuxAppUpdater.verifyManifestSignature(
      manifest: manifest,
      encodedSignature: base64Encode(signature.bytes),
      publicKey: publicKey.bytes,
    );
    await expectLater(
      LinuxAppUpdater.verifyManifestSignature(
        manifest: [...manifest, 0],
        encodedSignature: base64Encode(signature.bytes),
        publicKey: publicKey.bytes,
      ),
      throwsStateError,
    );
  });

  test(
    'runs the first automatic check and publishes an available update',
    () async {
      final directory = await Directory.systemTemp.createTemp(
        'heimdallm-linux-updater-test-',
      );
      addTearDown(() => directory.delete(recursive: true));
      final available = Completer<AppUpdateStatus>();
      final updater = LinuxAppUpdater(
        apiBaseURL: Uri.parse('http://127.0.0.1:7842'),
        apiTokenPath: '${directory.path}/api_token',
        dataDirectory: directory.path,
        executablePath: '/opt/heimdallm/heimdallm',
        environment: const {},
        processRunner: Process.run,
        daemonStarter: (_) async {},
        onStatus: (status) {
          if (status.updateAvailable && !available.isCompleted) {
            available.complete(status);
          }
        },
        installKind: LinuxInstallKind.appImage,
        currentVersion: '1.0.0',
        releaseLoader: () async => LinuxUpdateRelease(
          version: '1.1.0',
          assetName: 'Heimdallm-1.1.0-x86_64.AppImage',
          assetURL: Uri.parse(
            'https://github.com/theburrowhub/heimdallm/releases/download/v1.1.0/'
            'Heimdallm-1.1.0-x86_64.AppImage',
          ),
          checksumsURL: Uri.parse(
            'https://github.com/theburrowhub/heimdallm/releases/download/v1.1.0/'
            'linux-checksums.txt',
          ),
          checksumsSignatureURL: Uri.parse(
            'https://github.com/theburrowhub/heimdallm/releases/download/v1.1.0/'
            'linux-checksums.txt.sig',
          ),
        ),
      );
      addTearDown(updater.dispose);

      expect(await updater.initialize(), isTrue);
      final status = await available.future.timeout(const Duration(seconds: 3));
      expect(status.version, '1.1.0');
    },
  );
}

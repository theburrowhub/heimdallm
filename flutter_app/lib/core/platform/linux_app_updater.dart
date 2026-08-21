import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:math';

import 'package:crypto/crypto.dart';
import 'package:cryptography/cryptography.dart';
import 'package:flutter/foundation.dart' show visibleForTesting;

import 'platform_services.dart';

typedef LinuxUpdateProcessRunner =
    Future<ProcessResult> Function(String executable, List<String> arguments);
typedef LinuxUpdateDaemonStarter = Future<void> Function(String binaryPath);
typedef LinuxUpdateClock = DateTime Function();
typedef LinuxUpdateReleaseLoader = Future<LinuxUpdateRelease?> Function();
typedef LinuxUpdateDaemonIdentityVerifier =
    Future<void> Function(int pid, String daemonPath);

enum LinuxInstallKind { appImage, deb, rpm, unsupported }

class LinuxUpdateRelease {
  const LinuxUpdateRelease({
    required this.version,
    required this.assetName,
    required this.assetURL,
    required this.checksumsURL,
    required this.checksumsSignatureURL,
  });

  final String version;
  final String assetName;
  final Uri assetURL;
  final Uri checksumsURL;
  final Uri checksumsSignatureURL;

  Map<String, Object> toJson() => {
    'version': version,
    'assetName': assetName,
    'assetURL': assetURL.toString(),
    'checksumsURL': checksumsURL.toString(),
    'checksumsSignatureURL': checksumsSignatureURL.toString(),
  };
}

class LinuxInstallResult {
  const LinuxInstallResult({required this.version, required this.restartPath});

  final String version;
  final String restartPath;
}

class _LinuxRecoveryJournal {
  const _LinuxRecoveryJournal({
    required this.expectedVersion,
    required this.phase,
    required this.leaseID,
    required this.daemonPID,
    required this.daemonBootID,
    required this.daemonVersion,
  });

  final String expectedVersion;
  final String phase;
  final int daemonPID;
  final String daemonBootID;
  final String daemonVersion;
  final String leaseID;

  _LinuxRecoveryJournal copyWith({String? phase}) => _LinuxRecoveryJournal(
    expectedVersion: expectedVersion,
    phase: phase ?? this.phase,
    leaseID: leaseID,
    daemonPID: daemonPID,
    daemonBootID: daemonBootID,
    daemonVersion: daemonVersion,
  );

  Map<String, Object> toJson() => {
    'schemaVersion': 1,
    'expectedVersion': expectedVersion,
    'phase': phase,
    'leaseID': leaseID,
    'daemonPID': daemonPID,
    'daemonBootID': daemonBootID,
    'daemonVersion': daemonVersion,
    // The daemon journal schema is shared with macOS. Explicit false values
    // mean Linux has no launchd state to restore.
    'launchAgentWasLoaded': false,
    'launchAgentWasDisabled': false,
  };

  factory _LinuxRecoveryJournal.fromJson(Map<String, dynamic> json) {
    if (json['schemaVersion'] != 1 ||
        json['expectedVersion'] is! String ||
        json['phase'] is! String ||
        json['leaseID'] is! String ||
        json['daemonPID'] is! int ||
        json['daemonBootID'] is! String ||
        json['daemonVersion'] is! String ||
        json['launchAgentWasLoaded'] != false ||
        json['launchAgentWasDisabled'] != false) {
      throw const FormatException('invalid Linux update recovery journal');
    }
    final journal = _LinuxRecoveryJournal(
      expectedVersion: json['expectedVersion'] as String,
      phase: json['phase'] as String,
      leaseID: json['leaseID'] as String,
      daemonPID: json['daemonPID'] as int,
      daemonBootID: json['daemonBootID'] as String,
      daemonVersion: json['daemonVersion'] as String,
    );
    if (!_validUUID(journal.leaseID) ||
        journal.expectedVersion.trim().isEmpty ||
        journal.daemonPID <= 0 ||
        journal.daemonBootID.trim().isEmpty ||
        journal.daemonVersion.trim().isEmpty ||
        !const {'preparing', 'sealed', 'installing'}.contains(journal.phase)) {
      throw const FormatException('invalid Linux update recovery values');
    }
    return journal;
  }
}

class _DrainStatus {
  const _DrainStatus({
    required this.state,
    required this.pid,
    required this.version,
    required this.leaseID,
    required this.sealed,
    required this.bootstrapAuthorized,
    required this.bootID,
    required this.activeTotal,
  });

  final String state;
  final int pid;
  final String version;
  final String leaseID;
  final bool sealed;
  final bool bootstrapAuthorized;
  final String bootID;
  final int activeTotal;

  factory _DrainStatus.fromJson(Map<String, dynamic> value) {
    return _DrainStatus(
      state: value['state']?.toString() ?? '',
      pid: value['pid'] is int ? value['pid'] as int : -1,
      version: value['version']?.toString() ?? '',
      leaseID: value['lease_id']?.toString() ?? '',
      sealed: value['sealed'] == true,
      bootstrapAuthorized: value['bootstrap_authorized'] == true,
      bootID: value['boot_id']?.toString() ?? '',
      activeTotal: value['active_total'] is int
          ? value['active_total'] as int
          : -1,
    );
  }
}

/// Signed-release consumer and transactional installer for native Linux builds.
///
/// It supports every Linux artifact published by Heimdallm: AppImage, Debian,
/// and RPM. Package upgrades use PolicyKit for the single system authorization
/// prompt; AppImages are replaced atomically without elevation.
class LinuxAppUpdater {
  LinuxAppUpdater({
    required this.apiBaseURL,
    required this.apiTokenPath,
    required this.dataDirectory,
    required this.executablePath,
    required this.environment,
    required this.processRunner,
    required this.daemonStarter,
    required this.onStatus,
    HttpClient Function()? httpClientFactory,
    LinuxUpdateClock? clock,
    @visibleForTesting LinuxInstallKind? installKind,
    @visibleForTesting String? currentVersion,
    @visibleForTesting LinuxUpdateReleaseLoader? releaseLoader,
    @visibleForTesting List<int>? releasePublicKey,
    @visibleForTesting LinuxUpdateDaemonIdentityVerifier? identityVerifier,
    Duration checkInterval = const Duration(days: 1),
  }) : _httpClientFactory = httpClientFactory ?? HttpClient.new,
       _clock = clock ?? DateTime.now,
       _checkInterval = checkInterval,
       _forcedInstallKind = installKind,
       _forcedCurrentVersion = currentVersion,
       _releaseLoader = releaseLoader,
       _injectedReleasePublicKey = releasePublicKey,
       _identityVerifier = identityVerifier;

  static const _repository = 'theburrowhub/heimdallm';
  static const _releaseAPI =
      'https://api.github.com/repos/$_repository/releases/latest';
  static const _checksumsName = 'linux-checksums.txt';
  static const _checksumsSignatureName = 'linux-checksums.txt.sig';
  static const _releasePublicKey =
      'eBiLZ1yDuQ/NVCx7VJoQZJm6FpB8S0yH9WHtxaRfEDw=';
  static const _maxMetadataBytes = 1024 * 1024;
  static const _maxAssetBytes = 2 * 1024 * 1024 * 1024;
  static const _daemonRequestTimeout = Duration(seconds: 10);
  static const _drainTimeout = Duration(minutes: 10);
  static const _daemonStopTimeout = Duration(seconds: 20);
  static const _daemonStartTimeout = Duration(seconds: 60);

  final Uri apiBaseURL;
  final String apiTokenPath;
  final String dataDirectory;
  final String executablePath;
  final Map<String, String> environment;
  final LinuxUpdateProcessRunner processRunner;
  final LinuxUpdateDaemonStarter daemonStarter;
  final void Function(AppUpdateStatus status) onStatus;
  final HttpClient Function() _httpClientFactory;
  final LinuxUpdateClock _clock;
  final Duration _checkInterval;
  final LinuxInstallKind? _forcedInstallKind;
  final String? _forcedCurrentVersion;
  final LinuxUpdateReleaseLoader? _releaseLoader;
  final List<int>? _injectedReleasePublicKey;
  final LinuxUpdateDaemonIdentityVerifier? _identityVerifier;

  LinuxInstallKind _installKind = LinuxInstallKind.unsupported;
  String? _currentVersion;
  LinuxUpdateRelease? _availableRelease;
  Timer? _scheduledCheck;
  bool _checking = false;
  bool _installing = false;

  LinuxInstallKind get installKind => _installKind;
  LinuxUpdateRelease? get availableRelease => _availableRelease;
  String? get currentVersion => _currentVersion;

  @visibleForTesting
  void dispose() => _scheduledCheck?.cancel();

  String get _daemonPath => '${File(executablePath).parent.path}/heimdalld';
  String get _cachePath => '$dataDirectory/linux-update-cache.json';
  String get _journalPath => '$dataDirectory/app-update-recovery.json';

  Future<bool> initialize() async {
    if (!_isLoopbackAPI(apiBaseURL)) return false;
    _installKind = _forcedInstallKind ?? await _detectInstallKind();
    if (_installKind == LinuxInstallKind.unsupported) return false;
    _currentVersion =
        _forcedCurrentVersion ?? await _readBundledDaemonVersion();
    if (_currentVersion == null || !_isReleaseVersion(_currentVersion!)) {
      return false;
    }

    final cached = await _readCache();
    final checkedAt = cached?.$1;
    final cachedRelease = cached?.$2;
    if (cachedRelease != null &&
        compareVersions(cachedRelease.version, _currentVersion!) > 0 &&
        _assetMatchesInstall(cachedRelease.assetName)) {
      _availableRelease = cachedRelease;
      onStatus(
        AppUpdateStatus(
          phase: AppUpdatePhase.available,
          version: cachedRelease.version,
          message: 'Heimdallm ${cachedRelease.version} is ready to install.',
        ),
      );
    }

    final elapsed = checkedAt == null
        ? _checkInterval
        : _clock().toUtc().difference(checkedAt);
    final delay = elapsed >= _checkInterval
        ? Duration.zero
        : _checkInterval - elapsed;
    _scheduledCheck?.cancel();
    _scheduledCheck = Timer(delay, () {
      unawaited(checkForUpdates(silent: true));
      _scheduledCheck = Timer.periodic(
        _checkInterval,
        (_) => unawaited(checkForUpdates(silent: true)),
      );
    });
    return true;
  }

  Future<void> checkForUpdates({bool silent = false}) async {
    if (_checking || _installing) return;
    _checking = true;
    if (!silent) {
      onStatus(
        const AppUpdateStatus(
          phase: AppUpdatePhase.checking,
          message: 'Checking for updates…',
        ),
      );
    }
    try {
      final release = await _loadLatestRelease();
      await _writeCache(_clock().toUtc(), release);
      if (release != null &&
          compareVersions(release.version, _currentVersion!) > 0) {
        _availableRelease = release;
        onStatus(
          AppUpdateStatus(
            phase: AppUpdatePhase.available,
            version: release.version,
            message: 'Heimdallm ${release.version} is ready to install.',
          ),
        );
      } else {
        _availableRelease = null;
        onStatus(
          AppUpdateStatus.idle(
            message: silent ? null : 'Heimdallm is up to date.',
          ),
        );
      }
    } catch (error) {
      if (!silent) {
        onStatus(
          AppUpdateStatus(
            phase: AppUpdatePhase.error,
            message: 'Could not check for updates: $error',
          ),
        );
        rethrow;
      }
    } finally {
      _checking = false;
    }
  }

  Future<LinuxInstallResult> installAvailableUpdate() async {
    if (_installing) {
      throw StateError('An application update is already running.');
    }
    var release = _availableRelease;
    release ??= await _loadLatestRelease();
    if (release == null ||
        compareVersions(release.version, _currentVersion!) <= 0) {
      throw StateError('No newer Heimdallm release is available.');
    }
    _installing = true;
    onStatus(
      AppUpdateStatus(
        phase: AppUpdatePhase.installing,
        version: release.version,
        message: 'Downloading and verifying Heimdallm ${release.version}…',
      ),
    );

    final downloadDirectory = await Directory.systemTemp.createTemp(
      'heimdallm-update-',
    );
    await _chmod(downloadDirectory.path, '700');
    _LinuxRecoveryJournal? journal;
    try {
      final asset = File('${downloadDirectory.path}/${release.assetName}');
      await _download(release.assetURL, asset);
      final checksumBytes = await _readHTTPS(
        release.checksumsURL,
        _maxMetadataBytes,
      );
      final encodedSignature = utf8.decode(
        await _readHTTPS(release.checksumsSignatureURL, _maxMetadataBytes),
      );
      await verifyManifestSignature(
        manifest: checksumBytes,
        encodedSignature: encodedSignature,
        publicKey: _injectedReleasePublicKey,
      );
      await verifyChecksum(
        asset: asset,
        assetName: release.assetName,
        checksums: utf8.decode(checksumBytes),
      );

      final leaseID = _newUUID();
      final drained = await _drainDaemon(leaseID);
      await _verifyDaemonIdentity(drained);
      journal = _LinuxRecoveryJournal(
        expectedVersion: release.version,
        phase: 'preparing',
        leaseID: leaseID,
        daemonPID: drained.pid,
        daemonBootID: drained.bootID,
        daemonVersion: drained.version,
      );
      await _writeJournal(journal);

      final sealed = await _daemonRequest(
        method: 'POST',
        path: 'update/seal',
        leaseID: leaseID,
      );
      _verifyLeaseStatus(sealed, leaseID);
      if (!sealed.sealed ||
          sealed.activeTotal != 0 ||
          sealed.pid != drained.pid) {
        throw StateError(
          'The daemon did not confirm an idle sealed update barrier.',
        );
      }
      journal = journal.copyWith(phase: 'sealed');
      await _writeJournal(journal);

      await _shutdownDaemon(leaseID, drained.pid);
      journal = journal.copyWith(phase: 'installing');
      await _writeJournal(journal);

      onStatus(
        AppUpdateStatus(
          phase: AppUpdatePhase.installing,
          version: release.version,
          message: 'Installing Heimdallm ${release.version}…',
        ),
      );
      final restartPath = await _installAsset(asset);
      // A running AppImage remains mounted from its old inode even after the
      // outer image is atomically replaced. Its new bundled daemon can only be
      // inspected after relaunch; the common recovery path performs that exact
      // version check before it opens the sealed work gate.
      if (_installKind != LinuxInstallKind.appImage) {
        final installedVersion = await _readBundledDaemonVersion();
        if (installedVersion != release.version) {
          throw StateError(
            'The installed daemon reports ${installedVersion ?? 'no version'}, '
            'expected ${release.version}.',
          );
        }
      }

      onStatus(
        AppUpdateStatus(
          phase: AppUpdatePhase.restarting,
          version: release.version,
          message: 'Restarting Heimdallm ${release.version}…',
        ),
      );
      return LinuxInstallResult(
        version: release.version,
        restartPath: restartPath,
      );
    } catch (error) {
      if (journal != null) {
        try {
          await _recoverBarrier(
            journal,
            expectedDaemonVersion: _currentVersion!,
          );
          await _clearJournal();
        } catch (_) {
          // Keep the durable journal and sealed marker. The next launch will
          // retry recovery before ordinary daemon work is admitted.
        }
      }
      onStatus(
        AppUpdateStatus(
          phase: AppUpdatePhase.error,
          version: release.version,
          message: 'Update failed: $error',
        ),
      );
      rethrow;
    } finally {
      _installing = false;
      try {
        if (await downloadDirectory.exists()) {
          await downloadDirectory.delete(recursive: true);
        }
      } catch (_) {}
    }
  }

  /// Returns the expected version only when the on-disk installation was
  /// replaced. If a crash happened before replacement, the old daemon lease is
  /// safely released and normal startup continues on the old version.
  Future<String?> pendingUpdateVersion() async {
    final journal = await _readJournal();
    if (journal == null) return null;
    final diskVersion = await _readBundledDaemonVersion();
    if (diskVersion == journal.expectedVersion) return journal.expectedVersion;

    if (diskVersion == null || diskVersion != journal.daemonVersion) {
      throw StateError(
        'The interrupted update left an unknown daemon version on disk.',
      );
    }
    await _recoverBarrier(journal, expectedDaemonVersion: diskVersion);
    await _clearJournal();
    return null;
  }

  Future<void> completePendingUpdate() async {
    final journal = await _readJournal();
    if (journal == null) return;
    final diskVersion = await _readBundledDaemonVersion();
    if (diskVersion != journal.expectedVersion) {
      throw StateError(
        'The installed daemon reports ${diskVersion ?? 'no version'}, '
        'expected ${journal.expectedVersion}.',
      );
    }
    await _recoverBarrier(journal, expectedDaemonVersion: diskVersion!);
  }

  Future<void> finalizePendingUpdate() async {
    final journal = await _readJournal();
    if (journal == null) return;
    final diskVersion = await _readBundledDaemonVersion();
    if (diskVersion != journal.expectedVersion) {
      throw StateError(
        'Refusing to clear update recovery for a mismatched version.',
      );
    }
    await _clearJournal();
    _currentVersion = diskVersion;
    _availableRelease = null;
    onStatus(const AppUpdateStatus.idle(message: 'Heimdallm was updated.'));
  }

  Future<LinuxInstallKind> _detectInstallKind() async {
    final appImage = environment['APPIMAGE'];
    if (appImage != null && appImage.isNotEmpty) {
      final type = await FileSystemEntity.type(appImage, followLinks: false);
      if (type == FileSystemEntityType.file) return LinuxInstallKind.appImage;
      return LinuxInstallKind.unsupported;
    }
    final canonical = await File(executablePath).resolveSymbolicLinks();
    if (!canonical.startsWith('/opt/heimdallm/')) {
      return LinuxInstallKind.unsupported;
    }
    if (await File('/usr/bin/dpkg-query').exists()) {
      final result = await processRunner('/usr/bin/dpkg-query', const [
        '-W',
        r'-f=${Status}',
        'heimdallm',
      ]);
      if (result.exitCode == 0 &&
          '${result.stdout}'.contains('install ok installed')) {
        return LinuxInstallKind.deb;
      }
    }
    if (await File('/usr/bin/rpm').exists()) {
      final result = await processRunner('/usr/bin/rpm', const [
        '-q',
        'heimdallm',
      ]);
      if (result.exitCode == 0) return LinuxInstallKind.rpm;
    }
    return LinuxInstallKind.unsupported;
  }

  Future<String?> _readBundledDaemonVersion() async {
    final daemon = File(_daemonPath);
    if (!await daemon.exists()) return null;
    final result = await processRunner(daemon.path, const ['version']);
    if (result.exitCode != 0) return null;
    final version = '${result.stdout}'.trim().replaceFirst(RegExp(r'^v'), '');
    return _isReleaseVersion(version) ? version : null;
  }

  Future<LinuxUpdateRelease?> _loadLatestRelease() async {
    final injected = _releaseLoader;
    if (injected != null) return injected();
    final data = await _readHTTPS(Uri.parse(_releaseAPI), _maxMetadataBytes);
    final decoded = jsonDecode(utf8.decode(data));
    if (decoded is! Map<String, dynamic> || decoded['prerelease'] == true) {
      throw const FormatException('GitHub returned invalid release metadata.');
    }
    final version = decoded['tag_name']?.toString().trim().replaceFirst(
      RegExp(r'^v'),
      '',
    );
    if (version == null || !_isReleaseVersion(version)) {
      throw const FormatException('The latest release has an invalid version.');
    }
    final wantedAsset = switch (_installKind) {
      LinuxInstallKind.appImage => 'Heimdallm-$version-x86_64.AppImage',
      LinuxInstallKind.deb => 'heimdallm_${version}_amd64.deb',
      LinuxInstallKind.rpm => 'heimdallm_${version}_amd64.rpm',
      LinuxInstallKind.unsupported => '',
    };
    Uri? assetURL;
    Uri? checksumsURL;
    Uri? checksumsSignatureURL;
    final assets = decoded['assets'];
    if (assets is! List) {
      throw const FormatException('Release assets are missing.');
    }
    for (final entry in assets) {
      if (entry is! Map<String, dynamic>) continue;
      final name = entry['name']?.toString();
      final rawURL = entry['browser_download_url']?.toString();
      final uri = rawURL == null ? null : Uri.tryParse(rawURL);
      if (uri == null || !_isTrustedReleaseURL(uri, version)) continue;
      if (name == wantedAsset) assetURL = uri;
      if (name == _checksumsName) checksumsURL = uri;
      if (name == _checksumsSignatureName) checksumsSignatureURL = uri;
    }
    if (assetURL == null ||
        checksumsURL == null ||
        checksumsSignatureURL == null) {
      throw FormatException('Release $version is missing Linux update assets.');
    }
    return LinuxUpdateRelease(
      version: version,
      assetName: wantedAsset,
      assetURL: assetURL,
      checksumsURL: checksumsURL,
      checksumsSignatureURL: checksumsSignatureURL,
    );
  }

  bool _assetMatchesInstall(String name) => switch (_installKind) {
    LinuxInstallKind.appImage => name.endsWith('-x86_64.AppImage'),
    LinuxInstallKind.deb => name.endsWith('_amd64.deb'),
    LinuxInstallKind.rpm => name.endsWith('_amd64.rpm'),
    LinuxInstallKind.unsupported => false,
  };

  Future<List<int>> _readHTTPS(Uri uri, int maximumBytes) async {
    if (uri.scheme != 'https') {
      throw ArgumentError('Update URL must use HTTPS.');
    }
    final client = _httpClientFactory();
    try {
      final request = await client.getUrl(uri).timeout(_daemonRequestTimeout);
      request.headers.set(
        HttpHeaders.acceptHeader,
        'application/vnd.github+json',
      );
      request.headers.set(
        HttpHeaders.userAgentHeader,
        'Heimdallm desktop updater',
      );
      final response = await request.close().timeout(_daemonRequestTimeout);
      if (response.statusCode != HttpStatus.ok) {
        throw HttpException('HTTP ${response.statusCode}', uri: uri);
      }
      final bytes = <int>[];
      await for (final chunk in response.timeout(_daemonRequestTimeout)) {
        bytes.addAll(chunk);
        if (bytes.length > maximumBytes) {
          throw const FormatException(
            'Update metadata exceeds the size limit.',
          );
        }
      }
      return bytes;
    } finally {
      client.close(force: true);
    }
  }

  Future<void> _download(Uri uri, File destination) async {
    if (uri.scheme != 'https') {
      throw ArgumentError('Update URL must use HTTPS.');
    }
    final client = _httpClientFactory();
    IOSink? sink;
    try {
      final request = await client.getUrl(uri).timeout(_daemonRequestTimeout);
      request.headers.set(
        HttpHeaders.userAgentHeader,
        'Heimdallm desktop updater',
      );
      final response = await request.close().timeout(_daemonRequestTimeout);
      if (response.statusCode != HttpStatus.ok) {
        throw HttpException('HTTP ${response.statusCode}', uri: uri);
      }
      sink = destination.openWrite(mode: FileMode.writeOnly);
      var total = 0;
      await for (final chunk in response) {
        total += chunk.length;
        if (total > _maxAssetBytes) {
          throw const FormatException('Update asset exceeds the size limit.');
        }
        sink.add(chunk);
      }
      await sink.flush();
      await sink.close();
      sink = null;
      if (total == 0) {
        throw const FormatException('Downloaded update is empty.');
      }
    } finally {
      await sink?.close();
      client.close(force: true);
    }
  }

  @visibleForTesting
  static Future<void> verifyChecksum({
    required File asset,
    required String assetName,
    required String checksums,
  }) async {
    String? expected;
    for (final line in const LineSplitter().convert(checksums)) {
      final match = RegExp(
        r'^([0-9a-fA-F]{64})\s+\*?(.+)$',
      ).firstMatch(line.trim());
      if (match != null && match.group(2) == assetName) {
        if (expected != null) {
          throw const FormatException(
            'Checksum file contains duplicate entries.',
          );
        }
        expected = match.group(1)!.toLowerCase();
      }
    }
    if (expected == null) {
      throw FormatException('Checksum for $assetName is missing.');
    }
    final actual = (await sha256.bind(asset.openRead()).first).toString();
    if (actual != expected) {
      throw StateError('Checksum verification failed for $assetName.');
    }
  }

  @visibleForTesting
  static Future<void> verifyManifestSignature({
    required List<int> manifest,
    required String encodedSignature,
    List<int>? publicKey,
  }) async {
    late final List<int> signatureBytes;
    try {
      signatureBytes = base64Decode(encodedSignature.trim());
    } on FormatException {
      throw const FormatException(
        'Linux update signature is not valid base64.',
      );
    }
    final keyBytes = publicKey ?? base64Decode(_releasePublicKey);
    if (signatureBytes.length != 64 || keyBytes.length != 32) {
      throw const FormatException(
        'Linux update signature has an invalid size.',
      );
    }
    final algorithm = Ed25519();
    final valid = await algorithm.verify(
      manifest,
      signature: Signature(
        signatureBytes,
        publicKey: SimplePublicKey(keyBytes, type: KeyPairType.ed25519),
      ),
    );
    if (!valid) {
      throw StateError('Linux update signature verification failed.');
    }
  }

  Future<_DrainStatus> _drainDaemon(String leaseID) async {
    final deadline = _clock().add(_drainTimeout);
    int? originalPID;
    String? originalBootID;
    while (_clock().isBefore(deadline)) {
      final status = await _daemonRequest(
        method: 'POST',
        path: 'update/prepare',
        leaseID: leaseID,
      );
      _verifyLeaseStatus(status, leaseID);
      originalPID ??= status.pid;
      originalBootID ??= status.bootID;
      if (status.pid != originalPID || status.bootID != originalBootID) {
        throw StateError('The daemon identity changed while draining.');
      }
      if (status.activeTotal == 0 && status.state == 'ready') return status;
      await Future<void>.delayed(const Duration(seconds: 5));
    }
    throw TimeoutException(
      'Active daemon work did not drain within 10 minutes.',
    );
  }

  void _verifyLeaseStatus(_DrainStatus status, String leaseID) {
    if (status.leaseID != leaseID ||
        status.pid <= 0 ||
        status.bootID.isEmpty ||
        status.version.isEmpty ||
        status.activeTotal < 0) {
      throw StateError('The daemon returned invalid update lease state.');
    }
  }

  Future<void> _verifyDaemonIdentity(_DrainStatus status) async {
    final injected = _identityVerifier;
    if (injected != null) {
      await injected(status.pid, _daemonPath);
      return;
    }
    final procLink = Link('/proc/${status.pid}/exe');
    final actual = await procLink.resolveSymbolicLinks();
    final expected = await File(_daemonPath).resolveSymbolicLinks();
    if (actual != expected) {
      throw StateError(
        'PID ${status.pid} is not the bundled Heimdallm daemon.',
      );
    }
  }

  Future<void> _shutdownDaemon(String leaseID, int expectedPID) async {
    await _daemonRequest(method: 'POST', path: 'shutdown', leaseID: leaseID);
    final deadline = _clock().add(_daemonStopTimeout);
    while (_clock().isBefore(deadline)) {
      final alive = await processRunner('/bin/kill', ['-0', '$expectedPID']);
      if (alive.exitCode != 0) return;
      await Future<void>.delayed(const Duration(milliseconds: 100));
    }
    throw TimeoutException('Daemon PID $expectedPID did not stop.');
  }

  Future<_DrainStatus> _daemonRequest({
    required String method,
    required String path,
    required String leaseID,
    String? expectedBootID,
  }) async {
    final token = (await File(apiTokenPath).readAsString()).trim();
    if (token.isEmpty) throw StateError('Daemon API token is unavailable.');
    final uri = apiBaseURL.resolve(path);
    final client = _httpClientFactory();
    try {
      final request = await client
          .openUrl(method, uri)
          .timeout(_daemonRequestTimeout);
      request.headers.set('X-Heimdallm-Token', token);
      request.headers.set('X-Heimdallm-Update-Lease', leaseID);
      request.headers.set(HttpHeaders.acceptHeader, 'application/json');
      if (expectedBootID != null) {
        request.headers.set('X-Heimdallm-Expected-Boot-ID', expectedBootID);
      }
      final response = await request.close().timeout(_daemonRequestTimeout);
      final bytes = <int>[];
      await for (final chunk in response.timeout(_daemonRequestTimeout)) {
        bytes.addAll(chunk);
        if (bytes.length > _maxMetadataBytes) {
          throw const FormatException(
            'Daemon response exceeds the size limit.',
          );
        }
      }
      if (response.statusCode < 200 || response.statusCode >= 300) {
        throw HttpException(
          'Daemon update request failed with HTTP ${response.statusCode}: '
          '${utf8.decode(bytes, allowMalformed: true)}',
          uri: uri,
        );
      }
      if (path == 'shutdown') {
        return _DrainStatus(
          state: 'stopping',
          pid: 1,
          version: 'unknown',
          leaseID: leaseID,
          sealed: true,
          bootstrapAuthorized: false,
          bootID: 'unknown',
          activeTotal: 0,
        );
      }
      final decoded = jsonDecode(utf8.decode(bytes));
      if (decoded is! Map<String, dynamic>) {
        throw const FormatException('Daemon returned invalid update state.');
      }
      return _DrainStatus.fromJson(decoded);
    } finally {
      client.close(force: true);
    }
  }

  Future<String> _installAsset(File asset) async {
    switch (_installKind) {
      case LinuxInstallKind.appImage:
        return _replaceAppImage(asset);
      case LinuxInstallKind.deb:
        return _installPackage('/usr/bin/dpkg', ['--install', asset.path]);
      case LinuxInstallKind.rpm:
        return _installPackage('/usr/bin/rpm', [
          '--upgrade',
          '--replacepkgs',
          asset.path,
        ]);
      case LinuxInstallKind.unsupported:
        throw UnsupportedError('This Linux installation cannot update itself.');
    }
  }

  Future<String> _replaceAppImage(File asset) async {
    final targetPath = environment['APPIMAGE'];
    if (targetPath == null || targetPath.isEmpty) {
      throw StateError('APPIMAGE does not identify the running installation.');
    }
    final targetType = await FileSystemEntity.type(
      targetPath,
      followLinks: false,
    );
    if (targetType != FileSystemEntityType.file) {
      throw StateError('The running AppImage is not a regular file.');
    }
    final target = File(targetPath);
    final next = File('$targetPath.next.${pid.toString()}');
    final backup = File('$targetPath.previous');
    await asset.copy(next.path);
    await _chmod(next.path, '755');
    if (await backup.exists()) await backup.delete();
    await target.rename(backup.path);
    try {
      await next.rename(target.path);
    } catch (_) {
      if (await target.exists()) await target.delete();
      await backup.rename(target.path);
      rethrow;
    }
    return target.path;
  }

  Future<String> _installPackage(
    String installer,
    List<String> arguments,
  ) async {
    if (!await File('/usr/bin/pkexec').exists()) {
      throw StateError('PolicyKit (pkexec) is required for package updates.');
    }
    final result = await processRunner('/usr/bin/pkexec', [
      installer,
      ...arguments,
    ]);
    if (result.exitCode != 0) {
      final detail = '${result.stderr}'.trim();
      throw StateError(
        detail.isEmpty
            ? 'Package installer exited with ${result.exitCode}.'
            : detail,
      );
    }
    return '/opt/heimdallm/heimdallm';
  }

  Future<void> _recoverBarrier(
    _LinuxRecoveryJournal journal, {
    required String expectedDaemonVersion,
  }) async {
    var status = await _waitForDaemon(journal.leaseID);
    _verifyLeaseStatus(status, journal.leaseID);
    if (status.version != expectedDaemonVersion) {
      throw StateError(
        'Recovery daemon reports ${status.version}, expected $expectedDaemonVersion.',
      );
    }

    if (!status.sealed) {
      if (journal.phase == 'preparing' &&
          expectedDaemonVersion == journal.daemonVersion) {
        await _daemonRequest(
          method: 'DELETE',
          path: 'update/prepare',
          leaseID: journal.leaseID,
          expectedBootID: status.bootID,
        );
        return;
      }
      status = await _daemonRequest(
        method: 'POST',
        path: 'update/seal',
        leaseID: journal.leaseID,
      );
      if (!status.sealed || status.activeTotal != 0) {
        throw StateError('Could not restore the daemon update seal.');
      }
    }

    status = await _daemonRequest(
      method: 'POST',
      path: 'update/confirm',
      leaseID: journal.leaseID,
      expectedBootID: status.bootID,
    );
    if (!status.bootstrapAuthorized || status.bootID.isEmpty) {
      throw StateError('The replacement daemon did not authorize bootstrap.');
    }
    await _waitForHealthyVersion(expectedDaemonVersion);
    await _daemonRequest(
      method: 'DELETE',
      path: 'update/prepare',
      leaseID: journal.leaseID,
      expectedBootID: status.bootID,
    );
  }

  Future<_DrainStatus> _waitForDaemon(String leaseID) async {
    final deadline = _clock().add(_daemonStartTimeout);
    var started = false;
    Object? lastError;
    while (_clock().isBefore(deadline)) {
      try {
        return await _daemonRequest(
          method: 'POST',
          path: 'update/prepare',
          leaseID: leaseID,
        );
      } catch (error) {
        lastError = error;
      }
      if (!started) {
        if (await _portIsOpen()) {
          throw StateError(
            'Another process owns the daemon port during recovery.',
          );
        }
        await daemonStarter(_daemonPath);
        started = true;
      }
      await Future<void>.delayed(const Duration(milliseconds: 200));
    }
    throw TimeoutException('Replacement daemon did not start: $lastError');
  }

  Future<bool> _portIsOpen() async {
    Socket? socket;
    try {
      socket = await Socket.connect(
        apiBaseURL.host,
        apiBaseURL.port,
        timeout: const Duration(milliseconds: 500),
      );
      return true;
    } on SocketException {
      return false;
    } finally {
      socket?.destroy();
    }
  }

  Future<void> _waitForHealthyVersion(String expectedVersion) async {
    final deadline = _clock().add(_daemonStartTimeout);
    while (_clock().isBefore(deadline)) {
      final client = _httpClientFactory();
      try {
        final request = await client.getUrl(apiBaseURL.resolve('health'));
        final response = await request.close().timeout(_daemonRequestTimeout);
        final body = await response.transform(utf8.decoder).join();
        if (response.statusCode == HttpStatus.ok) {
          final decoded = jsonDecode(body);
          if (decoded is Map<String, dynamic> &&
              decoded['version'] == expectedVersion) {
            return;
          }
        }
      } catch (_) {
        // The replacement may still be opening its dependencies.
      } finally {
        client.close(force: true);
      }
      await Future<void>.delayed(const Duration(milliseconds: 200));
    }
    throw TimeoutException(
      'Replacement daemon did not become healthy as version $expectedVersion.',
    );
  }

  Future<void> _writeJournal(_LinuxRecoveryJournal journal) =>
      _writePrivateJSON(_journalPath, journal.toJson());

  Future<_LinuxRecoveryJournal?> _readJournal() async {
    final decoded = await _readPrivateJSON(_journalPath);
    return decoded == null ? null : _LinuxRecoveryJournal.fromJson(decoded);
  }

  Future<void> _clearJournal() async {
    final type = await FileSystemEntity.type(_journalPath, followLinks: false);
    if (type == FileSystemEntityType.notFound) return;
    if (type != FileSystemEntityType.file) {
      throw StateError('Update recovery journal is not a regular file.');
    }
    await File(_journalPath).delete();
  }

  Future<(DateTime, LinuxUpdateRelease?)?> _readCache() async {
    final decoded = await _readPrivateJSON(_cachePath);
    if (decoded == null || decoded['schemaVersion'] != 1) return null;
    final checkedAt = DateTime.tryParse(decoded['checkedAt']?.toString() ?? '');
    if (checkedAt == null) return null;
    final rawRelease = decoded['release'];
    if (rawRelease == null) return (checkedAt.toUtc(), null);
    if (rawRelease is! Map<String, dynamic>) return null;
    final version = rawRelease['version']?.toString();
    final assetName = rawRelease['assetName']?.toString();
    final assetURL = Uri.tryParse(rawRelease['assetURL']?.toString() ?? '');
    final checksumsURL = Uri.tryParse(
      rawRelease['checksumsURL']?.toString() ?? '',
    );
    final checksumsSignatureURL = Uri.tryParse(
      rawRelease['checksumsSignatureURL']?.toString() ?? '',
    );
    if (version == null ||
        assetName == null ||
        assetURL == null ||
        checksumsURL == null ||
        checksumsSignatureURL == null ||
        !_isReleaseVersion(version) ||
        !_isTrustedReleaseURL(assetURL, version) ||
        !_isTrustedReleaseURL(checksumsURL, version)) {
      return null;
    }
    if (!_isTrustedReleaseURL(checksumsSignatureURL, version)) {
      return null;
    }
    return (
      checkedAt.toUtc(),
      LinuxUpdateRelease(
        version: version,
        assetName: assetName,
        assetURL: assetURL,
        checksumsURL: checksumsURL,
        checksumsSignatureURL: checksumsSignatureURL,
      ),
    );
  }

  Future<void> _writeCache(DateTime checkedAt, LinuxUpdateRelease? release) =>
      _writePrivateJSON(_cachePath, {
        'schemaVersion': 1,
        'checkedAt': checkedAt.toIso8601String(),
        'release': release?.toJson(),
      });

  Future<Map<String, dynamic>?> _readPrivateJSON(String path) async {
    final type = await FileSystemEntity.type(path, followLinks: false);
    if (type == FileSystemEntityType.notFound) return null;
    if (type != FileSystemEntityType.file) {
      throw StateError('$path is not a regular file.');
    }
    final file = File(path);
    final stat = await file.stat();
    if (stat.size > _maxMetadataBytes || stat.mode & 0x3F != 0) {
      throw StateError('$path is not a private update file.');
    }
    final decoded = jsonDecode(await file.readAsString());
    if (decoded is! Map<String, dynamic>) {
      throw FormatException('$path does not contain a JSON object.');
    }
    return decoded;
  }

  Future<void> _writePrivateJSON(
    String path,
    Map<String, Object?> value,
  ) async {
    final directory = Directory(File(path).parent.path);
    await directory.create(recursive: true);
    await _chmod(directory.path, '700');
    final existing = await FileSystemEntity.type(path, followLinks: false);
    if (existing != FileSystemEntityType.notFound &&
        existing != FileSystemEntityType.file) {
      throw StateError('$path is not a regular file.');
    }
    final temporary = File('$path.${_newUUID()}.tmp');
    await temporary.writeAsString(
      jsonEncode(value),
      mode: FileMode.writeOnly,
      flush: true,
    );
    await _chmod(temporary.path, '600');
    await temporary.rename(path);
  }

  Future<void> _chmod(String path, String mode) async {
    final result = await processRunner('/bin/chmod', [mode, path]);
    if (result.exitCode != 0) throw StateError('Could not chmod $path.');
  }

  @visibleForTesting
  static int compareVersions(String left, String right) {
    List<int> parts(String value) {
      final match = RegExp(r'^v?(\d+)\.(\d+)\.(\d+)$').firstMatch(value.trim());
      if (match == null) {
        throw FormatException('Invalid release version: $value');
      }
      return [
        for (var index = 1; index <= 3; index++) int.parse(match.group(index)!),
      ];
    }

    final a = parts(left);
    final b = parts(right);
    for (var index = 0; index < 3; index++) {
      final comparison = a[index].compareTo(b[index]);
      if (comparison != 0) return comparison;
    }
    return 0;
  }

  static bool _isReleaseVersion(String value) =>
      RegExp(r'^\d+\.\d+\.\d+$').hasMatch(value.trim());

  static bool _isLoopbackAPI(Uri uri) =>
      uri.scheme == 'http' &&
      const {'127.0.0.1', 'localhost', '::1'}.contains(uri.host);

  static bool _isTrustedReleaseURL(Uri uri, String version) =>
      uri.scheme == 'https' &&
      uri.host == 'github.com' &&
      uri.path.startsWith('/$_repository/releases/download/v$version/');
}

String _newUUID() {
  final bytes = List<int>.generate(16, (_) => Random.secure().nextInt(256));
  bytes[6] = (bytes[6] & 0x0f) | 0x40;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;
  String hex(int value) => value.toRadixString(16).padLeft(2, '0');
  final raw = bytes.map(hex).join();
  return '${raw.substring(0, 8)}-${raw.substring(8, 12)}-'
      '${raw.substring(12, 16)}-${raw.substring(16, 20)}-${raw.substring(20)}';
}

bool _validUUID(String value) => RegExp(
  r'^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-'
  r'[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$',
).hasMatch(value);

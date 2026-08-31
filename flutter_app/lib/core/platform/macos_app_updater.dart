import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter/foundation.dart' show debugPrint, visibleForTesting;
import 'package:http/http.dart' as http;

import 'platform_services.dart';

typedef MacOSUpdateProcessRunner =
    Future<ProcessResult> Function(String executable, List<String> arguments);
typedef MacOSInstallerLauncher = Future<void> Function(MacOSInstallPlan plan);
typedef MacOSVersionLoader = Future<AppVersionInfo> Function();
typedef MacOSTempDirectoryFactory = Future<Directory> Function();

class MacOSRelease {
  const MacOSRelease({required this.version, required this.downloadURL});

  final String version;
  final Uri downloadURL;
}

class MacOSInstallPlan {
  const MacOSInstallPlan({
    required this.appPID,
    required this.daemonPID,
    required this.stagedBundlePath,
    required this.targetBundlePath,
    required this.backupBundlePath,
    required this.launchAgentPath,
  });

  final int appPID;
  final int? daemonPID;
  final String stagedBundlePath;
  final String targetBundlePath;
  final String backupBundlePath;
  final String launchAgentPath;
}

/// Deliberately small macOS updater:
///
/// 1. Poll GitHub's latest release.
/// 2. Download its DMG.
/// 3. Copy the bundled app next to the current installation.
/// 4. Exit, swap the app bundles, and relaunch.
///
/// There is no appcast, update signature, daemon drain, lease, or recovery
/// journal. If staging fails, the running installation is left untouched.
class MacOSAppUpdater {
  MacOSAppUpdater({
    required this.executablePath,
    required this.dataDirectory,
    required this.versionLoader,
    required this.processRunner,
    required this.onStatus,
    http.Client? client,
    MacOSInstallerLauncher? installerLauncher,
    MacOSTempDirectoryFactory? tempDirectoryFactory,
    this.pollInterval = const Duration(days: 1),
    this.startPolling = true,
  }) : _client = client ?? http.Client(),
       _installerLauncher = installerLauncher ?? launchMacOSInstaller,
       _tempDirectoryFactory =
           tempDirectoryFactory ??
           (() => Directory.systemTemp.createTemp('heimdallm-update-'));

  static final Uri latestReleaseURL = Uri.parse(
    'https://api.github.com/repos/theburrowhub/heimdallm/releases/latest',
  );

  final String executablePath;
  final String dataDirectory;
  final MacOSVersionLoader versionLoader;
  final MacOSUpdateProcessRunner processRunner;
  final void Function(AppUpdateStatus status) onStatus;
  final Duration pollInterval;
  final bool startPolling;
  final http.Client _client;
  final MacOSInstallerLauncher _installerLauncher;
  final MacOSTempDirectoryFactory _tempDirectoryFactory;

  String? _currentVersion;
  String? _bundlePath;
  MacOSRelease? _availableRelease;
  Timer? _pollTimer;
  bool _checking = false;
  bool _installing = false;

  @visibleForTesting
  MacOSRelease? get availableRelease => _availableRelease;

  Future<bool> initialize() async {
    final bundlePath = _appBundlePath(executablePath);
    if (bundlePath == null || !await Directory(bundlePath).exists()) {
      return false;
    }
    _bundlePath = bundlePath;
    _currentVersion = (await versionLoader()).version;
    if (startPolling) {
      unawaited(checkForUpdates(silent: true));
      _pollTimer = Timer.periodic(
        pollInterval,
        (_) => unawaited(checkForUpdates(silent: true)),
      );
    }
    return true;
  }

  Future<void> checkForUpdates({bool silent = false}) async {
    if (_checking) return;
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
      final release = await _fetchLatestRelease();
      if (_isNewer(release.version, _currentVersion!)) {
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
        if (!silent) {
          onStatus(
            AppUpdateStatus.idle(
              message: 'Heimdallm $_currentVersion is up to date.',
            ),
          );
        }
      }
    } catch (error) {
      if (silent) {
        debugPrint('automatic update check failed: $error');
      } else {
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

  Future<void> installAvailableUpdate() async {
    if (_installing) return;
    final release = _availableRelease;
    final targetBundle = _bundlePath;
    if (release == null || targetBundle == null) {
      throw StateError('No macOS update is ready to install.');
    }

    _installing = true;
    Directory? workspace;
    String? mountedAt;
    final stagedBundle = '$targetBundle.update-$pid';
    final backupBundle = '$targetBundle.previous-$pid';
    try {
      onStatus(
        AppUpdateStatus(
          phase: AppUpdatePhase.installing,
          version: release.version,
          message: 'Downloading Heimdallm ${release.version}…',
        ),
      );
      workspace = await _tempDirectoryFactory();
      final dmg = File('${workspace.path}/Heimdallm-${release.version}.dmg');
      await _download(release.downloadURL, dmg);

      final mount = Directory('${workspace.path}/mounted');
      await mount.create();
      mountedAt = mount.path;
      await _runChecked('/usr/bin/hdiutil', [
        'attach',
        '-nobrowse',
        '-readonly',
        '-mountpoint',
        mount.path,
        dmg.path,
      ]);

      final sourceBundle = '${mount.path}/Heimdallm.app';
      if (!await Directory(sourceBundle).exists()) {
        throw StateError('The downloaded DMG does not contain Heimdallm.app.');
      }
      final staged = Directory(stagedBundle);
      if (await staged.exists()) await staged.delete(recursive: true);
      await _runChecked('/usr/bin/ditto', [sourceBundle, stagedBundle]);

      await _runChecked('/usr/bin/hdiutil', ['detach', mount.path]);
      mountedAt = null;
      await workspace.delete(recursive: true);
      workspace = null;

      onStatus(
        AppUpdateStatus(
          phase: AppUpdatePhase.restarting,
          version: release.version,
          message: 'Installing Heimdallm ${release.version}…',
        ),
      );
      await _installerLauncher(
        MacOSInstallPlan(
          appPID: pid,
          daemonPID: await _readDaemonPID(),
          stagedBundlePath: stagedBundle,
          targetBundlePath: targetBundle,
          backupBundlePath: backupBundle,
          launchAgentPath:
              '${Platform.environment['HOME'] ?? ''}/Library/LaunchAgents/'
              'com.heimdallm.daemon.plist',
        ),
      );
    } catch (error) {
      if (mountedAt != null) {
        try {
          await processRunner('/usr/bin/hdiutil', ['detach', mountedAt]);
        } catch (_) {
          // Preserve the original installation failure.
        }
      }
      try {
        if (workspace != null && await workspace.exists()) {
          await workspace.delete(recursive: true);
        }
      } catch (_) {
        // Preserve the original installation failure.
      }
      try {
        final staged = Directory(stagedBundle);
        if (await staged.exists()) await staged.delete(recursive: true);
      } catch (_) {
        // Preserve the original installation failure.
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
    }
  }

  Future<MacOSRelease> _fetchLatestRelease() async {
    final request = http.Request('GET', latestReleaseURL)
      ..headers['Accept'] = 'application/vnd.github+json'
      ..headers['User-Agent'] = 'Heimdallm-updater';
    final response = await _client.send(request);
    final body = await response.stream.bytesToString();
    if (response.statusCode != HttpStatus.ok) {
      throw HttpException(
        'GitHub returned HTTP ${response.statusCode}',
        uri: latestReleaseURL,
      );
    }
    final decoded = jsonDecode(body);
    if (decoded is! Map<String, dynamic>) {
      throw const FormatException('GitHub returned an invalid release.');
    }
    final tag = decoded['tag_name']?.toString();
    final assets = decoded['assets'];
    if (tag == null || assets is! List) {
      throw const FormatException('GitHub release metadata is incomplete.');
    }
    final version = tag.startsWith('v') ? tag.substring(1) : tag;
    final expectedAsset = 'Heimdallm-v$version.dmg';
    for (final asset in assets) {
      if (asset is! Map<String, dynamic> || asset['name'] != expectedAsset) {
        continue;
      }
      final url = Uri.tryParse(asset['browser_download_url']?.toString() ?? '');
      if (url != null && url.scheme == 'https') {
        return MacOSRelease(version: version, downloadURL: url);
      }
    }
    throw FormatException('Release $tag has no $expectedAsset asset.');
  }

  Future<void> _download(Uri url, File destination) async {
    final request = http.Request('GET', url)
      ..headers['User-Agent'] = 'Heimdallm-updater';
    final response = await _client.send(request);
    if (response.statusCode != HttpStatus.ok) {
      await response.stream.drain<void>();
      throw HttpException(
        'Download returned HTTP ${response.statusCode}',
        uri: url,
      );
    }
    await response.stream.pipe(destination.openWrite());
  }

  Future<void> _runChecked(String executable, List<String> arguments) async {
    final result = await processRunner(executable, arguments);
    if (result.exitCode == 0) return;
    final stderr = '${result.stderr}'.trim();
    throw ProcessException(
      executable,
      arguments,
      stderr.isEmpty ? 'exit status ${result.exitCode}' : stderr,
      result.exitCode,
    );
  }

  Future<int?> _readDaemonPID() async {
    try {
      final value = int.tryParse(
        (await File('$dataDirectory/daemon.lock').readAsString()).trim(),
      );
      return value != null && value > 0 ? value : null;
    } on FileSystemException {
      return null;
    }
  }

  static String? _appBundlePath(String executablePath) {
    final macOS = File(executablePath).parent;
    final contents = macOS.parent;
    final bundle = contents.parent;
    if (!macOS.path.endsWith('/MacOS') ||
        !contents.path.endsWith('/Contents') ||
        !bundle.path.endsWith('.app')) {
      return null;
    }
    return bundle.path;
  }

  static bool _isNewer(String candidate, String current) {
    List<int> parts(String value) => value
        .split('-')
        .first
        .split('.')
        .map((part) => int.tryParse(part) ?? 0)
        .toList();
    final left = parts(candidate);
    final right = parts(current);
    final length = left.length > right.length ? left.length : right.length;
    for (var index = 0; index < length; index++) {
      final a = index < left.length ? left[index] : 0;
      final b = index < right.length ? right[index] : 0;
      if (a != b) return a > b;
    }
    return false;
  }

  void dispose() {
    _pollTimer?.cancel();
    _client.close();
  }
}

@visibleForTesting
Future<void> launchMacOSInstaller(MacOSInstallPlan plan) async {
  const script = r'''
set -u
app_pid="$1"
daemon_pid="$2"
staged="$3"
target="$4"
backup="$5"
agent="$6"

case "$target" in /*.app) ;; *) exit 2 ;; esac
case "$staged" in "$target".update-*) ;; *) exit 2 ;; esac
case "$backup" in "$target".previous-*) ;; *) exit 2 ;; esac

uid="$(/usr/bin/id -u)"
domain="gui/$uid"
/bin/launchctl bootout "$domain/com.heimdallm.daemon" >/dev/null 2>&1 || true

case "$daemon_pid" in
  ''|*[!0-9]*) ;;
  *) /bin/kill -TERM "$daemon_pid" >/dev/null 2>&1 || true ;;
esac

while /bin/kill -0 "$app_pid" >/dev/null 2>&1; do /bin/sleep 0.1; done

if [ -n "$daemon_pid" ]; then
  tries=0
  while /bin/kill -0 "$daemon_pid" >/dev/null 2>&1 && [ "$tries" -lt 50 ]; do
    /bin/sleep 0.1
    tries=$((tries + 1))
  done
  /bin/kill -KILL "$daemon_pid" >/dev/null 2>&1 || true
fi

[ ! -e "$backup" ] || exit 1
if ! /bin/mv "$target" "$backup"; then exit 1; fi
if ! /bin/mv "$staged" "$target"; then
  /bin/mv "$backup" "$target" || true
  exit 1
fi

if [ -f "$agent" ]; then
  /bin/launchctl bootstrap "$domain" "$agent" >/dev/null 2>&1 || true
  /bin/launchctl kickstart -k "$domain/com.heimdallm.daemon" >/dev/null 2>&1 || true
fi
/usr/bin/open "$target"
/bin/rm -rf "$backup"
''';

  await Process.start('/bin/sh', [
    '-c',
    script,
    'heimdallm-update',
    '${plan.appPID}',
    plan.daemonPID?.toString() ?? '',
    plan.stagedBundlePath,
    plan.targetBundlePath,
    plan.backupBundlePath,
    plan.launchAgentPath,
  ], mode: ProcessStartMode.detached);
}

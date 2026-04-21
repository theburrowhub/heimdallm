import 'dart:io';
import 'dart:ui' show VoidCallback;
import 'package:flutter/foundation.dart' show debugPrint;
import 'package:flutter/painting.dart' show Size;
import '../api/api_client.dart';
import '../models/config_model.dart';
import 'platform_services.dart';

/// Desktop implementation of [PlatformServices].
///
/// Wraps dart:io, tray_manager, window_manager, local_notifier, and the
/// existing FirstRunSetup / DaemonLifecycle helpers so that shared code
/// never has to import them directly.
class DesktopPlatformServices implements PlatformServices {
  DesktopPlatformServices({
    int apiPort = 7842,
    String? tokenPath,
    String? pidFilePath,
  })  : _apiPort = apiPort,
        _tokenPath = tokenPath,
        _pidFilePath = pidFilePath;

  final int _apiPort;
  final String? _tokenPath;
  final String? _pidFilePath;
  String? _cachedToken;

  String get _resolvedTokenPath =>
      _tokenPath ??
      '${Platform.environment['HOME'] ?? ''}/.local/share/heimdallm/api_token';

  String get _resolvedPidFilePath =>
      _pidFilePath ??
      '${Platform.environment['HOME'] ?? ''}/.local/share/heimdallm/ui.pid';

  @override
  String get apiBaseUrl => 'http://127.0.0.1:$_apiPort';

  // ── The rest is stubbed to throw for now — later tasks fill them in. ─────

  @override
  Future<String?> loadApiToken() async {
    if (_cachedToken != null) return _cachedToken;
    final file = File(_resolvedTokenPath);
    if (await file.exists()) {
      _cachedToken = (await file.readAsString()).trim();
    }
    return _cachedToken;
  }

  @override
  void clearApiTokenCache() {
    _cachedToken = null;
  }

  @override
  String? readEnv(String name) => Platform.environment[name];
  @override
  Future<bool> ensureSingleInstance() async {
    final pidFile = File(_resolvedPidFilePath);
    await pidFile.parent.create(recursive: true);

    if (await pidFile.exists()) {
      final existing = int.tryParse((await pidFile.readAsString()).trim());
      if (existing != null && existing != pid) {
        final check = await Process.run('kill', ['-0', '$existing']);
        if (check.exitCode == 0) {
          debugPrint('Another Heimdallm instance is running (PID $existing), signalling it.');
          await Process.run('kill', ['-USR1', '$existing']);
          return false;
        }
      }
    }

    await pidFile.writeAsString('$pid');
    return true;
  }

  @override
  void listenForActivationSignal(VoidCallback onActivate) {
    ProcessSignal.sigusr1.watch().listen((_) => onActivate());
  }
  @override
  Future<void> setupWindow({
    required String title,
    required Size size,
    required Size minimumSize,
  }) => throw UnimplementedError();
  @override
  Future<void> setupTray({required ApiClient apiClient}) => throw UnimplementedError();
  @override
  void setTrayNavigationHandler(void Function(String location) handler) => throw UnimplementedError();
  @override
  Future<void> setupNotifier({required String appName}) => throw UnimplementedError();
  @override
  void showNotification({
    required String title,
    required String body,
    VoidCallback? onClick,
  }) => throw UnimplementedError();
  @override
  Future<void> setPreventWindowClose(bool enable) => throw UnimplementedError();
  @override
  Future<void> showAndFocusWindow() => throw UnimplementedError();
  @override
  Future<void> hideWindow() => throw UnimplementedError();
  @override
  Never quitApp() => exit(0);
  @override
  Future<String?> detectGitHubToken() => throw UnimplementedError();
  @override
  Future<String?> getStoredGitHubToken() => throw UnimplementedError();
  @override
  Future<void> storeGitHubToken(String token) => throw UnimplementedError();
  @override
  Future<void> writeDaemonConfig(AppConfig config) => throw UnimplementedError();
  @override
  Future<bool> daemonConfigExists() => throw UnimplementedError();
  @override
  String? defaultDaemonBinaryPath() => throw UnimplementedError();
  @override
  Future<void> spawnDaemon(String binaryPath) => throw UnimplementedError();
}

/// Alias used by the conditional export in `platform_services.dart`.
typedef PlatformServicesImpl = DesktopPlatformServices;

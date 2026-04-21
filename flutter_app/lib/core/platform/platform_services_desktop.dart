import 'dart:io';
import 'dart:ui' show VoidCallback;
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
  })  : _apiPort = apiPort,
        _tokenPath = tokenPath;

  final int _apiPort;
  // ignore: unused_field
  final String? _tokenPath;

  @override
  String get apiBaseUrl => 'http://127.0.0.1:$_apiPort';

  // ── The rest is stubbed to throw for now — later tasks fill them in. ─────

  @override
  Future<String?> loadApiToken() => throw UnimplementedError();
  @override
  void clearApiTokenCache() => throw UnimplementedError();
  @override
  String? readEnv(String name) => throw UnimplementedError();
  @override
  Future<bool> ensureSingleInstance() => throw UnimplementedError();
  @override
  void listenForActivationSignal(VoidCallback onActivate) => throw UnimplementedError();
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

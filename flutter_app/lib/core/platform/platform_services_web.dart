import 'dart:ui' show VoidCallback;
import 'package:flutter/painting.dart' show Size;
import '../api/api_client.dart';
import '../models/config_model.dart';
import '../models/pr.dart';
import '../setup/repo_discovery.dart';
import 'platform_services.dart';

/// Web implementation of [PlatformServices]. Everything that would touch
/// the OS on desktop is a no-op here because the daemon is a neighboring
/// container proxied by Nginx — no process to spawn, no tray to manage,
/// no signals to send.
class WebPlatformServices implements PlatformServices {
  @override
  String get apiBaseUrl => '/api';

  @override
  Future<String?> loadApiToken() async => null;

  @override
  void clearApiTokenCache() {}

  @override
  String? readEnv(String name) => null;

  @override
  Future<bool> ensureSingleInstance() async => true;

  @override
  void listenForActivationSignal(VoidCallback onActivate) {}

  @override
  Future<void> setupWindow({
    required String title,
    required Size size,
    required Size minimumSize,
  }) async {}

  @override
  Future<void> setupTray({required ApiClient apiClient}) async {}

  @override
  void setTrayNavigationHandler(void Function(String location) handler) {}

  @override
  Future<void> setupNotifier({required String appName}) async {}

  @override
  void showNotification({
    required String title,
    required String body,
    VoidCallback? onClick,
  }) {}

  @override
  Future<void> setPreventWindowClose(bool enable) async {}

  @override
  Future<void> showAndFocusWindow() async {}

  @override
  Future<void> hideWindow() async {}

  @override
  Never quitApp() {
    // Cannot actually exit a browser tab from JS without a user gesture.
    // Throwing gives tests something to catch and desktop callers never
    // reach this branch.
    throw UnsupportedError('quitApp is not supported on web');
  }

  @override
  Future<String?> detectGitHubToken() async => null;

  @override
  Future<String?> getStoredGitHubToken() async => null;

  @override
  Future<void> storeGitHubToken(String token) async {}

  @override
  Future<void> writeDaemonConfig(AppConfig config) async {}

  @override
  Future<bool> daemonConfigExists() async => true;

  @override
  String? defaultDaemonBinaryPath() => null;

  @override
  Future<void> spawnDaemon(String binaryPath) async {
    throw UnsupportedError('spawnDaemon is not supported on web');
  }

  @override
  Future<void> rebuildTrayMenu({required List<PR> prs, required String me}) async {}

  @override
  Future<List<String>> discoverReposFromPRs(String token) =>
      RepoDiscovery.viaApi(token);
}

/// Alias used by the conditional export in `platform_services.dart`.
typedef PlatformServicesImpl = WebPlatformServices;

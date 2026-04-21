import 'dart:ui' show VoidCallback;
import 'package:flutter/painting.dart' show Size;
import '../api/api_client.dart';
import '../models/config_model.dart';

import 'platform_services_stub.dart'
    if (dart.library.io) 'platform_services_desktop.dart'
    if (dart.library.html) 'platform_services_web.dart';

export 'platform_services_stub.dart'
    if (dart.library.io) 'platform_services_desktop.dart'
    if (dart.library.html) 'platform_services_web.dart';

/// Platform-specific capabilities the rest of the app is free to call from
/// shared (i.e. non-`dart:io`) code. The desktop impl wraps dart:io and the
/// heavy packages (tray_manager, window_manager, local_notifier); the web
/// impl is a bundle of no-ops plus the `/api` base URL.
///
/// Use the factory:
///
///     final platform = PlatformServices.create();
///
/// …or, inside the widget tree, the Riverpod provider in
/// `platform_services_provider.dart`.
abstract class PlatformServices {
  /// Selects the right implementation for the current build.
  ///
  /// Declared here so shared code has a single entry point. The actual
  /// factory body lives in each conditional-import target: both
  /// `platform_services_desktop.dart` and `platform_services_web.dart`
  /// define a top-level `PlatformServicesImpl` class and the stub file
  /// throws if it is ever executed.
  static PlatformServices create() => PlatformServicesImpl();

  // ── URL / auth ──────────────────────────────────────────────────────────

  /// Prefix for HTTP + SSE requests. Callers append daemon-relative paths
  /// (e.g. `/prs`, `/events`). On desktop this is the absolute daemon URL;
  /// on web it is a relative prefix resolved against the browser origin.
  String get apiBaseUrl;

  /// Returns the daemon API token or null. On web this is always null
  /// because Nginx injects `X-Heimdallm-Token` server-side.
  Future<String?> loadApiToken();

  /// Forces the next `loadApiToken()` call to re-read from disk.
  /// On web, a no-op.
  void clearApiTokenCache();

  /// Environment-variable lookup. Returns null on web.
  String? readEnv(String name);

  // ── Single-instance + signals ───────────────────────────────────────────

  /// Returns true if this is the only running instance.
  /// Returns false if another instance was found (and signalled);
  /// the caller should then exit the process. Always true on web.
  Future<bool> ensureSingleInstance();

  /// Registers a listener that fires when another instance attempts to
  /// start (on desktop: SIGUSR1). No-op on web.
  void listenForActivationSignal(VoidCallback onActivate);

  // ── Window / tray / notifier ────────────────────────────────────────────

  Future<void> setupWindow({
    required String title,
    required Size size,
    required Size minimumSize,
  });

  /// Sets up system tray and wires the menu. No-op on web.
  /// Takes the shared `ApiClient` so tray-triggered review actions use
  /// the same token cache as the main app.
  Future<void> setupTray({required ApiClient apiClient});

  /// Called by main.dart once the router is ready so the tray menu can
  /// navigate to `/prs/:id` on click. No-op on web.
  void setTrayNavigationHandler(void Function(String location) handler);

  /// Initializes the notifier (local_notifier on desktop). No-op on web.
  Future<void> setupNotifier({required String appName});

  /// Fires a notification. On desktop: `LocalNotification`. On web: no-op.
  /// `onClick` is invoked when the user clicks the notification.
  void showNotification({
    required String title,
    required String body,
    VoidCallback? onClick,
  });

  /// Prevent the OS from closing the window (we intercept to hide to tray).
  /// No-op on web.
  Future<void> setPreventWindowClose(bool enable);

  /// Show + focus the main window. No-op on web.
  Future<void> showAndFocusWindow();

  /// Hide the main window (tray workflows). No-op on web.
  Future<void> hideWindow();

  /// Process-level quit. On desktop: `exit(0)`. On web: no-op.
  Never quitApp();

  // ── First-run setup / daemon spawn ──────────────────────────────────────

  Future<String?> detectGitHubToken();
  Future<String?> getStoredGitHubToken();
  Future<void> storeGitHubToken(String token);
  Future<void> writeDaemonConfig(AppConfig config);
  Future<bool> daemonConfigExists();
  String? defaultDaemonBinaryPath();

  /// Launches the daemon binary (detached). Throws `UnsupportedError` on web.
  Future<void> spawnDaemon(String binaryPath);
}

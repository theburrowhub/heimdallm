import 'dart:ui' show VoidCallback;
import 'package:flutter/painting.dart' show Size;
import '../api/api_client.dart';
import '../models/config_model.dart';
import '../models/pr.dart';

import 'platform_services_stub.dart'
    if (dart.library.io) 'platform_services_desktop.dart'
    if (dart.library.html) 'platform_services_web.dart';

export 'platform_services_stub.dart'
    if (dart.library.io) 'platform_services_desktop.dart'
    if (dart.library.html) 'platform_services_web.dart';

/// Result of probing the daemon's TCP port without making assumptions about
/// the HTTP client's platform-specific exception wrappers.
///
/// Only [closed] is permission to spawn a daemon. Both [open] and [unknown]
/// mean another process may own the port, so starting a second process would
/// be unsafe.
enum TcpPortState { open, closed, unknown }

/// Native update capability of the current application build.
enum AppUpdateSupport {
  /// Sparkle verifies and atomically replaces the complete macOS app bundle.
  native,

  /// Updates belong to the package/deployment owner on this platform.
  unavailable,
}

/// Raised by [PlatformServices.spawnDaemon] when another process owns the
/// daemon port. The guarded spawn operation is the final authority: an HTTP
/// request can fail while a silent or still-starting process already owns the
/// socket.
class DaemonPortOccupiedException implements Exception {
  const DaemonPortOccupiedException(this.port);

  final int port;

  @override
  String toString() => 'Port $port is already occupied; no daemon was started.';
}

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
  /// Returns false if another instance owns the lifetime lock (and was asked
  /// to activate over local IPC);
  /// the caller should then exit the process. Always true on web.
  Future<bool> ensureSingleInstance();

  /// Registers a listener that fires when another instance attempts to start.
  /// No-op on web.
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

  /// Requests normal application termination. macOS must go through AppKit so
  /// Sparkle and Flutter can postpone termination while the daemon drains.
  void quitApp();

  // ── Application updates ────────────────────────────────────────────────

  // ── First-run setup / daemon spawn ──────────────────────────────────────

  Future<String?> detectGitHubToken();
  Future<String?> getStoredGitHubToken();
  Future<void> storeGitHubToken(String token);
  Future<void> writeDaemonConfig(AppConfig config);
  Future<bool> daemonConfigExists();
  String? defaultDaemonBinaryPath();

  /// Starts the daemon only after proving its TCP port is free. Desktop
  /// implementations may delegate to the native service supervisor instead of
  /// launching a detached process. Throws [DaemonPortOccupiedException] when
  /// the port is owned and `UnsupportedError` on web.
  Future<void> spawnDaemon(String binaryPath);

  /// Rebuilds the system tray context menu with current PR data.
  /// No-op on web. Takes [me] (the user's login) so the desktop impl
  /// can distinguish reviewer / author for urgency counts.
  Future<void> rebuildTrayMenu({required List<PR> prs, required String me});

  /// Returns user's repos, with gh CLI preferred on desktop and HTTP API
  /// fallback. Safe to call from shared code; on web it's HTTP-only.
  Future<List<String>> discoverReposFromPRs(String token);
}

/// Optional application-update boundary implemented only by platforms that
/// own an in-process updater. Package- and deployment-managed platforms do not
/// have to pretend they can update themselves merely to satisfy the main
/// platform interface.
abstract interface class AppUpdatePlatformCapability {
  AppUpdateSupport get appUpdateSupport;

  Future<void> setupAppUpdater();
  Future<void> checkForAppUpdates();
  Future<String?> pendingAppUpdateVersion();
  Future<void> completeAppUpdate();
}

/// Optional termination path for a process that has already disproved desktop
/// singleton ownership. It is separate from normal application termination so
/// an update-owning process can still drain safely through
/// [PlatformServices.quitApp].
abstract interface class DuplicateInstancePlatformCapability {
  void quitDuplicateInstance();
}

/// Safe defaults for optional native capabilities.
///
/// Keeping these defaults in VM-loadable shared code means the browser adapter
/// stays independent of the macOS updater and remains covered by its existing
/// browser-only contract.
extension OptionalPlatformCapabilities on PlatformServices {
  AppUpdateSupport get appUpdateSupport {
    final platform = this;
    return platform is AppUpdatePlatformCapability
        ? (platform as AppUpdatePlatformCapability).appUpdateSupport
        : AppUpdateSupport.unavailable;
  }

  Future<void> setupAppUpdater() async {
    final platform = this;
    if (platform is AppUpdatePlatformCapability) {
      await (platform as AppUpdatePlatformCapability).setupAppUpdater();
    }
  }

  Future<void> checkForAppUpdates() async {
    final platform = this;
    if (platform is! AppUpdatePlatformCapability) {
      throw UnsupportedError(
        'Application updates are managed outside this Heimdallm process',
      );
    }
    await (platform as AppUpdatePlatformCapability).checkForAppUpdates();
  }

  Future<String?> pendingAppUpdateVersion() async {
    final platform = this;
    return platform is AppUpdatePlatformCapability
        ? (platform as AppUpdatePlatformCapability).pendingAppUpdateVersion()
        : null;
  }

  Future<void> completeAppUpdate() async {
    final platform = this;
    if (platform is AppUpdatePlatformCapability) {
      await (platform as AppUpdatePlatformCapability).completeAppUpdate();
    }
  }

  void quitDuplicateInstance() {
    final platform = this;
    if (platform is DuplicateInstancePlatformCapability) {
      (platform as DuplicateInstancePlatformCapability).quitDuplicateInstance();
    }
  }
}

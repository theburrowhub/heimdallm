import 'dart:async';
import 'dart:js_interop';
import 'dart:ui' show VoidCallback;

import 'package:flutter/painting.dart' show Size;
import 'package:web/web.dart' as web;

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
  Future<void> setupNotifier({required String appName}) async {
    // Permission is requested lazily on the first real notification —
    // asking at page load would pop a dialog before the user has a
    // reason to care, which browsers (and users) treat as spammy.
  }

  @override
  void showNotification({
    required String title,
    required String body,
    VoidCallback? onClick,
  }) {
    // Fire-and-forget: the Notification API's permission check is async
    // but the interface method is sync.
    unawaited(_showNotification(title, body, onClick));
  }

  Future<void> _showNotification(
    String title,
    String body,
    VoidCallback? onClick,
  ) async {
    // `permission` is a static string; `requestPermission()` returns a
    // JSPromise<String> resolving to the updated state. Every browser
    // new enough to run a Flutter Web build exposes the Notification
    // API, so we don't need a capability probe — a runtime throw here
    // would still be harmless because we're inside an unawaited future.
    try {
      var permission = web.Notification.permission;
      if (permission == 'default') {
        permission =
            (await web.Notification.requestPermission().toDart).toDart;
      }
      if (permission != 'granted') return;

      // Use the same 192x192 icon we ship to the web bundle so the OS
      // notification matches the tab favicon and PWA icon.
      //
      // `tag` groups repeat notifications for the same entity so a
      // rapid-fire sequence (Review Started → Review Complete → …) for
      // one PR replaces the previous OS notification instead of
      // stacking. The body is already "<repo> #<number>" in every
      // caller, which uniquely identifies a PR without threading a
      // separate id through the interface.
      final n = web.Notification(
        title,
        web.NotificationOptions(
          body: body,
          icon: '/icons/Icon-192.png',
          tag: body,
        ),
      );
      // onclick fires when the user clicks the notification in the OS
      // notification center. window.focus() brings the tab back to the
      // front (Chrome / Firefox do this automatically, Safari does not
      // — call it explicitly to be consistent); the callback runs any
      // app-level navigation (the dashboard uses it to go to /prs/:id).
      // Close the notification afterwards so it doesn't linger.
      n.onclick = (web.Event _) {
        web.window.focus();
        onClick?.call();
        n.close();
      }.toJS;
    } catch (_) {
      // Browser without Notification support, or blocked by CSP /
      // extension. Swallow — desktop still gets a tray/OS notification
      // via the desktop impl, and web users just see no notification
      // (the in-app SSE refresh still updates the PR list live).
      return;
    }
  }

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

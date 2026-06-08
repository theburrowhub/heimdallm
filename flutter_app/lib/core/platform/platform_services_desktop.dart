import 'dart:io';
import 'dart:ui' show VoidCallback;
import 'package:flutter/foundation.dart' show debugPrint;
import 'package:flutter/painting.dart' show Size;
import 'package:flutter_local_notifications/flutter_local_notifications.dart';
import 'package:tray_manager/tray_manager.dart';
import 'package:window_manager/window_manager.dart';
import '../api/api_client.dart';
import '../daemon/daemon_lifecycle.dart';
import '../models/config_model.dart';
import '../models/pr.dart';
import '../setup/desktop_repo_discovery.dart';
import '../setup/first_run_setup.dart';
import '../setup/repo_discovery.dart';
import '../tray/tray_menu.dart';
import 'platform_services.dart';

/// Desktop implementation of [PlatformServices].
///
/// Wraps dart:io, tray_manager, window_manager, flutter_local_notifications,
/// and the existing FirstRunSetup / DaemonLifecycle helpers so that shared
/// code never has to import them directly.
class DesktopPlatformServices with WindowListener implements PlatformServices {
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
  void Function(String location)? _onTrayNavigate;

  String get _resolvedTokenPath =>
      _tokenPath ??
      '${Platform.environment['HOME'] ?? ''}/.local/share/heimdallm/api_token';

  String get _resolvedPidFilePath =>
      _pidFilePath ??
      '${Platform.environment['HOME'] ?? ''}/.local/share/heimdallm/ui.pid';

  @override
  String get apiBaseUrl => 'http://127.0.0.1:$_apiPort';

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
  }) async {
    await windowManager.ensureInitialized();
    // Register the hide-on-close listener. When main.dart later calls
    // setPreventWindowClose(true), `onWindowClose` hides the window
    // instead of letting it actually close.
    windowManager.addListener(this);
    final options = WindowOptions(
      size: size,
      minimumSize: minimumSize,
      title: title,
      titleBarStyle: TitleBarStyle.normal,
    );
    await windowManager.setSize(size);
    await windowManager.setMinimumSize(minimumSize);
    await windowManager.setTitle(title);
    await windowManager.show();
    await windowManager.focus();
    windowManager.waitUntilReadyToShow(options, () async {
      await windowManager.show();
      await windowManager.focus();
    });
  }

  @override
  Future<void> setupTray({required ApiClient apiClient}) async {
    await trayManager.setIcon(
      Platform.isLinux ? 'assets/tray_icon@2x.png' : 'assets/tray_icon.png',
    );
    await trayManager.setContextMenu(Menu(items: [
      MenuItem(key: 'open', label: 'Open Heimdallm'),
      MenuItem.separator(),
      MenuItem(key: 'quit', label: 'Quit'),
    ]));
    // At this point the router isn't created yet, so we pass a no-op
    // navigation handler. main.dart calls setTrayNavigationHandler() later
    // with the real handler, which is forwarded into TrayMenu via rebind.
    TrayMenu.instance.init(
      apiClient: apiClient,
      onNavigate: _onTrayNavigate ?? (_) {},
    );
  }

  @override
  void setTrayNavigationHandler(void Function(String location) handler) {
    _onTrayNavigate = handler;
    TrayMenu.instance.rebindNavigation(handler);
  }

  // Notification plumbing. flutter_local_notifications wires onClick through
  // a single global callback (per payload) instead of per-instance handlers,
  // so we keep a small id → callback map here and use the id as payload.
  //
  // The map is bounded by `_maxNotifierHandlers` with FIFO eviction (Dart's
  // default Map preserves insertion order). Without this, notifications that
  // are dismissed or expire without being clicked would leak their closures
  // indefinitely in a long-running tray app.
  static const int _maxNotifierHandlers = 128;
  final FlutterLocalNotificationsPlugin _notifier =
      FlutterLocalNotificationsPlugin();
  final Map<int, VoidCallback> _notifierHandlers = {};
  int _nextNotifierId = 0;

  @override
  Future<void> setupNotifier({required String appName}) async {
    // appName is unused — the bundle's CFBundleName/Info.plist drives the
    // notification source label on every platform. Kept in the signature so
    // the abstract API stays platform-neutral.
    await _notifier.initialize(
      settings: const InitializationSettings(
        macOS: DarwinInitializationSettings(
          // Triggers UNUserNotificationCenter.requestAuthorization on first
          // run — the modern Notification Center prompt, replacing the
          // deprecated NSUserNotification path that silently failed on
          // recent macOS (see issue #438).
          requestAlertPermission: true,
          requestSoundPermission: true,
          requestBadgePermission: true,
        ),
        linux: LinuxInitializationSettings(defaultActionName: 'Open'),
      ),
      onDidReceiveNotificationResponse: (response) {
        final id = int.tryParse(response.payload ?? '');
        if (id == null) return;
        final handler = _notifierHandlers.remove(id);
        handler?.call();
      },
    );
  }

  @override
  void showNotification({
    required String title,
    required String body,
    VoidCallback? onClick,
  }) {
    // Bitmask keeps the id in 32-bit positive range; some platform notifier
    // backends use 32-bit ids internally, so we avoid relying on Dart's
    // 64-bit ints surviving the boundary.
    final id = _nextNotifierId = (_nextNotifierId + 1) & 0x7FFFFFFF;
    if (onClick != null) {
      _notifierHandlers[id] = onClick;
      if (_notifierHandlers.length > _maxNotifierHandlers) {
        // Drop the oldest handler. Map preserves insertion order, so the
        // first key is the eldest entry.
        _notifierHandlers.remove(_notifierHandlers.keys.first);
      }
    }
    _notifier.show(
      id: id,
      title: title,
      body: body,
      notificationDetails: const NotificationDetails(
        macOS: DarwinNotificationDetails(),
        linux: LinuxNotificationDetails(),
      ),
      payload: id.toString(),
    );
  }

  @override
  Future<void> setPreventWindowClose(bool enable) =>
      windowManager.setPreventClose(enable);

  @override
  Future<void> showAndFocusWindow() async {
    await windowManager.show();
    await windowManager.focus();
  }

  @override
  Future<void> hideWindow() => windowManager.hide();

  @override
  void onWindowClose() async {
    if (await windowManager.isPreventClose()) {
      await windowManager.hide();
    }
  }

  @override
  Never quitApp() => exit(0);

  @override
  Future<String?> detectGitHubToken() => FirstRunSetup.detectToken();

  @override
  Future<String?> getStoredGitHubToken() => FirstRunSetup.getToken();

  @override
  Future<void> storeGitHubToken(String token) => FirstRunSetup.storeToken(token);

  @override
  Future<void> writeDaemonConfig(AppConfig config) =>
      FirstRunSetup.writeConfig(config);

  @override
  Future<bool> daemonConfigExists() => FirstRunSetup.configExists();

  @override
  String? defaultDaemonBinaryPath() => DaemonLifecycle.defaultBinaryPath();

  @override
  Future<void> spawnDaemon(String binaryPath) async {
    final binary = File(binaryPath);
    if (!binary.existsSync()) {
      throw DaemonException('Daemon binary not found: $binaryPath');
    }
    // Detached: daemon outlives the Flutter process so in-flight reviews
    // survive window hides and dev restarts.
    await Process.start(binaryPath, [], mode: ProcessStartMode.detached);
  }

  @override
  Future<void> rebuildTrayMenu({required List<PR> prs, required String me}) =>
      TrayMenu.instance.rebuild(prs: prs, me: me);

  @override
  Future<List<String>> discoverReposFromPRs(String token) =>
      RepoDiscovery.discoverFromPRs(
        token,
        localSearch: DesktopRepoDiscovery.viaGhCli,
      );
}

/// Alias used by the conditional export in `platform_services.dart`.
typedef PlatformServicesImpl = DesktopPlatformServices;

import 'dart:async';
import 'dart:io';
import 'dart:ui' show VoidCallback;
import 'package:flutter/foundation.dart' show debugPrint, visibleForTesting;
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

@visibleForTesting
typedef PlatformProcessRunner =
    Future<ProcessResult> Function(String executable, List<String> arguments);
@visibleForTesting
typedef DetachedDaemonStarter = Future<void> Function(String binaryPath);

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
    @visibleForTesting bool? isMacOS,
    @visibleForTesting PlatformProcessRunner? processRunner,
    @visibleForTesting DetachedDaemonStarter? detachedDaemonStarter,
  }) : _apiPort = apiPort,
       _tokenPath = tokenPath,
       _pidFilePath = pidFilePath,
       _isMacOS = isMacOS ?? Platform.isMacOS,
       _processRunner =
           processRunner ??
           ((executable, arguments) => Process.run(executable, arguments)),
       _detachedDaemonStarter =
           detachedDaemonStarter ??
           ((binaryPath) async {
             await Process.start(
               binaryPath,
               const [],
               mode: ProcessStartMode.detached,
             );
           });

  final int _apiPort;
  final String? _tokenPath;
  final String? _pidFilePath;
  final bool _isMacOS;
  final PlatformProcessRunner _processRunner;
  final DetachedDaemonStarter _detachedDaemonStarter;
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
          debugPrint(
            'Another Heimdallm instance is running (PID $existing), signalling it.',
          );
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
    await trayManager.setContextMenu(
      Menu(
        items: [
          MenuItem(key: 'open', label: 'Open Heimdallm'),
          MenuItem.separator(),
          MenuItem(key: 'quit', label: 'Quit'),
        ],
      ),
    );
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
  Future<void> storeGitHubToken(String token) =>
      FirstRunSetup.storeToken(token);

  @override
  Future<void> writeDaemonConfig(AppConfig config) =>
      FirstRunSetup.writeConfig(config);

  @override
  Future<bool> daemonConfigExists() => FirstRunSetup.configExists();

  @override
  String? defaultDaemonBinaryPath() => DaemonLifecycle.defaultBinaryPath();

  @override
  Future<TcpPortState> probeDaemonPort({
    Duration timeout = const Duration(milliseconds: 500),
  }) async {
    Socket? socket;
    try {
      socket = await Socket.connect('127.0.0.1', _apiPort, timeout: timeout);
      return TcpPortState.open;
    } on SocketException catch (e) {
      // These are ECONNREFUSED on macOS, Linux and Windows respectively. Only
      // an explicit refusal proves no listener owns the port. Everything else
      // (including ENETUNREACH/EHOSTUNREACH) is ambiguous and must not permit
      // spawning a second daemon.
      const connectionRefusedCodes = {61, 111, 10061};
      return connectionRefusedCodes.contains(e.osError?.errorCode)
          ? TcpPortState.closed
          : TcpPortState.unknown;
    } on TimeoutException {
      return TcpPortState.unknown;
    } catch (_) {
      return TcpPortState.unknown;
    } finally {
      socket?.destroy();
    }
  }

  @override
  Future<bool> isDaemonSupervised() async {
    if (!_isMacOS) return false;
    return await _loadedCanonicalLaunchAgentTarget() != null;
  }

  @override
  Future<void> spawnDaemon(String binaryPath) async {
    final binary = File(binaryPath);
    if (!binary.existsSync()) {
      throw DaemonException('Daemon binary not found: $binaryPath');
    }

    // A loaded LaunchAgent is the canonical owner on macOS. Starting a
    // detached sibling here would move the daemon outside launchd supervision
    // and leave the KeepAlive job retrying against the process-lifetime lock.
    // Ask launchd to start the existing job instead. Any ambiguous inspection
    // failure is fatal: falling back to Process.start would be unsafe because
    // the job may still be loaded.
    final launchAgentTarget = _isMacOS
        ? await _loadedCanonicalLaunchAgentTarget()
        : null;
    if (launchAgentTarget != null) {
      final result = await _processRunner('/bin/launchctl', [
        'kickstart',
        launchAgentTarget,
      ]);
      if (result.exitCode != 0) {
        throw DaemonException(
          'Could not start the supervised daemon: '
          '${_processResultDetails(result)}',
        );
      }
      return;
    }

    // Detached: daemon outlives the Flutter process so in-flight reviews
    // survive window hides and dev restarts.
    await _detachedDaemonStarter(binaryPath);
  }

  static const _canonicalLaunchAgentLabel = 'com.heimdallm.daemon';

  Future<String> _canonicalLaunchAgentTarget() async {
    final uidResult = await _processRunner('/usr/bin/id', const ['-u']);
    final uid = '${uidResult.stdout}'.trim();
    if (uidResult.exitCode != 0 || !RegExp(r'^\d+$').hasMatch(uid)) {
      throw DaemonException(
        'Could not determine the launchd user domain: '
        '${_processResultDetails(uidResult)}',
      );
    }
    return 'gui/$uid/$_canonicalLaunchAgentLabel';
  }

  Future<String?> _loadedCanonicalLaunchAgentTarget() async {
    final target = await _canonicalLaunchAgentTarget();
    final result = await _processRunner('/bin/launchctl', ['print', target]);
    if (result.exitCode == 0) return target;

    final details = '${result.stdout}\n${result.stderr}';
    if (result.exitCode == 113 || details.contains('Could not find service')) {
      return null;
    }
    throw DaemonException(
      'Could not determine whether the supervised daemon is loaded: '
      '${_processResultDetails(result)}',
    );
  }

  static String _processResultDetails(ProcessResult result) {
    final stderr = '${result.stderr}'.trim();
    final stdout = '${result.stdout}'.trim();
    final output = stderr.isNotEmpty ? stderr : stdout;
    return output.isEmpty ? 'exit status ${result.exitCode}' : output;
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

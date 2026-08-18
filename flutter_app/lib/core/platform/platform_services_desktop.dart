import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:ui' show VoidCallback;
import 'package:flutter/foundation.dart'
    show debugPrint, kReleaseMode, visibleForTesting;
import 'package:flutter/painting.dart' show Size;
import 'package:flutter/services.dart' show MethodCall, MethodChannel;
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
@visibleForTesting
typedef DaemonPortProbe = Future<TcpPortState> Function(Duration timeout);
@visibleForTesting
typedef PlatformMethodInvoker =
    Future<dynamic> Function(String method, dynamic arguments);
@visibleForTesting
typedef PlatformEnvironmentReader = String? Function(String name);
@visibleForTesting
typedef PlatformProcessExit = void Function(int code);
@visibleForTesting
typedef InstanceLockAcquirer = Future<void> Function(RandomAccessFile lock);
@visibleForTesting
typedef ActivationSocketBinder = Future<ServerSocket> Function(String path);
@visibleForTesting
typedef DaemonBinaryPathResolver = String? Function();

const _defaultProcessTimeout = Duration(seconds: 7);
const _processKillGrace = Duration(seconds: 1);

@visibleForTesting
Future<ProcessResult> runDefaultPlatformProcess(
  String executable,
  List<String> arguments, {
  Duration timeout = _defaultProcessTimeout,
  Duration killGrace = _processKillGrace,
}) async {
  final process = await Process.start(executable, arguments);
  final stdoutFuture = process.stdout.transform(systemEncoding.decoder).join();
  final stderrFuture = process.stderr.transform(systemEncoding.decoder).join();
  try {
    final exitCode = await process.exitCode.timeout(timeout);
    return ProcessResult(
      process.pid,
      exitCode,
      await stdoutFuture,
      await stderrFuture,
    );
  } on TimeoutException {
    process.kill(ProcessSignal.sigterm);
    try {
      await process.exitCode.timeout(killGrace);
    } on TimeoutException {
      process.kill(ProcessSignal.sigkill);
      try {
        await process.exitCode.timeout(killGrace);
      } on TimeoutException {
        // The caller still receives a bounded failure. SIGKILL cannot be
        // ignored; a second timeout means the OS has not reaped the child yet.
      }
    }
    throw DaemonException('Timed out running $executable.');
  }
}

/// Desktop implementation of [PlatformServices].
///
/// Wraps dart:io, tray_manager, window_manager, flutter_local_notifications,
/// and the existing FirstRunSetup / DaemonLifecycle helpers so that shared
/// code never has to import them directly.
class DesktopPlatformServices
    with WindowListener
    implements
        PlatformServices,
        AppUpdatePlatformCapability,
        DuplicateInstancePlatformCapability {
  DesktopPlatformServices({
    int apiPort = 7842,
    String? tokenPath,
    @visibleForTesting String? dataDir,
    String? pidFilePath,
    @visibleForTesting bool? isMacOS,
    @visibleForTesting PlatformProcessRunner? processRunner,
    @visibleForTesting DetachedDaemonStarter? detachedDaemonStarter,
    @visibleForTesting DaemonPortProbe? daemonPortProbe,
    @visibleForTesting PlatformMethodInvoker? methodInvoker,
    @visibleForTesting PlatformEnvironmentReader? environmentReader,
    @visibleForTesting PlatformProcessExit? processExit,
    @visibleForTesting InstanceLockAcquirer? instanceLockAcquirer,
    @visibleForTesting ActivationSocketBinder? activationSocketBinder,
    @visibleForTesting DaemonBinaryPathResolver? daemonBinaryPathResolver,
    @visibleForTesting bool? enableNativeAppUpdates,
    @visibleForTesting Duration processTimeout = const Duration(seconds: 10),
  }) : _apiPort = apiPort,
       _tokenPath = tokenPath,
       _dataDir = dataDir,
       _pidFilePath = pidFilePath,
       _isMacOS = isMacOS ?? Platform.isMacOS,
       _processRunner = processRunner ?? runDefaultPlatformProcess,
       _processTimeout = processTimeout,
       _detachedDaemonStarter =
           detachedDaemonStarter ??
           ((binaryPath) async {
             await Process.start(
               binaryPath,
               const [],
               mode: ProcessStartMode.detached,
             );
           }),
       _daemonPortProbe = daemonPortProbe,
       _environmentReader =
           environmentReader ?? ((name) => Platform.environment[name]),
       _processExit = processExit ?? exit,
       _instanceLockAcquirer =
           instanceLockAcquirer ?? ((lock) => lock.lock(FileLock.exclusive)),
       _activationSocketBinder =
           activationSocketBinder ??
           ((path) => ServerSocket.bind(
             InternetAddress(path, type: InternetAddressType.unix),
             0,
             shared: false,
           )),
       _daemonBinaryPathResolver =
           daemonBinaryPathResolver ?? DaemonLifecycle.defaultBinaryPath,
       _methodInvoker =
           methodInvoker ??
           ((method, arguments) =>
               _appUpdateChannel.invokeMethod<dynamic>(method, arguments)),
       _usesDefaultMethodChannel = methodInvoker == null,
       _nativeAppUpdatesRequested =
           enableNativeAppUpdates ??
           ((isMacOS ?? Platform.isMacOS) && kReleaseMode);

  final int _apiPort;
  final String? _tokenPath;
  final String? _dataDir;
  final String? _pidFilePath;
  final bool _isMacOS;
  final PlatformProcessRunner _processRunner;
  final Duration _processTimeout;
  final DetachedDaemonStarter _detachedDaemonStarter;
  final DaemonPortProbe? _daemonPortProbe;
  final PlatformEnvironmentReader _environmentReader;
  final PlatformProcessExit _processExit;
  final InstanceLockAcquirer _instanceLockAcquirer;
  final ActivationSocketBinder _activationSocketBinder;
  final DaemonBinaryPathResolver _daemonBinaryPathResolver;
  final PlatformMethodInvoker _methodInvoker;
  final bool _usesDefaultMethodChannel;
  final bool _nativeAppUpdatesRequested;
  bool _nativeAppUpdatesEnabled = false;
  Object? _appUpdaterSetupError;
  RandomAccessFile? _instanceLock;
  ServerSocket? _activationServer;
  StreamSubscription<Socket>? _activationSubscription;
  Directory? _activationSocketDirectory;
  VoidCallback? _activationCallback;
  bool _activationPending = false;
  String? _cachedToken;
  void Function(String location)? _onTrayNavigate;

  String get _resolvedTokenPath => _tokenPath ?? '$_resolvedDataDir/api_token';

  String get _resolvedPidFilePath =>
      _pidFilePath ??
      readEnv('HEIMDALLM_UI_PID_FILE') ??
      _xctestPidFilePath ??
      '${readEnv('HOME') ?? ''}/.local/share/heimdallm/ui.pid';

  String? get _xctestPidFilePath {
    final configurationPath = readEnv('XCTestConfigurationFilePath');
    if (configurationPath == null || configurationPath.isEmpty) {
      return null;
    }
    return '$configurationPath.heimdallm-ui.pid';
  }

  String get _resolvedDataDir =>
      _dataDir ??
      readEnv('HEIMDALLM_DATA_DIR') ??
      '${readEnv('HOME') ?? ''}/.local/share/heimdallm';

  static const MethodChannel _appUpdateChannel = MethodChannel(
    'com.theburrowhub.heimdallm/app_updater',
  );

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
  String? readEnv(String name) => _environmentReader(name);
  @override
  Future<bool> ensureSingleInstance() async {
    if (_instanceLock != null) return true;

    final pidFile = File(_resolvedPidFilePath);
    await pidFile.parent.create(recursive: true);

    // Keep this advisory lock descriptor open for the complete process
    // lifetime. The non-blocking lock is the actual singleton boundary; the
    // JSON record is only authenticated activation routing metadata.
    final lock = await pidFile.open(mode: FileMode.append);
    try {
      await _instanceLockAcquirer(lock);
    } on FileSystemException {
      await lock.close();
      await _activateLockOwner(pidFile);
      return false;
    }

    ServerSocket? server;
    StreamSubscription<Socket>? subscription;
    Directory? socketDirectory;
    try {
      final previousRecord = await _readInstanceRecord(lock);
      socketDirectory = await _reusableSocketDirectory(previousRecord);
      final socketPath = '${socketDirectory.path}/activate.sock';
      server = await _activationSocketBinder(socketPath);
      subscription = server.listen((socket) {
        socket.destroy();
        final callback = _activationCallback;
        if (callback == null) {
          _activationPending = true;
        } else {
          callback();
        }
      });

      final record = jsonEncode({
        'schema_version': 1,
        'pid': pid,
        'executable': _canonicalExecutablePath,
        'activation_socket': socketPath,
      });
      await lock.truncate(0);
      await lock.setPosition(0);
      await lock.writeString(record);
      await lock.flush();

      _instanceLock = lock;
      _activationServer = server;
      _activationSubscription = subscription;
      _activationSocketDirectory = socketDirectory;
      return true;
    } catch (_) {
      await subscription?.cancel();
      await server?.close();
      if (socketDirectory != null) {
        final socket = File('${socketDirectory.path}/activate.sock');
        if (await socket.exists()) await socket.delete();
        if (await socketDirectory.exists()) await socketDirectory.delete();
      }
      await lock.unlock();
      await lock.close();
      rethrow;
    }
  }

  @override
  void listenForActivationSignal(VoidCallback onActivate) {
    _activationCallback = onActivate;
    if (_activationPending) {
      _activationPending = false;
      scheduleMicrotask(onActivate);
    }
  }

  String get _canonicalExecutablePath {
    final executable = File(Platform.resolvedExecutable);
    try {
      return executable.resolveSymbolicLinksSync();
    } on FileSystemException {
      return executable.absolute.path;
    }
  }

  Future<Map<String, dynamic>?> _readInstanceRecord(
    RandomAccessFile lock,
  ) async {
    try {
      await lock.setPosition(0);
      final contents = await lock.read(await lock.length());
      if (contents.isEmpty) return null;
      final decoded = jsonDecode(utf8.decode(contents));
      if (decoded is! Map<String, dynamic> || decoded['schema_version'] != 1) {
        return null;
      }
      return decoded;
    } on FormatException {
      return null;
    } on FileSystemException {
      return null;
    }
  }

  Future<Directory> _reusableSocketDirectory(
    Map<String, dynamic>? previousRecord,
  ) async {
    final previousPath = previousRecord?['activation_socket'];
    if (previousPath is String && previousPath.isNotEmpty) {
      final socket = File(previousPath);
      final directory = socket.parent;
      final systemTemp = Directory.systemTemp.absolute.path;
      final pathSegments = directory.uri.pathSegments
          .where((segment) => segment.isNotEmpty)
          .toList();
      final directoryName = pathSegments.isEmpty ? null : pathSegments.last;
      if (socket.uri.pathSegments.last == 'activate.sock' &&
          directory.parent.absolute.path == systemTemp &&
          directoryName != null &&
          directoryName.startsWith('heimdallm-ui-')) {
        try {
          if (await socket.exists()) await socket.delete();
          return directory;
        } on FileSystemException {
          // Fall through to a fresh private temporary directory.
        }
      }
    }
    return Directory.systemTemp.createTemp('heimdallm-ui-');
  }

  Future<void> _activateLockOwner(File pidFile) async {
    for (var attempt = 0; attempt < 20; attempt++) {
      try {
        final decoded = jsonDecode(await pidFile.readAsString());
        if (decoded is Map<String, dynamic> &&
            decoded['schema_version'] == 1 &&
            decoded['pid'] is int &&
            decoded['pid'] != pid &&
            decoded['executable'] == _canonicalExecutablePath &&
            decoded['activation_socket'] is String) {
          final path = decoded['activation_socket'] as String;
          final socket = await Socket.connect(
            InternetAddress(path, type: InternetAddressType.unix),
            0,
            timeout: const Duration(milliseconds: 100),
          );
          await socket.flush();
          await socket.close();
          debugPrint(
            'Another verified Heimdallm instance owns the singleton lock '
            '(PID ${decoded['pid']}); activated it over local IPC.',
          );
          return;
        }
      } on FileSystemException {
        // The owner may still be publishing its atomic routing metadata.
      } on FormatException {
        // The owner may still be publishing its atomic routing metadata.
      } on SocketException {
        // The owner may still be binding its activation socket.
      }
      await Future<void>.delayed(const Duration(milliseconds: 50));
    }
    debugPrint(
      'Another process owns the Heimdallm singleton lock, but its activation '
      'endpoint could not be verified. This duplicate will exit safely.',
    );
  }

  @visibleForTesting
  Future<void> releaseSingleInstanceForTesting() async {
    await _activationSubscription?.cancel();
    _activationSubscription = null;
    await _activationServer?.close();
    _activationServer = null;
    final directory = _activationSocketDirectory;
    _activationSocketDirectory = null;
    if (directory != null) {
      final socket = File('${directory.path}/activate.sock');
      if (await socket.exists()) await socket.delete();
      if (await directory.exists()) await directory.delete();
    }
    final lock = _instanceLock;
    _instanceLock = null;
    if (lock != null) {
      await lock.unlock();
      await lock.close();
    }
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
      onQuit: quitApp,
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
  void quitApp() {
    if (!_isMacOS) {
      _processExit(0);
      return;
    }
    // AppKit termination is asynchronous and may legitimately be postponed
    // while Sparkle drains the daemon. A raw exit would bypass both AppDelegate
    // and Sparkle, so a broken native bridge must fail closed.
    unawaited(
      _methodInvoker('terminateApplication', null).catchError((Object error) {
        debugPrint('native termination failed: $error');
      }),
    );
  }

  @override
  void quitDuplicateInstance() {
    if (!_isMacOS) {
      _processExit(0);
      return;
    }
    unawaited(
      _methodInvoker('terminateDuplicateApplication', null).catchError((
        Object error,
      ) {
        // Singleton ownership has already been disproved. A native-channel
        // failure cannot risk daemon/update state because this process owns
        // neither; make sure the duplicate does not linger indefinitely.
        _processExit(0);
      }),
    );
  }

  @override
  AppUpdateSupport get appUpdateSupport => _nativeAppUpdatesEnabled
      ? AppUpdateSupport.native
      : AppUpdateSupport.unavailable;

  @override
  Future<void> setupAppUpdater() async {
    _nativeAppUpdatesEnabled = false;
    _appUpdaterSetupError = null;
    if (!_isMacOS) return;
    try {
      if (_usesDefaultMethodChannel) {
        _appUpdateChannel.setMethodCallHandler(handleAppUpdaterCallForTesting);
      }
      final effective = await _methodInvoker('configure', {
        'updatesEnabled': _nativeAppUpdatesRequested,
        'apiBaseUrl': apiBaseUrl,
        'apiToken': await loadApiToken(),
        'apiTokenPath': _resolvedTokenPath,
        'dataDir': _resolvedDataDir,
      });
      _nativeAppUpdatesEnabled = effective == true;
    } catch (error) {
      // main() may continue when setup fails and no recovery exists. Retain
      // the error so a durable recovery journal can still block bootstrap.
      _appUpdaterSetupError = error;
      rethrow;
    }
  }

  @visibleForTesting
  Future<dynamic> handleAppUpdaterCallForTesting(MethodCall call) async {
    if (call.method != 'restartDaemonAfterUpdateAbort') return null;
    final binaryPath = defaultDaemonBinaryPath();
    if (binaryPath == null) {
      throw DaemonException(
        'Bundled daemon is unavailable; update recovery cannot continue.',
      );
    }
    try {
      await spawnDaemon(binaryPath);
    } catch (error) {
      // The guarded spawn fails closed if any process still owns the daemon.
      // Surface the failure across the method channel so native recovery keeps
      // its durable journal and never claims the daemon was restored.
      debugPrint('could not restore daemon after update abort: $error');
      rethrow;
    }
    return null;
  }

  @override
  Future<void> checkForAppUpdates() async {
    if (appUpdateSupport != AppUpdateSupport.native) {
      throw UnsupportedError('Native app updates are only available on macOS');
    }
    await _methodInvoker('checkForUpdates', null);
  }

  @override
  Future<String?> pendingAppUpdateVersion() async {
    if (!_isMacOS) return null;
    if (appUpdateSupport != AppUpdateSupport.native) {
      final recoveryPath = '$_resolvedDataDir/app-update-recovery.json';
      final recoveryType = await FileSystemEntity.type(
        recoveryPath,
        followLinks: false,
      );
      if (recoveryType != FileSystemEntityType.notFound) {
        final setupDetail = _appUpdaterSetupError == null
            ? 'the native updater was rejected by its signing/configuration gate'
            : 'native updater setup failed: $_appUpdaterSetupError';
        throw StateError(
          'A protected app-update recovery journal exists at $recoveryPath, '
          'but $setupDetail. Daemon startup is blocked until the signed '
          'updater can resume recovery.',
        );
      }
      return null;
    }
    return await _methodInvoker('pendingUpdateVersion', null) as String?;
  }

  @override
  Future<void> completeAppUpdate() async {
    if (appUpdateSupport != AppUpdateSupport.native) return;
    await _methodInvoker('completeUpdate', null);
  }

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
  String? defaultDaemonBinaryPath() => _daemonBinaryPathResolver();

  Future<TcpPortState> _probeDaemonPort(Duration timeout) async {
    final injected = _daemonPortProbe;
    if (injected != null) return injected(timeout);

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
  Future<void> spawnDaemon(String binaryPath) async {
    final binary = File(binaryPath);
    if (!binary.existsSync()) {
      throw DaemonException('Daemon binary not found: $binaryPath');
    }

    // This is the final spawn gate. ApiClient can identify a daemon response,
    // but an HTTP transport error cannot prove the TCP port is free (a silent
    // foreign process or a daemon between bind and HTTP readiness may own it).
    // Only an explicit connection refusal permits process creation.
    final portState = await _probeDaemonPort(const Duration(milliseconds: 500));
    if (portState == TcpPortState.open) {
      throw DaemonPortOccupiedException(_apiPort);
    }
    if (portState != TcpPortState.closed) {
      throw DaemonException(
        'Could not prove that port $_apiPort is free; no daemon was started.',
      );
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
      final result = await _runPlatformProcess('/bin/launchctl', [
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
    final uidResult = await _runPlatformProcess('/usr/bin/id', const ['-u']);
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
    final result = await _runPlatformProcess('/bin/launchctl', [
      'print',
      target,
    ]);
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

  Future<ProcessResult> _runPlatformProcess(
    String executable,
    List<String> arguments,
  ) async {
    try {
      return await _processRunner(
        executable,
        arguments,
      ).timeout(_processTimeout);
    } on TimeoutException {
      throw DaemonException('Timed out running $executable.');
    }
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

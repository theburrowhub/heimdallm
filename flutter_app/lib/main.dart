import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'core/api/api_client.dart';
import 'core/daemon/daemon_startup.dart';
import 'core/models/config_model.dart';
import 'core/platform/platform_services.dart';
import 'core/platform/platform_services_provider.dart';
import 'shared/router.dart';

/// Global router — accessible via the container so the tray menu +
/// notification handlers can push routes without a BuildContext.
final _appRouter = createRouter(initialLocation: '/');
GoRouter get appRouter => _appRouter;

@visibleForTesting
Future<bool> initializePlatformForApp(PlatformServices platform) async {
  if (!await platform.ensureSingleInstance()) {
    platform.quitDuplicateInstance();
    return false;
  }

  try {
    await platform.setupAppUpdater();
  } catch (e) {
    // Update infrastructure must never delay or prevent daemon startup.
    debugPrint('app updater init failed: $e');
  }
  return true;
}

Future<void> main() async {
  WidgetsFlutterBinding.ensureInitialized();

  final platform = PlatformServices.create();
  if (!await initializePlatformForApp(platform)) return;

  platform.listenForActivationSignal(() async {
    await platform.showAndFocusWindow();
  });

  FlutterError.onError = (details) {
    debugPrint('Flutter error: ${details.exceptionAsString()}');
    FlutterError.presentError(details);
  };

  try {
    await platform.setupWindow(
      title: 'Heimdallm',
      size: const Size(1200, 720),
      minimumSize: const Size(900, 520),
    );
  } catch (e) {
    debugPrint('window init failed: $e');
  }

  // Tray needs the shared ApiClient so the token cache is consistent.
  final trayApiClient = ApiClient(platform: platform);
  try {
    await platform.setupTray(apiClient: trayApiClient);
    platform.setTrayNavigationHandler((location) async {
      await platform.showAndFocusWindow();
      // Small delay so the window is visible before we navigate.
      Future.delayed(const Duration(milliseconds: 200), () {
        _appRouter.push(location);
      });
    });
  } catch (e) {
    debugPrint('tray init failed: $e');
  }

  try {
    await platform.setupNotifier(appName: 'Heimdallm');
  } catch (e) {
    debugPrint('notifier init failed: $e');
  }

  runApp(
    ProviderScope(
      overrides: [platformServicesProvider.overrideWithValue(platform)],
      child: _BootstrapApp(appRouter: _appRouter),
    ),
  );
}

/// Public entry point for features to fire notifications.
/// Takes the [PlatformServices] (from a `ref.read(platformServicesProvider)`)
/// so the caller controls platform availability.
void sendPRNotification({
  required PlatformServices platform,
  required String title,
  required String body,
  int? prId,
}) {
  platform.showNotification(
    title: title,
    body: body,
    onClick: () async {
      await platform.showAndFocusWindow();
      if (prId != null) _appRouter.go('/prs/$prId');
    },
  );
}

/// Builds the real bootstrap state machine with deterministic dependencies.
/// Production uses the private widget directly; tests use this entry point so
/// every daemon-ownership outcome can be exercised without sockets, processes
/// or wall-clock waits.
@visibleForTesting
Widget buildBootstrapAppForTest({
  required GoRouter router,
  required PlatformServices platform,
  required ApiClient apiClient,
  Duration healthPollInterval = Duration.zero,
  int healthRetryEvery = 1,
  int maxSpawnAttempts = 4,
}) {
  return ProviderScope(
    overrides: [platformServicesProvider.overrideWithValue(platform)],
    child: _BootstrapApp(
      appRouter: router,
      apiClient: apiClient,
      healthPollInterval: healthPollInterval,
      healthRetryEvery: healthRetryEvery,
      maxSpawnAttempts: maxSpawnAttempts,
    ),
  );
}

class _BootstrapApp extends ConsumerStatefulWidget {
  final GoRouter appRouter;
  final ApiClient? apiClient;
  final Duration healthPollInterval;
  final int healthRetryEvery;
  final int maxSpawnAttempts;

  const _BootstrapApp({
    required this.appRouter,
    this.apiClient,
    this.healthPollInterval = const Duration(milliseconds: 400),
    this.healthRetryEvery = 25,
    this.maxSpawnAttempts = 4,
  }) : assert(healthRetryEvery > 0),
       assert(maxSpawnAttempts > 0);
  @override
  ConsumerState<_BootstrapApp> createState() => _BootstrapAppState();
}

class _BootstrapAppState extends ConsumerState<_BootstrapApp> {
  String? _destination;
  String _status = 'Starting…';
  String? _errorTitle;
  String? _errorDetails;
  String? _errorHint;
  String? _pendingUpdateVersion;

  PlatformServices get _platform => ref.read(platformServicesProvider);

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback((_) {
      _platform.setPreventWindowClose(true);
    });
    _boot();
  }

  Future<void> _boot() async {
    final api = widget.apiClient ?? ApiClient(platform: _platform);
    try {
      _pendingUpdateVersion = await _platform.pendingAppUpdateVersion();
    } catch (e) {
      _setError(
        title: 'Update recovery failed',
        details:
            'Heimdallm could not restore the daemon service after an app '
            'update: $e',
        hint:
            'Quit Heimdallm and reopen it. If the problem persists, reinstall '
            'the latest signed release.',
      );
      return;
    }

    // Determine port ownership before asking for credentials, config or even a
    // local daemon binary. A live daemon can legitimately answer 503 while
    // starting, stopping or degraded; it is still the authoritative process
    // and the dashboard remains useful for diagnosis (#646).
    switch (await api.daemonReachable()) {
      case PortOwner.daemon:
        if (!await _validatePendingUpdate(api)) return;
        _go('/');
        return;
      case PortOwner.foreign:
        _setForeignPortError(api.daemonPort);
        return;
      case PortOwner.none:
        break;
    }

    _setStatus('Detecting credentials…');
    final token = await _platform.detectGitHubToken();
    if (token == null) {
      _go('/config');
      return;
    }

    if (!await _platform.daemonConfigExists()) {
      _setStatus('Discovering repositories…');
      final repos = await _platform.discoverReposFromPRs(token);

      _setStatus('Setting up…');
      // Seed local_dir_base from HEIMDALLM_LOCAL_DIR_BASE when the
      // operator has it set — otherwise a wipe + first-run loses the
      // full-repo-analysis base path and every repo falls back to
      // diff-only until the operator re-adds it by hand. Comma-
      // separated list, matches the daemon's env parsing.
      final reposDirEnv = _platform.readEnv('HEIMDALLM_LOCAL_DIR_BASE') ?? '';
      final localDirBase = reposDirEnv
          .split(',')
          .map((p) => p.trim())
          .where((p) => p.isNotEmpty)
          .toList();
      final config = AppConfig(
        repoConfigs: {
          for (final r in repos) r: const RepoConfig(prEnabled: true),
        },
        localDirBase: localDirBase,
      );
      await _platform.storeGitHubToken(token);
      await _platform.writeDaemonConfig(config);
    }

    final binaryPath = _platform.defaultDaemonBinaryPath();
    if (binaryPath == null) {
      _setError(
        title: 'Daemon binary not found',
        details:
            'Heimdallm could not locate its background service.\n'
            'This usually means the installation is incomplete.',
        hint:
            'If you installed from a DMG, open Terminal and run:\n'
            'xattr -cr /Applications/Heimdallm.app\n'
            'then relaunch the app.',
      );
      return;
    }

    _setStatus('Starting Heimdallm…');
    final startup = DaemonStartupCoordinator(
      api: api,
      platform: _platform,
      binaryPath: binaryPath,
      maxSpawnAttempts: widget.maxSpawnAttempts,
    );
    final initial = await startup.ensureAvailable();
    if (!_handleStartupResult(initial, api.daemonPort)) {
      return;
    }

    _setStatus('Waiting for Heimdallm…');
    await _waitForHealth(api, startup: startup);
  }

  Future<void> _waitForHealth(
    ApiClient api, {
    required DaemonStartupCoordinator startup,
  }) async {
    for (var attempt = 0; ; attempt++) {
      await Future.delayed(widget.healthPollInterval);
      if (await api.checkHealth()) {
        if (!await _validatePendingUpdate(api)) return;
        _go('/');
        return;
      }
      if (attempt > 0 && attempt % widget.healthRetryEvery == 0) {
        final result = await startup.ensureAvailable();
        if (!_handleStartupResult(result, api.daemonPort)) return;
      }
    }
  }

  /// Handles every result from the shared guarded startup path. Returns true
  /// while the health loop should keep waiting, false after surfacing a
  /// terminal error.
  bool _handleStartupResult(DaemonStartupResult result, int port) {
    switch (result.outcome) {
      case DaemonStartupOutcome.spawned:
        _setStatus('Waiting for Heimdallm…');
        return true;
      case DaemonStartupOutcome.daemonPresent:
        // Reachability is enough to enter the app. Health remains visible in
        // the dashboard; blocking the splash on a stale last_poll was the local
        // failure mode which triggered duplicate spawn attempts in #646.
        if (_pendingUpdateVersion != null) {
          _setStatus('Validating updated Heimdallm…');
          return true;
        }
        _go('/');
        return false;
      case DaemonStartupOutcome.spawnFailedRetryable:
        _setStatus('Could not launch Heimdallm — retrying…');
        return true;
      case DaemonStartupOutcome.portOccupied:
        _setForeignPortError(port);
        return false;
      case DaemonStartupOutcome.spawnFailedTerminal:
        _setSpawnFailureError(result.error);
        return false;
      case DaemonStartupOutcome.spawnBudgetExhausted:
        _setError(
          title: 'Daemon failed to start',
          details:
              'Heimdallm exhausted its guarded start attempts without '
              'finding a daemon on the configured port.',
          hint:
              'Try restarting the app. If the problem persists, check your installation:\n'
              'xattr -cr /Applications/Heimdallm.app',
        );
        return false;
    }
  }

  Future<bool> _validatePendingUpdate(ApiClient api) async {
    final expected = _pendingUpdateVersion;
    if (expected == null) return true;
    try {
      // Native recovery first authenticates the sealed daemon version/PID,
      // confirms bootstrap, waits for exact health, and only then releases the
      // lease. Asking /health before this call deadlocks a deliberately sealed
      // replacement daemon in its minimal bootstrap router.
      await _platform.completeAppUpdate();
    } catch (e) {
      _setError(
        title: 'Update acknowledgement failed',
        details:
            'Heimdallm could not verify daemon version $expected and release '
            'the protected update lease: $e',
        hint:
            'Keep Heimdallm open and retry. Do not force quit while update '
            'recovery is pending.',
      );
      return false;
    }

    final health = await api.fetchHealth();
    final actual = health?['version']?.toString();
    if (actual != expected) {
      _setError(
        title: 'Update validation failed',
        details:
            'The app was updated to $expected, but the running daemon reports '
            '${actual ?? 'no version'}. Heimdallm will not continue with a '
            'mixed-version installation.',
        hint:
            'Quit Heimdallm, stop the daemon, then reopen the app. If the '
            'versions still differ, reinstall the latest signed release.',
      );
      return false;
    }
    _pendingUpdateVersion = null;
    return true;
  }

  void _setSpawnFailureError(Object? error) {
    _setError(
      title: 'Could not start daemon',
      details: error?.toString() ?? 'The daemon process could not be launched.',
      hint:
          'Check that Heimdallm has permission to run sub-processes.\n'
          'Try: xattr -cr /Applications/Heimdallm.app',
    );
  }

  void _setForeignPortError(int port) {
    if (port <= 0) {
      _setError(
        title: 'Daemon endpoint unavailable',
        details:
            'Heimdallm could not identify a daemon at the configured API '
            'endpoint. No local process was started.',
        hint: 'Check the daemon or reverse-proxy logs, then retry.',
      );
      return;
    }
    _setError(
      title: 'Port $port is already occupied',
      details:
          'Heimdallm could not prove that its port is free. Another '
          'process is listening, or a service on the port did not answer the '
          'health check. No daemon was started, to avoid duplicate workers.',
      hint:
          'Find the process and stop it, then relaunch:\n'
          'lsof -nP -iTCP:$port -sTCP:LISTEN',
    );
  }

  void _setStatus(String s) {
    if (mounted) setState(() => _status = s);
  }

  void _setError({
    required String title,
    required String details,
    String? hint,
  }) {
    if (mounted) {
      setState(() {
        _errorTitle = title;
        _errorDetails = details;
        _errorHint = hint;
      });
    }
  }

  void _go(String location) {
    if (!mounted) return;
    widget.appRouter.go(location);
    setState(() => _destination = location);
  }

  @override
  Widget build(BuildContext context) {
    if (_destination != null) {
      return HeimdallmApp(router: widget.appRouter);
    }
    if (_errorTitle != null) {
      return _ErrorApp(
        title: _errorTitle!,
        details: _errorDetails ?? '',
        hint: _errorHint,
        onRetry: () {
          setState(() {
            _errorTitle = null;
            _errorDetails = null;
            _errorHint = null;
          });
          _boot();
        },
        onQuit: _platform.quitApp,
      );
    }
    return _SplashApp(status: _status);
  }
}

class _SplashApp extends StatelessWidget {
  final String status;
  const _SplashApp({required this.status});

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      debugShowCheckedModeBanner: false,
      theme: ThemeData(
        colorScheme: ColorScheme.fromSeed(seedColor: const Color(0xFF0969DA)),
        useMaterial3: true,
      ),
      darkTheme: ThemeData(
        colorScheme: ColorScheme.fromSeed(
          seedColor: const Color(0xFF0969DA),
          brightness: Brightness.dark,
        ),
        useMaterial3: true,
      ),
      home: Scaffold(
        body: Center(
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              Image.asset(
                'assets/icon.png',
                width: 96,
                height: 96,
                errorBuilder: (_, _, _) => const Icon(Icons.shield, size: 96),
              ),
              const SizedBox(height: 24),
              const Text(
                'Heimdallm',
                style: TextStyle(fontSize: 28, fontWeight: FontWeight.bold),
              ),
              const SizedBox(height: 20),
              const SizedBox(
                width: 24,
                height: 24,
                child: CircularProgressIndicator(strokeWidth: 2.5),
              ),
              const SizedBox(height: 12),
              Text(status, style: const TextStyle(color: Colors.grey)),
            ],
          ),
        ),
      ),
    );
  }
}

class _ErrorApp extends StatelessWidget {
  final String title;
  final String details;
  final String? hint;
  final VoidCallback onRetry;
  final VoidCallback onQuit;

  const _ErrorApp({
    required this.title,
    required this.details,
    this.hint,
    required this.onRetry,
    required this.onQuit,
  });

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      debugShowCheckedModeBanner: false,
      theme: ThemeData(
        colorScheme: ColorScheme.fromSeed(seedColor: const Color(0xFF0969DA)),
        useMaterial3: true,
      ),
      darkTheme: ThemeData(
        colorScheme: ColorScheme.fromSeed(
          seedColor: const Color(0xFF0969DA),
          brightness: Brightness.dark,
        ),
        useMaterial3: true,
      ),
      home: Scaffold(
        body: Center(
          child: ConstrainedBox(
            constraints: const BoxConstraints(maxWidth: 480),
            child: Padding(
              padding: const EdgeInsets.all(32),
              child: Column(
                mainAxisSize: MainAxisSize.min,
                children: [
                  const Icon(Icons.error_outline, size: 56, color: Colors.red),
                  const SizedBox(height: 20),
                  Text(
                    title,
                    style: const TextStyle(
                      fontSize: 20,
                      fontWeight: FontWeight.bold,
                    ),
                    textAlign: TextAlign.center,
                  ),
                  const SizedBox(height: 12),
                  Text(
                    details,
                    style: const TextStyle(color: Colors.grey, fontSize: 13),
                    textAlign: TextAlign.center,
                  ),
                  if (hint != null) ...[
                    const SizedBox(height: 20),
                    Container(
                      width: double.infinity,
                      padding: const EdgeInsets.all(12),
                      decoration: BoxDecoration(
                        color: Colors.orange.withValues(alpha: 0.1),
                        border: Border.all(
                          color: Colors.orange.withValues(alpha: 0.4),
                        ),
                        borderRadius: BorderRadius.circular(8),
                      ),
                      child: Text(
                        hint!,
                        style: const TextStyle(
                          fontSize: 12,
                          fontFamily: 'monospace',
                        ),
                        textAlign: TextAlign.left,
                      ),
                    ),
                  ],
                  const SizedBox(height: 28),
                  Row(
                    mainAxisAlignment: MainAxisAlignment.center,
                    children: [
                      OutlinedButton(
                        onPressed: onQuit,
                        child: const Text('Quit'),
                      ),
                      const SizedBox(width: 12),
                      FilledButton.icon(
                        icon: const Icon(Icons.refresh, size: 16),
                        label: const Text('Retry'),
                        onPressed: onRetry,
                      ),
                    ],
                  ),
                ],
              ),
            ),
          ),
        ),
      ),
    );
  }
}

class HeimdallmApp extends StatelessWidget {
  final String initialLocation;
  final GoRouter? router;
  const HeimdallmApp({super.key, this.initialLocation = '/', this.router});

  @override
  Widget build(BuildContext context) {
    return MaterialApp.router(
      title: 'Heimdallm',
      debugShowCheckedModeBanner: false,
      theme: ThemeData(
        colorScheme: ColorScheme.fromSeed(seedColor: const Color(0xFF0969DA)),
        useMaterial3: true,
      ),
      darkTheme: ThemeData(
        colorScheme: ColorScheme.fromSeed(
          seedColor: const Color(0xFF0969DA),
          brightness: Brightness.dark,
        ),
        useMaterial3: true,
      ),
      routerConfig: router ?? createRouter(initialLocation: initialLocation),
    );
  }
}

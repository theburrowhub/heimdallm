import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'core/api/api_client.dart';
import 'core/models/config_model.dart';
import 'core/platform/platform_services.dart';
import 'core/platform/platform_services_provider.dart';
import 'shared/router.dart';

/// Global router — accessible via the container so the tray menu +
/// notification handlers can push routes without a BuildContext.
final _appRouter = createRouter(initialLocation: '/');
GoRouter get appRouter => _appRouter;

Future<void> main() async {
  WidgetsFlutterBinding.ensureInitialized();

  final platform = PlatformServices.create();

  if (!await platform.ensureSingleInstance()) {
    platform.quitApp();
  }

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

  runApp(ProviderScope(
    overrides: [
      platformServicesProvider.overrideWithValue(platform),
    ],
    child: _BootstrapApp(appRouter: _appRouter),
  ));
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

/// A non-Heimdallm process holds the daemon port. Distinct from a spawn failure
/// because no amount of retrying fixes it — the user has to free the port.
class _ForeignPortException implements Exception {}

class _BootstrapApp extends ConsumerStatefulWidget {
  final GoRouter appRouter;
  const _BootstrapApp({required this.appRouter});
  @override
  ConsumerState<_BootstrapApp> createState() => _BootstrapAppState();
}

class _BootstrapAppState extends ConsumerState<_BootstrapApp> {
  String? _destination;
  String _status = 'Starting…';
  String? _errorTitle;
  String? _errorDetails;
  String? _errorHint;

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
    final api = ApiClient(platform: _platform);

    if (await api.checkHealth()) {
      _go('/');
      return;
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
        repoConfigs: {for (final r in repos) r: const RepoConfig(prEnabled: true)},
        localDirBase: localDirBase,
      );
      await _platform.storeGitHubToken(token);
      await _platform.writeDaemonConfig(config);
    }

    final binaryPath = _platform.defaultDaemonBinaryPath();
    if (binaryPath == null) {
      _setError(
        title: 'Daemon binary not found',
        details: 'Heimdallm could not locate its background service.\n'
            'This usually means the installation is incomplete.',
        hint: 'If you installed from a DMG, open Terminal and run:\n'
            'xattr -cr /Applications/Heimdallm.app\n'
            'then relaunch the app.',
      );
      return;
    }

    _setStatus('Starting Heimdallm…');
    try {
      await _spawnDaemonUnlessRunning(api, binaryPath);
    } on _ForeignPortException {
      _setForeignPortError(api.daemonPort);
      return;
    } catch (e) {
      _setError(
        title: 'Could not start daemon',
        details: e.toString(),
        hint: 'Check that Heimdallm has permission to run sub-processes.\n'
            'Try: xattr -cr /Applications/Heimdallm.app',
      );
      return;
    }

    _setStatus('Waiting for Heimdallm…');
    await _waitForHealth(api, retryBinary: binaryPath);
  }

  /// Spawns the daemon only when nothing is answering on its port.
  ///
  /// The single most important invariant of the boot path: never add a second
  /// daemon. A daemon that loses the port bind used to stay alive and keep
  /// polling GitHub on the same token, so every redundant spawn permanently
  /// multiplied API consumption until the hourly quota was gone (#646). The
  /// daemon now exits on a failed bind, but the app must not rely on that —
  /// older daemons are still out there, and not spawning is free.
  ///
  /// Reachability, not health, is the right question here: see
  /// [ApiClient.daemonReachable].
  /// Returns true when a daemon was actually spawned, false when the port was
  /// already taken. Throws [_ForeignPortException] when a process that is not
  /// Heimdallm holds the port — spawning would just fail on the bind, so that
  /// case has to reach the user rather than be retried in silence.
  Future<bool> _spawnDaemonUnlessRunning(ApiClient api, String binaryPath) async {
    switch (await api.daemonReachable()) {
      case PortOwner.daemon:
        return false;
      case PortOwner.foreign:
        throw _ForeignPortException();
      case PortOwner.none:
        await _platform.spawnDaemon(binaryPath);
        return true;
    }
  }

  Future<void> _waitForHealth(ApiClient api, {String? retryBinary}) async {
    const maxDaemonRestarts = 3;
    // How many spawn windows to allow a daemon that owns the port but is not
    // healthy before offering the user a way out. A "starting" daemon clears in
    // seconds; a rate-limited one can stay degraded for hours, and blocking the
    // splash on that locks the user out of the app entirely.
    const maxDegradedWindows = 3;
    var daemonRestarts = 0;
    var degradedWindows = 0;
    for (var attempt = 0; ; attempt++) {
      await Future.delayed(const Duration(milliseconds: 400));
      if (await api.checkHealth()) {
        _go('/');
        return;
      }
      if (attempt > 0 && attempt % 25 == 0 && retryBinary != null) {
        // Check the budget BEFORE spawning: counting the spawn first and then
        // testing the limit fires the terminal error immediately after the last
        // daemon was launched, denying it the health window the retry exists to
        // provide.
        if (daemonRestarts >= maxDaemonRestarts) {
          _setError(
            title: 'Daemon failed to start',
            details: 'Heimdallm could not start after $maxDaemonRestarts attempts.',
            hint: 'Try restarting the app. If the problem persists, check your installation:\n'
                'xattr -cr /Applications/Heimdallm.app',
          );
          return;
        }

        // A daemon that owns the port but answers 503 keeps failing
        // checkHealth: it is degraded (rate-limited, stale last_poll) or still
        // wiring up, not absent. Re-spawning on that signal is what stacked
        // five daemons on one token in #646, so we skip the spawn — and must
        // not spend the restart budget on a spawn that never happened, or the
        // user gets a terminal "failed to start" for a daemon that is running.
        final bool spawned;
        try {
          spawned = await _spawnDaemonUnlessRunning(api, retryBinary);
        } on _ForeignPortException {
          _setForeignPortError(api.daemonPort);
          return;
        } catch (_) {
          continue;
        }

        if (!spawned) {
          degradedWindows++;
          if (degradedWindows >= maxDegradedWindows) {
            _setDegradedDaemonError();
            return;
          }
          // Tell the user what we are actually waiting on, instead of leaving
          // the splash on a generic message while nothing appears to happen.
          _setStatus('Heimdallm is running but not ready yet — waiting…');
          continue;
        }
        daemonRestarts++;
      }
    }
  }

  void _setForeignPortError(int port) {
    _setError(
      title: 'Port $port is in use by another process',
      details: 'Something is answering on Heimdallm\'s port, but it is not the '
          'Heimdallm daemon. Heimdallm will not start a daemon that cannot bind '
          'the port.',
      hint: 'Find the process and stop it, then relaunch:\n'
          'lsof -nP -iTCP:$port -sTCP:LISTEN',
    );
  }

  /// The daemon owns the port and answers, but never reports healthy. Most
  /// often a GitHub rate-limit episode leaving `last_poll` stale, which can
  /// last hours — so give the user an exit instead of an endless splash.
  void _setDegradedDaemonError() {
    _setError(
      title: 'Heimdallm is running but not ready',
      details: 'The daemon is answering but reports itself unhealthy. This is '
          'usually a GitHub rate-limit episode leaving the last poll stale, '
          'which can take a while to clear.',
      hint: 'Retry to keep waiting, or check the daemon log:\n'
          '~/.local/share/heimdallm/heimdallm.log',
    );
  }

  void _setStatus(String s) { if (mounted) setState(() => _status = s); }
  void _setError({required String title, required String details, String? hint}) {
    if (mounted) {
      setState(() { _errorTitle = title; _errorDetails = details; _errorHint = hint; });
    }
  }
  void _go(String location) { if (mounted) setState(() => _destination = location); }

  @override
  Widget build(BuildContext context) {
    if (_destination != null) {
      return HeimdallmApp(router: widget.appRouter, initialLocation: _destination!);
    }
    if (_errorTitle != null) {
      return _ErrorApp(
        title: _errorTitle!,
        details: _errorDetails ?? '',
        hint: _errorHint,
        onRetry: () {
          setState(() { _errorTitle = null; _errorDetails = null; _errorHint = null; });
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
              Image.asset('assets/icon.png', width: 96, height: 96,
                  errorBuilder: (_, _, _) => const Icon(Icons.shield, size: 96)),
              const SizedBox(height: 24),
              const Text('Heimdallm',
                  style: TextStyle(fontSize: 28, fontWeight: FontWeight.bold)),
              const SizedBox(height: 20),
              const SizedBox(
                width: 24, height: 24,
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
                  Text(title,
                      style: const TextStyle(fontSize: 20, fontWeight: FontWeight.bold),
                      textAlign: TextAlign.center),
                  const SizedBox(height: 12),
                  Text(details,
                      style: const TextStyle(color: Colors.grey, fontSize: 13),
                      textAlign: TextAlign.center),
                  if (hint != null) ...[
                    const SizedBox(height: 20),
                    Container(
                      width: double.infinity,
                      padding: const EdgeInsets.all(12),
                      decoration: BoxDecoration(
                        color: Colors.orange.withValues(alpha: 0.1),
                        border: Border.all(color: Colors.orange.withValues(alpha: 0.4)),
                        borderRadius: BorderRadius.circular(8),
                      ),
                      child: Text(hint!,
                          style: const TextStyle(fontSize: 12, fontFamily: 'monospace'),
                          textAlign: TextAlign.left),
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

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'core/api/api_client.dart';
import 'core/models/config_model.dart';
import 'core/platform/platform_services.dart';
import 'core/platform/platform_services_provider.dart';
import 'core/setup/repo_discovery.dart';
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
      final repos = await RepoDiscovery.discoverFromPRs(token);

      _setStatus('Setting up…');
      final config = AppConfig(
        repoConfigs: {for (final r in repos) r: const RepoConfig(prEnabled: true)},
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
      await _platform.spawnDaemon(binaryPath);
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

  Future<void> _waitForHealth(ApiClient api, {String? retryBinary}) async {
    const maxDaemonRestarts = 3;
    var daemonRestarts = 0;
    for (var attempt = 0; ; attempt++) {
      await Future.delayed(const Duration(milliseconds: 400));
      if (await api.checkHealth()) {
        _go('/');
        return;
      }
      if (attempt > 0 && attempt % 25 == 0 && retryBinary != null) {
        if (daemonRestarts >= maxDaemonRestarts) {
          _setError(
            title: 'Daemon failed to start',
            details: 'Heimdallm could not start after $maxDaemonRestarts attempts.',
            hint: 'Try restarting the app. If the problem persists, check your installation:\n'
                'xattr -cr /Applications/Heimdallm.app',
          );
          return;
        }
        daemonRestarts++;
        try { await _platform.spawnDaemon(retryBinary); } catch (_) {}
      }
    }
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
                  errorBuilder: (_, __, ___) => const Icon(Icons.shield, size: 96)),
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

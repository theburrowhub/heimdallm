import 'dart:io';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:local_notifier/local_notifier.dart';
import 'package:tray_manager/tray_manager.dart';
import 'package:window_manager/window_manager.dart';
import 'core/api/api_client.dart';
import 'core/daemon/daemon_lifecycle.dart';
import 'core/platform/platform_services.dart';
import 'core/setup/first_run_setup.dart';
import 'core/setup/repo_discovery.dart';
import 'core/models/config_model.dart';
import 'core/tray/tray_menu.dart';
import 'shared/router.dart';

/// Global router — accessible by tray menu and notification handlers.
final _appRouter = createRouter(initialLocation: '/');
GoRouter get appRouter => _appRouter;

/// Platform services singleton — initialised once in main() before any
/// ApiClient is created. Task 10 will fully refactor this into the widget tree.
late final PlatformServices _bootPlatform;

/// Checks for a running instance using a PID file.
/// If another instance is found, sends it SIGUSR1 (which shows its window)
/// and returns false so this new process can exit cleanly.
Future<bool> _ensureSingleInstance() async {
  final home = Platform.environment['HOME'] ?? '';
  final dir  = Directory('$home/.local/share/heimdallm');
  await dir.create(recursive: true);
  final pidFile = File('${dir.path}/ui.pid');

  if (await pidFile.exists()) {
    final existing = int.tryParse((await pidFile.readAsString()).trim());
    if (existing != null && existing != pid) {
      // kill -0 = existence check (no actual kill, just verifies the process is alive)
      final check = await Process.run('kill', ['-0', '$existing']);
      if (check.exitCode == 0) {
        // Another instance is running — signal it to show its window, then exit.
        debugPrint('Another Heimdallm instance is running (PID $existing), signalling it.');
        await Process.run('kill', ['-USR1', '$existing']);
        return false;
      }
      // Stale PID file — that process is gone, continue normally.
    }
  }

  await pidFile.writeAsString('$pid');
  return true;
}

/// Activates the window when the app receives SIGUSR1 from another
/// instance that tried to start. Called once during main() setup.
void _listenForActivationSignal() {
  ProcessSignal.sigusr1.watch().listen((_) async {
    try {
      await windowManager.show();
      await windowManager.focus();
    } catch (_) {}
  });
}

void main() async {
  WidgetsFlutterBinding.ensureInitialized();

  // Platform abstraction — used by ApiClient and DaemonLifecycle below.
  // Task 10 will move this into the widget tree properly.
  _bootPlatform = PlatformServices.create();

  // Single-instance guard: works even outside the .app bundle (debug / direct binary).
  if (!await _ensureSingleInstance()) {
    exit(0);
  }

  // Listen for SIGUSR1 so that a second launch attempt activates this window.
  _listenForActivationSignal();

  // Catch all init errors so a crash shows a window rather than silently dying.
  FlutterError.onError = (details) {
    debugPrint('Flutter error: ${details.exceptionAsString()}');
    FlutterError.presentError(details);
  };

  try {
    await _setupWindow();
  } catch (e) {
    debugPrint('window_manager init failed: $e');
    // Continue without custom window options — better than a blank crash
  }

  try {
    await _setupTray();
  } catch (e) {
    debugPrint('tray_manager init failed: $e');
  }

  try {
    await localNotifier.setup(appName: 'Heimdallm');
  } catch (e) {
    debugPrint('local_notifier init failed: $e');
  }

  runApp(ProviderScope(child: _BootstrapApp(appRouter: _appRouter)));
}

Future<void> _setupWindow() async {
  await windowManager.ensureInitialized();

  const options = WindowOptions(
    size: Size(1200, 720),
    minimumSize: Size(900, 520),
    title: 'Heimdallm',
    titleBarStyle: TitleBarStyle.normal,
  );

  // waitUntilReadyToShow may not fire in production bundles launched
  // outside of 'flutter run'. We call show() directly and also hook
  // the callback as a belt-and-suspenders measure.
  await windowManager.setSize(const Size(1200, 720));
  await windowManager.setMinimumSize(const Size(900, 520));
  await windowManager.setTitle('Heimdallm');
  await windowManager.show();
  await windowManager.focus();

  // Also register the callback in case the above calls happen too early
  windowManager.waitUntilReadyToShow(options, () async {
    await windowManager.show();
    await windowManager.focus();
  });
}

Future<void> _setupTray() async {
  await trayManager.setIcon(
    Platform.isLinux ? 'assets/tray_icon@2x.png' : 'assets/tray_icon.png',
  );
  // Initial minimal menu until data loads
  await trayManager.setContextMenu(Menu(items: [
    MenuItem(key: 'open', label: 'Open Heimdallm'),
    MenuItem.separator(),
    MenuItem(key: 'quit', label: 'Quit'),
  ]));
  // Pass the shared ApiClient so TrayMenu uses the same token cache
  // as the rest of the app — avoids stale-token issues after rotation.
  TrayMenu.instance.init(apiClient: ApiClient(platform: _bootPlatform), onNavigate: (_) {});
}

/// Send a macOS notification from the Flutter app (correct icon, clickable).
/// [prId] is the store PR id to navigate to on click; null = just open the app.
void sendPRNotification({
  required String title,
  required String body,
  int? prId,
}) {
  final n = LocalNotification(title: title, body: body);
  n.onShow = () {};
  n.onClick = () {
    windowManager.show();
    windowManager.focus();
    if (prId != null) {
      _appRouter.go('/prs/$prId');
    }
  };
  n.show();
}

class _BootstrapApp extends StatefulWidget {
  final GoRouter appRouter;
  const _BootstrapApp({required this.appRouter});
  @override
  State<_BootstrapApp> createState() => _BootstrapAppState();
}

class _BootstrapAppState extends State<_BootstrapApp> with WindowListener {
  String? _destination;
  String _status = 'Starting…';
  String? _errorTitle;   // non-null = show error screen instead of spinner
  String? _errorDetails;
  String? _errorHint;

  @override
  void initState() {
    super.initState();
    windowManager.addListener(this);
    // setPreventClose must be called after the first frame so the window is ready.
    WidgetsBinding.instance.addPostFrameCallback((_) {
      windowManager.setPreventClose(true);
    });
    _boot();
  }

  @override
  void dispose() {
    windowManager.removeListener(this);
    super.dispose();
  }

  /// When the user clicks the window's close (✕) button, hide to tray instead
  /// of quitting. The daemon keeps running. Use "Quit" in the tray menu to exit.
  @override
  void onWindowClose() async {
    if (await windowManager.isPreventClose()) {
      await windowManager.hide();
    }
  }

  Future<void> _boot() async {
    final api = ApiClient(platform: _bootPlatform);

    // ── 1. Daemon already healthy? ───────────────────────────────────────
    if (await api.checkHealth()) {
      _go('/');
      return;
    }

    // ── 2. Get token ─────────────────────────────────────────────────────
    _setStatus('Detecting credentials…');
    final token = await FirstRunSetup.detectToken();

    if (token == null) {
      // No token anywhere → user must configure manually
      _go('/config');
      return;
    }

    // ── 3. Write config if it doesn't exist yet ──────────────────────────
    if (!await FirstRunSetup.configExists()) {
      _setStatus('Discovering repositories…');
      final repos = await RepoDiscovery.discoverFromPRs(token);

      _setStatus('Setting up…');
      final config = AppConfig(
        repoConfigs: {
          for (final r in repos) r: const RepoConfig(prEnabled: true),
        },
      );
      await FirstRunSetup.storeToken(token);
      await FirstRunSetup.writeConfig(config);
    }

    // ── 4. Locate daemon binary ───────────────────────────────────────────
    final binaryPath = _daemonBinaryPath();
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

    // ── 5. Launch daemon ──────────────────────────────────────────────────
    _setStatus('Starting Heimdallm…');
    try {
      // Use detached mode so the daemon process outlives the Flutter process.
      // This ensures reviews in progress survive window hides and dev restarts.
      await Process.start(binaryPath, [], mode: ProcessStartMode.detached);
    } catch (e) {
      _setError(
        title: 'Could not start daemon',
        details: e.toString(),
        hint: 'Check that Heimdallm has permission to run sub-processes.\n'
            'Try: xattr -cr /Applications/Heimdallm.app',
      );
      return;
    }

    // ── 6. Wait for daemon to become healthy ──────────────────────────────
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
      // Re-launch every 10 seconds in case the daemon crashed at startup,
      // but only up to maxDaemonRestarts times to avoid accumulating zombies.
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
        try { await Process.start(retryBinary, [], mode: ProcessStartMode.detached); } catch (_) {}
      }
    }
  }

  /// Returns the daemon binary path, or null if not found.
  /// Delegates to DaemonLifecycle.defaultBinaryPath() — single source of truth.
  String? _daemonBinaryPath() => DaemonLifecycle.defaultBinaryPath();

  void _setStatus(String s) {
    if (mounted) setState(() => _status = s);
  }

  void _setError({required String title, required String details, String? hint}) {
    if (mounted) {
      setState(() {
        _errorTitle   = title;
        _errorDetails = details;
        _errorHint    = hint;
      });
    }
  }

  void _go(String location) {
    if (mounted) setState(() => _destination = location);
  }

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

  const _ErrorApp({
    required this.title,
    required this.details,
    this.hint,
    required this.onRetry,
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
                      style: const TextStyle(
                          fontSize: 20, fontWeight: FontWeight.bold),
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
                          style: const TextStyle(
                              fontSize: 12, fontFamily: 'monospace'),
                          textAlign: TextAlign.left),
                    ),
                  ],
                  const SizedBox(height: 28),
                  Row(
                    mainAxisAlignment: MainAxisAlignment.center,
                    children: [
                      OutlinedButton(
                        onPressed: () => exit(0),
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

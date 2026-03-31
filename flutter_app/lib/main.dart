import 'dart:io';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:local_notifier/local_notifier.dart';
import 'package:tray_manager/tray_manager.dart';
import 'package:window_manager/window_manager.dart';
import 'core/api/api_client.dart';
import 'core/setup/first_run_setup.dart';
import 'core/setup/repo_discovery.dart';
import 'core/models/config_model.dart';
import 'core/tray/tray_menu.dart';
import 'shared/router.dart';

/// Global router — accessible by tray menu and notification handlers.
final _appRouter = createRouter(initialLocation: '/');
GoRouter get appRouter => _appRouter;

void main() async {
  WidgetsFlutterBinding.ensureInitialized();
  await _setupWindow();
  await _setupTray();
  await localNotifier.setup(appName: 'Heimdallr');
  runApp(ProviderScope(child: _BootstrapApp(appRouter: _appRouter)));
}

Future<void> _setupWindow() async {
  await windowManager.ensureInitialized();
  const options = WindowOptions(
    size: Size(1200, 720),
    minimumSize: Size(900, 520),
    title: 'Heimdallr',
    titleBarStyle: TitleBarStyle.normal,
  );
  await windowManager.waitUntilReadyToShow(options, () async {
    await windowManager.show();
    await windowManager.focus();
  });
}

Future<void> _setupTray() async {
  await trayManager.setIcon('assets/tray_icon.png');
  // Initial minimal menu until data loads
  await trayManager.setContextMenu(Menu(items: [
    MenuItem(key: 'open', label: 'Open Heimdallr'),
    MenuItem.separator(),
    MenuItem(key: 'quit', label: 'Quit'),
  ]));
  TrayMenu.instance.init();
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

class _BootstrapAppState extends State<_BootstrapApp> {
  String? _destination; // null = still booting
  String _status = 'Starting…';

  @override
  void initState() {
    super.initState();
    _boot();
  }

  Future<void> _boot() async {
    final api = ApiClient();

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
          for (final r in repos) r: const RepoConfig(monitored: true),
        },
      );
      await FirstRunSetup.storeToken(token);
      await FirstRunSetup.writeConfig(config);
    }

    // ── 4. Start daemon if binary exists ─────────────────────────────────
    final binaryPath = _daemonBinaryPath();
    if (binaryPath != null && File(binaryPath).existsSync()) {
      _setStatus('Starting Heimdallr…');
      try {
        await Process.start(binaryPath, []);
      } catch (_) {}
    }

    // ── 5. Wait indefinitely for daemon to be healthy ────────────────────
    //      The daemon may take a moment to bind to the port.
    //      Show the splash with the icon until it responds.
    _setStatus('Waiting for Heimdallr…');
    for (var attempt = 0; ; attempt++) {
      await Future.delayed(const Duration(milliseconds: 400));
      if (await api.checkHealth()) {
        _go('/');
        return;
      }
      // Every 10 seconds try re-launching (in case it crashed at start)
      if (attempt > 0 && attempt % 25 == 0 && binaryPath != null) {
        try { await Process.start(binaryPath, []); } catch (_) {}
      }
    }
  }

  /// Returns the daemon binary path, or null if not determinable.
  String? _daemonBinaryPath() {
    final env = Platform.environment['HEIMDALLR_DAEMON_PATH'];
    if (env != null && env.isNotEmpty) return env;
    final dir = File(Platform.resolvedExecutable).parent.path;
    final candidate = '$dir/heimdallr';
    return File(candidate).existsSync() ? candidate : null;
  }

  void _setStatus(String s) {
    if (mounted) setState(() => _status = s);
  }

  void _go(String location) {
    if (mounted) setState(() => _destination = location);
  }

  @override
  Widget build(BuildContext context) {
    if (_destination != null) {
      // Use the shared router (supports deep-link from notifications)
      return HeimdallrApp(router: widget.appRouter, initialLocation: _destination!);
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
              const Text('Heimdallr',
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

class HeimdallrApp extends StatelessWidget {
  final String initialLocation;
  final GoRouter? router;
  const HeimdallrApp({super.key, this.initialLocation = '/', this.router});

  @override
  Widget build(BuildContext context) {
    return MaterialApp.router(
      title: 'Heimdallr',
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

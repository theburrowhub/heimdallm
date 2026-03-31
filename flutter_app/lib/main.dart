import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'core/daemon/daemon_lifecycle.dart';
import 'shared/router.dart';

void main() async {
  WidgetsFlutterBinding.ensureInitialized();
  await _ensureDaemonRunning();
  runApp(const ProviderScope(child: HeimdallrApp()));
}

Future<void> _ensureDaemonRunning() async {
  try {
    final lifecycle = DaemonLifecycle(
      daemonBinaryPath: DaemonLifecycle.defaultBinaryPath(),
    );
    await lifecycle.ensureRunning();
  } catch (e) {
    // Log and continue — user will see error state in UI
    debugPrint('Daemon start failed: $e');
  }
}

class HeimdallrApp extends StatelessWidget {
  const HeimdallrApp({super.key});

  @override
  Widget build(BuildContext context) {
    return MaterialApp.router(
      title: 'Heimdallr',
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
      routerConfig: appRouter,
    );
  }
}

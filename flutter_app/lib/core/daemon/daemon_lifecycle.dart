import 'dart:io';
import '../api/api_client.dart';
import '../platform/platform_services.dart';

class DaemonLifecycle {
  final int port;
  final String daemonBinaryPath;
  final ApiClient _client;
  Process? _process;

  DaemonLifecycle({
    this.port = 7842,
    required this.daemonBinaryPath,
    ApiClient? client,
    required PlatformServices platform,
  }) : _client = client ?? ApiClient(platform: platform);

  Future<bool> isRunning() => _client.checkHealth();

  Future<void> ensureRunning() async {
    if (await isRunning()) return;

    final binary = File(daemonBinaryPath);
    if (!binary.existsSync()) {
      throw DaemonException('Daemon binary not found: $daemonBinaryPath');
    }

    _process = await Process.start(daemonBinaryPath, []);

    for (var i = 0; i < 50; i++) {
      await Future.delayed(const Duration(milliseconds: 100));
      if (await isRunning()) return;
    }
    throw DaemonException('Daemon did not become healthy within 5 seconds');
  }

  Future<void> stop() async {
    _process?.kill();
    _process = null;
  }

  /// Returns the daemon binary path, or null if not found.
  ///
  /// Priority:
  ///   1. HEIMDALLM_DAEMON_PATH env var  (set by `make dev`)
  ///   2. 'heimdalld' next to the Flutter binary  (production .app bundle)
  ///
  /// IMPORTANT: 'heimdallm' is NOT used as a fallback because on macOS APFS
  /// (case-insensitive) 'heimdallm' resolves to 'Heimdallm' — the Flutter app
  /// binary itself. Using it as a spawn target creates an infinite fork bomb.
  static String? defaultBinaryPath() {
    // 1. Explicit override (make dev)
    final envPath = Platform.environment['HEIMDALLM_DAEMON_PATH'];
    if (envPath != null && envPath.isNotEmpty) {
      return File(envPath).existsSync() ? envPath : null;
    }

    // 2. Bundle-embedded daemon (production)
    final dir = File(Platform.resolvedExecutable).parent.path;
    final bundled = File('$dir/heimdalld');
    if (bundled.existsSync()) return bundled.path;

    return null; // not found — caller shows error, never spawns self
  }
}

class DaemonException implements Exception {
  final String message;
  DaemonException(this.message);
  @override
  String toString() => 'DaemonException: $message';
}

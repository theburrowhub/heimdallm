import 'dart:io';
import '../api/api_client.dart';

class DaemonLifecycle {
  final int port;
  final String daemonBinaryPath;
  final ApiClient _client;
  Process? _process;

  DaemonLifecycle({
    this.port = 7842,
    required this.daemonBinaryPath,
    ApiClient? client,
  }) : _client = client ?? ApiClient(port: port);

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

  /// Returns the daemon binary path.
  /// Priority:
  ///   1. HEIMDALLR_DAEMON_PATH env var (set by `make dev` for local dev)
  ///   2. 'heimdalld' next to the Flutter binary (production .app bundle)
  ///      NOTE: named 'heimdalld' — not 'heimdallr' — to avoid overwriting
  ///      Flutter's 'Heimdallr' binary on case-insensitive APFS filesystems.
  ///   3. 'heimdallr' fallback (legacy / dev builds)
  static String defaultBinaryPath() {
    final envPath = Platform.environment['HEIMDALLR_DAEMON_PATH'];
    if (envPath != null && envPath.isNotEmpty) return envPath;
    final dir = File(Platform.resolvedExecutable).parent.path;
    final preferred = File('$dir/heimdalld');
    if (preferred.existsSync()) return preferred.path;
    return '$dir/heimdallr'; // fallback for make dev
  }
}

class DaemonException implements Exception {
  final String message;
  DaemonException(this.message);
  @override
  String toString() => 'DaemonException: $message';
}

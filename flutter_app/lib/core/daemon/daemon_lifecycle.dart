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
  ///   2. Next to the app executable (production .app bundle)
  static String defaultBinaryPath() {
    final envPath = Platform.environment['HEIMDALLR_DAEMON_PATH'];
    if (envPath != null && envPath.isNotEmpty) return envPath;
    return '${File(Platform.resolvedExecutable).parent.path}/heimdallr';
  }
}

class DaemonException implements Exception {
  final String message;
  DaemonException(this.message);
  @override
  String toString() => 'DaemonException: $message';
}

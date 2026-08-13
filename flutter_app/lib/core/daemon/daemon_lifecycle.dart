import 'dart:io';

/// Locates the daemon binary. Spawning itself lives in
/// `PlatformServices.spawnDaemon`, and the single guarded startup policy lives
/// in `DaemonStartupCoordinator`.
///
/// This class used to also own an `ensureRunning()` with its own health check,
/// but nothing called it — the boot path had grown a second, unguarded spawn
/// instead, which is how five daemons ended up sharing one GitHub token and
/// exhausting the API quota (#646). Keeping a single spawn guard means a future
/// caller cannot pick the wrong one.
class DaemonLifecycle {
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

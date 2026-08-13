import '../api/api_client.dart';
import '../platform/platform_services.dart';

/// Outcomes from the single guarded daemon-start path used by both initial
/// bootstrap and retries.
enum DaemonStartupOutcome {
  spawned,
  daemonPresent,
  portOccupied,
  spawnFailedRetryable,
  spawnFailedTerminal,
  spawnBudgetExhausted,
}

class DaemonStartupResult {
  const DaemonStartupResult(this.outcome, {this.error});

  final DaemonStartupOutcome outcome;
  final Object? error;
}

/// Serializes daemon spawning and bounds every retry path.
///
/// The safety invariant is intentionally strict: [PlatformServices.spawnDaemon]
/// is called only after [ApiClient.daemonReachable] proves the TCP port is
/// closed. A healthy, degraded, starting, silent or ambiguously reachable
/// process therefore never causes a second daemon to be launched (#646).
class DaemonStartupCoordinator {
  DaemonStartupCoordinator({
    required this.api,
    required this.platform,
    required this.binaryPath,
    this.maxSpawnAttempts = 4,
  }) : assert(maxSpawnAttempts > 0);

  final ApiClient api;
  final PlatformServices platform;
  final String binaryPath;
  final int maxSpawnAttempts;

  int _spawnAttempts = 0;
  Future<DaemonStartupResult>? _inFlight;

  int get spawnAttempts => _spawnAttempts;

  Future<DaemonStartupResult> ensureAvailable() {
    final running = _inFlight;
    if (running != null) return running;

    late final Future<DaemonStartupResult> pending;
    pending = _ensureAvailable().whenComplete(() {
      if (identical(_inFlight, pending)) _inFlight = null;
    });
    _inFlight = pending;
    return pending;
  }

  Future<DaemonStartupResult> _ensureAvailable() async {
    // Probe ownership before consulting the spawn budget. A slow daemon from a
    // previous attempt may now own the port and answer 503; reporting
    // "failed to start" in that state is both wrong and a temptation to spawn
    // yet another process.
    switch (await api.daemonReachable()) {
      case PortOwner.daemon:
        return const DaemonStartupResult(DaemonStartupOutcome.daemonPresent);
      case PortOwner.foreign:
        return const DaemonStartupResult(DaemonStartupOutcome.portOccupied);
      case PortOwner.none:
        break;
    }

    if (_spawnAttempts >= maxSpawnAttempts) {
      return const DaemonStartupResult(
        DaemonStartupOutcome.spawnBudgetExhausted,
      );
    }

    // Count before spawning so a deterministic Process.start failure cannot
    // leave the splash in an unbounded retry loop.
    _spawnAttempts++;
    try {
      await platform.spawnDaemon(binaryPath);
      return const DaemonStartupResult(DaemonStartupOutcome.spawned);
    } on DaemonPortOccupiedException catch (error) {
      return DaemonStartupResult(
        DaemonStartupOutcome.portOccupied,
        error: error,
      );
    } catch (error) {
      return DaemonStartupResult(
        _spawnAttempts >= maxSpawnAttempts
            ? DaemonStartupOutcome.spawnFailedTerminal
            : DaemonStartupOutcome.spawnFailedRetryable,
        error: error,
      );
    }
  }
}

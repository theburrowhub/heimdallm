import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../../core/api/api_client.dart';
import '../../core/daemon/daemon_startup.dart';
import '../../core/platform/platform_services_provider.dart';
import '../../shared/widgets/toast.dart';
import '../activity/activity_providers.dart';
import '../config/config_providers.dart';
import '../dashboard/dashboard_providers.dart';
import '../issues/issues_providers.dart';

// ── Constants ─────────────────────────────────────────────────────────────────

const kDaemonStartHealthMaxAttempts = 80;
const kDaemonStartHealthInterval = Duration(milliseconds: 100);

@visibleForTesting
String daemonStartupFailureMessage(DaemonStartupResult result, int port) {
  switch (result.outcome) {
    case DaemonStartupOutcome.portOccupied:
      return 'Port $port is occupied; no daemon was started.';
    case DaemonStartupOutcome.daemonPresent:
      return 'Heimdallm is already running but is not healthy yet.';
    case DaemonStartupOutcome.spawnFailedRetryable:
    case DaemonStartupOutcome.spawnFailedTerminal:
      return 'Could not start Heimdallm: ${result.error ?? 'unknown error'}';
    case DaemonStartupOutcome.spawnBudgetExhausted:
      return 'Heimdallm exhausted its guarded start attempts.';
    case DaemonStartupOutcome.spawned:
      return '';
  }
}

/// Waits for the daemon to relinquish its port, not merely to become
/// unhealthy. A degraded daemon answers /health with 503; treating that as
/// "stopped" lets a restart race its still-running predecessor (#646).
Future<PortOwner> waitForDaemonPortRelease(
  ApiClient api, {
  List<Duration> delays = const [
    Duration(milliseconds: 200),
    Duration(milliseconds: 300),
    Duration(milliseconds: 500),
    Duration(milliseconds: 800),
    Duration(milliseconds: 1200),
    Duration(seconds: 2),
  ],
}) async {
  var owner = PortOwner.daemon;
  for (final delay in delays) {
    await Future<void>.delayed(delay);
    owner = await api.daemonReachable();
    if (owner != PortOwner.daemon) return owner;
  }
  return owner;
}

// ── Shared helpers ────────────────────────────────────────────────────────────

Future<void> confirmShutdown(BuildContext context, WidgetRef ref) async {
  final confirmed = await showDialog<bool>(
    context: context,
    builder: (context) => AlertDialog(
      title: const Text('Stop Server?'),
      content: const Text('Active reviews may be interrupted.'),
      actions: [
        TextButton(
          onPressed: () => Navigator.of(context).pop(false),
          child: const Text('Cancel'),
        ),
        FilledButton(
          onPressed: () => Navigator.of(context).pop(true),
          child: const Text('Stop Server'),
        ),
      ],
    ),
  );
  if (confirmed != true || !context.mounted) return;

  try {
    await ref.read(apiClientProvider).shutdownDaemon();
    if (!context.mounted) return;
    showToast(context, 'Shutdown requested');
    ref.invalidate(sseStreamProvider);
    ref.invalidate(daemonHealthProvider);
    await refreshWhenDaemonStops(context, ref);
  } catch (e) {
    if (context.mounted) showToast(context, 'Error: $e', isError: true);
  }
}

Future<void> startDaemon(BuildContext context, WidgetRef ref) async {
  if (ref.read(daemonStartingProvider)) return;

  final platform = ref.read(platformServicesProvider);
  final binaryPath = platform.defaultDaemonBinaryPath();
  if (binaryPath == null || binaryPath.isEmpty) {
    showToast(context, 'Daemon binary not found', isError: true);
    return;
  }

  ref.read(daemonStartingProvider.notifier).set(true);
  try {
    final api = ref.read(apiClientProvider);
    final startup = DaemonStartupCoordinator(
      api: api,
      platform: platform,
      binaryPath: binaryPath,
      maxSpawnAttempts: 1,
    );
    final result = await startup.ensureAvailable();
    if (result.outcome == DaemonStartupOutcome.daemonPresent) {
      _invalidateDashboardData(ref);
      if (context.mounted) showToast(context, 'Server is already running');
      return;
    }
    if (result.outcome != DaemonStartupOutcome.spawned) {
      if (context.mounted) {
        showToast(
          context,
          daemonStartupFailureMessage(result, api.daemonPort),
          isError: true,
        );
      }
      return;
    }
    var healthy = false;
    for (var i = 0; i < kDaemonStartHealthMaxAttempts; i++) {
      await Future<void>.delayed(kDaemonStartHealthInterval);
      healthy = await api.checkHealth();
      if (healthy) break;
    }
    _invalidateDashboardData(ref);
    if (!context.mounted) return;
    if (healthy) {
      showToast(context, 'Server started');
    } else {
      showToast(
        context,
        'Heimdallm could not start. Check the app installation.',
        isError: true,
      );
    }
  } catch (e) {
    if (context.mounted) showToast(context, 'Error: $e', isError: true);
  } finally {
    ref.read(daemonStartingProvider.notifier).set(false);
  }
}

Future<PortOwner?> refreshWhenDaemonStops(
  BuildContext context,
  WidgetRef ref, {
  List<Duration> delays = const [
    Duration(milliseconds: 200),
    Duration(milliseconds: 300),
    Duration(milliseconds: 500),
    Duration(milliseconds: 800),
    Duration(milliseconds: 1200),
    Duration(seconds: 2),
  ],
}) async {
  final api = ref.read(apiClientProvider);
  final owner = await waitForDaemonPortRelease(api, delays: delays);
  if (!context.mounted) return null;
  ref.invalidate(daemonHealthProvider);
  _invalidateDashboardData(ref);
  return owner;
}

/// Stop the daemon and immediately respawn it. Used by the Server screen's
/// Restart banner after a Listen URL change.
Future<void> restartDaemon(
  BuildContext context,
  WidgetRef ref, {
  List<Duration> portReleaseDelays = const [
    Duration(milliseconds: 200),
    Duration(milliseconds: 300),
    Duration(milliseconds: 500),
    Duration(milliseconds: 800),
    Duration(milliseconds: 1200),
    Duration(seconds: 2),
  ],
}) async {
  if (ref.read(daemonStartingProvider)) return;

  final api = ref.read(apiClientProvider);
  final platform = ref.read(platformServicesProvider);
  ref.read(daemonStartingProvider.notifier).set(true);
  try {
    await api.shutdownDaemon();
    if (!context.mounted) return;
    showToast(context, 'Restarting…');
    final stoppedAs = await refreshWhenDaemonStops(
      context,
      ref,
      delays: portReleaseDelays,
    );
    if (!context.mounted) return;
    if (stoppedAs == PortOwner.foreign) {
      showToast(
        context,
        'The daemon stopped, but port ${api.daemonPort} is now occupied; restart cancelled.',
        isError: true,
      );
      return;
    }
    DaemonStartupResult result;
    if (stoppedAs == PortOwner.daemon) {
      // The old process may still be draining, or a service supervisor may
      // already have replaced it. Either way, a daemon owns the port and is the
      // only safe process to reuse; never race it with another child.
      result = const DaemonStartupResult(DaemonStartupOutcome.daemonPresent);
    } else {
      final binary = platform.defaultDaemonBinaryPath();
      if (binary == null || binary.isEmpty) {
        showToast(context, 'Daemon binary not found', isError: true);
        return;
      }
      final startup = DaemonStartupCoordinator(
        api: api,
        platform: platform,
        binaryPath: binary,
        maxSpawnAttempts: 1,
      );
      result = await startup.ensureAvailable();
    }
    if (result.outcome != DaemonStartupOutcome.spawned &&
        result.outcome != DaemonStartupOutcome.daemonPresent) {
      if (context.mounted) {
        showToast(
          context,
          daemonStartupFailureMessage(result, api.daemonPort),
          isError: true,
        );
      }
      return;
    }
    var healthy = false;
    for (var i = 0; i < kDaemonStartHealthMaxAttempts; i++) {
      await Future<void>.delayed(kDaemonStartHealthInterval);
      healthy = await api.checkHealth();
      if (healthy) break;
    }
    if (!context.mounted) return;
    showToast(
      context,
      healthy ? 'Server restarted' : 'Restart timed out',
      isError: !healthy,
    );
  } catch (e) {
    if (context.mounted) showToast(context, 'Error: $e', isError: true);
  } finally {
    ref.read(daemonStartingProvider.notifier).set(false);
  }
}

// ── Private helpers ───────────────────────────────────────────────────────────

void _invalidateDashboardData(WidgetRef ref) {
  ref.invalidate(sseStreamProvider);
  ref.invalidate(daemonHealthProvider);
  ref.invalidate(prsProvider);
  ref.invalidate(issuesProvider);
  ref.invalidate(statsProvider);
  ref.invalidate(githubRateLimitProvider);
  ref.invalidate(activityEntriesProvider);
  ref.invalidate(activityOptionsProvider);
}

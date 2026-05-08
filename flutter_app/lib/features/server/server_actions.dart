import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../../core/platform/platform_services_provider.dart';
import '../../shared/widgets/toast.dart';
import '../activity/activity_providers.dart';
import '../config/config_providers.dart';
import '../dashboard/dashboard_providers.dart';
import '../issues/issues_providers.dart';

// ── Constants ─────────────────────────────────────────────────────────────────

const kDaemonStartHealthMaxAttempts = 80;
const kDaemonStartHealthInterval = Duration(milliseconds: 100);

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

  ref.read(daemonStartingProvider.notifier).state = true;
  try {
    await platform.spawnDaemon(binaryPath);
    final api = ref.read(apiClientProvider);
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
    ref.read(daemonStartingProvider.notifier).state = false;
  }
}

Future<void> refreshWhenDaemonStops(
  BuildContext context,
  WidgetRef ref,
) async {
  final api = ref.read(apiClientProvider);
  const delays = [
    Duration(milliseconds: 200),
    Duration(milliseconds: 300),
    Duration(milliseconds: 500),
    Duration(milliseconds: 800),
    Duration(milliseconds: 1200),
    Duration(seconds: 2),
  ];

  for (final delay in delays) {
    await Future<void>.delayed(delay);
    if (!context.mounted) return;
    final healthy = await api.checkHealth();
    if (!context.mounted) return;
    ref.invalidate(daemonHealthProvider);
    if (!healthy) break;
  }

  if (!context.mounted) return;
  _invalidateDashboardData(ref);
}

/// Stop the daemon and immediately respawn it. Used by the Server screen's
/// Restart banner after a Listen URL change.
Future<void> restartDaemon(BuildContext context, WidgetRef ref) async {
  final api = ref.read(apiClientProvider);
  ref.read(daemonStartingProvider.notifier).state = true;
  try {
    await api.shutdownDaemon();
    if (!context.mounted) return;
    showToast(context, 'Restarting…');
    await refreshWhenDaemonStops(context, ref);
    // refreshWhenDaemonStops returns once /health reports unreachable.
    if (!context.mounted) return;
    final platform = ref.read(platformServicesProvider);
    final binary = platform.defaultDaemonBinaryPath();
    if (binary == null || binary.isEmpty) {
      showToast(context, 'Daemon binary not found', isError: true);
      return;
    }
    await platform.spawnDaemon(binary);
    var healthy = false;
    for (var i = 0; i < kDaemonStartHealthMaxAttempts; i++) {
      await Future<void>.delayed(kDaemonStartHealthInterval);
      healthy = await api.checkHealth();
      if (healthy) break;
    }
    if (!context.mounted) return;
    showToast(context, healthy ? 'Server restarted' : 'Restart timed out',
        isError: !healthy);
  } catch (e) {
    if (context.mounted) showToast(context, 'Error: $e', isError: true);
  } finally {
    ref.read(daemonStartingProvider.notifier).state = false;
  }
}

// ── Private helpers ───────────────────────────────────────────────────────────

void _invalidateDashboardData(WidgetRef ref) {
  ref.invalidate(sseStreamProvider);
  ref.invalidate(daemonHealthProvider);
  ref.invalidate(prsProvider);
  ref.invalidate(issuesProvider);
  ref.invalidate(statsProvider);
  ref.invalidate(activityEntriesProvider);
  ref.invalidate(activityOptionsProvider);
}

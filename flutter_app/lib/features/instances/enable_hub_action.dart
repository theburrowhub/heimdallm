import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../core/instances/instances_providers.dart';
import '../../core/instances/models.dart';
import '../../core/models/config_model.dart';
import '../../core/platform/platform_services_provider.dart';
import '../../shared/widgets/toast.dart';
import '../config/config_providers.dart';
import '../server/server_actions.dart' as server_actions;

/// Turns this daemon into a cluster hub: persists `cluster.role = "hub"` and
/// offers the restart that actually activates it.
///
/// Single implementation on purpose. Three places need this (the Config
/// screen's Cluster section, the Instances tab's call-to-action banner, and
/// the add-instance dialog's error recovery), and three copies of "which key,
/// which value, and does it need a restart" would be three chances to get the
/// restart wrong.
///
/// Returns true if the role was saved (whether or not the restart itself
/// completed — the daemon start/stop lifecycle already reports its own
/// outcome via toasts).
Future<bool> enableHubMode(BuildContext context, WidgetRef ref) async {
  final confirmed = await showDialog<bool>(
    context: context,
    builder: (context) => AlertDialog(
      title: const Text('Make this daemon a cluster hub?'),
      content: const Text(
        'A hub manages other Heimdallm daemons: it registers them, routes '
        'organizations and repositories to them, and can push the same '
        'configuration to all of them. The setting is saved immediately, but '
        'the hub only starts serving once the daemon restarts.',
      ),
      actions: [
        TextButton(
          onPressed: () => Navigator.of(context).pop(false),
          child: const Text('Cancel'),
        ),
        FilledButton(
          onPressed: () => Navigator.of(context).pop(true),
          child: const Text('Enable and restart'),
        ),
      ],
    ),
  );
  if (confirmed != true || !context.mounted) return false;

  try {
    final AppConfig? cached = ref.read(configNotifierProvider).value;
    // `cached ?? await ...` does not promote `config` to non-nullable here (a
    // known analyzer limitation with an awaited RHS), which then rejects the
    // unconditional `.copyWith` below — hence the explicit ternary.
    // ignore: prefer_if_null_operators
    final AppConfig config = cached == null
        ? await ref.read(configNotifierProvider.future)
        : cached;
    if (!context.mounted) return false;
    final updated = config.copyWith(clusterRole: ClusterRole.hub);
    await ref.read(configNotifierProvider.notifier).save(updated);
  } catch (e) {
    if (context.mounted) showToast(context, 'Error: $e', isError: true);
    return false;
  }
  if (!context.mounted) return false;

  showToast(context, 'Saved (restart required)');

  // Some installs (e.g. containerised/managed daemons) have no binary this
  // app can respawn — restartDaemon would otherwise leave the operator on an
  // unexplained "Daemon binary not found" toast with no next step.
  final binaryPath = ref
      .read(platformServicesProvider)
      .defaultDaemonBinaryPath();
  if (binaryPath == null || binaryPath.isEmpty) {
    if (context.mounted) {
      showToast(context, 'Restart Heimdallm for hub mode to take effect');
    }
  } else {
    await server_actions.restartDaemon(context, ref);
  }

  ref.invalidate(daemonInstancesProvider);
  ref.invalidate(routingRulesProvider);
  return true;
}

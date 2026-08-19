import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../core/platform/platform_services.dart';
import '../../core/platform/platform_services_provider.dart';

final appUpdateStatusProvider = StreamProvider<AppUpdateStatus>((ref) async* {
  final platform = ref.watch(platformServicesProvider);
  yield platform.appUpdateStatus;
  yield* platform.appUpdateEvents;
});

/// Compact app-bar entry point. When an update is known, the same control
/// installs it instead of starting another network check.
class CheckForUpdatesButton extends ConsumerWidget {
  const CheckForUpdatesButton({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final platform = ref.watch(platformServicesProvider);
    if (platform.appUpdateSupport != AppUpdateSupport.native) {
      return const SizedBox.shrink();
    }
    final status =
        ref.watch(appUpdateStatusProvider).value ?? platform.appUpdateStatus;
    final available = status.updateAvailable;
    final busy = status.busy;
    return IconButton(
      key: const Key('check-for-updates'),
      tooltip: available
          ? 'Update to ${status.version ?? 'the latest version'}'
          : 'Check for updates',
      onPressed: busy
          ? null
          : () => _run(
              context,
              available
                  ? platform.installAppUpdate
                  : platform.checkForAppUpdates,
            ),
      icon: busy
          ? const SizedBox(
              width: 20,
              height: 20,
              child: CircularProgressIndicator(strokeWidth: 2),
            )
          : Icon(
              available ? Icons.system_update : Icons.system_update_alt,
              color: available ? Theme.of(context).colorScheme.primary : null,
            ),
    );
  }

  Future<void> _run(
    BuildContext context,
    Future<void> Function() action,
  ) async {
    try {
      await action();
    } catch (error) {
      if (!context.mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(
        SnackBar(content: Text('Could not update Heimdallm: $error')),
      );
    }
  }
}

/// Persistent, non-modal update notice shared by macOS and Linux.
class AppUpdateBanner extends ConsumerWidget {
  const AppUpdateBanner({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final platform = ref.watch(platformServicesProvider);
    if (platform.appUpdateSupport != AppUpdateSupport.native) {
      return const SizedBox.shrink();
    }
    final status =
        ref.watch(appUpdateStatusProvider).value ?? platform.appUpdateStatus;
    if (!status.updateAvailable &&
        status.phase != AppUpdatePhase.installing &&
        status.phase != AppUpdatePhase.restarting) {
      return const SizedBox.shrink();
    }

    final theme = Theme.of(context);
    final busy =
        status.phase == AppUpdatePhase.installing ||
        status.phase == AppUpdatePhase.restarting;
    return Material(
      key: const Key('app-update-banner'),
      color: theme.colorScheme.primaryContainer,
      child: SafeArea(
        bottom: false,
        child: Padding(
          padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 8),
          child: Row(
            children: [
              if (busy)
                const SizedBox(
                  width: 18,
                  height: 18,
                  child: CircularProgressIndicator(strokeWidth: 2),
                )
              else
                Icon(Icons.system_update, color: theme.colorScheme.primary),
              const SizedBox(width: 10),
              Expanded(
                child: Text(
                  status.message ??
                      'Heimdallm ${status.version ?? ''} is available.',
                  style: theme.textTheme.bodyMedium?.copyWith(
                    fontWeight: FontWeight.w600,
                  ),
                ),
              ),
              if (!busy)
                FilledButton.icon(
                  key: const Key('install-app-update'),
                  onPressed: () async {
                    try {
                      await platform.installAppUpdate();
                    } catch (error) {
                      if (!context.mounted) return;
                      ScaffoldMessenger.of(context).showSnackBar(
                        SnackBar(content: Text('Update failed: $error')),
                      );
                    }
                  },
                  icon: const Icon(Icons.restart_alt),
                  label: const Text('Update now'),
                ),
            ],
          ),
        ),
      ),
    );
  }
}

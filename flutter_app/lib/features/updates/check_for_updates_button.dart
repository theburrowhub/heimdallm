import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../core/platform/platform_services.dart';
import '../../core/platform/platform_services_provider.dart';

final appUpdateStatusProvider = StreamProvider<AppUpdateStatus>((ref) async* {
  final platform = ref.watch(platformServicesProvider);
  yield platform.appUpdateStatus;
  yield* platform.appUpdateEvents;
});

final appVersionProvider = FutureProvider<AppVersionInfo>((ref) {
  return ref.watch(platformServicesProvider).loadAppVersion();
});

/// Compact app-bar entry point. When an update is known, the same control
/// installs it instead of starting another network check.
class CheckForUpdatesButton extends ConsumerWidget {
  const CheckForUpdatesButton({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final platform = ref.watch(platformServicesProvider);
    if (platform.appUpdateSupport != AppUpdateSupport.native) {
      return IconButton(
        key: const Key('check-for-updates'),
        tooltip:
            platform.appUpdateUnavailableReason ?? 'Updates are unavailable',
        onPressed: null,
        icon: const Icon(Icons.system_update_alt),
      );
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

/// Global Settings surface for application version and update management.
///
/// This card is intentionally present even when native updates are unavailable:
/// unsupported deployments must explain why instead of making the feature
/// disappear. macOS ad-hoc builds use the normal native-update path.
class AppUpdateSettingsCard extends ConsumerWidget {
  const AppUpdateSettingsCard({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final platform = ref.watch(platformServicesProvider);
    final status =
        ref.watch(appUpdateStatusProvider).value ?? platform.appUpdateStatus;
    final version = ref.watch(appVersionProvider);
    final native = platform.appUpdateSupport == AppUpdateSupport.native;
    final theme = Theme.of(context);
    final presentation = _settingsPresentation(
      status: status,
      native: native,
      unavailableReason: platform.appUpdateUnavailableReason,
    );

    return SizedBox(
      width: double.infinity,
      child: Card(
        key: const Key('app-update-settings'),
        margin: const EdgeInsets.only(bottom: 12),
        child: Padding(
          padding: const EdgeInsets.all(14),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Row(
                children: [
                  const Expanded(
                    child: Text(
                      'Application updates',
                      style: TextStyle(
                        fontWeight: FontWeight.w600,
                        fontSize: 15,
                      ),
                    ),
                  ),
                  version.when(
                    data: (info) => Text(
                      'Version ${info.displayVersion}',
                      key: const Key('current-app-version'),
                      style: theme.textTheme.bodySmall?.copyWith(
                        color: theme.colorScheme.onSurfaceVariant,
                      ),
                    ),
                    loading: () =>
                        Text('Version…', style: theme.textTheme.bodySmall),
                    error: (_, _) => Text(
                      'Version unavailable',
                      style: theme.textTheme.bodySmall,
                    ),
                  ),
                ],
              ),
              const SizedBox(height: 12),
              Row(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  if (status.busy)
                    const SizedBox(
                      width: 20,
                      height: 20,
                      child: CircularProgressIndicator(strokeWidth: 2),
                    )
                  else
                    Icon(
                      presentation.icon,
                      size: 20,
                      color: presentation.color(theme.colorScheme),
                    ),
                  const SizedBox(width: 10),
                  Expanded(
                    child: Column(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        Text(
                          presentation.title,
                          key: const Key('app-update-status'),
                          style: const TextStyle(
                            fontWeight: FontWeight.w600,
                            fontSize: 13,
                          ),
                        ),
                        if (presentation.detail != null) ...[
                          const SizedBox(height: 3),
                          Text(
                            presentation.detail!,
                            style: theme.textTheme.bodySmall?.copyWith(
                              color: theme.colorScheme.onSurfaceVariant,
                            ),
                          ),
                        ],
                      ],
                    ),
                  ),
                  if (native) ...[
                    const SizedBox(width: 12),
                    FilledButton.icon(
                      key: const Key('settings-update-action'),
                      onPressed: status.busy
                          ? null
                          : () => _runSettingsAction(
                              context,
                              status.updateAvailable
                                  ? platform.installAppUpdate
                                  : platform.checkForAppUpdates,
                            ),
                      icon: Icon(
                        status.updateAvailable
                            ? Icons.restart_alt
                            : Icons.refresh,
                        size: 18,
                      ),
                      label: Text(
                        status.updateAvailable
                            ? 'Update now'
                            : 'Check for updates',
                      ),
                    ),
                  ],
                ],
              ),
            ],
          ),
        ),
      ),
    );
  }

  Future<void> _runSettingsAction(
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

class _UpdateSettingsPresentation {
  const _UpdateSettingsPresentation({
    required this.icon,
    required this.title,
    this.detail,
    required this.color,
  });

  final IconData icon;
  final String title;
  final String? detail;
  final Color Function(ColorScheme colors) color;
}

_UpdateSettingsPresentation _settingsPresentation({
  required AppUpdateStatus status,
  required bool native,
  required String? unavailableReason,
}) {
  if (!native) {
    return _UpdateSettingsPresentation(
      icon: Icons.info_outline,
      title: 'Updates unavailable',
      detail:
          unavailableReason ??
          'Updates are managed by this installation\'s package or deployment.',
      color: (colors) => colors.onSurfaceVariant,
    );
  }
  switch (status.phase) {
    case AppUpdatePhase.idle:
      final message = status.message?.trim();
      return _UpdateSettingsPresentation(
        icon: message == null || message.isEmpty
            ? Icons.system_update_alt
            : Icons.check_circle_outline,
        title: message == null || message.isEmpty
            ? 'Ready to check for updates'
            : message,
        detail: 'Automatic updates are enabled for this signed build.',
        color: (colors) => message == null || message.isEmpty
            ? colors.onSurfaceVariant
            : Colors.green,
      );
    case AppUpdatePhase.checking:
      return _UpdateSettingsPresentation(
        icon: Icons.refresh,
        title: status.message ?? 'Checking for updates…',
        color: (colors) => colors.primary,
      );
    case AppUpdatePhase.available:
      return _UpdateSettingsPresentation(
        icon: Icons.system_update,
        title:
            status.message ??
            'Heimdallm ${status.version ?? ''} is ready to install.',
        color: (colors) => colors.primary,
      );
    case AppUpdatePhase.installing:
      return _UpdateSettingsPresentation(
        icon: Icons.downloading,
        title: status.message ?? 'Installing the Heimdallm update…',
        color: (colors) => colors.primary,
      );
    case AppUpdatePhase.restarting:
      return _UpdateSettingsPresentation(
        icon: Icons.restart_alt,
        title: status.message ?? 'Restarting Heimdallm…',
        color: (colors) => colors.primary,
      );
    case AppUpdatePhase.error:
      return _UpdateSettingsPresentation(
        icon: Icons.error_outline,
        title: status.message ?? 'Could not check for updates.',
        detail: 'Try checking again.',
        color: (colors) => colors.error,
      );
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

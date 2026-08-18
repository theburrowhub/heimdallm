import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../core/platform/platform_services.dart';
import '../../core/platform/platform_services_provider.dart';

/// Manual entry point for Sparkle. Scheduled checks remain native so they keep
/// working while the main Heimdallm window is hidden in the tray.
class CheckForUpdatesButton extends ConsumerStatefulWidget {
  const CheckForUpdatesButton({super.key});

  @override
  ConsumerState<CheckForUpdatesButton> createState() =>
      _CheckForUpdatesButtonState();
}

class _CheckForUpdatesButtonState extends ConsumerState<CheckForUpdatesButton> {
  bool _checking = false;

  @override
  Widget build(BuildContext context) {
    final platform = ref.watch(platformServicesProvider);
    if (platform.appUpdateSupport != AppUpdateSupport.native) {
      return const SizedBox.shrink();
    }
    return IconButton(
      key: const Key('check-for-updates'),
      tooltip: 'Check for updates',
      onPressed: _checking ? null : () => _check(platform),
      icon: _checking
          ? const SizedBox(
              width: 20,
              height: 20,
              child: CircularProgressIndicator(strokeWidth: 2),
            )
          : const Icon(Icons.system_update_alt),
    );
  }

  Future<void> _check(PlatformServices platform) async {
    setState(() => _checking = true);
    try {
      await platform.checkForAppUpdates();
    } catch (error) {
      if (!mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(
        SnackBar(content: Text('Could not check for updates: $error')),
      );
    } finally {
      if (mounted) setState(() => _checking = false);
    }
  }
}

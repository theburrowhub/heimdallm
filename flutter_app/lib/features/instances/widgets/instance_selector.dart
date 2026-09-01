import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../../core/instances/instances_providers.dart';
import '../../../core/instances/models.dart';

/// AppBar control for scoping the dashboard to one instance, or to all of them.
///
/// Renders nothing unless more than one instance is registered: a single
/// instance is indistinguishable from a plain single-daemon install, and a
/// dropdown with one entry is just clutter.
class InstanceSelector extends ConsumerWidget {
  const InstanceSelector({super.key});

  static const allValue = '__all__';
  static const manageValue = '__manage__';

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final registry =
        ref.watch(daemonInstancesProvider).value ?? ClusterRegistry.empty;
    if (!registry.isMultiInstance) return const SizedBox.shrink();

    final active = ref.watch(activeInstanceProvider);
    final scheme = Theme.of(context).colorScheme;

    return Padding(
      padding: const EdgeInsets.symmetric(horizontal: 4),
      child: PopupMenuButton<String>(
        tooltip: 'Instance scope',
        position: PopupMenuPosition.under,
        onSelected: (value) {
          if (value == manageValue) {
            context.push('/instances');
            return;
          }
          ref
              .read(activeInstanceProvider.notifier)
              .select(value == allValue ? null : value);
        },
        itemBuilder: (context) => [
          CheckedPopupMenuItem<String>(
            value: allValue,
            checked: active == null,
            child: Text('All instances (${registry.usable.length})'),
          ),
          const PopupMenuDivider(),
          for (final instance in registry.instances)
            CheckedPopupMenuItem<String>(
              value: instance.id,
              checked: active == instance.id,
              enabled: instance.usable,
              child: Row(
                children: [
                  Icon(
                    instance.reachable
                        ? Icons.circle
                        : Icons.circle_outlined,
                    size: 9,
                    color: instance.reachable ? Colors.green : scheme.error,
                  ),
                  const SizedBox(width: 8),
                  Flexible(
                    child: Text(
                      instance.displayName,
                      overflow: TextOverflow.ellipsis,
                    ),
                  ),
                  if (instance.isSelf) ...[
                    const SizedBox(width: 6),
                    Text(
                      'hub',
                      style: TextStyle(
                        fontSize: 10,
                        color: scheme.onSurfaceVariant,
                      ),
                    ),
                  ],
                ],
              ),
            ),
          const PopupMenuDivider(),
          const PopupMenuItem<String>(
            value: manageValue,
            child: Row(
              children: [
                Icon(Icons.settings_ethernet, size: 18),
                SizedBox(width: 8),
                Text('Manage instances…'),
              ],
            ),
          ),
        ],
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            const Icon(Icons.dns_outlined, size: 18),
            const SizedBox(width: 6),
            ConstrainedBox(
              constraints: const BoxConstraints(maxWidth: 160),
              child: Text(
                active == null
                    ? 'All instances'
                    : (registry.byId(active)?.displayName ?? active),
                overflow: TextOverflow.ellipsis,
                style: const TextStyle(fontSize: 13),
              ),
            ),
            const Icon(Icons.arrow_drop_down, size: 18),
          ],
        ),
      ),
    );
  }
}

/// Banner shown when the dashboard is missing data because an instance could
/// not be read.
///
/// Degrading loudly matters here: a list that silently drops one machine's PRs
/// is indistinguishable from that machine having no work.
class InstanceFailureBanner extends ConsumerWidget {
  const InstanceFailureBanner({super.key, required this.failureLabels});

  final List<String> failureLabels;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    if (failureLabels.isEmpty) return const SizedBox.shrink();
    final scheme = Theme.of(context).colorScheme;
    final names = failureLabels.join(', ');
    return Material(
      color: scheme.errorContainer,
      child: Padding(
        padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 8),
        child: Row(
          children: [
            Icon(Icons.cloud_off_outlined, size: 16, color: scheme.onErrorContainer),
            const SizedBox(width: 8),
            Expanded(
              child: Text(
                failureLabels.length == 1
                    ? 'Showing partial data — $names could not be reached.'
                    : 'Showing partial data — ${failureLabels.length} instances could not be reached ($names).',
                style: TextStyle(fontSize: 12, color: scheme.onErrorContainer),
              ),
            ),
            TextButton(
              onPressed: () => context.push('/instances'),
              child: const Text('Instances'),
            ),
          ],
        ),
      ),
    );
  }
}

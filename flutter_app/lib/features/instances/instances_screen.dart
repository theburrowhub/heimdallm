import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../core/api/api_client.dart';
import '../../core/api/cluster_api.dart';
import '../../core/instances/instances_providers.dart';
import '../../core/instances/models.dart';
import 'config_propagation_dialog.dart';
import 'instance_dialog.dart';

/// Manages the registered Heimdallm instances: health, versions, routing share,
/// and add/edit/remove.
class InstancesScreen extends ConsumerWidget {
  const InstancesScreen({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final registryAsync = ref.watch(daemonInstancesProvider);

    return Scaffold(
      appBar: AppBar(
        title: const Text('Instances'),
        leading: IconButton(
          icon: const Icon(Icons.arrow_back),
          onPressed: () => Navigator.of(context).maybePop(),
        ),
        actions: [
          IconButton(
            tooltip: 'Routing rules',
            icon: const Icon(Icons.alt_route),
            onPressed: () => context.push('/instances/routing'),
          ),
          IconButton(
            tooltip: 'Apply configuration to all instances',
            icon: const Icon(Icons.sync_alt),
            onPressed: () => showConfigPropagationDialog(context, ref),
          ),
          IconButton(
            tooltip: 'Refresh',
            icon: const Icon(Icons.refresh),
            onPressed: () => ref.invalidate(daemonInstancesProvider),
          ),
        ],
      ),
      floatingActionButton: FloatingActionButton.extended(
        onPressed: () => showInstanceDialog(context, ref),
        icon: const Icon(Icons.add),
        label: const Text('Add instance'),
      ),
      body: registryAsync.when(
        loading: () => const Center(child: CircularProgressIndicator()),
        error: (e, _) => Center(child: Text('Could not load instances: $e')),
        data: (registry) => _InstanceList(registry: registry),
      ),
    );
  }
}

/// Same content as [InstancesScreen], embedded directly as a dashboard tab
/// instead of pushed as a route.
///
/// This is the entry point that actually makes instance management
/// reachable: [InstanceSelector]'s "Manage instances…" item only appears
/// once a second instance already exists, which nothing could ever satisfy
/// without a way in. The tab has no such precondition.
class InstancesTabView extends ConsumerWidget {
  const InstancesTabView({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final registryAsync = ref.watch(daemonInstancesProvider);

    return Column(
      children: [
        Padding(
          padding: const EdgeInsets.fromLTRB(12, 8, 8, 4),
          child: Row(
            children: [
              const Spacer(),
              TextButton.icon(
                onPressed: () => showInstanceDialog(context, ref),
                icon: const Icon(Icons.add, size: 18),
                label: const Text('Add instance'),
              ),
              IconButton(
                tooltip: 'Routing rules',
                icon: const Icon(Icons.alt_route),
                onPressed: () => context.push('/instances/routing'),
              ),
              IconButton(
                tooltip: 'Apply configuration to all instances',
                icon: const Icon(Icons.sync_alt),
                onPressed: () => showConfigPropagationDialog(context, ref),
              ),
              IconButton(
                tooltip: 'Refresh',
                icon: const Icon(Icons.refresh),
                onPressed: () => ref.invalidate(daemonInstancesProvider),
              ),
            ],
          ),
        ),
        Expanded(
          child: registryAsync.when(
            loading: () => const Center(child: CircularProgressIndicator()),
            error: (e, _) =>
                Center(child: Text('Could not load instances: $e')),
            data: (registry) => _InstanceList(registry: registry),
          ),
        ),
      ],
    );
  }
}

class _InstanceList extends ConsumerWidget {
  const _InstanceList({required this.registry});

  final ClusterRegistry registry;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    if (registry.instances.isEmpty) {
      return const _EmptyState();
    }
    return ListView.separated(
      padding: const EdgeInsets.fromLTRB(12, 12, 12, 88),
      itemCount: registry.instances.length,
      separatorBuilder: (_, _) => const SizedBox(height: 6),
      itemBuilder: (context, i) =>
          _InstanceCard(instance: registry.instances[i]),
    );
  }
}

class _EmptyState extends StatelessWidget {
  const _EmptyState();

  @override
  Widget build(BuildContext context) {
    return Center(
      child: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 460),
        child: Padding(
          padding: const EdgeInsets.all(24),
          child: Column(
            mainAxisSize: MainAxisSize.min,
            children: [
              const Icon(Icons.dns_outlined, size: 40),
              const SizedBox(height: 12),
              Text(
                'No instances registered',
                style: Theme.of(context).textTheme.titleMedium,
              ),
              const SizedBox(height: 8),
              const Text(
                'An instance is a Heimdallm daemon running on another machine '
                'or in a container. Register one to route organizations and '
                'repositories to it, apply the same configuration everywhere, '
                'and spread reviews and merges across the fleet.',
                textAlign: TextAlign.center,
                style: TextStyle(fontSize: 12),
              ),
            ],
          ),
        ),
      ),
    );
  }
}

class _InstanceCard extends ConsumerWidget {
  const _InstanceCard({required this.instance});

  final DaemonInstance instance;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final scheme = Theme.of(context).colorScheme;
    final state = instance.state;
    final unreachable = state != null && !state.reachable;

    return Card(
      margin: EdgeInsets.zero,
      child: Padding(
        padding: const EdgeInsets.fromLTRB(12, 10, 8, 10),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Icon(
                  unreachable ? Icons.cloud_off_outlined : Icons.dns_outlined,
                  color: unreachable ? scheme.error : null,
                ),
                const SizedBox(width: 10),
                Expanded(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Row(
                        children: [
                          Flexible(
                            child: Text(
                              instance.displayName,
                              style: const TextStyle(
                                fontWeight: FontWeight.w600,
                              ),
                              overflow: TextOverflow.ellipsis,
                            ),
                          ),
                          if (instance.isSelf) ...[
                            const SizedBox(width: 6),
                            _Tag(label: 'hub', color: scheme.primary),
                          ],
                          if (!instance.enabled) ...[
                            const SizedBox(width: 6),
                            _Tag(
                              label: 'disabled',
                              color: scheme.onSurfaceVariant,
                            ),
                          ],
                        ],
                      ),
                      Text(
                        instance.baseUrl,
                        style: TextStyle(
                          fontSize: 11,
                          color: scheme.onSurfaceVariant,
                        ),
                        overflow: TextOverflow.ellipsis,
                      ),
                    ],
                  ),
                ),
                _InstanceMenu(instance: instance),
              ],
            ),
            const SizedBox(height: 8),
            Wrap(
              spacing: 14,
              runSpacing: 4,
              children: [
                _Metric(
                  icon: unreachable
                      ? Icons.error_outline
                      : Icons.check_circle_outline,
                  label: _statusLabel(state),
                  color: unreachable ? scheme.error : null,
                ),
                if (state != null && state.version.isNotEmpty)
                  _Metric(icon: Icons.tag, label: state.version),
                if (state != null && state.uptimeSeconds > 0)
                  _Metric(
                    icon: Icons.schedule,
                    label: 'up ${_formatUptime(state.uptimeSeconds)}',
                  ),
                _Metric(
                  icon: Icons.folder_outlined,
                  label: instance.assignedRepos == 1
                      ? '1 repo routed here'
                      : '${instance.assignedRepos} repos routed here',
                ),
                if (instance.isFallback)
                  const _Metric(
                    icon: Icons.call_split,
                    label: 'owns unrouted repos',
                  ),
                if (instance.inPool)
                  const _Metric(
                    icon: Icons.loop,
                    label: 'in round-robin pool',
                  ),
                for (final label in instance.labels)
                  _Metric(icon: Icons.label_outline, label: label),
              ],
            ),
            if (instance.tokenError.isNotEmpty)
              _Problem(
                text: 'Token unavailable: ${instance.tokenError}',
                color: scheme.error,
              ),
            if (unreachable && state.lastError.isNotEmpty)
              _Problem(
                text: state.consecutiveFailures > 1
                    ? '${state.lastError} (${state.consecutiveFailures} failed probes)'
                    : state.lastError,
                color: scheme.error,
              ),
          ],
        ),
      ),
    );
  }

  static String _statusLabel(InstanceState? state) {
    if (state == null) return 'not probed yet';
    if (!state.reachable) return 'unreachable';
    return state.status.isNotEmpty ? state.status : 'reachable';
  }

  static String _formatUptime(double seconds) {
    final d = Duration(seconds: seconds.round());
    if (d.inDays > 0) return '${d.inDays}d ${d.inHours % 24}h';
    if (d.inHours > 0) return '${d.inHours}h ${d.inMinutes % 60}m';
    if (d.inMinutes > 0) return '${d.inMinutes}m';
    return '${d.inSeconds}s';
  }
}

class _InstanceMenu extends ConsumerWidget {
  const _InstanceMenu({required this.instance});

  final DaemonInstance instance;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    return PopupMenuButton<String>(
      tooltip: 'Instance actions',
      onSelected: (action) => _run(context, ref, action),
      itemBuilder: (context) => [
        const PopupMenuItem(value: 'probe', child: Text('Probe now')),
        const PopupMenuItem(value: 'edit', child: Text('Edit…')),
        PopupMenuItem(
          value: 'toggle',
          child: Text(instance.enabled ? 'Disable' : 'Enable'),
        ),
        // The hub cannot deregister itself: it is the daemon serving this very
        // request, and removing it would leave the app with nothing to talk to.
        if (!instance.isSelf) ...[
          const PopupMenuDivider(),
          const PopupMenuItem(value: 'remove', child: Text('Remove…')),
        ],
      ],
    );
  }

  Future<void> _run(BuildContext context, WidgetRef ref, String action) async {
    final api = ref.read(hubApiClientProvider);
    final messenger = ScaffoldMessenger.of(context);

    try {
      switch (action) {
        case 'probe':
          final state = await api.probeInstance(instance.id);
          messenger.showSnackBar(
            SnackBar(
              content: Text(
                state.reachable
                    ? '${instance.displayName} is reachable'
                    : '${instance.displayName}: ${state.lastError}',
              ),
            ),
          );
        case 'edit':
          if (context.mounted) {
            await showInstanceDialog(context, ref, existing: instance);
          }
        case 'toggle':
          await api.patchInstance(instance.id, enabled: !instance.enabled);
        case 'remove':
          if (!context.mounted) return;
          final confirmed = await _confirmRemoval(context);
          if (confirmed != true) return;
          await api.deleteInstance(instance.id);
          messenger.showSnackBar(
            SnackBar(content: Text('Removed ${instance.displayName}')),
          );
      }
    } on ApiException catch (e) {
      messenger.showSnackBar(SnackBar(content: Text(e.message)));
    } finally {
      ref.invalidate(daemonInstancesProvider);
      ref.invalidate(routingRulesProvider);
    }
  }

  Future<bool?> _confirmRemoval(BuildContext context) {
    return showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: Text('Remove ${instance.displayName}?'),
        content: const Text(
          'The instance keeps running; it is only removed from this hub. '
          'Any organization or repository routed to it is unrouted, so those '
          'repos fall back to the default instance.',
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(context, false),
            child: const Text('Cancel'),
          ),
          FilledButton(
            onPressed: () => Navigator.pop(context, true),
            child: const Text('Remove'),
          ),
        ],
      ),
    );
  }
}

class _Tag extends StatelessWidget {
  const _Tag({required this.label, required this.color});

  final String label;
  final Color color;

  @override
  Widget build(BuildContext context) {
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 5, vertical: 1),
      decoration: BoxDecoration(
        border: Border.all(color: color.withValues(alpha: 0.5)),
        borderRadius: BorderRadius.circular(3),
      ),
      child: Text(label, style: TextStyle(fontSize: 10, color: color)),
    );
  }
}

class _Metric extends StatelessWidget {
  const _Metric({required this.icon, required this.label, this.color});

  final IconData icon;
  final String label;
  final Color? color;

  @override
  Widget build(BuildContext context) {
    final fg = color ?? Theme.of(context).colorScheme.onSurfaceVariant;
    return Row(
      mainAxisSize: MainAxisSize.min,
      children: [
        Icon(icon, size: 13, color: fg),
        const SizedBox(width: 4),
        Text(label, style: TextStyle(fontSize: 11, color: fg)),
      ],
    );
  }
}

class _Problem extends StatelessWidget {
  const _Problem({required this.text, required this.color});

  final String text;
  final Color color;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.only(top: 8),
      child: Text(text, style: TextStyle(fontSize: 11, color: color)),
    );
  }
}

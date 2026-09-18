import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:mix/mix.dart';

import '../../core/api/api_client.dart';
import '../../core/api/cluster_api.dart';
import '../../core/instances/instances_providers.dart';
import '../../core/instances/models.dart';
import '../../shared/design_system/components/components.dart';
import '../../shared/design_system/tokens.dart';
import '../../shared/widgets/restart_required_banner.dart';
import '../config/config_providers.dart';
import '../dashboard/dashboard_providers.dart';
import '../server/server_actions.dart' as server_actions;
import 'config_propagation_dialog.dart';
import 'discovered_peers_section.dart';
import 'enable_hub_action.dart';
import 'instance_dialog.dart';

/// Manages the registered Heimdallm instances: health, versions, routing share,
/// and add/edit/remove.
class InstancesScreen extends ConsumerWidget {
  const InstancesScreen({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final registryAsync = ref.watch(daemonInstancesProvider);
    final isHub = ref.watch(localIsHubProvider);

    return Scaffold(
      appBar: AppBar(
        title: const AppText.sectionTitle('Instances'),
        leading: IconButton(
          icon: const Icon(Icons.arrow_back),
          onPressed: () => Navigator.of(context).maybePop(),
        ),
        actions: [
          IconButton(
            tooltip: isHub == false ? 'Enable hub mode first' : 'Routing rules',
            icon: const Icon(Icons.alt_route),
            onPressed: isHub == false
                ? null
                : () => context.push('/instances/routing'),
          ),
          IconButton(
            tooltip: isHub == false
                ? 'Enable hub mode first'
                : 'Apply configuration to all instances',
            icon: const Icon(Icons.sync_alt),
            onPressed: isHub == false
                ? null
                : () => showConfigPropagationDialog(context, ref),
          ),
          IconButton(
            tooltip: 'Refresh',
            icon: const Icon(Icons.refresh),
            onPressed: () {
              ref.invalidate(daemonInstancesProvider);
              // localClusterRoleProvider is a plain FutureProvider with no
              // polling of its own (same shape as daemonHealthProvider) — if
              // the daemon recovered from being unreachable without going
              // through the restart flow, this is the only thing that
              // notices.
              ref.invalidate(localClusterRoleProvider);
              ref.invalidate(discoveredPeersProvider);
            },
          ),
        ],
      ),
      floatingActionButton: FloatingActionButton.extended(
        onPressed: isHub == false
            ? null
            : () => showInstanceDialog(context, ref),
        icon: const Icon(Icons.add),
        label: const Text('Add instance'),
      ),
      body: Column(
        children: [
          const _HubSetupBanner(),
          Expanded(
            child: registryAsync.when(
              loading: () => const Center(child: CircularProgressIndicator()),
              error: (e, _) =>
                  Center(child: AppText('Could not load instances: $e')),
              data: (registry) => _InstanceList(registry: registry),
            ),
          ),
        ],
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
    final isHub = ref.watch(localIsHubProvider);

    return Column(
      children: [
        const _HubSetupBanner(),
        Padding(
          padding: const EdgeInsets.fromLTRB(12, 8, 8, 4),
          child: Row(
            children: [
              const Spacer(),
              TextButton.icon(
                onPressed: isHub == false
                    ? null
                    : () => showInstanceDialog(context, ref),
                icon: const Icon(Icons.add, size: 18),
                label: const Text('Add instance'),
              ),
              IconButton(
                tooltip: isHub == false
                    ? 'Enable hub mode first'
                    : 'Routing rules',
                icon: const Icon(Icons.alt_route),
                onPressed: isHub == false
                    ? null
                    : () => context.push('/instances/routing'),
              ),
              IconButton(
                tooltip: isHub == false
                    ? 'Enable hub mode first'
                    : 'Apply configuration to all instances',
                icon: const Icon(Icons.sync_alt),
                onPressed: isHub == false
                    ? null
                    : () => showConfigPropagationDialog(context, ref),
              ),
              IconButton(
                tooltip: 'Refresh',
                icon: const Icon(Icons.refresh),
                onPressed: () {
                  ref.invalidate(daemonInstancesProvider);
                  ref.invalidate(localClusterRoleProvider);
                  ref.invalidate(discoveredPeersProvider);
                },
              ),
            ],
          ),
        ),
        Expanded(
          child: registryAsync.when(
            loading: () => const Center(child: CircularProgressIndicator()),
            error: (e, _) =>
                Center(child: AppText('Could not load instances: $e')),
            data: (registry) => _InstanceList(registry: registry),
          ),
        ),
      ],
    );
  }
}

/// Proactive call-to-action shown above both [InstancesScreen] and
/// [InstancesTabView], so the "this daemon isn't a hub" state is visible
/// before the operator ever opens the add-instance dialog and hits a
/// dead end.
///
/// One shared widget rather than one copy per surface, so the two screens
/// cannot silently diverge in how they explain (or fail to explain) the
/// same precondition.
class _HubSetupBanner extends ConsumerWidget {
  const _HubSetupBanner();

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final isHub = ref.watch(localIsHubProvider);
    // Unknown (daemon unreachable, or the health probe hasn't resolved yet)
    // is treated as "not confirmed" rather than "not a hub" — no CTA for
    // someone whose daemon is simply offline or starting up.
    if (isHub == null || isHub) return const SizedBox.shrink();

    final savedRole = ref.watch(configNotifierProvider).value?.clusterRole;
    final padding = const EdgeInsets.fromLTRB(12, 8, 12, 0);

    if (savedRole == ClusterRole.hub) {
      // Enabled once already, but the daemon hasn't been restarted since
      // (or the restart failed) — same restart affordance as the Config
      // screen's Cluster section, not the "enable" card below.
      return Padding(
        padding: padding,
        child: RestartRequiredBanner(
          message:
              'Hub mode is saved but not active yet. Restart the daemon '
              'to start managing instances.',
          onRestart: () => server_actions.restartDaemon(context, ref),
          starting: ref.watch(daemonStartingProvider),
        ),
      );
    }

    return Padding(
      padding: padding,
      child: Box(
        style: BoxStyler()
            .color(
              AppColors.accentMuted.resolve(context).withValues(alpha: 0.20),
            )
            .borderAll(
              color: AppColors.accent.resolve(context).withValues(alpha: 0.22),
              width: 1,
            )
            .borderRadiusAll(AppRadius.md())
            .padding(EdgeInsetsGeometryMix.value(const EdgeInsets.all(12))),
        child: Row(
          children: [
            Icon(
              Icons.hub_outlined,
              size: 18,
              color: AppColors.accent.resolve(context),
            ),
            const SizedBox(width: 8),
            const Expanded(
              child: AppText(
                'This daemon is not a cluster hub. Registering another '
                'Heimdallm instance requires enabling hub mode first.',
              ),
            ),
            FilledButton.icon(
              onPressed: () => enableHubMode(context, ref),
              icon: const Icon(Icons.hub, size: 16),
              label: const Text('Enable clustering'),
            ),
          ],
        ),
      ),
    );
  }
}

class _InstanceList extends ConsumerWidget {
  const _InstanceList({required this.registry});

  final ClusterRegistry registry;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    // The discovered section sits above the registry, and above the empty
    // state too: a fresh hub with nothing registered and a daemon waiting on
    // the LAN is exactly the case this feature exists for, and the old empty
    // state would otherwise hide it behind "register your first instance".
    return ListView(
      padding: const EdgeInsets.fromLTRB(12, 12, 12, 88),
      children: [
        const DiscoveredPeersSection(),
        if (registry.instances.isEmpty)
          const _EmptyState()
        else
          for (final instance in registry.instances) ...[
            _InstanceCard(instance: instance),
            const SizedBox(height: 6),
          ],
      ],
    );
  }
}

class _EmptyState extends ConsumerWidget {
  const _EmptyState();

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    // While not a hub, [_HubSetupBanner] above already carries the full
    // explanation and the "Enable clustering" action — this copy would
    // otherwise both repeat it and invite an action ("Register one") that
    // cannot work yet.
    final isHub = ref.watch(localIsHubProvider);
    final body = isHub == false
        ? 'Enable hub mode above to register your first instance.'
        : 'An instance is a Heimdallm daemon running on another machine '
              'or in a container. Register one to route organizations and '
              'repositories to it, apply the same configuration everywhere, '
              'and spread reviews and merges across the fleet.';

    return Align(
      alignment: Alignment.topCenter,
      child: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 460),
        child: Padding(
          padding: const EdgeInsets.all(24),
          child: AppSurface(
            elevation: AppSurfaceElevation.raised,
            padding: const EdgeInsets.all(24),
            child: Column(
              mainAxisSize: MainAxisSize.min,
              children: [
                Icon(
                  Icons.dns_outlined,
                  size: 40,
                  color: AppColors.textMuted.resolve(context),
                ),
                const SizedBox(height: 12),
                const AppText.sectionTitle('No instances registered'),
                const SizedBox(height: 8),
                AppText(body, textAlign: TextAlign.center),
              ],
            ),
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
    final state = instance.state;
    final unreachable = state != null && !state.reachable;
    final primary = AppColors.accent.resolve(context);
    final muted = AppColors.textMuted.resolve(context);

    return AppSurface(
      elevation: AppSurfaceElevation.surface,
      padding: const EdgeInsets.fromLTRB(12, 10, 8, 10),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Row(
            children: [
              Icon(
                unreachable ? Icons.cloud_off_outlined : Icons.dns_outlined,
                color: unreachable
                    ? AppColors.danger.resolve(context)
                    : primary,
              ),
              const SizedBox(width: 10),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Row(
                      children: [
                        Flexible(
                          child: AppText(
                            instance.displayName,
                            role: AppTextRole.label,
                            color: AppColors.text.resolve(context),
                            maxLines: 1,
                            overflow: TextOverflow.ellipsis,
                          ),
                        ),
                        if (instance.isSelf) ...[
                          const SizedBox(width: 6),
                          _Tag(label: 'hub', color: primary),
                        ],
                        if (!instance.enabled) ...[
                          const SizedBox(width: 6),
                          _Tag(label: 'disabled', color: muted),
                        ],
                      ],
                    ),
                    AppText.muted(
                      instance.baseUrl,
                      maxLines: 1,
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
              AppBadge(
                label: _statusLabel(state),
                foreground: unreachable
                    ? AppColors.danger.resolve(context)
                    : AppColors.success.resolve(context),
                background: (unreachable ? AppColors.danger : AppColors.success)
                    .resolve(context)
                    .withValues(alpha: 0.12),
                border: (unreachable ? AppColors.danger : AppColors.success)
                    .resolve(context)
                    .withValues(alpha: 0.28),
                icon: Icon(
                  unreachable
                      ? Icons.error_outline
                      : Icons.check_circle_outline,
                ),
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
                const _Metric(icon: Icons.loop, label: 'in round-robin pool'),
              for (final label in instance.labels)
                _Metric(icon: Icons.label_outline, label: label),
            ],
          ),
          if (instance.tokenError.isNotEmpty)
            _Problem(
              text: 'Token unavailable: ${instance.tokenError}',
              color: AppColors.danger.resolve(context),
            ),
          if (unreachable && state.lastError.isNotEmpty)
            _Problem(
              text: state.consecutiveFailures > 1
                  ? '${state.lastError} (${state.consecutiveFailures} failed probes)'
                  : state.lastError,
              color: AppColors.danger.resolve(context),
            ),
          // Only renders when the network says this instance moved, so it
          // costs nothing on a healthy fleet.
          AddressChangedBanner(instanceId: instance.id),
        ],
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
        title: AppText.sectionTitle('Remove ${instance.displayName}?'),
        content: const AppText(
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
    return AppBadge(
      label: label,
      foreground: color,
      background: color.withValues(alpha: 0.10),
      border: color.withValues(alpha: 0.28),
      padding: const EdgeInsets.symmetric(horizontal: 5, vertical: 1),
      radius: 6,
      fontSize: 10,
      fontWeight: FontWeight.w700,
      letterSpacing: 0,
    );
  }
}

class _Metric extends StatelessWidget {
  const _Metric({required this.icon, required this.label});

  final IconData icon;
  final String label;

  @override
  Widget build(BuildContext context) {
    final fg = AppColors.textMuted.resolve(context);
    return Row(
      mainAxisSize: MainAxisSize.min,
      children: [
        Icon(icon, size: 13, color: fg),
        const SizedBox(width: 4),
        AppText.label(label, color: fg),
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
      child: AppText(text, role: AppTextRole.bodyMuted, color: color),
    );
  }
}

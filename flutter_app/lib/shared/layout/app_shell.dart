import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../core/instances/instances_providers.dart';
import '../../core/state/sidebar_preferences.dart';
import '../../features/activity/activity_providers.dart';
import '../../features/circuit_breaker/circuit_breaker_banner.dart';
import '../../features/config/config_providers.dart';
import '../../features/dashboard/dashboard_providers.dart';
import '../../features/instances/widgets/instance_selector.dart';
import '../../features/merge_tracking/merge_tracking_providers.dart';
import '../../features/server/server_actions.dart' as server_actions;
import '../../features/updates/check_for_updates_button.dart';
import '../design_system/components/app_icon_button.dart';
import '../design_system/tokens.dart';

/// One destination in the primary navigation (sidebar/rail/drawer).
///
/// [branchIndex] must match the corresponding [StatefulShellBranch]'s
/// position in `router.dart`'s `branches` list.
class AppDestination {
  final int branchIndex;
  final String label;
  final IconData icon;
  final Widget Function(BuildContext context, WidgetRef ref)? badgedIcon;

  const AppDestination({
    required this.branchIndex,
    required this.label,
    required this.icon,
    this.badgedIcon,
  });
}

final appDestinations = <AppDestination>[
  const AppDestination(
    branchIndex: 0,
    label: 'Activity',
    icon: Icons.dashboard,
  ),
  const AppDestination(
    branchIndex: 1,
    label: 'Activity log',
    icon: Icons.timeline,
  ),
  AppDestination(
    branchIndex: 2,
    label: 'My PRs',
    icon: Icons.merge_type,
    badgedIcon: (context, ref) {
      final summary = ref.watch(myPrsSummaryProvider);
      if (summary.isEmpty) return const Icon(Icons.merge_type);
      final scheme = Theme.of(context).colorScheme;
      // Red only when something is broken on the operator's side; a PR that
      // is ready to merge or has merely gone quiet is a nudge, not an alarm.
      return Badge.count(
        count: summary.total,
        backgroundColor: summary.needAction > 0 ? scheme.error : scheme.primary,
        child: const Icon(Icons.merge_type),
      );
    },
  ),
  const AppDestination(
    branchIndex: 3,
    label: 'Repositories',
    icon: Icons.folder_outlined,
  ),
  const AppDestination(
    branchIndex: 4,
    label: 'Organizations',
    icon: Icons.business_outlined,
  ),
  const AppDestination(
    branchIndex: 5,
    label: 'Prompts',
    icon: Icons.auto_awesome,
  ),
  const AppDestination(branchIndex: 6, label: 'Agents', icon: Icons.smart_toy),
  const AppDestination(branchIndex: 9, label: 'Flows', icon: Icons.alt_route),
  const AppDestination(branchIndex: 7, label: 'Stats', icon: Icons.bar_chart),
  const AppDestination(
    branchIndex: 8,
    label: 'Instances',
    icon: Icons.dns_outlined,
  ),
];

/// The application's responsive navigation container.
///
/// Replaces the previous 9-tab [DefaultTabController]/`TabBar` with a
/// destination list that adapts to width:
/// - `< AppBreakpoints.compact`: a [Drawer] opened from the app bar.
/// - `< AppBreakpoints.medium`: a collapsed [NavigationRail].
/// - `>= AppBreakpoints.medium`: an extended (labeled) [NavigationRail].
///
/// A toggle at the foot of the rail switches between the two rail modes and
/// the choice is remembered (see `sidebar_preferences.dart`).
///
/// Each destination is a [StatefulShellBranch] (see `shared/router.dart`),
/// so switching sections preserves that branch's navigation stack and
/// widget state — the same guarantee the old `KeepAliveTab` worked around
/// for `TabBarView`, now provided natively by `StatefulShellRoute`.
class AppShell extends ConsumerWidget {
  final StatefulNavigationShell navigationShell;

  const AppShell({super.key, required this.navigationShell});

  void _onDestinationSelected(int branchIndex) {
    navigationShell.goBranch(
      branchIndex,
      initialLocation: branchIndex == navigationShell.currentIndex,
    );
  }

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    // My PRs nudges live here, in the one widget that is always mounted, so
    // they fire whichever tab is open.
    ref.watch(mergeTrackingSseListenerProvider);
    ref.watch(myPrsDigestProvider);
    ref.watch(myPrsTraySyncProvider);
    final cbMessage = ref.watch(circuitBreakerProvider);
    final daemonRunning = ref.watch(daemonHealthProvider).value ?? false;
    final daemonStarting = ref.watch(daemonStartingProvider);
    final connection = daemonRunning
        ? ref.watch(daemonConnectionProvider)
        : null;

    final width = MediaQuery.sizeOf(context).width;
    final isCompact = width < AppBreakpoints.compact;
    final sidebarPreference = ref.watch(sidebarModeProvider);
    final effectiveSidebar = effectiveSidebarMode(sidebarPreference, width);
    final isRailExtended = effectiveSidebar == AppSidebarMode.extended;

    final body = Column(
      children: [
        const AppUpdateBanner(),
        if (cbMessage != null)
          CircuitBreakerBanner(
            message: cbMessage,
            onDismiss: () =>
                ref.read(circuitBreakerProvider.notifier).set(null),
          ),
        if (connection != null &&
            connection.phase != DaemonConnectionPhase.connected)
          ConnectionBanner(
            status: connection,
            onRestart: () => server_actions.restartDaemon(context, ref),
          ),
        InstanceFailureBanner(
          failureLabels: ref
              .watch(instanceReadFailuresProvider)
              .map((f) => f.label)
              .toList(),
        ),
        Expanded(child: navigationShell),
      ],
    );

    return Scaffold(
      appBar: AppBar(
        title: const Text('Heimdallm'),
        actions: [
          const InstanceSelector(),
          const CheckForUpdatesButton(),
          IconButton(
            icon: daemonStarting
                ? const SizedBox(
                    width: 20,
                    height: 20,
                    child: CircularProgressIndicator(strokeWidth: 2),
                  )
                : Icon(
                    daemonRunning ? Icons.power_settings_new : Icons.play_arrow,
                  ),
            tooltip: daemonRunning ? 'Stop Server' : 'Start Server',
            onPressed: daemonStarting
                ? null
                : daemonRunning
                ? () => server_actions.confirmShutdown(context, ref)
                : () => server_actions.startDaemon(context, ref),
          ),
          IconButton(
            icon: const Icon(Icons.dns_outlined),
            tooltip: 'Server',
            onPressed: () => context.push('/server'),
          ),
          IconButton(
            icon: const Icon(Icons.settings),
            onPressed: () => context.push('/config'),
          ),
          IconButton(
            icon: const Icon(Icons.refresh),
            onPressed: () {
              // Invalidate the aggregating providers: the flat lists derive
              // from them, so refreshing only the derived ones would replay
              // the same cached fan-out.
              ref.invalidate(daemonInstancesProvider);
              ref.invalidate(prsByInstanceProvider);
              ref.invalidate(mergeTrackingByInstanceProvider);
              ref.invalidate(statsByInstanceProvider);
              ref.invalidate(githubRateLimitProvider);
              ref.invalidate(activityEntriesProvider);
              ref.invalidate(activityOptionsProvider);
            },
          ),
        ],
      ),
      drawer: isCompact ? _NavDrawer(navigationShell: navigationShell) : null,
      body: isCompact
          ? body
          : Row(
              children: [
                NavigationRail(
                  extended: isRailExtended,
                  selectedIndex: navigationShell.currentIndex,
                  onDestinationSelected: _onDestinationSelected,
                  destinations: [
                    for (final d in appDestinations)
                      NavigationRailDestination(
                        icon: d.badgedIcon?.call(context, ref) ?? Icon(d.icon),
                        label: Text(d.label),
                      ),
                  ],
                  // The toggle sits at the foot of the rail it controls, not
                  // in the app bar at the far end of the window.
                  trailingAtBottom: true,
                  trailing: Padding(
                    padding: const EdgeInsets.only(bottom: 12),
                    child: AppIconButton(
                      key: const Key('sidebar-toggle'),
                      icon: isRailExtended ? Icons.menu_open : Icons.menu,
                      tooltip: isRailExtended
                          ? 'Collapse sidebar'
                          : 'Expand sidebar',
                      onPressed: () => ref
                          .read(sidebarModeProvider.notifier)
                          .cycleFrom(effectiveSidebar),
                    ),
                  ),
                ),
                const VerticalDivider(width: 1),
                Expanded(child: body),
              ],
            ),
    );
  }
}

class _NavDrawer extends ConsumerWidget {
  final StatefulNavigationShell navigationShell;

  const _NavDrawer({required this.navigationShell});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    return Drawer(
      child: SafeArea(
        child: ListView(
          padding: EdgeInsets.zero,
          children: [
            const DrawerHeader(child: Text('Heimdallm')),
            for (final d in appDestinations)
              ListTile(
                leading: d.badgedIcon?.call(context, ref) ?? Icon(d.icon),
                title: Text(d.label),
                selected: d.branchIndex == navigationShell.currentIndex,
                onTap: () {
                  Navigator.of(context).pop();
                  navigationShell.goBranch(
                    d.branchIndex,
                    initialLocation:
                        d.branchIndex == navigationShell.currentIndex,
                  );
                },
              ),
          ],
        ),
      ),
    );
  }
}

/// Shows the daemon connection health (connecting/stale/offline/connected)
/// as a dismissible-by-recovery banner above the active branch content.
class ConnectionBanner extends StatelessWidget {
  const ConnectionBanner({
    super.key,
    required this.status,
    required this.onRestart,
  });

  final DaemonConnectionStatus status;
  final VoidCallback onRestart;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final (color, icon, label) = switch (status.phase) {
      DaemonConnectionPhase.connected => (
        Colors.green,
        Icons.check_circle_outline,
        'Connected',
      ),
      DaemonConnectionPhase.stale => (
        Colors.amber,
        Icons.sync_problem,
        'No events received — reconnecting',
      ),
      DaemonConnectionPhase.offline => (
        theme.colorScheme.error,
        Icons.error_outline,
        'Server unavailable',
      ),
      DaemonConnectionPhase.connecting => (
        Colors.blueGrey,
        Icons.sync,
        'Connecting',
      ),
    };
    return Material(
      color: color.withValues(alpha: 0.10),
      child: SafeArea(
        bottom: false,
        child: ConstrainedBox(
          constraints: const BoxConstraints(minHeight: 36),
          child: Padding(
            padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 6),
            child: Row(
              children: [
                Icon(icon, size: 18, color: color),
                const SizedBox(width: 8),
                Expanded(
                  child: Text(
                    label,
                    style: theme.textTheme.bodySmall?.copyWith(
                      color: color,
                      fontWeight: FontWeight.w600,
                    ),
                  ),
                ),
                if (status.phase == DaemonConnectionPhase.offline)
                  TextButton(
                    onPressed: onRestart,
                    child: const Text('Restart'),
                  ),
              ],
            ),
          ),
        ),
      ),
    );
  }
}

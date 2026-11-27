import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:mix/mix.dart' show ColorToken;

import '../../core/instances/instances_providers.dart';
import '../../core/platform/platform_services.dart';
import '../../core/platform/platform_services_provider.dart';
import '../../core/state/sidebar_preferences.dart';
import '../../features/activity/activity_providers.dart';
import '../../features/circuit_breaker/circuit_breaker_banner.dart';
import '../../features/config/config_providers.dart';
import '../../features/dashboard/daemon_card.dart';
import '../../features/dashboard/dashboard_providers.dart';
import '../../features/instances/widgets/instance_selector.dart';
import '../../features/merge_tracking/merge_tracking_providers.dart';
import '../../features/server/server_actions.dart' as server_actions;
import '../../features/updates/check_for_updates_button.dart';
import '../design_system/color_resolver.dart';
import '../design_system/components/app_icon_button.dart';
import '../design_system/components/app_source_list.dart';
import '../design_system/tokens.dart';

/// One destination in the primary navigation (sidebar/drawer).
///
/// [branchIndex] must match the corresponding [StatefulShellBranch]'s
/// position in `router.dart`'s `branches` list.
class AppDestination {
  final int branchIndex;
  final String label;
  final IconData icon;

  /// The sidebar section the destination is listed under (#917).
  final String section;

  /// A count to show with the destination and its colour, or null for none.
  final ({int count, ColorToken color})? Function(WidgetRef ref)? badge;

  const AppDestination({
    required this.branchIndex,
    required this.label,
    required this.icon,
    required this.section,
    this.badge,
  });

  /// [icon] with [badge] over it, for the drawer.
  Widget badgedIcon(BuildContext context, WidgetRef ref) {
    final b = badge?.call(ref);
    if (b == null) return Icon(icon);
    return Badge.count(
      count: b.count,
      backgroundColor: resolveAppColor(context, b.color),
      child: Icon(icon),
    );
  }
}

final appDestinations = <AppDestination>[
  const AppDestination(
    branchIndex: 0,
    label: 'Activity',
    icon: Icons.dashboard,
    section: 'Review',
  ),
  const AppDestination(
    branchIndex: 1,
    label: 'Activity log',
    icon: Icons.timeline,
    section: 'Review',
  ),
  AppDestination(
    branchIndex: 2,
    label: 'My PRs',
    icon: Icons.merge_type,
    section: 'Review',
    badge: (ref) {
      final summary = ref.watch(myPrsSummaryProvider);
      if (summary.isEmpty) return null;
      // Red only when something is broken on the operator's side; a PR that
      // is ready to merge or has merely gone quiet is a nudge, not an alarm.
      return (
        count: summary.total,
        color: summary.needAction > 0 ? AppColors.danger : AppColors.accent,
      );
    },
  ),
  const AppDestination(
    branchIndex: 3,
    label: 'Repositories',
    icon: Icons.folder_outlined,
    section: 'Setup',
  ),
  const AppDestination(
    branchIndex: 4,
    label: 'Organizations',
    icon: Icons.business_outlined,
    section: 'Setup',
  ),
  const AppDestination(
    branchIndex: 5,
    label: 'Prompts',
    icon: Icons.auto_awesome,
    section: 'Setup',
  ),
  const AppDestination(
    branchIndex: 6,
    label: 'Agents',
    icon: Icons.smart_toy,
    section: 'Setup',
  ),
  const AppDestination(
    branchIndex: 9,
    label: 'Flows',
    icon: Icons.alt_route,
    section: 'Setup',
  ),
  const AppDestination(
    branchIndex: 7,
    label: 'Stats',
    icon: Icons.bar_chart,
    section: 'System',
  ),
  const AppDestination(
    branchIndex: 8,
    label: 'Instances',
    icon: Icons.dns_outlined,
    section: 'System',
  ),
];

/// The application's responsive navigation container.
///
/// Replaces the previous 9-tab [DefaultTabController]/`TabBar` with a
/// destination list that adapts to width:
/// - `< AppBreakpoints.compact`: a [Drawer] opened from the app bar.
/// - `< AppBreakpoints.medium`: an [AppSidebar] collapsed to icons.
/// - `>= AppBreakpoints.medium`: an extended (labeled) [AppSidebar].
///
/// A toggle at the foot of the sidebar switches between the two modes and the
/// choice is remembered (see `sidebar_preferences.dart`).
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
    // On macOS the sidebar paints nothing, so the window's material shows
    // through it; the content keeps an opaque surface (#927).
    final translucent =
        !isCompact && ref.watch(platformServicesProvider).translucentSidebar;
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
      backgroundColor: translucent ? Colors.transparent : null,
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
                AppSidebar(
                  currentBranch: navigationShell.currentIndex,
                  extended: isRailExtended,
                  onSelect: _onDestinationSelected,
                  onToggle: () => ref
                      .read(sidebarModeProvider.notifier)
                      .cycleFrom(effectiveSidebar),
                ),
                const VerticalDivider(width: 1),
                Expanded(
                  child: translucent
                      ? ColoredBox(
                          color: Theme.of(context).colorScheme.surface,
                          child: body,
                        )
                      : body,
                ),
              ],
            ),
    );
  }
}

/// The app navigation as a source list (#917): sections, the shared
/// [AppSourceListItem] and the expand/collapse toggle at its foot. Collapsed,
/// it shows icons only, with section breaks as dividers.
class AppSidebar extends ConsumerWidget {
  final int currentBranch;
  final bool extended;
  final ValueChanged<int> onSelect;
  final VoidCallback onToggle;

  static const double extendedWidth = 220;
  static const double compactWidth = 68;

  const AppSidebar({
    super.key,
    required this.currentBranch,
    required this.extended,
    required this.onSelect,
    required this.onToggle,
  });

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final items = <Widget>[];
    String? section;
    for (final d in appDestinations) {
      if (d.section != section) {
        if (extended) {
          if (section != null) items.add(const SizedBox(height: 14));
          items.add(AppSourceListSection(d.section));
        } else if (section != null) {
          items.add(const Divider(height: 14, indent: 12, endIndent: 12));
        }
        section = d.section;
      }
      final badge = d.badge?.call(ref);
      items.add(
        AppSourceListItem(
          key: Key('nav-${d.branchIndex}'),
          icon: d.icon,
          label: d.label,
          count: badge?.count,
          badge: badge?.color,
          selected: d.branchIndex == currentBranch,
          compact: !extended,
          onTap: () => onSelect(d.branchIndex),
        ),
      );
    }
    return SizedBox(
      key: const Key('app-sidebar'),
      width: extended ? extendedWidth : compactWidth,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Expanded(
            child: ListView(
              padding: const EdgeInsets.fromLTRB(10, 14, 10, 12),
              children: items,
            ),
          ),
          Padding(
            padding: EdgeInsets.fromLTRB(10, 0, 10, extended ? 8 : 4),
            child: extended
                ? const DaemonCard()
                : const Center(child: DaemonCard(compact: true)),
          ),
          // The toggle sits at the foot of the sidebar it controls, not in
          // the app bar at the far end of the window.
          Padding(
            padding: const EdgeInsets.only(bottom: 12),
            child: Center(
              child: AppIconButton(
                key: const Key('sidebar-toggle'),
                icon: extended ? Icons.menu_open : Icons.menu,
                tooltip: extended ? 'Collapse sidebar' : 'Expand sidebar',
                onPressed: onToggle,
              ),
            ),
          ),
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
                leading: d.badgedIcon(context, ref),
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

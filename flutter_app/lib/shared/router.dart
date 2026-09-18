import 'package:go_router/go_router.dart';
import '../features/activity/activity_screen.dart';
import '../features/agents/agents_screen.dart';
import '../features/cli_agents/cli_agents_screen.dart';
import '../features/dashboard/dashboard_screen.dart';
import '../features/instances/instances_screen.dart';
import '../features/instances/routing_screen.dart';
import '../features/issues/issue_detail_screen.dart';
import '../features/merge_tracking/merge_tracking_screen.dart';
import '../features/organizations/orgs_screen.dart';
import '../features/pr_detail/pr_detail_screen.dart';
import '../features/config/config_screen.dart';
import '../features/organizations/org_detail_screen.dart';
import '../features/repositories/repo_detail_screen.dart';
import '../features/repositories/repos_screen.dart';
import '../features/server/server_screen.dart';
import '../features/stats/stats_screen.dart';
import 'layout/app_shell.dart';

/// Destination order here MUST match `appDestinations` in `app_shell.dart`
/// (each branch's position is what the navigation rail/drawer highlights).
GoRouter createRouter({String initialLocation = '/'}) => GoRouter(
  initialLocation: initialLocation,
  routes: [
    StatefulShellRoute.indexedStack(
      builder: (context, state, navigationShell) =>
          AppShell(navigationShell: navigationShell),
      branches: [
        StatefulShellBranch(
          routes: [
            GoRoute(
              path: '/',
              builder: (context, state) => const DashboardScreen(),
            ),
          ],
        ),
        StatefulShellBranch(
          routes: [
            GoRoute(
              path: '/activity',
              builder: (context, state) => const ActivityScreen(),
            ),
          ],
        ),
        StatefulShellBranch(
          routes: [
            GoRoute(
              path: '/merge',
              builder: (context, state) => const MergeTrackingScreen(),
            ),
          ],
        ),
        StatefulShellBranch(
          routes: [
            GoRoute(
              path: '/repos',
              builder: (context, state) => const ReposScreen(),
            ),
          ],
        ),
        StatefulShellBranch(
          routes: [
            GoRoute(
              path: '/orgs',
              builder: (context, state) => const OrgsScreen(),
            ),
          ],
        ),
        StatefulShellBranch(
          routes: [
            GoRoute(
              path: '/prompts',
              builder: (context, state) => const AgentsScreen(),
            ),
            // Renamed from "Agents" to "Prompts" during the navigation
            // redesign; kept so bookmarks/deep links to the old name keep
            // working.
            GoRoute(
              path: '/agents',
              redirect: (context, state) => '/prompts',
            ),
          ],
        ),
        StatefulShellBranch(
          routes: [
            GoRoute(
              path: '/cli-agents',
              builder: (context, state) => const CLIAgentsScreen(),
            ),
          ],
        ),
        StatefulShellBranch(
          routes: [
            GoRoute(
              path: '/stats',
              builder: (context, state) => const StatsScreen(),
            ),
          ],
        ),
        StatefulShellBranch(
          routes: [
            GoRoute(
              path: '/instances',
              builder: (context, state) => const InstancesScreen(),
              routes: [
                GoRoute(
                  path: 'routing',
                  builder: (context, state) => const RoutingScreen(),
                ),
              ],
            ),
          ],
        ),
      ],
    ),
    GoRoute(
      path: '/prs/:id',
      builder: (context, state) {
        final id = int.parse(state.pathParameters['id']!);
        // Store ids are per-instance, so /prs/42 alone is ambiguous once more
        // than one daemon is registered.
        final instance = state.uri.queryParameters['instance'] ?? '';
        return PRDetailScreen(prId: id, instanceId: instance);
      },
    ),
    GoRoute(
      path: '/issues/:id',
      builder: (context, state) {
        final id = int.parse(state.pathParameters['id']!);
        final instance = state.uri.queryParameters['instance'] ?? '';
        return IssueDetailScreen(issueId: id, instanceId: instance);
      },
    ),
    GoRoute(
      path: '/repos/:name',
      builder: (context, state) {
        final name = Uri.decodeComponent(state.pathParameters['name']!);
        return RepoDetailScreen(repoName: name);
      },
    ),
    GoRoute(
      path: '/orgs/:name',
      builder: (context, state) {
        final name = Uri.decodeComponent(state.pathParameters['name']!);
        return OrgDetailScreen(orgName: name);
      },
    ),
    GoRoute(path: '/config', builder: (context, state) => const ConfigScreen()),
    GoRoute(
      path: '/server',
      builder: (context, state) {
        final tab = state.uri.queryParameters['tab'] ?? 'status';
        return ServerScreen(initialTab: tab);
      },
    ),
    GoRoute(
      path: '/logs',
      redirect: (context, state) => '/server?tab=logs',
    ),
  ],
);

// Kept for backwards compat with tests that use appRouter directly
final appRouter = createRouter();

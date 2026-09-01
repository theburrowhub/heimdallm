import 'package:go_router/go_router.dart';
import '../features/dashboard/dashboard_screen.dart';
import '../features/instances/instances_screen.dart';
import '../features/instances/routing_screen.dart';
import '../features/issues/issue_detail_screen.dart';
import '../features/pr_detail/pr_detail_screen.dart';
import '../features/config/config_screen.dart';
import '../features/organizations/org_detail_screen.dart';
import '../features/repositories/repo_detail_screen.dart';
import '../features/server/server_screen.dart';

GoRouter createRouter({String initialLocation = '/'}) => GoRouter(
  initialLocation: initialLocation,
  routes: [
    GoRoute(path: '/', builder: (context, state) => const DashboardScreen()),
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
      path: '/instances',
      builder: (context, state) => const InstancesScreen(),
      routes: [
        GoRoute(
          path: 'routing',
          builder: (context, state) => const RoutingScreen(),
        ),
      ],
    ),
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

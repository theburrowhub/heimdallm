import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:heimdallm/core/instances/instances_providers.dart';
import 'package:heimdallm/core/instances/models.dart';
import 'package:heimdallm/features/instances/instances_screen.dart';
import 'package:heimdallm/features/instances/widgets/instance_badge.dart';
import 'package:heimdallm/features/instances/widgets/instance_selector.dart';

ClusterRegistry _registry({List<Map<String, dynamic>> instances = const []}) {
  return ClusterRegistry.fromJson({
    'role': 'hub',
    'self_id': 'hub-1',
    'self_name': 'Local hub',
    'instances': instances,
  });
}

/// Router host for a screen. Overrides stay at the call site because
/// flutter_riverpod does not export the Override type to name here.
Widget _app(Widget child) {
  return MaterialApp.router(
    routerConfig: GoRouter(
      routes: [GoRoute(path: '/', builder: (_, _) => child)],
    ),
  );
}

void main() {
  group('InstancesScreen', () {
    testWidgets('explains what an instance is when none are registered', (
      tester,
    ) async {
      await tester.pumpWidget(
        ProviderScope(
          overrides: [
            daemonInstancesProvider.overrideWith((ref) async => _registry()),
          ],
          child: _app(const InstancesScreen()),
        ),
      );
      await tester.pumpAndSettle();

      expect(find.text('No instances registered'), findsOneWidget);
      expect(find.textContaining('another machine'), findsOneWidget);
    });

    testWidgets('shows health, version and routing share per instance', (
      tester,
    ) async {
      await tester.pumpWidget(
        ProviderScope(
          overrides: [
            daemonInstancesProvider.overrideWith(
              (ref) async => _registry(
                instances: [
                  {
                    'id': 'hub-1',
                    'name': 'Local hub',
                    'base_url': 'http://127.0.0.1:7842',
                    'self': true,
                    'assigned_repos': 2,
                    'is_fallback': true,
                    'state': {
                      'reachable': true,
                      'status': 'ok',
                      'version': '0.9.0',
                      'uptime_seconds': 3700,
                    },
                  },
                  {
                    'id': 'srv-a',
                    'name': 'Server A',
                    'base_url': 'http://10.0.0.11:7842',
                    'assigned_repos': 1,
                    'state': {
                      'reachable': false,
                      'last_error': 'connection refused',
                      'consecutive_failures': 3,
                    },
                  },
                ],
              ),
            ),
          ],
          child: _app(const InstancesScreen()),
        ),
      );
      await tester.pumpAndSettle();

      expect(find.text('Local hub'), findsWidgets);
      expect(find.text('hub'), findsOneWidget);
      expect(find.text('0.9.0'), findsOneWidget);
      expect(find.text('up 1h 1m'), findsOneWidget);
      expect(find.text('2 repos routed here'), findsOneWidget);
      expect(find.text('1 repo routed here'), findsOneWidget);
      expect(find.text('owns unrouted repos'), findsOneWidget);

      // An unreachable instance must say why and for how long, not just
      // disappear or show a bare icon.
      expect(find.text('unreachable'), findsOneWidget);
      expect(find.text('connection refused (3 failed probes)'), findsOneWidget);
    });

    testWidgets('surfaces a token problem instead of hiding the instance', (
      tester,
    ) async {
      await tester.pumpWidget(
        ProviderScope(
          overrides: [
            daemonInstancesProvider.overrideWith(
              (ref) async => _registry(
                instances: [
                  {
                    'id': 'srv-a',
                    'name': 'Server A',
                    'token_error': 'token_env HEIMDALLM_SRV_A is unset',
                  },
                ],
              ),
            ),
          ],
          child: _app(const InstancesScreen()),
        ),
      );
      await tester.pumpAndSettle();

      expect(find.text('Server A'), findsOneWidget);
      expect(find.textContaining('HEIMDALLM_SRV_A is unset'), findsOneWidget);
    });

    testWidgets('the hub cannot deregister itself', (tester) async {
      await tester.pumpWidget(
        ProviderScope(
          overrides: [
            daemonInstancesProvider.overrideWith(
              (ref) async => _registry(
                instances: [
                  {'id': 'hub-1', 'name': 'Local hub', 'self': true},
                ],
              ),
            ),
          ],
          child: _app(const InstancesScreen()),
        ),
      );
      await tester.pumpAndSettle();

      await tester.tap(find.byIcon(Icons.more_vert).first);
      await tester.pumpAndSettle();

      expect(find.text('Probe now'), findsOneWidget);
      // Removing the daemon serving this very request would leave the app with
      // nothing to talk to.
      expect(find.text('Remove…'), findsNothing);
    });

    testWidgets('routing rules button pushes /instances/routing', (
      tester,
    ) async {
      await tester.pumpWidget(
        ProviderScope(
          overrides: [
            daemonInstancesProvider.overrideWith((ref) async => _registry()),
          ],
          child: MaterialApp.router(
            routerConfig: GoRouter(
              initialLocation: '/instances',
              routes: [
                GoRoute(
                  path: '/instances',
                  builder: (_, _) => const InstancesScreen(),
                  routes: [
                    GoRoute(
                      path: 'routing',
                      builder: (_, _) =>
                          const Scaffold(body: Text('Routing screen')),
                    ),
                  ],
                ),
              ],
            ),
          ),
        ),
      );
      await tester.pumpAndSettle();

      await tester.tap(find.byTooltip('Routing rules'));
      await tester.pumpAndSettle();

      expect(find.text('Routing screen'), findsOneWidget);
    });

    testWidgets('propagate config button opens the configuration dialog', (
      tester,
    ) async {
      await tester.pumpWidget(
        ProviderScope(
          overrides: [
            daemonInstancesProvider.overrideWith((ref) async => _registry()),
            configDriftProvider.overrideWith((ref) async => const []),
          ],
          child: _app(const InstancesScreen()),
        ),
      );
      await tester.pumpAndSettle();

      await tester.tap(find.byTooltip('Apply configuration to all instances'));
      await tester.pumpAndSettle();

      expect(find.text('Configuration across instances'), findsOneWidget);
    });
  });

  group('InstancesTabView', () {
    testWidgets('shows the same empty state as the routed screen', (
      tester,
    ) async {
      await tester.pumpWidget(
        ProviderScope(
          overrides: [
            daemonInstancesProvider.overrideWith((ref) async => _registry()),
          ],
          child: _app(const Scaffold(body: InstancesTabView())),
        ),
      );
      await tester.pumpAndSettle();

      expect(find.text('No instances registered'), findsOneWidget);
    });

    testWidgets('inlines the toolbar actions since there is no AppBar here', (
      tester,
    ) async {
      await tester.pumpWidget(
        ProviderScope(
          overrides: [
            daemonInstancesProvider.overrideWith(
              (ref) async => _registry(
                instances: [
                  {'id': 'hub-1', 'name': 'Local hub', 'self': true},
                ],
              ),
            ),
          ],
          child: _app(const Scaffold(body: InstancesTabView())),
        ),
      );
      await tester.pumpAndSettle();

      expect(find.text('Add instance'), findsOneWidget);
      expect(find.byTooltip('Routing rules'), findsOneWidget);
      expect(
        find.byTooltip('Apply configuration to all instances'),
        findsOneWidget,
      );
      expect(find.byTooltip('Refresh'), findsOneWidget);
      // Body content still renders below the inline toolbar.
      expect(find.text('Local hub'), findsWidgets);
    });

    testWidgets('routing rules button pushes /instances/routing', (
      tester,
    ) async {
      await tester.pumpWidget(
        ProviderScope(
          overrides: [
            daemonInstancesProvider.overrideWith((ref) async => _registry()),
          ],
          child: MaterialApp.router(
            routerConfig: GoRouter(
              initialLocation: '/instances',
              routes: [
                GoRoute(
                  path: '/instances',
                  builder: (_, _) =>
                      const Scaffold(body: InstancesTabView()),
                  routes: [
                    GoRoute(
                      path: 'routing',
                      builder: (_, _) =>
                          const Scaffold(body: Text('Routing screen')),
                    ),
                  ],
                ),
              ],
            ),
          ),
        ),
      );
      await tester.pumpAndSettle();

      await tester.tap(find.byTooltip('Routing rules'));
      await tester.pumpAndSettle();

      expect(find.text('Routing screen'), findsOneWidget);
    });

    testWidgets('add instance button opens the registration dialog', (
      tester,
    ) async {
      await tester.pumpWidget(
        ProviderScope(
          overrides: [
            daemonInstancesProvider.overrideWith((ref) async => _registry()),
          ],
          child: _app(const Scaffold(body: InstancesTabView())),
        ),
      );
      await tester.pumpAndSettle();

      await tester.tap(find.text('Add instance'));
      await tester.pumpAndSettle();

      expect(
        find.widgetWithText(AlertDialog, 'Add instance'),
        findsOneWidget,
      );
    });

    testWidgets(
      'propagate config button opens the configuration dialog',
      (tester) async {
        await tester.pumpWidget(
          ProviderScope(
            overrides: [
              daemonInstancesProvider.overrideWith(
                (ref) async => _registry(),
              ),
              configDriftProvider.overrideWith((ref) async => const []),
            ],
            child: _app(const Scaffold(body: InstancesTabView())),
          ),
        );
        await tester.pumpAndSettle();

        await tester.tap(
          find.byTooltip('Apply configuration to all instances'),
        );
        await tester.pumpAndSettle();

        expect(find.text('Configuration across instances'), findsOneWidget);
      },
    );

    testWidgets('refresh button reloads the registry', (tester) async {
      var loads = 0;
      await tester.pumpWidget(
        ProviderScope(
          overrides: [
            daemonInstancesProvider.overrideWith((ref) async {
              loads++;
              return _registry();
            }),
          ],
          child: _app(const Scaffold(body: InstancesTabView())),
        ),
      );
      await tester.pumpAndSettle();
      expect(loads, 1);

      await tester.tap(find.byTooltip('Refresh'));
      await tester.pumpAndSettle();

      expect(loads, 2);
    });

    testWidgets('shows an error when the registry fails to load', (
      tester,
    ) async {
      await tester.pumpWidget(
        ProviderScope(
          overrides: [
            daemonInstancesProvider.overrideWith(
              (ref) async => throw Exception('daemon unreachable'),
            ),
          ],
          child: _app(const Scaffold(body: InstancesTabView())),
        ),
      );
      await tester.pumpAndSettle();

      expect(
        find.textContaining('Could not load instances:'),
        findsOneWidget,
      );
      expect(find.textContaining('daemon unreachable'), findsOneWidget);
    });
  });

  group('InstanceSelector', () {
    testWidgets('stays hidden with a single instance', (tester) async {
      // One instance is indistinguishable from a plain single-daemon install.
      await tester.pumpWidget(
        ProviderScope(
          overrides: [
            daemonInstancesProvider.overrideWith(
              (ref) async => _registry(
                instances: [
                  {'id': 'hub-1', 'self': true},
                ],
              ),
            ),
          ],
          child: _app(const Scaffold(body: InstanceSelector())),
        ),
      );
      await tester.pumpAndSettle();

      expect(find.text('All instances'), findsNothing);
    });

    testWidgets('offers all instances plus each one individually', (
      tester,
    ) async {
      await tester.pumpWidget(
        ProviderScope(
          overrides: [
            daemonInstancesProvider.overrideWith(
              (ref) async => _registry(
                instances: [
                  {'id': 'hub-1', 'name': 'Local hub', 'self': true},
                  {'id': 'srv-a', 'name': 'Server A'},
                ],
              ),
            ),
          ],
          child: _app(const Scaffold(body: InstanceSelector())),
        ),
      );
      await tester.pumpAndSettle();

      expect(find.text('All instances'), findsOneWidget);
      await tester.tap(find.text('All instances'));
      await tester.pumpAndSettle();

      expect(find.text('All instances (2)'), findsOneWidget);
      expect(find.text('Server A'), findsOneWidget);
      expect(find.text('Manage instances…'), findsOneWidget);
    });
  });

  group('InstanceBadge', () {
    testWidgets('renders nothing without an instance id', (tester) async {
      // On a single-daemon install every row would carry the same badge.
      await tester.pumpWidget(
        const MaterialApp(home: Scaffold(body: InstanceBadge(instanceId: ''))),
      );
      expect(find.byType(Text), findsNothing);
    });

    testWidgets('shows the name and marks an unreachable instance', (
      tester,
    ) async {
      await tester.pumpWidget(
        const MaterialApp(
          home: Scaffold(
            body: InstanceBadge(
              instanceId: 'srv-a',
              instanceName: 'Server A',
              reachable: false,
            ),
          ),
        ),
      );
      expect(find.text('Server A'), findsOneWidget);
      expect(find.byIcon(Icons.cloud_off_outlined), findsOneWidget);
    });
  });

  group('InstanceFailureBanner', () {
    testWidgets('is silent with nothing to report', (tester) async {
      await tester.pumpWidget(
        ProviderScope(
          child: _app(
            const Scaffold(body: InstanceFailureBanner(failureLabels: [])),
          ),
        ),
      );
      expect(find.textContaining('partial data'), findsNothing);
    });

    testWidgets('names the instance that could not be reached', (tester) async {
      // Degrading loudly matters: a list that silently drops one machine's PRs
      // is indistinguishable from that machine having no work.
      await tester.pumpWidget(
        ProviderScope(
          child: _app(
            const Scaffold(
              body: InstanceFailureBanner(failureLabels: ['Server A']),
            ),
          ),
        ),
      );
      expect(
        find.text('Showing partial data — Server A could not be reached.'),
        findsOneWidget,
      );
    });

    testWidgets('summarises when several are down', (tester) async {
      await tester.pumpWidget(
        ProviderScope(
          child: _app(
            const Scaffold(
              body: InstanceFailureBanner(failureLabels: ['A', 'B']),
            ),
          ),
        ),
      );
      expect(
        find.textContaining('2 instances could not be reached'),
        findsOneWidget,
      );
    });
  });
}

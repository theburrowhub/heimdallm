import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:heimdallm/core/instances/instances_providers.dart';
import 'package:heimdallm/core/instances/models.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/features/config/config_providers.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:mocktail/mocktail.dart';
import 'package:heimdallm/features/instances/routing_screen.dart';

ClusterRegistry _registry() => ClusterRegistry.fromJson({
  'role': 'hub',
  'self_id': 'hub-1',
  'instances': [
    {'id': 'hub-1', 'name': 'Local hub', 'self': true},
    {'id': 'srv-a', 'name': 'Server A'},
  ],
});

Widget _app(Widget child) => MaterialApp.router(
  routerConfig: GoRouter(
    routes: [GoRoute(path: '/', builder: (_, _) => child)],
  ),
);

class _MockApiClient extends Mock implements ApiClient {}

/// The repo/org universe the routing table lists. ConfigNotifier builds itself
/// from GET /config, so the fixture goes through the API the way the app does.
const _configJson = <String, dynamic>{
  'repositories': ['acme/tools', 'other/thing'],
};

Future<void> _pump(WidgetTester tester, RoutingRules rules) async {
  // The screen is a tall ListView; the default 800x600 test viewport builds
  // only the first section, so the repository table would never be laid out.
  tester.view.physicalSize = const Size(1200, 2000);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.resetPhysicalSize);
  addTearDown(tester.view.resetDevicePixelRatio);

  final api = _MockApiClient();
  when(api.fetchConfig).thenAnswer((_) async => _configJson);

  await tester.pumpWidget(
    ProviderScope(
      overrides: [
        apiClientProvider.overrideWithValue(api),
        daemonInstancesProvider.overrideWith((ref) async => _registry()),
        routingRulesProvider.overrideWith((ref) async => rules),
        configNotifierProvider.overrideWith(ConfigNotifier.new),
      ],
      child: _app(const RoutingScreen()),
    ),
  );
  await tester.pumpAndSettle();
}

void main() {
  testWidgets('assignment mode explains that repos are partitioned', (
    tester,
  ) async {
    await _pump(tester, const RoutingRules(mode: RoutingMode.assignment));

    expect(find.text('Assignment'), findsOneWidget);
    expect(find.text('Dispatch'), findsOneWidget);
    expect(
      find.textContaining('polls, reviews and merges only what is routed'),
      findsOneWidget,
    );
    // Per-operation rotation is dispatch-only, so its controls must not be
    // offered in assignment mode.
    expect(find.text('Rotate these operations'), findsNothing);
  });

  testWidgets('dispatch mode exposes the per-operation rotation', (
    tester,
  ) async {
    await _pump(
      tester,
      const RoutingRules(
        mode: RoutingMode.dispatch,
        roundRobinOps: [RoutingOp.review],
      ),
    );

    expect(find.text('Rotate these operations'), findsOneWidget);
    for (final op in RoutingOp.all) {
      expect(find.text(op), findsOneWidget);
    }
    expect(
      find.textContaining('regardless of which instance owns the repo'),
      findsOneWidget,
    );
  });

  testWidgets('lists every monitored repository with its assignment', (
    tester,
  ) async {
    await _pump(
      tester,
      const RoutingRules(
        repos: {'acme/tools': 'srv-a'},
        defaultInstance: 'hub-1',
      ),
    );

    expect(find.text('acme/tools'), findsOneWidget);
    expect(find.text('other/thing'), findsOneWidget);
    // A repo with no rule of its own still has an owner; showing where it
    // comes from is the difference between "unconfigured" and "nobody polls
    // this".
    expect(find.text('inherits Local hub'), findsOneWidget);
  });

  testWidgets('names the owner of everything unrouted', (tester) async {
    await _pump(tester, const RoutingRules(defaultInstance: 'hub-1'));
    expect(find.text('Owner of everything unrouted'), findsOneWidget);
  });

  testWidgets('the pool lists every instance', (tester) async {
    await _pump(tester, const RoutingRules(roundRobinPool: ['hub-1']));
    expect(find.text('Round-robin pool'), findsOneWidget);
    expect(
      find.textContaining('Empty means every enabled instance'),
      findsOneWidget,
    );
  });
}

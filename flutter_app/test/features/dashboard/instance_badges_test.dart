import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/instances/aggregation.dart';
import 'package:heimdallm/core/instances/instances_providers.dart';
import 'package:heimdallm/core/instances/models.dart';
import 'package:heimdallm/core/models/pr.dart';
import 'package:heimdallm/core/models/tracked_issue.dart';
import 'package:heimdallm/core/platform/platform_services_provider.dart';
import 'package:heimdallm/features/config/config_providers.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/features/dashboard/dashboard_screen.dart';
import 'package:heimdallm/features/instances/widgets/instance_badge.dart';
import 'package:heimdallm/features/issues/issues_providers.dart';
import 'package:mocktail/mocktail.dart';

import '../../core/platform/fake_platform_services.dart';

class _MockApiClient extends Mock implements ApiClient {}

PR _pr(int id, String repo, int number, String title) => PR(
  id: id,
  githubId: 1000 + id,
  repo: repo,
  number: number,
  title: title,
  author: 'alice',
  url: 'https://github.com/$repo/pull/$number',
  state: 'open',
  updatedAt: DateTime(2026, 9, 1),
);

ClusterRegistry _registry() => ClusterRegistry.fromJson({
  'self_id': 'hub-1',
  'instances': [
    {'id': 'hub-1', 'name': 'Local hub', 'self': true, 'enabled': true},
    {'id': 'srv-a', 'name': 'Server A', 'enabled': true},
  ],
});

Future<void> _pumpDashboard(
  WidgetTester tester, {
  required AggregatedResult<PR> prs,
  ClusterRegistry? registry,
}) async {
  tester.view.physicalSize = const Size(1400, 1200);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.resetPhysicalSize);
  addTearDown(tester.view.resetDevicePixelRatio);

  final api = _MockApiClient();
  await tester.pumpWidget(
    ProviderScope(
      overrides: [
        platformServicesProvider.overrideWithValue(FakePlatformServices()),
        apiClientProvider.overrideWithValue(api),
        daemonHealthProvider.overrideWith((ref) async => true),
        daemonInstancesProvider.overrideWith(
          (ref) async => registry ?? ClusterRegistry.empty,
        ),
        prsByInstanceProvider.overrideWith((ref) async => prs),
        issuesByInstanceProvider.overrideWith(
          (ref) async => singleInstanceResult(<TrackedIssue>[]),
        ),
        sseStreamProvider.overrideWith((ref) => const Stream.empty()),
      ],
      child: MaterialApp.router(
        routerConfig: GoRouter(
          routes: [
            GoRoute(path: '/', builder: (_, _) => const DashboardScreen()),
          ],
        ),
      ),
    ),
  );
  await tester.pumpAndSettle();
}

void main() {
  testWidgets('a single-daemon install shows no badges and no selector', (
    tester,
  ) async {
    await _pumpDashboard(
      tester,
      prs: singleInstanceResult([_pr(1, 'acme/tools', 7, 'Fix the thing')]),
    );

    expect(find.text('Fix the thing'), findsOneWidget);
    // Every row would carry the same badge, which is pure noise.
    expect(find.byType(InstanceBadge), findsNothing);
    expect(find.text('All instances'), findsNothing);
  });

  testWidgets('rows from several instances carry their origin', (tester) async {
    await _pumpDashboard(
      tester,
      registry: _registry(),
      prs: AggregatedResult<PR>(
        items: [
          InstanceScoped(
            instanceId: 'hub-1',
            instanceName: 'Local hub',
            value: _pr(1, 'acme/tools', 7, 'From the hub'),
          ),
          InstanceScoped(
            instanceId: 'srv-a',
            instanceName: 'Server A',
            value: _pr(2, 'acme/other', 8, 'From server A'),
          ),
        ],
      ),
    );

    expect(find.text('From the hub'), findsOneWidget);
    expect(find.text('From server A'), findsOneWidget);
    expect(find.byType(InstanceBadge), findsNWidgets(2));
    expect(find.text('Local hub'), findsWidgets);
    expect(find.text('Server A'), findsWidgets);
    // The scope selector appears once there is more than one instance.
    expect(find.text('All instances'), findsOneWidget);
  });

  testWidgets('a partial read is announced rather than silently trimmed', (
    tester,
  ) async {
    // A list that silently drops one machine's PRs is indistinguishable from
    // that machine having no work.
    await _pumpDashboard(
      tester,
      registry: _registry(),
      prs: AggregatedResult<PR>(
        items: [
          InstanceScoped(
            instanceId: 'hub-1',
            instanceName: 'Local hub',
            value: _pr(1, 'acme/tools', 7, 'Still here'),
          ),
        ],
        failures: const [
          InstanceFailure(
            instanceId: 'srv-a',
            instanceName: 'Server A',
            error: 'connection refused',
          ),
        ],
      ),
    );

    expect(find.text('Still here'), findsOneWidget);
    expect(
      find.text('Showing partial data — Server A could not be reached.'),
      findsOneWidget,
    );
  });
}

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/instances/aggregation.dart';
import 'package:heimdallm/core/instances/instances_providers.dart';
import 'package:heimdallm/core/instances/models.dart';
import 'package:heimdallm/core/models/pr.dart';
import 'package:heimdallm/core/models/review.dart';
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

PR _pr(
  int id,
  String repo,
  int number,
  String title, {
  Review? latestReview,
}) => PR(
  id: id,
  githubId: 1000 + id,
  repo: repo,
  number: number,
  title: title,
  author: 'alice',
  url: 'https://github.com/$repo/pull/$number',
  state: 'open',
  updatedAt: DateTime(2026, 9, 1),
  latestReview: latestReview,
);

Review _review(int id, String severity) => Review(
  id: id,
  prId: 0,
  cliUsed: 'claude',
  summary: '',
  issues: const [],
  severity: severity,
  createdAt: DateTime(2026, 9, 1),
);

TrackedIssue _issue(
  int id,
  String repo,
  int number,
  String title, {
  TrackedIssueReview? latestReview,
}) => TrackedIssue(
  id: id,
  githubId: 2000 + id,
  repo: repo,
  number: number,
  title: title,
  body: '',
  author: 'alice',
  assignees: const [],
  labels: const [],
  state: 'open',
  createdAt: DateTime(2026, 9, 1),
  fetchedAt: DateTime(2026, 9, 1),
  dismissed: false,
  latestReview: latestReview,
);

TrackedIssueReview _issueReview(int id) => TrackedIssueReview(
  id: id,
  issueId: 0,
  cliUsed: 'claude',
  summary: '',
  triage: const {},
  nextSteps: const [],
  actionTaken: '',
  prCreated: 0,
  createdAt: DateTime(2026, 9, 1),
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
  AggregatedResult<TrackedIssue>? issues,
  ClusterRegistry? registry,
  RoutingRules? routing,
  ApiClient? api,
  Map<String, ApiClient>? apiByInstance,
}) async {
  tester.view.physicalSize = const Size(1400, 1200);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.resetPhysicalSize);
  addTearDown(tester.view.resetDevicePixelRatio);

  final localApi = api ?? _MockApiClient();
  await tester.pumpWidget(
    ProviderScope(
      overrides: [
        platformServicesProvider.overrideWithValue(FakePlatformServices()),
        apiClientProvider.overrideWithValue(localApi),
        if (apiByInstance != null)
          apiClientForProvider.overrideWith(
            (ref, id) => apiByInstance[id] ?? localApi,
          ),
        daemonHealthProvider.overrideWith((ref) async => true),
        daemonInstancesProvider.overrideWith(
          (ref) async => registry ?? ClusterRegistry.empty,
        ),
        prsByInstanceProvider.overrideWith((ref) async => prs),
        issuesByInstanceProvider.overrideWith(
          (ref) async => issues ?? singleInstanceResult(<TrackedIssue>[]),
        ),
        routingRulesProvider.overrideWith((ref) async => routing ?? RoutingRules.empty),
        sseStreamProvider.overrideWith((ref) => const Stream.empty()),
      ],
      child: MaterialApp.router(
        routerConfig: GoRouter(
          routes: [
            GoRoute(path: '/', builder: (_, _) => const DashboardScreen()),
            // Placeholders so a tap-to-navigate test can assert on arrival
            // without pulling in the real detail screens' own providers.
            GoRoute(
              path: '/prs/:id',
              builder: (_, state) =>
                  Scaffold(body: Text('PR detail ${state.pathParameters['id']}')),
            ),
            GoRoute(
              path: '/issues/:id',
              builder: (_, state) => Scaffold(
                body: Text('Issue detail ${state.pathParameters['id']}'),
              ),
            ),
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

  // ------------------------------------------------- theburrowhub/heimdallm#769

  testWidgets(
    'the same PR reported by two instances renders as one row with both badges',
    (tester) async {
      await _pumpDashboard(
        tester,
        registry: _registry(),
        prs: AggregatedResult<PR>(
          items: [
            InstanceScoped(
              instanceId: 'srv-a',
              instanceName: 'Server A',
              value: _pr(1, 'theburrowhub/heimdallm', 769, 'Same PR'),
            ),
            InstanceScoped(
              instanceId: 'hub-1',
              instanceName: 'Local hub',
              value: _pr(2, 'theburrowhub/heimdallm', 769, 'Same PR'),
            ),
          ],
        ),
      );

      // One row, not two: this is the bug itself, reproduced.
      expect(find.byType(Card), findsOneWidget);
      expect(find.text('Same PR'), findsOneWidget);
      expect(find.byType(InstanceBadge), findsNWidgets(2));
      expect(find.text('Server A'), findsWidgets);
      expect(find.text('Local hub'), findsWidgets);
    },
  );

  testWidgets('the routing owner wins the displayed data', (tester) async {
    await _pumpDashboard(
      tester,
      registry: _registry(),
      routing: const RoutingRules(
        enabled: true,
        repos: {'theburrowhub/heimdallm': 'srv-a'},
      ),
      prs: AggregatedResult<PR>(
        items: [
          InstanceScoped(
            instanceId: 'hub-1',
            instanceName: 'Local hub',
            value: _pr(1, 'theburrowhub/heimdallm', 769, 'Same PR'),
          ),
          InstanceScoped(
            instanceId: 'srv-a',
            instanceName: 'Server A',
            value: _pr(
              2,
              'theburrowhub/heimdallm',
              769,
              'Same PR',
              latestReview: _review(99, 'high'),
            ),
          ),
        ],
      ),
    );

    // The owner (srv-a) has the review; its severity must be shown, not the
    // hub's PENDING placeholder.
    expect(find.text('HIGH'), findsOneWidget);
    expect(find.text('PENDING'), findsNothing);
  });

  testWidgets('without routing rules, the instance with a review wins', (
    tester,
  ) async {
    await _pumpDashboard(
      tester,
      registry: _registry(),
      // routingRulesProvider defaults to RoutingRules.empty (enabled: false).
      prs: AggregatedResult<PR>(
        items: [
          InstanceScoped(
            instanceId: 'hub-1',
            instanceName: 'Local hub',
            value: _pr(1, 'theburrowhub/heimdallm', 769, 'Same PR'),
          ),
          InstanceScoped(
            instanceId: 'srv-a',
            instanceName: 'Server A',
            value: _pr(
              2,
              'theburrowhub/heimdallm',
              769,
              'Same PR',
              latestReview: _review(99, 'medium'),
            ),
          ),
        ],
      ),
    );

    expect(find.text('MEDIUM'), findsOneWidget);
    expect(find.text('PENDING'), findsNothing);
  });

  testWidgets(
    'a disabled default_instance fallback does not win over a real review',
    (tester) async {
      await _pumpDashboard(
        tester,
        registry: _registry(),
        // enabled: false must not let defaultInstance decide, even though
        // it is set — RoutingRules.ownerFor would still resolve it, but the
        // call site must gate on `enabled` first (Router.OwnerFor returns ""
        // when disabled; RoutingRules.ownerFor alone does not know that).
        routing: const RoutingRules(defaultInstance: 'hub-1'),
        prs: AggregatedResult<PR>(
          items: [
            InstanceScoped(
              instanceId: 'hub-1',
              instanceName: 'Local hub',
              value: _pr(1, 'theburrowhub/heimdallm', 769, 'Same PR'),
            ),
            InstanceScoped(
              instanceId: 'srv-a',
              instanceName: 'Server A',
              value: _pr(
                2,
                'theburrowhub/heimdallm',
                769,
                'Same PR',
                latestReview: _review(99, 'low'),
              ),
            ),
          ],
        ),
      );

      expect(find.text('LOW'), findsOneWidget);
      expect(find.text('PENDING'), findsNothing);
    },
  );

  testWidgets('the same issue reported by two instances renders as one row', (
    tester,
  ) async {
    await _pumpDashboard(
      tester,
      registry: _registry(),
      prs: singleInstanceResult(const <PR>[]),
      issues: AggregatedResult<TrackedIssue>(
        items: [
          InstanceScoped(
            instanceId: 'srv-a',
            instanceName: 'Server A',
            value: _issue(1, 'acme/tools', 12, 'Same issue'),
          ),
          InstanceScoped(
            instanceId: 'hub-1',
            instanceName: 'Local hub',
            value: _issue(2, 'acme/tools', 12, 'Same issue'),
          ),
        ],
      ),
    );

    expect(find.byType(Card), findsOneWidget);
    expect(find.text('Same issue'), findsOneWidget);
  });

  testWidgets('dismissing a shared PR fans out to every reporting instance', (
    tester,
  ) async {
    final hubApi = _MockApiClient();
    final srvApi = _MockApiClient();
    when(() => hubApi.dismissPR(any())).thenAnswer((_) async {});
    when(() => srvApi.dismissPR(any())).thenAnswer((_) async {});

    await _pumpDashboard(
      tester,
      registry: _registry(),
      apiByInstance: {'hub-1': hubApi, 'srv-a': srvApi},
      prs: AggregatedResult<PR>(
        items: [
          InstanceScoped(
            instanceId: 'hub-1',
            instanceName: 'Local hub',
            value: _pr(11, 'theburrowhub/heimdallm', 769, 'Same PR'),
          ),
          InstanceScoped(
            instanceId: 'srv-a',
            instanceName: 'Server A',
            value: _pr(42, 'theburrowhub/heimdallm', 769, 'Same PR'),
          ),
        ],
      ),
    );

    await tester.tap(find.byTooltip('Dismiss PR'));
    await tester.pumpAndSettle();

    verify(() => hubApi.dismissPR(11)).called(1);
    verify(() => srvApi.dismissPR(42)).called(1);
  });

  testWidgets('undoing a dismissed PR un-dismisses it on every instance', (
    tester,
  ) async {
    final hubApi = _MockApiClient();
    final srvApi = _MockApiClient();
    when(() => hubApi.dismissPR(any())).thenAnswer((_) async {});
    when(() => srvApi.dismissPR(any())).thenAnswer((_) async {});
    when(() => hubApi.undismissPR(any())).thenAnswer((_) async {});
    when(() => srvApi.undismissPR(any())).thenAnswer((_) async {});

    await _pumpDashboard(
      tester,
      registry: _registry(),
      apiByInstance: {'hub-1': hubApi, 'srv-a': srvApi},
      prs: AggregatedResult<PR>(
        items: [
          InstanceScoped(
            instanceId: 'hub-1',
            instanceName: 'Local hub',
            value: _pr(11, 'theburrowhub/heimdallm', 769, 'Same PR'),
          ),
          InstanceScoped(
            instanceId: 'srv-a',
            instanceName: 'Server A',
            value: _pr(42, 'theburrowhub/heimdallm', 769, 'Same PR'),
          ),
        ],
      ),
    );

    await tester.tap(find.byTooltip('Dismiss PR'));
    await tester.pump();
    await tester.tap(find.text('Undo'));
    await tester.pumpAndSettle();

    verify(() => hubApi.undismissPR(11)).called(1);
    verify(() => srvApi.undismissPR(42)).called(1);
  });

  testWidgets(
    'a partial dismiss failure is reported and does not offer Undo',
    (tester) async {
      final hubApi = _MockApiClient();
      final srvApi = _MockApiClient();
      when(() => hubApi.dismissPR(any())).thenAnswer((_) async {});
      when(() => srvApi.dismissPR(any())).thenThrow(Exception('offline'));

      await _pumpDashboard(
        tester,
        registry: _registry(),
        apiByInstance: {'hub-1': hubApi, 'srv-a': srvApi},
        prs: AggregatedResult<PR>(
          items: [
            InstanceScoped(
              instanceId: 'hub-1',
              instanceName: 'Local hub',
              value: _pr(11, 'theburrowhub/heimdallm', 769, 'Same PR'),
            ),
            InstanceScoped(
              instanceId: 'srv-a',
              instanceName: 'Server A',
              value: _pr(42, 'theburrowhub/heimdallm', 769, 'Same PR'),
            ),
          ],
        ),
      );

      await tester.tap(find.byTooltip('Dismiss PR'));
      await tester.pumpAndSettle();

      expect(find.textContaining('Error dismissing PR #769'), findsOneWidget);
      expect(find.text('Undo'), findsNothing);
    },
  );

  testWidgets('tapping a PR row navigates to its detail route', (
    tester,
  ) async {
    await _pumpDashboard(
      tester,
      registry: _registry(),
      prs: AggregatedResult<PR>(
        items: [
          InstanceScoped(
            instanceId: 'srv-a',
            instanceName: 'Server A',
            value: _pr(42, 'theburrowhub/heimdallm', 769, 'Same PR'),
          ),
        ],
      ),
    );

    await tester.tap(find.text('Same PR'));
    await tester.pumpAndSettle();

    expect(find.text('PR detail 42'), findsOneWidget);
  });

  testWidgets('tapping Review triggers a review on the winning instance', (
    tester,
  ) async {
    final srvApi = _MockApiClient();
    when(() => srvApi.triggerReview(any())).thenAnswer((_) async {});

    await _pumpDashboard(
      tester,
      registry: _registry(),
      apiByInstance: {'srv-a': srvApi},
      prs: AggregatedResult<PR>(
        items: [
          InstanceScoped(
            instanceId: 'srv-a',
            instanceName: 'Server A',
            value: _pr(42, 'theburrowhub/heimdallm', 769, 'Same PR'),
          ),
        ],
      ),
    );

    await tester.tap(find.text('Review'));
    await tester.pump();

    verify(() => srvApi.triggerReview(42)).called(1);
  });

  testWidgets('tapping an issue row navigates to its detail route', (
    tester,
  ) async {
    await _pumpDashboard(
      tester,
      registry: _registry(),
      prs: singleInstanceResult(const <PR>[]),
      issues: AggregatedResult<TrackedIssue>(
        items: [
          InstanceScoped(
            instanceId: 'srv-a',
            instanceName: 'Server A',
            value: _issue(9, 'acme/tools', 12, 'Some issue'),
          ),
        ],
      ),
    );

    await tester.tap(find.text('Some issue'));
    await tester.pumpAndSettle();

    expect(find.text('Issue detail 9'), findsOneWidget);
  });

  testWidgets('undoing a dismissed issue un-dismisses it on every instance', (
    tester,
  ) async {
    final hubApi = _MockApiClient();
    final srvApi = _MockApiClient();
    when(() => hubApi.dismissIssue(any())).thenAnswer((_) async {});
    when(() => srvApi.dismissIssue(any())).thenAnswer((_) async {});
    when(() => hubApi.undismissIssue(any())).thenAnswer((_) async {});
    when(() => srvApi.undismissIssue(any())).thenAnswer((_) async {});

    await _pumpDashboard(
      tester,
      registry: _registry(),
      apiByInstance: {'hub-1': hubApi, 'srv-a': srvApi},
      prs: singleInstanceResult(const <PR>[]),
      issues: AggregatedResult<TrackedIssue>(
        items: [
          InstanceScoped(
            instanceId: 'hub-1',
            instanceName: 'Local hub',
            value: _issue(1, 'acme/tools', 12, 'Same issue'),
          ),
          InstanceScoped(
            instanceId: 'srv-a',
            instanceName: 'Server A',
            value: _issue(2, 'acme/tools', 12, 'Same issue'),
          ),
        ],
      ),
    );

    await tester.tap(find.byTooltip('Dismiss issue'));
    await tester.pump();
    await tester.tap(find.text('Undo'));
    await tester.pumpAndSettle();

    verify(() => hubApi.undismissIssue(1)).called(1);
    verify(() => srvApi.undismissIssue(2)).called(1);
  });

  testWidgets(
    'when both instances have a review, the newer one wins the tie-break',
    (tester) async {
      await _pumpDashboard(
        tester,
        registry: _registry(),
        prs: AggregatedResult<PR>(
          items: [
            InstanceScoped(
              instanceId: 'hub-1',
              instanceName: 'Local hub',
              value: _pr(
                1,
                'theburrowhub/heimdallm',
                769,
                'Same PR',
                latestReview: _review(10, 'low'),
              ),
            ),
            InstanceScoped(
              instanceId: 'srv-a',
              instanceName: 'Server A',
              value: _pr(
                2,
                'theburrowhub/heimdallm',
                769,
                'Same PR',
                latestReview: _review(20, 'high'),
              ),
            ),
          ],
        ),
      );

      expect(find.text('HIGH'), findsOneWidget);
      expect(find.text('LOW'), findsNothing);
    },
  );

  testWidgets('the routing owner wins for issues too', (tester) async {
    await _pumpDashboard(
      tester,
      registry: _registry(),
      routing: const RoutingRules(
        enabled: true,
        repos: {'acme/tools': 'srv-a'},
      ),
      prs: singleInstanceResult(const <PR>[]),
      issues: AggregatedResult<TrackedIssue>(
        items: [
          InstanceScoped(
            instanceId: 'hub-1',
            instanceName: 'Local hub',
            value: _issue(1, 'acme/tools', 12, 'Same issue'),
          ),
          InstanceScoped(
            instanceId: 'srv-a',
            instanceName: 'Server A',
            value: _issue(2, 'acme/tools', 12, 'Same issue'),
          ),
        ],
      ),
    );

    // Only the owner's badge shows first; both are present since the row is
    // still shared, but the tap target must resolve to srv-a — verified via
    // detail navigation.
    await tester.tap(find.text('Same issue'));
    await tester.pumpAndSettle();
    expect(find.text('Issue detail 2'), findsOneWidget);
  });

  testWidgets(
    'when both instances have reviewed the issue, the newer one wins',
    (tester) async {
      await _pumpDashboard(
        tester,
        registry: _registry(),
        prs: singleInstanceResult(const <PR>[]),
        issues: AggregatedResult<TrackedIssue>(
          items: [
            InstanceScoped(
              instanceId: 'hub-1',
              instanceName: 'Local hub',
              value: _issue(
                1,
                'acme/tools',
                12,
                'Same issue',
                latestReview: _issueReview(10),
              ),
            ),
            InstanceScoped(
              instanceId: 'srv-a',
              instanceName: 'Server A',
              value: _issue(
                2,
                'acme/tools',
                12,
                'Same issue',
                latestReview: _issueReview(20),
              ),
            ),
          ],
        ),
      );

      await tester.tap(find.text('Same issue'));
      await tester.pumpAndSettle();
      // The winner is whichever candidate carries the newer review (id 20 =
      // srv-a's local row id 2), the same tie-break _preferOwner uses for PRs.
      expect(find.text('Issue detail 2'), findsOneWidget);
    },
  );

  testWidgets(
    'a partial issue dismiss failure is reported and does not offer Undo',
    (tester) async {
      final hubApi = _MockApiClient();
      final srvApi = _MockApiClient();
      when(() => hubApi.dismissIssue(any())).thenAnswer((_) async {});
      when(() => srvApi.dismissIssue(any())).thenThrow(Exception('offline'));

      await _pumpDashboard(
        tester,
        registry: _registry(),
        apiByInstance: {'hub-1': hubApi, 'srv-a': srvApi},
        prs: singleInstanceResult(const <PR>[]),
        issues: AggregatedResult<TrackedIssue>(
          items: [
            InstanceScoped(
              instanceId: 'hub-1',
              instanceName: 'Local hub',
              value: _issue(1, 'acme/tools', 12, 'Same issue'),
            ),
            InstanceScoped(
              instanceId: 'srv-a',
              instanceName: 'Server A',
              value: _issue(2, 'acme/tools', 12, 'Same issue'),
            ),
          ],
        ),
      );

      await tester.tap(find.byTooltip('Dismiss issue'));
      await tester.pumpAndSettle();

      expect(
        find.textContaining('Error dismissing issue #12'),
        findsOneWidget,
      );
      expect(find.text('Undo'), findsNothing);
    },
  );
}

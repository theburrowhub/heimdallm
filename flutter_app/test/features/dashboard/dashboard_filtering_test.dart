import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/api/sse_client.dart';
import 'package:heimdallm/core/instances/aggregation.dart';
import 'package:heimdallm/core/models/pr.dart';
import 'package:heimdallm/core/models/review.dart';
import 'package:heimdallm/features/dashboard/activity_filters.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/features/dashboard/dashboard_screen.dart';
import 'package:heimdallm/shared/design_system/theme.dart';
import 'package:heimdallm/shared/widgets/severity_badge.dart';
import 'package:shared_preferences/shared_preferences.dart';

Review _review(String severity) => Review(
  id: 1,
  prId: 1,
  cliUsed: 'claude',
  summary: '',
  issues: const [],
  severity: severity,
  createdAt: DateTime.utc(2026, 1, 1),
);

PR _pr(
  int id, {
  required String repo,
  required String title,
  String author = 'alice',
  String state = 'open',
  String? severity,
  DateTime? updatedAt,
}) => PR(
  id: id,
  githubId: 1000 + id,
  repo: repo,
  number: id,
  title: title,
  author: author,
  url: 'https://github.com/$repo/pull/$id',
  state: state,
  updatedAt: updatedAt ?? DateTime.utc(2026, 1, 1),
  latestReview: severity == null ? null : _review(severity),
);

Future<ProviderContainer> _pump(
  WidgetTester tester, {
  required Future<AggregatedResult<PR>> Function() load,
  Stream<SseEvent> sse = const Stream.empty(),
}) async {
  await tester.binding.setSurfaceSize(const Size(1800, 1200));
  addTearDown(() => tester.binding.setSurfaceSize(null));
  final container = ProviderContainer(
    overrides: [
      prsByInstanceProvider.overrideWith((ref) => load()),
      sseStreamProvider.overrideWith((ref) => sse),
    ],
  );
  addTearDown(container.dispose);
  await tester.pumpWidget(
    UncontrolledProviderScope(
      container: container,
      child: MaterialApp.router(
        builder: (context, child) =>
            HeimdallmTheme.scope(child: child ?? const SizedBox.shrink()),
        routerConfig: GoRouter(
          routes: [
            GoRoute(path: '/', builder: (_, _) => const DashboardScreen()),
          ],
        ),
      ),
    ),
  );
  await tester.pumpAndSettle();
  return container;
}

void _setFilters(
  ProviderContainer container,
  ActivityFilters Function(ActivityFilters) edit,
) {
  final notifier = container.read(activityFiltersProvider.notifier);
  notifier.update(edit(container.read(activityFiltersProvider)));
}

double _top(WidgetTester tester, String title) =>
    tester.getTopLeft(find.text(title)).dy;

void main() {
  setUp(() => SharedPreferences.setMockInitialValues({}));

  final prs = [
    _pr(1, repo: 'acme/api', title: 'Add rate limiter', author: 'alice'),
    _pr(2, repo: 'acme/web', title: 'Fix login page', author: 'bob'),
    _pr(3, repo: 'globex/core', title: 'Bump deps', author: 'carol'),
    _pr(
      4,
      repo: 'acme/api',
      title: 'Old closed change',
      author: 'dave',
      state: 'closed',
    ),
  ];

  testWidgets('org, repo and state filters narrow the PR rows', (tester) async {
    final container = await _pump(
      tester,
      load: () async => singleInstanceResult(prs),
    );

    // Default: only open PRs.
    expect(find.text('Add rate limiter'), findsOneWidget);
    expect(find.text('Bump deps'), findsOneWidget);
    expect(find.text('Old closed change'), findsNothing);

    _setFilters(container, (f) => f.copyWith(orgs: {'acme'}));
    await tester.pumpAndSettle();
    expect(find.text('Add rate limiter'), findsOneWidget);
    expect(find.text('Fix login page'), findsOneWidget);
    expect(find.text('Bump deps'), findsNothing);

    _setFilters(container, (f) => f.copyWith(repos: {'acme/api'}));
    await tester.pumpAndSettle();
    expect(find.text('Add rate limiter'), findsOneWidget);
    expect(find.text('Fix login page'), findsNothing);

    _setFilters(container, (f) => f.copyWith(states: {'closed'}));
    await tester.pumpAndSettle();
    expect(find.text('Old closed change'), findsOneWidget);
    expect(find.text('Add rate limiter'), findsNothing);

    _setFilters(
      container,
      (f) => f.copyWith(orgs: {'initech'}, repos: {}, states: {}),
    );
    await tester.pumpAndSettle();
    expect(find.text('No items match the current filters.'), findsOneWidget);
  });

  testWidgets('search matches title, repo, number and author', (tester) async {
    final container = await _pump(
      tester,
      load: () async => singleInstanceResult(prs),
    );

    Future<void> search(String q) async {
      _setFilters(container, (f) => f.copyWith(search: q));
      await tester.pumpAndSettle();
    }

    await search('LOGIN');
    expect(find.text('Fix login page'), findsOneWidget);
    expect(find.text('Add rate limiter'), findsNothing);

    await search('globex');
    expect(find.text('Bump deps'), findsOneWidget);
    expect(find.text('Fix login page'), findsNothing);

    await search('carol');
    expect(find.text('Bump deps'), findsOneWidget);

    await search('1');
    expect(find.text('Add rate limiter'), findsOneWidget);
    expect(find.text('Bump deps'), findsNothing);

    await search('nothing-matches-this');
    expect(find.text('No items match the current filters.'), findsOneWidget);
  });

  testWidgets(
    'priority sort puts unreviewed first, then high → low, newest on ties',
    (tester) async {
      final ranked = [
        _pr(1, repo: 'a/a', title: 'low', severity: 'low'),
        _pr(2, repo: 'a/a', title: 'high old', severity: 'high'),
        _pr(
          3,
          repo: 'a/a',
          title: 'high new',
          severity: 'high',
          updatedAt: DateTime.utc(2026, 3, 1),
        ),
        _pr(4, repo: 'a/a', title: 'medium', severity: 'medium'),
        _pr(5, repo: 'a/a', title: 'unreviewed'),
      ];
      final container = await _pump(
        tester,
        load: () async => singleInstanceResult(ranked),
      );

      final order = [
        'unreviewed',
        'high new',
        'high old',
        'medium',
        'low',
      ].map((t) => _top(tester, t)).toList();
      expect(order, orderedEquals([...order]..sort()));

      container.read(reviewsSortProvider.notifier).set(SortMode.newest);
      await tester.pumpAndSettle();
      expect(_top(tester, 'high new'), lessThan(_top(tester, 'low')));
    },
  );

  testWidgets('grid view renders one card per PR with its severity', (
    tester,
  ) async {
    final container = await _pump(
      tester,
      load: () async => singleInstanceResult([
        _pr(7, repo: 'acme/api', title: 'Grid PR', severity: 'high'),
        _pr(8, repo: 'acme/api', title: 'Grid unreviewed'),
      ]),
    );

    _setFilters(container, (f) => f.copyWith(viewMode: 'grid'));
    await tester.pumpAndSettle();

    expect(find.byType(GridView), findsOneWidget);
    expect(find.text('Grid PR'), findsOneWidget);
    expect(find.text('acme/api #7 · alice'), findsOneWidget);
    expect(
      find.descendant(
        of: find.byType(GridView),
        matching: find.byType(SeverityBadge),
      ),
      findsOneWidget,
    );
  });

  testWidgets('a pr_state_changed event reloads the PR list', (tester) async {
    var loads = 0;
    final events = StreamController<SseEvent>.broadcast();
    addTearDown(events.close);
    await _pump(
      tester,
      load: () async {
        loads++;
        return singleInstanceResult(prs);
      },
      sse: events.stream,
    );
    final before = loads;

    events.add(const SseEvent(type: 'review_started', data: '{}'));
    await tester.pumpAndSettle();
    expect(loads, before, reason: 'unrelated events must not reload');

    events.add(const SseEvent(type: 'pr_state_changed', data: '{}'));
    await tester.pumpAndSettle();
    expect(loads, greaterThan(before));
  });

  testWidgets('Retry on the error view reloads the PR list', (tester) async {
    var loads = 0;
    await _pump(
      tester,
      load: () async {
        loads++;
        throw ApiException('offline');
      },
    );
    expect(find.text('Could not reach the Heimdallm daemon.'), findsOneWidget);
    final before = loads;

    await tester.tap(find.text('Retry'));
    await tester.pumpAndSettle();
    expect(loads, greaterThan(before));
  });
}

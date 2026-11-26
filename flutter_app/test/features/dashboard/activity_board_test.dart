import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/instances/aggregation.dart';
import 'package:heimdallm/core/models/issue.dart';
import 'package:heimdallm/core/models/my_prs_summary.dart';
import 'package:heimdallm/core/models/pr.dart';
import 'package:heimdallm/core/models/review.dart';
import 'package:heimdallm/core/models/review_status.dart';
import 'package:heimdallm/features/dashboard/activity_filters.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/features/dashboard/dashboard_screen.dart';
import 'package:heimdallm/features/instances/widgets/instance_badge.dart';
import 'package:heimdallm/features/merge_tracking/merge_tracking_providers.dart';
import 'package:heimdallm/shared/design_system/components/app_list_row.dart';
import 'package:heimdallm/shared/design_system/theme.dart';
import 'package:mocktail/mocktail.dart';
import 'package:shared_preferences/shared_preferences.dart';

class _MockApiClient extends Mock implements ApiClient {}

final _now = DateTime.now();

PR _pr(
  int id,
  String title, {
  String repo = 'acme/api',
  String state = 'open',
  Review? review,
  ReviewExecutionStatus? status,
}) => PR(
  id: id,
  githubId: 1000 + id,
  repo: repo,
  number: id,
  title: title,
  author: 'alice',
  url: 'https://github.com/$repo/pull/$id',
  state: state,
  updatedAt: _now.subtract(Duration(minutes: id)),
  latestReview: review,
  reviewStatus: status,
);

Review _review(int prId, {int githubReviewId = 55, DateTime? at}) => Review(
  id: prId,
  prId: prId,
  cliUsed: 'claude',
  summary: 'Summary of review $prId',
  issues: const [
    Issue(file: 'a.go', line: 3, description: 'Bug', severity: 'high'),
    Issue(file: 'a.go', line: 9, description: 'Nit', severity: 'low'),
  ],
  severity: 'high',
  // Exactly now, so "today" holds even when the suite runs at midnight.
  createdAt: at ?? _now,
  githubReviewId: githubReviewId,
  githubReviewState: githubReviewId > 0 ? 'CHANGES_REQUESTED' : '',
);

ReviewExecutionStatus _status({String error = '', bool active = false}) =>
    ReviewExecutionStatus(
      headSha: 'abc',
      attempts: 1,
      failedAt: _now,
      retryAt: _now.add(const Duration(minutes: 30)),
      error: error,
      active: active,
    );

final _prs = [
  _pr(1, 'Failed PR', status: _status(error: 'agent exited')),
  _pr(2, 'Running PR', repo: 'acme/web', status: _status(active: true)),
  _pr(3, 'Done PR', review: _review(3)),
  _pr(4, 'Waiting PR', repo: 'acme/web', review: _review(4, githubReviewId: 0)),
  _pr(
    5,
    'Old PR',
    review: _review(5, at: _now.subtract(const Duration(days: 3))),
  ),
];

Future<(ProviderContainer, _MockApiClient)> _pump(
  WidgetTester tester, {
  Size size = const Size(1400, 1000),
  List<PR>? prs,
  AggregatedResult<PR>? result,
  List<PR> Function()? load,
  MyPrsSummary yourPrs = const MyPrsSummary(),
  Map<String, Object> prefs = const {},
}) async {
  SharedPreferences.setMockInitialValues(prefs);
  tester.view.physicalSize = size;
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.resetPhysicalSize);
  addTearDown(tester.view.resetDevicePixelRatio);
  final api = _MockApiClient();
  final container = ProviderContainer(
    overrides: [
      apiClientProvider.overrideWithValue(api),
      prsByInstanceProvider.overrideWith(
        (ref) async =>
            result ?? singleInstanceResult(load?.call() ?? prs ?? _prs),
      ),
      sseStreamProvider.overrideWith((ref) => const Stream.empty()),
      myPrsSummaryProvider.overrideWith((ref) => yourPrs),
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
            GoRoute(
              path: '/merge',
              builder: (_, _) => const Scaffold(body: Text('My PRs screen')),
            ),
            GoRoute(
              path: '/prs/:id',
              builder: (_, state) => Scaffold(
                body: Text('PR detail ${state.pathParameters['id']}'),
              ),
            ),
          ],
        ),
      ),
    ),
  );
  await _settle(tester);
  return (container, api);
}

/// A running review's spinner never settles, so pump a fixed time instead.
Future<void> _settle(WidgetTester tester) async {
  for (var i = 0; i < 5; i++) {
    await tester.pump(const Duration(milliseconds: 200));
  }
}

Finder _lane(String name) => find.byKey(Key('board-lane-$name'));

Finder _inLane(String name, String text) =>
    find.descendant(of: _lane(name), matching: find.text(text));

Finder _card(String title) => find.ancestor(
  of: find.text(title),
  matching: find.byWidgetPredicate(
    (w) => w.runtimeType.toString() == '_BoardCard',
  ),
);

Finder _inCard(String title, Finder finder) =>
    find.descendant(of: _card(title), matching: finder);

/// The card whose border shows the keyboard selection, or null.
String? _selectedTitle(WidgetTester tester) {
  for (final t in [
    'Failed PR',
    'Running PR',
    'Waiting PR',
    'Done PR',
    'Old PR',
  ]) {
    if (_card(t).evaluate().isEmpty) continue;
    final material = tester.widget<Material>(
      find.descendant(of: _card(t), matching: find.byType(Material)).first,
    );
    if ((material.shape as RoundedRectangleBorder).side.width == 2) return t;
  }
  return null;
}

void main() {
  testWidgets('every PR sits in exactly one lane', (tester) async {
    await _pump(tester);

    expect(find.byKey(const Key('activity-board')), findsOneWidget);
    expect(find.byType(AppListRow), findsNothing);
    expect(_inLane('needsYou', 'Failed PR'), findsOneWidget);
    expect(_inLane('inProgress', 'Running PR'), findsOneWidget);
    expect(_inLane('inProgress', 'Waiting PR'), findsOneWidget);
    expect(_inLane('reviewed', 'Done PR'), findsOneWidget);
    expect(_inLane('reviewed', 'Old PR'), findsOneWidget);
    for (final t in [
      'Failed PR',
      'Running PR',
      'Waiting PR',
      'Done PR',
      'Old PR',
    ]) {
      expect(find.text(t), findsOneWidget, reason: t);
    }
    // Today's review leads the earlier one, under its own heading.
    expect(_inLane('reviewed', 'TODAY'), findsOneWidget);
    expect(_inLane('reviewed', 'EARLIER'), findsOneWidget);
    expect(
      tester.getTopLeft(find.text('Done PR')).dy,
      lessThan(tester.getTopLeft(find.text('Old PR')).dy),
    );
    // What each card says about its review.
    expect(_inCard('Done PR', find.text('Changes requested')), findsOneWidget);
    expect(_inCard('Done PR', find.text('1 high')), findsOneWidget);
    expect(_inCard('Done PR', find.text('1 low')), findsOneWidget);
    expect(
      _inCard('Waiting PR', find.text('Waiting to publish')),
      findsOneWidget,
    );
    expect(
      _inCard('Failed PR', find.textContaining('agent exited · failed')),
      findsOneWidget,
    );
  });

  testWidgets('a lane with one section has no headings; empty lanes say so', (
    tester,
  ) async {
    await _pump(tester, prs: [_pr(3, 'Done PR', review: _review(3))]);

    expect(find.text('TODAY'), findsNothing);
    expect(find.text('Nothing needs you.'), findsOneWidget);
    expect(find.text('Nothing in progress.'), findsOneWidget);
  });

  testWidgets('only closed PRs under the default filter say there are none', (
    tester,
  ) async {
    await _pump(tester, prs: [_pr(9, 'Merged PR', state: 'closed')]);

    expect(find.text('No open PRs.'), findsOneWidget);
    expect(find.byKey(const Key('activity-board')), findsNothing);
  });

  testWidgets('a closed PR, shown with the Closed filter, says so', (
    tester,
  ) async {
    final (container, _) = await _pump(
      tester,
      prs: [
        _pr(
          9,
          'Merged PR',
          state: 'closed',
          review: _review(9, at: _now.subtract(const Duration(days: 2))),
        ),
      ],
    );
    container
        .read(activityFiltersProvider.notifier)
        .update(const ActivityFilters(states: {'open', 'closed'}));
    await _settle(tester);

    expect(_inLane('reviewed', 'Merged PR'), findsOneWidget);
    expect(
      _inCard('Merged PR', find.textContaining('· closed')),
      findsOneWidget,
    );
    expect(find.text('1 item'), findsOneWidget);
  });

  testWidgets('a review GitHub rejected for good says so on its card', (
    tester,
  ) async {
    await _pump(
      tester,
      prs: [_pr(7, 'Rejected PR', review: _review(7, githubReviewId: -1))],
    );

    expect(_inLane('needsYou', 'Rejected PR'), findsOneWidget);
    expect(
      _inCard('Rejected PR', find.text('GitHub refused the review for good')),
      findsOneWidget,
    );
  });

  testWidgets('clicking a card opens the PR', (tester) async {
    await _pump(tester);

    await tester.tap(find.text('Done PR'));
    await _settle(tester);
    expect(find.text('PR detail 3'), findsOneWidget);
  });

  testWidgets('Retry on a failed card starts a review', (tester) async {
    final (_, api) = await _pump(tester);
    when(() => api.triggerReview(any())).thenAnswer((_) async {});

    await tester.tap(_inCard('Failed PR', find.text('Retry')));
    await tester.pump();

    verify(() => api.triggerReview(1)).called(1);
  });

  testWidgets('Review now starts a review and the card offers Cancel', (
    tester,
  ) async {
    final (_, api) = await _pump(tester, prs: [_pr(6, 'New PR')]);
    when(() => api.triggerReview(any())).thenAnswer((_) async {});

    await tester.tap(_inCard('New PR', find.text('Review now')));
    await tester.pump();

    verify(() => api.triggerReview(6)).called(1);
    expect(_inCard('New PR', find.text('Reviewing…')), findsOneWidget);
    expect(_inCard('New PR', find.text('Cancel')), findsOneWidget);
  });

  testWidgets('Cancel on a running card stops its review', (tester) async {
    final (_, api) = await _pump(tester);
    when(() => api.cancelReview(any())).thenAnswer((_) async {});

    await tester.tap(_inCard('Running PR', find.text('Cancel')));
    await _settle(tester);
    await tester.tap(find.widgetWithText(FilledButton, 'Cancel review'));
    await _settle(tester);

    verify(() => api.cancelReview(2)).called(1);
  });

  // The daemon leaves dismissed PRs out of /prs, so the reload after a dismiss
  // unmounts the card that showed the toast (#911).
  testWidgets('Undo from a card brings the PR back once it left', (
    tester,
  ) async {
    var dismissed = false;
    final done = _pr(3, 'Done PR', review: _review(3));
    final (_, api) = await _pump(
      tester,
      load: () => dismissed ? const [] : [done],
    );
    when(() => api.dismissPR(any())).thenAnswer((_) async => dismissed = true);
    when(
      () => api.undismissPR(any()),
    ).thenAnswer((_) async => dismissed = false);

    await tester.tap(_inCard('Done PR', find.byTooltip('Dismiss PR')));
    await _settle(tester);
    expect(find.text('Done PR'), findsNothing);

    await tester.tap(find.text('Undo'));
    await _settle(tester);
    verify(() => api.undismissPR(3)).called(1);
    expect(find.text('Done PR'), findsOneWidget);
    await tester.pump(const Duration(seconds: 6));
  });

  group('Your PRs', () {
    final card = find.byKey(const Key('board-your-prs'));

    testWidgets('leads Needs you and opens My PRs', (tester) async {
      await _pump(
        tester,
        yourPrs: const MyPrsSummary(needAction: 1, ready: 1, total: 2),
      );

      expect(
        find.descendant(of: _lane('needsYou'), matching: card),
        findsOneWidget,
      );
      expect(find.text('2 of your PRs need you'), findsOneWidget);
      expect(
        find.text('1 needs your action · 1 ready to merge'),
        findsOneWidget,
      );

      await tester.tap(card);
      await _settle(tester);
      expect(find.text('My PRs screen'), findsOneWidget);
    });

    testWidgets('does not take a grid column of its own', (tester) async {
      // 1400 px with one card per lane: an extra column would go to the lane
      // with the most cards per column, so counting this card as one would
      // halve the PR card beside an empty cell.
      await _pump(
        tester,
        yourPrs: const MyPrsSummary(needAction: 1, total: 1),
        prs: [
          _pr(1, 'Failed PR', status: _status(error: 'agent exited')),
          _pr(2, 'Running PR', status: _status(active: true)),
          _pr(3, 'Done PR', review: _review(3)),
        ],
      );

      final lane = tester.getSize(_lane('needsYou')).width;
      expect(tester.getSize(_card('Failed PR')).width, closeTo(lane - 20, 1));
      expect(tester.getSize(card).width, closeTo(lane - 20, 1));
    });

    testWidgets('says one PR in the singular, and hides at zero', (
      tester,
    ) async {
      await _pump(tester, yourPrs: const MyPrsSummary(ready: 1, total: 1));
      expect(find.text('1 of your PRs needs you'), findsOneWidget);
    });

    testWidgets('is hidden when none needs the operator', (tester) async {
      await _pump(tester);
      expect(card, findsNothing);
    });
  });

  testWidgets('a PR several instances report carries their badges', (
    tester,
  ) async {
    final a = _pr(3, 'Shared PR', review: _review(3));
    final b = _pr(30, 'Shared PR', review: _review(30));
    await _pump(
      tester,
      result: AggregatedResult<PR>(
        items: [
          InstanceScoped(instanceId: 'a', instanceName: 'A', value: a),
          InstanceScoped(
            instanceId: 'b',
            instanceName: 'B',
            value: PR(
              id: b.id,
              githubId: b.githubId,
              repo: a.repo,
              number: a.number,
              title: a.title,
              author: a.author,
              url: a.url,
              state: a.state,
              updatedAt: a.updatedAt,
              latestReview: b.latestReview,
            ),
          ),
        ],
      ),
    );

    expect(find.text('Shared PR'), findsOneWidget);
    expect(_inCard('Shared PR', find.byType(InstanceBadges)), findsOneWidget);
  });

  group('boardLayout', () {
    test('empty lanes stay narrow and the full one spreads over columns', () {
      final l = boardLayout([0, 0, 31], 1700);
      expect(l.columns, [0, 0, 4]);
      expect(l.widths[0], boardEmptyLaneWidth);
      expect(l.widths[1], boardEmptyLaneWidth);
      expect(l.widths.reduce((a, b) => a + b), closeTo(1700, 0.01));
    });

    test('spare columns go to the lane with the most cards per column', () {
      final l = boardLayout([1, 1, 31], 1450);
      expect(l.columns, [1, 1, 2]);
      expect(l.widths[2], greaterThan(l.widths[0]));
    });

    test('a lane never gets more columns than cards', () {
      expect(boardLayout([0, 2, 0], 3000).columns, [0, 2, 0]);
    });

    test('cards stop at their cap and the spare width is not handed out', () {
      final few = boardLayout([1, 1, 1], 3000);
      expect(few.columns, [1, 1, 1]);
      for (final w in few.widths) {
        expect(w, boardCardMaxWidth + 20);
      }
      final one = boardLayout([0, 0, 1], 3000);
      expect(one.widths, [
        boardEmptyLaneWidth,
        boardEmptyLaneWidth,
        boardCardMaxWidth + 20,
      ]);
      // Several columns stop at the cap per card, plus the gaps between.
      final two = boardLayout([0, 0, 2], 3000);
      expect(two.columns, [0, 0, 2]);
      expect(two.widths[2], 2 * boardCardMaxWidth + 8 + 20);
    });

    test('a narrow board keeps one column per lane and still fills it', () {
      final l = boardLayout([3, 3, 3], 904);
      expect(l.columns, [1, 1, 1]);
      expect(l.widths.reduce((a, b) => a + b), closeTo(904, 0.01));
    });
  });

  group('keyboard', () {
    Future<void> press(WidgetTester tester, LogicalKeyboardKey key) async {
      await tester.sendKeyEvent(key);
      await _settle(tester);
    }

    testWidgets(
      'arrows move within and across lanes, Esc clears, Enter opens',
      (tester) async {
        // Narrow enough for one card column per lane.
        await _pump(tester, size: const Size(1000, 1000));
        expect(_selectedTitle(tester), isNull);

        await press(tester, LogicalKeyboardKey.arrowDown);
        expect(_selectedTitle(tester), 'Failed PR', reason: 'first card');
        await press(tester, LogicalKeyboardKey.arrowRight);
        expect(_selectedTitle(tester), 'Running PR');
        await press(tester, LogicalKeyboardKey.arrowDown);
        expect(_selectedTitle(tester), 'Waiting PR');
        await press(tester, LogicalKeyboardKey.arrowDown);
        expect(
          _selectedTitle(tester),
          'Waiting PR',
          reason: 'stops at the end',
        );
        await press(tester, LogicalKeyboardKey.arrowRight);
        expect(_selectedTitle(tester), 'Old PR', reason: 'same position');
        await press(tester, LogicalKeyboardKey.arrowUp);
        expect(_selectedTitle(tester), 'Done PR');
        await press(tester, LogicalKeyboardKey.arrowRight);
        expect(_selectedTitle(tester), 'Done PR', reason: 'no lane further');
        await press(tester, LogicalKeyboardKey.arrowLeft);
        expect(_selectedTitle(tester), 'Running PR');

        await press(tester, LogicalKeyboardKey.escape);
        expect(_selectedTitle(tester), isNull);

        await press(tester, LogicalKeyboardKey.arrowLeft);
        expect(_selectedTitle(tester), 'Failed PR');
        await press(tester, LogicalKeyboardKey.enter);
        expect(find.text('PR detail 1'), findsOneWidget);
      },
    );

    testWidgets('on a lane with several columns, arrows walk the grid', (
      tester,
    ) async {
      // 1400 px: In progress gets two columns, so its cards share a row.
      await _pump(tester);
      final running = tester.getTopLeft(find.text('Running PR'));
      final waiting = tester.getTopLeft(find.text('Waiting PR'));
      expect(running.dy, waiting.dy, reason: 'one row');
      expect(running.dx, lessThan(waiting.dx));

      await press(tester, LogicalKeyboardKey.arrowDown);
      await press(tester, LogicalKeyboardKey.arrowRight);
      expect(_selectedTitle(tester), 'Running PR');
      await press(tester, LogicalKeyboardKey.arrowDown);
      expect(_selectedTitle(tester), 'Running PR', reason: 'no row below');
      await press(tester, LogicalKeyboardKey.arrowRight);
      expect(_selectedTitle(tester), 'Waiting PR', reason: 'along the row');
      await press(tester, LogicalKeyboardKey.arrowRight);
      expect(_selectedTitle(tester), 'Done PR', reason: 'into the next lane');
      await press(tester, LogicalKeyboardKey.arrowLeft);
      expect(_selectedTitle(tester), 'Waiting PR', reason: 'back to the end');
    });

    testWidgets('sideways skips an empty lane', (tester) async {
      await _pump(
        tester,
        prs: [
          _pr(1, 'Failed PR', status: _status(error: 'agent exited')),
          _pr(3, 'Done PR', review: _review(3)),
        ],
      );
      await press(tester, LogicalKeyboardKey.arrowDown);
      await press(tester, LogicalKeyboardKey.arrowRight);
      expect(_selectedTitle(tester), 'Done PR');
    });

    testWidgets('the search field keeps its keys; a click restores them', (
      tester,
    ) async {
      await _pump(tester);

      await tester.tap(find.byType(TextField));
      await _settle(tester);
      await press(tester, LogicalKeyboardKey.arrowDown);
      expect(_selectedTitle(tester), isNull);

      await tester.tap(find.text('Needs you'));
      await _settle(tester);
      await press(tester, LogicalKeyboardKey.arrowDown);
      expect(_selectedTitle(tester), 'Failed PR');
    });

    testWidgets('keys do nothing while another shell destination shows', (
      tester,
    ) async {
      SharedPreferences.setMockInitialValues({});
      tester.view.physicalSize = const Size(1400, 1000);
      tester.view.devicePixelRatio = 1;
      addTearDown(tester.view.resetPhysicalSize);
      addTearDown(tester.view.resetDevicePixelRatio);
      final container = ProviderContainer(
        overrides: [
          apiClientProvider.overrideWithValue(_MockApiClient()),
          prsByInstanceProvider.overrideWith(
            (ref) async => singleInstanceResult(_prs),
          ),
          sseStreamProvider.overrideWith((ref) => const Stream.empty()),
          myPrsSummaryProvider.overrideWith((ref) => const MyPrsSummary()),
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
                StatefulShellRoute.indexedStack(
                  builder: (context, state, shell) => Column(
                    children: [
                      TextButton(
                        onPressed: () => shell.goBranch(1),
                        child: const Text('Go to other'),
                      ),
                      Expanded(child: shell),
                    ],
                  ),
                  branches: [
                    StatefulShellBranch(
                      routes: [
                        GoRoute(
                          path: '/',
                          builder: (_, _) => const DashboardScreen(),
                        ),
                      ],
                    ),
                    StatefulShellBranch(
                      routes: [
                        GoRoute(
                          path: '/other',
                          builder: (_, _) =>
                              const Scaffold(body: Text('Other screen')),
                        ),
                      ],
                    ),
                  ],
                ),
                GoRoute(
                  path: '/prs/:id',
                  builder: (_, state) => Scaffold(
                    body: Text('PR detail ${state.pathParameters['id']}'),
                  ),
                ),
              ],
            ),
          ),
        ),
      );
      await _settle(tester);
      await press(tester, LogicalKeyboardKey.arrowDown);
      expect(_selectedTitle(tester), 'Failed PR');

      await tester.tap(find.text('Go to other'));
      await _settle(tester);
      expect(find.text('Other screen'), findsOneWidget);

      await press(tester, LogicalKeyboardKey.enter);
      expect(find.textContaining('PR detail'), findsNothing);
    });
  });

  group('views', () {
    testWidgets('the toggle switches between board, list and grid', (
      tester,
    ) async {
      final (container, _) = await _pump(tester);

      await tester.tap(find.byTooltip('List view'));
      await _settle(tester);
      expect(find.byKey(const Key('activity-board')), findsNothing);
      expect(find.byType(AppListRow), findsNWidgets(5));
      expect(container.read(activityFiltersProvider).viewMode, 'list');

      await tester.tap(find.byTooltip('Grid view'));
      await _settle(tester);
      expect(find.byType(GridView), findsOneWidget);

      await tester.tap(find.byTooltip('Board view'));
      await _settle(tester);
      expect(find.byKey(const Key('activity-board')), findsOneWidget);
      final prefs = await SharedPreferences.getInstance();
      expect(prefs.getString('activity_view'), 'board');
    });

    testWidgets('a stored list from before the board opens the board', (
      tester,
    ) async {
      await _pump(tester, prefs: {'activity_view_mode': 'list'});
      expect(find.byKey(const Key('activity-board')), findsOneWidget);
    });

    testWidgets('a stored grid from before the board stays a grid', (
      tester,
    ) async {
      await _pump(tester, prefs: {'activity_view_mode': 'grid'});
      expect(find.byType(GridView), findsOneWidget);

      // The next save stores the view under the new key and drops the old.
      await tester.tap(find.byTooltip('List view'));
      await _settle(tester);
      final prefs = await SharedPreferences.getInstance();
      expect(prefs.getString('activity_view'), 'list');
      expect(prefs.getString('activity_view_mode'), isNull);
    });

    testWidgets('a wider window brings a stored board back', (tester) async {
      await _pump(tester, size: const Size(boardBreakpoint - 1, 1000));
      expect(find.byKey(const Key('activity-board')), findsNothing);

      tester.view.physicalSize = const Size(1400, 1000);
      await _settle(tester);
      expect(find.byKey(const Key('activity-board')), findsOneWidget);
      expect(find.byTooltip('Board view'), findsOneWidget);
    });

    testWidgets('a list picked after the board stays a list', (tester) async {
      await _pump(tester, prefs: {'activity_view': 'list'});
      expect(find.byType(AppListRow), findsNWidgets(5));
    });

    testWidgets('Reset clears the filters and keeps the view', (tester) async {
      final (container, _) = await _pump(
        tester,
        prefs: {'activity_view': 'list'},
      );
      container
          .read(activityFiltersProvider.notifier)
          .update(
            container.read(activityFiltersProvider).copyWith(search: 'Done'),
          );
      await _settle(tester);

      await tester.tap(find.text('Reset'));
      await _settle(tester);
      final filters = container.read(activityFiltersProvider);
      expect(filters.search, isEmpty);
      expect(filters.viewMode, 'list');
    });

    testWidgets('below the breakpoint the board falls back to rows', (
      tester,
    ) async {
      await _pump(tester, size: const Size(boardBreakpoint - 1, 1000));

      expect(find.byKey(const Key('activity-board')), findsNothing);
      expect(find.byType(AppListRow), findsNWidgets(5));
      // The toggle does not offer a board that cannot show.
      expect(find.byTooltip('Board view'), findsNothing);
      expect(find.byTooltip('List view'), findsOneWidget);

      await tester.tap(find.text('Needs you'));
      await _settle(tester);
      expect(find.byType(AppListRow), findsOneWidget);
      expect(find.text('Failed PR'), findsOneWidget);
      expect(find.text('1 item'), findsOneWidget);
    });

    testWidgets('at the breakpoint the board fits without overflowing', (
      tester,
    ) async {
      await _pump(
        tester,
        size: const Size(boardBreakpoint, 700),
        yourPrs: const MyPrsSummary(needAction: 1, total: 1),
        prs: [
          ..._prs,
          _pr(
            8,
            'A PR whose very long title has to wrap inside the narrowest lane '
            'the board draws, twice over',
            review: _review(8),
          ),
        ],
      );

      expect(find.byKey(const Key('activity-board')), findsOneWidget);
      expect(tester.takeException(), isNull);
    });
  });
}

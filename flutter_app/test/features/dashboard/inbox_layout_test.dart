import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/instances/aggregation.dart';
import 'package:heimdallm/core/models/pr.dart';
import 'package:heimdallm/core/models/review.dart';
import 'package:heimdallm/core/models/review_status.dart';
import 'package:heimdallm/features/dashboard/activity_filters.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/features/dashboard/dashboard_screen.dart';
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
  state: 'open',
  updatedAt: _now.subtract(Duration(minutes: id)),
  latestReview: review,
  reviewStatus: status,
);

Review _review(int prId, {int githubReviewId = 55}) => Review(
  id: prId,
  prId: prId,
  cliUsed: 'claude',
  summary: 'Summary of review $prId',
  issues: const [],
  severity: 'low',
  // Exactly now, so "done today" holds even when the suite runs at midnight.
  createdAt: _now,
  githubReviewId: githubReviewId,
  githubReviewState: githubReviewId > 0 ? 'APPROVED' : '',
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
];

Future<(ProviderContainer, _MockApiClient)> _pump(
  WidgetTester tester, {
  Size size = const Size(1600, 1000),
  List<PR>? prs,
}) async {
  SharedPreferences.setMockInitialValues({});
  tester.view.physicalSize = size;
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.resetPhysicalSize);
  addTearDown(tester.view.resetDevicePixelRatio);
  final api = _MockApiClient();
  final container = ProviderContainer(
    overrides: [
      apiClientProvider.overrideWithValue(api),
      prsByInstanceProvider.overrideWith(
        (ref) async => singleInstanceResult(prs ?? _prs),
      ),
      sseStreamProvider.overrideWith((ref) => const Stream.empty()),
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

Finder _inList(String text) => find.descendant(
  of: find.byKey(const Key('inbox-list')),
  matching: find.text(text),
);

Finder _bucket(String name) => find.byKey(Key('inbox-bucket-$name'));

void main() {
  testWidgets('wide windows show the buckets with their counts', (
    tester,
  ) async {
    await _pump(tester);

    expect(find.byKey(const Key('inbox-sidebar')), findsOneWidget);
    String countOf(String name) => tester
        .widgetList<Text>(
          find.descendant(of: _bucket(name), matching: find.byType(Text)),
        )
        .last
        .data!;
    expect(countOf('needsYou'), '1');
    expect(countOf('inProgress'), '2');
    expect(countOf('doneToday'), '1');
    expect(countOf('all'), '4');
    // The rows are compact: their actions are in the preview.
    expect(find.byType(AppListRow), findsNothing);
    expect(find.text('Select a PR to read its review'), findsOneWidget);
    expect(
      find.text('1 need you · 2 in progress · 1 done today'),
      findsOneWidget,
    );
  });

  testWidgets('a bucket narrows the list and an empty one says so', (
    tester,
  ) async {
    await _pump(tester, prs: [_pr(3, 'Done PR', review: _review(3))]);

    await tester.tap(_bucket('doneToday'));
    await _settle(tester);
    expect(_inList('Done PR'), findsOneWidget);

    await tester.tap(_bucket('needsYou'));
    await _settle(tester);
    expect(_inList('Done PR'), findsNothing);
    expect(find.text('Nothing needs you.'), findsOneWidget);
    expect(find.text('0 items'), findsOneWidget);

    await tester.tap(_bucket('inProgress'));
    await _settle(tester);
    expect(find.text('Nothing in progress.'), findsOneWidget);
  });

  testWidgets('a row previews its review and Open goes to the detail', (
    tester,
  ) async {
    await _pump(tester);

    await tester.tap(_inList('Done PR'));
    await _settle(tester);
    expect(find.text('Summary of review 3'), findsOneWidget);
    expect(find.text('Select a PR to read its review'), findsNothing);

    await tester.tap(find.byKey(const Key('inbox-preview-open')));
    await _settle(tester);
    expect(find.text('PR detail 3'), findsOneWidget);
  });

  testWidgets('the preview empties when its PR leaves the list', (
    tester,
  ) async {
    await _pump(tester);
    await tester.tap(_inList('Done PR'));
    await _settle(tester);

    await tester.tap(_bucket('needsYou'));
    await _settle(tester);
    expect(find.text('Summary of review 3'), findsNothing);
    expect(find.text('Select a PR to read its review'), findsOneWidget);
  });

  testWidgets('Re-review in the preview starts a review and offers Cancel', (
    tester,
  ) async {
    final (_, api) = await _pump(tester);
    when(() => api.triggerReview(any())).thenAnswer((_) async {});

    await tester.tap(_inList('Done PR'));
    await _settle(tester);
    await tester.tap(find.text('Re-review'));
    await tester.pump();

    verify(() => api.triggerReview(3)).called(1);
    expect(find.text('Cancel review'), findsOneWidget);
    // The running PR and the one just started.
    expect(_inList('Reviewing…'), findsNWidgets(2));
  });

  testWidgets('a failed review shows its error and Retry in the preview', (
    tester,
  ) async {
    final (_, api) = await _pump(tester);
    when(() => api.triggerReview(any())).thenAnswer((_) async {});

    await tester.tap(_inList('Failed PR'));
    await _settle(tester);
    expect(find.textContaining('agent exited · failed'), findsOneWidget);

    await tester.tap(find.text('Retry'));
    await tester.pump();
    verify(() => api.triggerReview(1)).called(1);
  });

  testWidgets('Cancel review in the preview stops the running review', (
    tester,
  ) async {
    final (_, api) = await _pump(tester);
    when(() => api.cancelReview(any())).thenAnswer((_) async {});

    await tester.tap(_inList('Running PR'));
    await _settle(tester);
    await tester.tap(find.text('Cancel review'));
    await _settle(tester);
    await tester.tap(find.widgetWithText(FilledButton, 'Cancel review'));
    await _settle(tester);

    verify(() => api.cancelReview(2)).called(1);
  });

  testWidgets('Dismiss in the preview dismisses the PR', (tester) async {
    final (_, api) = await _pump(tester);
    when(() => api.dismissPR(any())).thenAnswer((_) async {});

    await tester.tap(_inList('Waiting PR'));
    await _settle(tester);
    await tester.tap(find.byTooltip('Dismiss PR'));
    await _settle(tester);

    verify(() => api.dismissPR(4)).called(1);
  });

  testWidgets('a repository narrows the list and a second tap clears it', (
    tester,
  ) async {
    final (container, _) = await _pump(tester);
    final web = find.descendant(
      of: find.byKey(const Key('inbox-sidebar')),
      matching: find.text('web'),
    );

    await tester.tap(web);
    await _settle(tester);
    expect(container.read(activityFiltersProvider).repos, {'acme/web'});
    expect(_inList('Running PR'), findsOneWidget);
    expect(_inList('Done PR'), findsNothing);
    // The other repository stays listed so the operator can switch to it.
    expect(
      find.descendant(
        of: find.byKey(const Key('inbox-sidebar')),
        matching: find.text('api'),
      ),
      findsOneWidget,
    );

    await tester.tap(web);
    await _settle(tester);
    expect(container.read(activityFiltersProvider).repos, isEmpty);
    expect(_inList('Done PR'), findsOneWidget);
  });

  testWidgets('grid view keeps the buckets beside the cards', (tester) async {
    final (container, _) = await _pump(tester);
    final filters = container.read(activityFiltersProvider.notifier);
    filters.update(
      container.read(activityFiltersProvider).copyWith(viewMode: 'grid'),
    );
    await _settle(tester);

    expect(find.byType(GridView), findsOneWidget);
    await tester.tap(_bucket('needsYou'));
    await _settle(tester);
    expect(find.text('Failed PR'), findsOneWidget);
    expect(find.text('Done PR'), findsNothing);

    await tester.tap(_bucket('doneToday'));
    await _settle(tester);
    filters.update(
      container.read(activityFiltersProvider).copyWith(search: 'Failed'),
    );
    await _settle(tester);
    expect(find.text('No reviews finished today yet.'), findsOneWidget);
  });

  testWidgets('narrow windows filter with bucket segments above the rows', (
    tester,
  ) async {
    await _pump(tester, size: const Size(1000, 1000));

    expect(find.byKey(const Key('inbox-sidebar')), findsNothing);
    expect(find.byType(AppListRow), findsNWidgets(4));

    await tester.tap(find.text('Needs you'));
    await _settle(tester);
    expect(find.byType(AppListRow), findsOneWidget);
    expect(find.text('Failed PR'), findsOneWidget);

    await tester.tap(find.text('Done today'));
    await _settle(tester);
    expect(find.text('Done PR'), findsOneWidget);
    expect(find.text('1 item'), findsOneWidget);
  });

  testWidgets('narrow grid view shows the cards of the chosen bucket', (
    tester,
  ) async {
    final (container, _) = await _pump(tester, size: const Size(1000, 1000));
    container
        .read(activityFiltersProvider.notifier)
        .update(
          container.read(activityFiltersProvider).copyWith(viewMode: 'grid'),
        );
    await _settle(tester);
    await tester.tap(find.text('In progress'));
    await _settle(tester);

    expect(find.byType(GridView), findsOneWidget);
    expect(find.text('Running PR'), findsOneWidget);
    expect(find.text('Done PR'), findsNothing);
  });
}

import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/activity.dart';
import 'package:heimdallm/features/activity/activity_providers.dart';
import 'package:heimdallm/features/activity/activity_screen.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:mocktail/mocktail.dart';

class MockApiClient extends Mock implements ApiClient {}

ActivityEntry _mk(
  int n,
  DateTime ts, {
  ActivityAction a = ActivityAction.review,
  String org = 'acme',
  String repo = 'acme/api',
  String outcome = 'minor',
}) => ActivityEntry(
  id: n,
  timestamp: ts,
  org: org,
  repo: repo,
  itemType: 'pr',
  itemNumber: n,
  itemTitle: 'Title $n',
  action: a,
  outcome: outcome,
  details: const {},
);

ProviderScope _scope({
  required AsyncValue<ActivityPage> value,
  ApiClient? api,
}) {
  Future<ActivityPage> resolve() async {
    if (value is AsyncError) {
      throw (value as AsyncError).error;
    }
    return (value.value)!;
  }

  return ProviderScope(
    overrides: [
      activityEntriesProvider.overrideWith((ref) => resolve()),
      activityOptionsProvider.overrideWith((ref) => resolve()),
      if (api != null) apiClientProvider.overrideWithValue(api),
    ],
    child: const MaterialApp(home: Scaffold(body: ActivityScreen())),
  );
}

void main() {
  const emptyPage = AsyncData(
    ActivityPage(entries: [], truncated: false, count: 0),
  );

  testWidgets('empty state when no entries', (tester) async {
    await tester.pumpWidget(
      _scope(
        value: const AsyncData(
          ActivityPage(entries: [], truncated: false, count: 0),
        ),
      ),
    );
    await tester.pumpAndSettle();
    expect(find.textContaining('No activity'), findsOneWidget);
  });

  testWidgets('groups entries by hour', (tester) async {
    final base = DateTime(2026, 4, 20, 9);
    await tester.pumpWidget(
      _scope(
        value: AsyncData(
          ActivityPage(
            entries: [
              _mk(1, base.add(const Duration(minutes: 5))),
              _mk(2, base.add(const Duration(minutes: 30))),
              _mk(3, base.add(const Duration(hours: 1, minutes: 10))),
            ],
            truncated: false,
            count: 3,
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();
    expect(find.text('09:00'), findsOneWidget);
    expect(find.text('10:00'), findsOneWidget);
  });

  testWidgets('shows truncation banner when truncated', (tester) async {
    await tester.pumpWidget(
      _scope(
        value: AsyncData(
          ActivityPage(
            entries: [_mk(1, DateTime.now())],
            truncated: true,
            count: 1,
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();
    expect(find.textContaining('Showing'), findsOneWidget);
    expect(find.textContaining('Narrow filters'), findsOneWidget);
  });

  testWidgets('emits a date header per day in multi-day ranges', (
    tester,
  ) async {
    await tester.pumpWidget(
      _scope(
        value: AsyncData(
          ActivityPage(
            entries: [
              _mk(1, DateTime(2026, 4, 18, 9, 5)),
              _mk(2, DateTime(2026, 4, 19, 9, 30)),
              _mk(3, DateTime(2026, 4, 19, 10, 0)),
            ],
            truncated: false,
            count: 3,
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();
    expect(find.text('Apr 18, 2026'), findsOneWidget);
    expect(find.text('Apr 19, 2026'), findsOneWidget);
    // '09:00' appears twice — once per day — which was the pre-fix bug
    expect(find.text('09:00'), findsNWidgets(2));
    expect(find.text('10:00'), findsOneWidget);
  });

  testWidgets('ActivityDisabledException renders friendly empty state', (
    tester,
  ) async {
    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          activityEntriesProvider.overrideWith(
            (ref) => Future<ActivityPage>.error(ActivityDisabledException()),
          ),
          activityOptionsProvider.overrideWith(
            (ref) => Future<ActivityPage>.error(ActivityDisabledException()),
          ),
        ],
        child: const MaterialApp(home: Scaffold(body: ActivityScreen())),
      ),
    );
    await tester.pumpAndSettle();
    expect(find.text('Activity log is disabled'), findsOneWidget);
    expect(find.textContaining('Enable activity_log'), findsOneWidget);
    expect(find.textContaining('Error:'), findsNothing);
  });

  testWidgets('filter options fall back to visible entries when options fail', (
    tester,
  ) async {
    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          activityEntriesProvider.overrideWith(
            (ref) async => ActivityPage(
              entries: [_mk(1, DateTime(2026, 4, 20, 9))],
              truncated: false,
              count: 1,
            ),
          ),
          activityOptionsProvider.overrideWith(
            (ref) => Future<ActivityPage>.error(Exception('bad limit')),
          ),
        ],
        child: const MaterialApp(home: Scaffold(body: ActivityScreen())),
      ),
    );
    await tester.pumpAndSettle();

    await tester.tap(find.text('Organization'));
    await tester.pumpAndSettle();

    expect(find.text('acme'), findsOneWidget);
    expect(find.text('Options limited to visible activity'), findsOneWidget);
  });

  testWidgets('filter options are sorted for stable picker order', (
    tester,
  ) async {
    await tester.pumpWidget(
      _scope(
        value: AsyncData(
          ActivityPage(
            entries: [
              _mk(1, DateTime(2026, 4, 20, 9), org: 'zeta'),
              _mk(2, DateTime(2026, 4, 20, 10), org: 'acme'),
            ],
            truncated: false,
            count: 2,
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    await tester.tap(find.text('Organization'));
    await tester.pumpAndSettle();

    final tiles = tester
        .widgetList<CheckboxListTile>(find.byType(CheckboxListTile))
        .toList();
    expect((tiles[0].title as Text).data, 'acme');
    expect((tiles[1].title as Text).data, 'zeta');
  });

  testWidgets('Add PR dialog validates a PR URL submitted from the keyboard', (
    tester,
  ) async {
    final api = MockApiClient();
    await tester.pumpWidget(_scope(value: emptyPage, api: api));
    await tester.pumpAndSettle();

    await tester.tap(find.text('Add PR'));
    await tester.pumpAndSettle();
    expect(find.text('Add a pull request'), findsOneWidget);

    await tester.enterText(find.byType(TextField), 'not a GitHub PR');
    await tester.testTextInput.receiveAction(TextInputAction.done);
    await tester.pump();

    expect(find.textContaining('Enter a GitHub PR link'), findsOneWidget);
    verifyNever(() => api.addPRByUrl(any()));
  });

  testWidgets('Add PR dialog shows daemon errors and allows retry', (
    tester,
  ) async {
    final api = MockApiClient();
    when(() => api.addPRByUrl(any())).thenThrow(ApiException('PR not found'));
    await tester.pumpWidget(_scope(value: emptyPage, api: api));
    await tester.pumpAndSettle();

    await tester.tap(find.text('Add PR'));
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byType(TextField),
      'https://github.com/acme/widgets/pull/404',
    );
    await tester.tap(find.text('Add & review'));
    await tester.pumpAndSettle();

    expect(find.text('PR not found'), findsOneWidget);
    final submit = tester.widget<FilledButton>(find.byType(FilledButton));
    expect(submit.onPressed, isNotNull);
    verify(
      () => api.addPRByUrl('https://github.com/acme/widgets/pull/404'),
    ).called(1);
  });

  testWidgets('Add PR dialog closes and confirms a successful submission', (
    tester,
  ) async {
    final api = MockApiClient();
    final result = Completer<int>();
    when(() => api.addPRByUrl(any())).thenAnswer((_) => result.future);
    await tester.pumpWidget(_scope(value: emptyPage, api: api));
    await tester.pumpAndSettle();

    await tester.tap(find.text('Add PR'));
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byType(TextField),
      'https://github.com/acme/widgets/pull/42',
    );
    await tester.tap(find.text('Add & review'));
    await tester.pump();

    expect(find.byType(CircularProgressIndicator), findsOneWidget);
    expect(tester.widget<TextField>(find.byType(TextField)).enabled, isFalse);

    result.complete(73);
    await tester.pumpAndSettle();

    expect(find.text('Add a pull request'), findsNothing);
    expect(
      find.text('PR added — repository monitored and review started.'),
      findsOneWidget,
    );
  });

  testWidgets('Add PR dialog can be cancelled', (tester) async {
    final api = MockApiClient();
    await tester.pumpWidget(_scope(value: emptyPage, api: api));
    await tester.pumpAndSettle();

    await tester.tap(find.text('Add PR'));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Cancel'));
    await tester.pumpAndSettle();

    expect(find.text('Add a pull request'), findsNothing);
    verifyNever(() => api.addPRByUrl(any()));
  });
}

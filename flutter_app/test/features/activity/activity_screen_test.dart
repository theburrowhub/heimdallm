import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/activity.dart';
import 'package:heimdallm/features/activity/activity_providers.dart';
import 'package:heimdallm/features/activity/activity_screen.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/shared/design_system/theme.dart';
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
    child: const MaterialApp(
      builder: _withMixScope,
      home: Scaffold(body: ActivityScreen()),
    ),
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
    expect(_textContaining('No activity'), findsOneWidget);
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
    expect(_text('09:00'), findsOneWidget);
    expect(_text('10:00'), findsOneWidget);
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
    expect(_textContaining('Showing'), findsOneWidget);
    expect(_textContaining('Narrow filters'), findsOneWidget);
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
    expect(_text('Apr 18, 2026'), findsOneWidget);
    expect(_text('Apr 19, 2026'), findsOneWidget);
    // '09:00' appears twice — once per day — which was the pre-fix bug
    expect(_text('09:00'), findsNWidgets(2));
    expect(_text('10:00'), findsOneWidget);
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
        child: const MaterialApp(
          builder: _withMixScope,
          home: Scaffold(body: ActivityScreen()),
        ),
      ),
    );
    await tester.pumpAndSettle();
    expect(_text('Activity log is disabled'), findsOneWidget);
    expect(_textContaining('Enable activity_log'), findsOneWidget);
    expect(_textContaining('Error:'), findsNothing);
  });

  testWidgets('unexpected activity failures render the generic error state', (
    tester,
  ) async {
    await tester.pumpWidget(
      _scope(value: AsyncError(Exception('daemon is down'), StackTrace.empty)),
    );
    await tester.pumpAndSettle();

    expect(
      _textContaining('Could not load activity: Exception: daemon is down'),
      findsOneWidget,
    );
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
        child: const MaterialApp(
          builder: _withMixScope,
          home: Scaffold(body: ActivityScreen()),
        ),
      ),
    );
    await tester.pumpAndSettle();

    await tester.tap(_text('Organization'));
    await tester.pumpAndSettle();

    expect(_text('acme'), findsOneWidget);
    expect(_text('Options limited to visible activity'), findsOneWidget);
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

    await tester.tap(_text('Organization'));
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

    await tester.tap(_text('Add PR'));
    await tester.pumpAndSettle();
    expect(_text('Add a pull request'), findsOneWidget);

    await tester.enterText(find.byType(TextField), 'not a GitHub PR');
    await tester.testTextInput.receiveAction(TextInputAction.done);
    await tester.pump();

    expect(_textContaining('Enter a GitHub PR link'), findsOneWidget);
    verifyNever(() => api.addPRByUrl(any()));
  });

  testWidgets('Add PR dialog shows daemon errors and allows retry', (
    tester,
  ) async {
    final api = MockApiClient();
    when(() => api.addPRByUrl(any())).thenThrow(ApiException('PR not found'));
    await tester.pumpWidget(_scope(value: emptyPage, api: api));
    await tester.pumpAndSettle();

    await tester.tap(_text('Add PR'));
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byType(TextField),
      'https://github.com/acme/widgets/pull/404',
    );
    await tester.tap(_text('Add & review'));
    await tester.pumpAndSettle();

    expect(_text('PR not found'), findsOneWidget);
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

    await tester.tap(_text('Add PR'));
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byType(TextField),
      'https://github.com/acme/widgets/pull/42',
    );
    await tester.tap(_text('Add & review'));
    await tester.pump();

    expect(find.byType(CircularProgressIndicator), findsOneWidget);
    expect(tester.widget<TextField>(find.byType(TextField)).enabled, isFalse);

    result.complete(73);
    await tester.pumpAndSettle();

    expect(_text('Add a pull request'), findsNothing);
    expect(
      _text('PR added — repository monitored and review started.'),
      findsOneWidget,
    );
  });

  testWidgets('Add PR dialog can be cancelled', (tester) async {
    final api = MockApiClient();
    await tester.pumpWidget(_scope(value: emptyPage, api: api));
    await tester.pumpAndSettle();

    await tester.tap(_text('Add PR'));
    await tester.pumpAndSettle();
    await tester.tap(_text('Cancel'));
    await tester.pumpAndSettle();

    expect(_text('Add a pull request'), findsNothing);
    verifyNever(() => api.addPRByUrl(any()));
  });

  testWidgets(
    'tapping an entry opens details and reports GitHub launch failures',
    (tester) async {
      const channel = MethodChannel('plugins.flutter.io/url_launcher');
      final messenger =
          TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger;
      final launchedUrls = <String>[];
      messenger.setMockMethodCallHandler(channel, (call) async {
        if (call.method == 'launch') {
          launchedUrls.add((call.arguments as Map)['url'] as String);
          return false;
        }
        if (call.method == 'canLaunch') return true;
        return null;
      });
      addTearDown(() => messenger.setMockMethodCallHandler(channel, null));

      await tester.pumpWidget(
        _scope(
          value: AsyncData(
            ActivityPage(
              entries: [
                ActivityEntry(
                  id: 42,
                  timestamp: DateTime(2026, 4, 20, 9, 34, 12),
                  org: 'acme',
                  repo: 'acme/widgets',
                  itemType: 'pr',
                  itemNumber: 42,
                  itemTitle: 'Title 42',
                  action: ActivityAction.review,
                  outcome: 'major',
                  details: const {'cli_used': 'claude', 'attempts': 2},
                ),
              ],
              truncated: false,
              count: 1,
            ),
          ),
        ),
      );
      await tester.pumpAndSettle();

      await tester.tap(_textContaining('Title 42'));
      await tester.pumpAndSettle();

      expect(_textContaining('major review by claude'), findsWidgets);
      expect(_text('Repository'), findsWidgets);
      expect(_text('acme/widgets'), findsOneWidget);
      expect(_text('Details'), findsOneWidget);
      expect(_textContaining('"attempts": 2'), findsOneWidget);

      await tester.tap(_text('Open in GitHub'));
      await tester.pumpAndSettle();

      expect(launchedUrls, contains('https://github.com/acme/widgets/pull/42'));
      expect(
        _textContaining(
          'Could not open https://github.com/acme/widgets/pull/42',
        ),
        findsOneWidget,
      );

      await tester.tap(find.byTooltip('Close'));
      await tester.pumpAndSettle();
      expect(_text('Details'), findsNothing);
    },
  );
}

Widget _withMixScope(BuildContext context, Widget? child) =>
    HeimdallmTheme.scope(child: child ?? const SizedBox.shrink());

Finder _text(String value) => find.text(value, findRichText: true);

Finder _textContaining(String value) =>
    find.textContaining(value, findRichText: true);

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/activity.dart';
import 'package:heimdallm/features/activity/activity_providers.dart';
import 'package:heimdallm/features/activity/activity_screen.dart';

ActivityEntry _mk(int n, DateTime ts, {ActivityAction a = ActivityAction.review}) =>
    ActivityEntry(
      id: n,
      timestamp: ts,
      org: 'acme',
      repo: 'acme/api',
      itemType: 'pr',
      itemNumber: n,
      itemTitle: 'Title $n',
      action: a,
      outcome: 'minor',
      details: const {},
    );

ProviderScope _scope({required AsyncValue<ActivityPage> value}) {
  Future<ActivityPage> resolve() async {
    if (value is AsyncError) {
      throw (value as AsyncError).error;
    }
    return (value.valueOrNull)!;
  }

  return ProviderScope(
    overrides: [
      activityEntriesProvider.overrideWith((ref) => resolve()),
      activityOptionsProvider.overrideWith((ref) => resolve()),
    ],
    child: const MaterialApp(home: Scaffold(body: ActivityScreen())),
  );
}

void main() {
  testWidgets('empty state when no entries', (tester) async {
    await tester.pumpWidget(_scope(
      value: const AsyncData(ActivityPage(entries: [], truncated: false, count: 0)),
    ));
    await tester.pumpAndSettle();
    expect(find.textContaining('No activity'), findsOneWidget);
  });

  testWidgets('groups entries by hour', (tester) async {
    final base = DateTime(2026, 4, 20, 9);
    await tester.pumpWidget(_scope(
      value: AsyncData(ActivityPage(
        entries: [
          _mk(1, base.add(const Duration(minutes: 5))),
          _mk(2, base.add(const Duration(minutes: 30))),
          _mk(3, base.add(const Duration(hours: 1, minutes: 10))),
        ],
        truncated: false,
        count: 3,
      )),
    ));
    await tester.pumpAndSettle();
    expect(find.text('09:00'), findsOneWidget);
    expect(find.text('10:00'), findsOneWidget);
  });

  testWidgets('shows truncation banner when truncated', (tester) async {
    await tester.pumpWidget(_scope(
      value: AsyncData(ActivityPage(
        entries: [_mk(1, DateTime.now())],
        truncated: true,
        count: 1,
      )),
    ));
    await tester.pumpAndSettle();
    expect(find.textContaining('Showing'), findsOneWidget);
    expect(find.textContaining('Narrow filters'), findsOneWidget);
  });

  testWidgets('emits a date header per day in multi-day ranges', (tester) async {
    await tester.pumpWidget(_scope(
      value: AsyncData(ActivityPage(
        entries: [
          _mk(1, DateTime(2026, 4, 18, 9, 5)),
          _mk(2, DateTime(2026, 4, 19, 9, 30)),
          _mk(3, DateTime(2026, 4, 19, 10, 0)),
        ],
        truncated: false,
        count: 3,
      )),
    ));
    await tester.pumpAndSettle();
    expect(find.text('Apr 18, 2026'), findsOneWidget);
    expect(find.text('Apr 19, 2026'), findsOneWidget);
    // '09:00' appears twice — once per day — which was the pre-fix bug
    expect(find.text('09:00'), findsNWidgets(2));
    expect(find.text('10:00'), findsOneWidget);
  });

  testWidgets('ActivityDisabledException renders friendly empty state',
      (tester) async {
    await tester.pumpWidget(ProviderScope(
      overrides: [
        activityEntriesProvider.overrideWith(
          (ref) => Future<ActivityPage>.error(ActivityDisabledException()),
        ),
        activityOptionsProvider.overrideWith(
          (ref) => Future<ActivityPage>.error(ActivityDisabledException()),
        ),
      ],
      child: const MaterialApp(home: Scaffold(body: ActivityScreen())),
    ));
    await tester.pumpAndSettle();
    expect(find.text('Activity log is disabled'), findsOneWidget);
    expect(find.textContaining('Enable activity_log'), findsOneWidget);
    expect(find.textContaining('Error:'), findsNothing);
  });
}

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/activity.dart';
import 'package:heimdallm/features/activity/activity_providers.dart';
import 'package:heimdallm/features/activity/widgets/activity_filter_chips.dart';

Widget _host({List<String> orgs = const ['acme', 'initech']}) {
  return ProviderScope(
    child: MaterialApp(
      home: Scaffold(
        body: ActivityFilterChips(
          availableOrgs: orgs,
          availableRepos: const [],
        ),
      ),
    ),
  );
}

void main() {
  testWidgets('org checkbox updates visually when tapped in bottom sheet', (
    tester,
  ) async {
    await tester.pumpWidget(_host());
    await tester.pumpAndSettle();

    await tester.tap(find.text('Organization'));
    await tester.pumpAndSettle();

    final acmeTile = find.ancestor(
      of: find.text('acme'),
      matching: find.byType(CheckboxListTile),
    );
    expect(tester.widget<CheckboxListTile>(acmeTile).value, isFalse);

    await tester.tap(acmeTile);
    await tester.pumpAndSettle();

    expect(tester.widget<CheckboxListTile>(acmeTile).value, isTrue);

    await tester.tap(acmeTile);
    await tester.pumpAndSettle();

    expect(tester.widget<CheckboxListTile>(acmeTile).value, isFalse);
  });

  testWidgets('action checkbox updates visually when tapped in bottom sheet', (
    tester,
  ) async {
    await tester.pumpWidget(_host());
    await tester.pumpAndSettle();

    await tester.tap(find.text('Action'));
    await tester.pumpAndSettle();

    final reviewTile = find.ancestor(
      of: find.text('Reviews'),
      matching: find.byType(CheckboxListTile),
    );
    expect(tester.widget<CheckboxListTile>(reviewTile).value, isFalse);

    await tester.tap(reviewTile);
    await tester.pumpAndSettle();

    expect(tester.widget<CheckboxListTile>(reviewTile).value, isTrue);

    expect(find.text('Refinement'), findsOneWidget);
  });

  testWidgets('string picker shows an empty state instead of a blank sheet', (
    tester,
  ) async {
    await tester.pumpWidget(_host(orgs: const []));
    await tester.pumpAndSettle();

    await tester.tap(find.text('Organization'));
    await tester.pumpAndSettle();

    expect(find.text('No options available'), findsOneWidget);
  });

  testWidgets('quick chips update type, action, and outcome filters', (
    tester,
  ) async {
    final container = ProviderContainer();
    addTearDown(container.dispose);

    await tester.pumpWidget(
      UncontrolledProviderScope(
        container: container,
        child: const MaterialApp(
          home: Scaffold(
            body: ActivityFilterChips(
              availableOrgs: [],
              availableRepos: [],
              availableOutcomes: ['draft'],
            ),
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    await tester.tap(find.text('PRs'));
    await tester.tap(find.text('Draft skips'));
    await tester.pumpAndSettle();

    final q = container.read(activityQueryProvider);
    expect(q.itemTypes, {'pr'});
    expect(q.actions, {ActivityAction.reviewSkipped});
    expect(q.outcomes, {'draft'});
  });

  testWidgets('chip label shows selection count', (tester) async {
    final container = ProviderContainer();
    addTearDown(container.dispose);
    container.read(activityQueryProvider.notifier).toggleOrg('acme');

    await tester.pumpWidget(
      UncontrolledProviderScope(
        container: container,
        child: const MaterialApp(
          home: Scaffold(
            body: ActivityFilterChips(
              availableOrgs: ['acme'],
              availableRepos: [],
            ),
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('Organization · 1'), findsOneWidget);
  });

  testWidgets(
    'Clear filters button appears when any filter is active and resets',
    (tester) async {
      final container = ProviderContainer();
      addTearDown(container.dispose);
      container.read(activityQueryProvider.notifier).toggleOrg('acme');

      await tester.pumpWidget(
        UncontrolledProviderScope(
          container: container,
          child: const MaterialApp(
            home: Scaffold(
              body: ActivityFilterChips(
                availableOrgs: ['acme'],
                availableRepos: [],
              ),
            ),
          ),
        ),
      );
      await tester.pumpAndSettle();

      expect(find.text('Clear filters'), findsOneWidget);
      await tester.tap(find.text('Clear filters'));
      await tester.pumpAndSettle();

      expect(find.text('Clear filters'), findsNothing);
      expect(container.read(activityQueryProvider).orgs, isEmpty);
    },
  );
}

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/features/stats/stats_filter_bar.dart';
import 'package:heimdallm/features/stats/stats_filters.dart';

const _allRepos = {'acme/api', 'acme/web', 'globex/payments', 'solo'};

Widget _host(ProviderContainer container) => UncontrolledProviderScope(
  container: container,
  child: const MaterialApp(
    home: Scaffold(body: StatsFilterBar(allRepos: _allRepos)),
  ),
);

Finder _filterChip(String label) => find.ancestor(
  of: find.text(label),
  matching: find.byWidgetPredicate(
    (widget) => widget is GestureDetector && widget.onTap != null,
  ),
);

Finder _option(String label) => find.widgetWithText(CheckboxListTile, label);

Finder _dialogButton(Type type, String label) =>
    find.widgetWithText(type, label);

void main() {
  testWidgets('renders available filters without a reset action', (
    tester,
  ) async {
    final container = ProviderContainer();
    addTearDown(container.dispose);

    await tester.pumpWidget(_host(container));
    await tester.pumpAndSettle();

    expect(find.text('Filter:'), findsOneWidget);
    expect(_filterChip('Org'), findsOneWidget);
    expect(_filterChip('Repo'), findsOneWidget);
    expect(find.widgetWithText(ActionChip, 'Reset'), findsNothing);

    await tester.tap(_filterChip('Org'));
    await tester.pumpAndSettle();

    expect(_option('acme'), findsOneWidget);
    expect(_option('globex'), findsOneWidget);
    expect(_option('solo'), findsOneWidget);
  });

  testWidgets('applies repository selections to the filter state', (
    tester,
  ) async {
    final container = ProviderContainer();
    addTearDown(container.dispose);

    await tester.pumpWidget(_host(container));
    await tester.pumpAndSettle();

    await tester.tap(_filterChip('Repo'));
    await tester.pumpAndSettle();
    await tester.tap(_option('acme/api'));
    await tester.pump();
    await tester.tap(_option('globex/payments'));
    await tester.pump();
    await tester.tap(_dialogButton(FilledButton, 'Apply'));
    await tester.pumpAndSettle();

    final filters = container.read(statsFiltersProvider);
    expect(filters.orgs, isEmpty);
    expect(filters.repos, {'acme/api', 'globex/payments'});
    expect(_filterChip('Repo (2)'), findsOneWidget);
    expect(find.widgetWithText(ActionChip, 'Reset'), findsOneWidget);
  });

  testWidgets('cancel discards changes made inside the dialog', (tester) async {
    final container = ProviderContainer();
    addTearDown(container.dispose);
    container
        .read(statsFiltersProvider.notifier)
        .set(const StatsFilters(repos: {'acme/api'}));

    await tester.pumpWidget(_host(container));
    await tester.pumpAndSettle();

    await tester.tap(_filterChip('Repo (1)'));
    await tester.pumpAndSettle();

    expect(tester.widget<CheckboxListTile>(_option('acme/api')).value, isTrue);
    await tester.tap(_option('acme/api'));
    await tester.pump();
    expect(tester.widget<CheckboxListTile>(_option('acme/api')).value, isFalse);

    await tester.tap(_dialogButton(TextButton, 'Cancel'));
    await tester.pumpAndSettle();

    expect(container.read(statsFiltersProvider).repos, {'acme/api'});
    expect(_filterChip('Repo (1)'), findsOneWidget);
  });

  testWidgets('selecting an org prunes repos and reset clears all filters', (
    tester,
  ) async {
    final container = ProviderContainer();
    addTearDown(container.dispose);
    container
        .read(statsFiltersProvider.notifier)
        .set(const StatsFilters(repos: {'acme/api', 'globex/payments'}));

    await tester.pumpWidget(_host(container));
    await tester.pumpAndSettle();

    await tester.tap(_filterChip('Org'));
    await tester.pumpAndSettle();
    await tester.tap(_option('acme'));
    await tester.pump();
    await tester.tap(_dialogButton(FilledButton, 'Apply'));
    await tester.pumpAndSettle();

    var filters = container.read(statsFiltersProvider);
    expect(filters.orgs, {'acme'});
    expect(filters.repos, {'acme/api'});
    expect(_filterChip('Org (1)'), findsOneWidget);
    expect(_filterChip('Repo (1)'), findsOneWidget);

    await tester.tap(_filterChip('Repo (1)'));
    await tester.pumpAndSettle();

    expect(_option('acme/api'), findsOneWidget);
    expect(_option('acme/web'), findsOneWidget);
    expect(_option('globex/payments'), findsNothing);
    expect(_option('solo'), findsNothing);

    await tester.tap(_dialogButton(TextButton, 'Cancel'));
    await tester.pumpAndSettle();
    await tester.tap(find.widgetWithText(ActionChip, 'Reset'));
    await tester.pumpAndSettle();

    filters = container.read(statsFiltersProvider);
    expect(filters.orgs, isEmpty);
    expect(filters.repos, isEmpty);
    expect(_filterChip('Org'), findsOneWidget);
    expect(_filterChip('Repo'), findsOneWidget);
    expect(find.widgetWithText(ActionChip, 'Reset'), findsNothing);
  });
}

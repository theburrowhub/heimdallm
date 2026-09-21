import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/shared/design_system/components/components.dart';
import 'package:heimdallm/shared/design_system/theme.dart';

Widget _hosted(Widget child) {
  return MaterialApp(
    theme: HeimdallmTheme.light(),
    debugShowCheckedModeBanner: false,
    builder: (context, navigatorChild) =>
        HeimdallmTheme.scope(child: navigatorChild ?? const SizedBox.shrink()),
    home: Scaffold(body: child),
  );
}

void main() {
  testWidgets('AppMultiSelectChip opens the dialog and applies a selection', (
    tester,
  ) async {
    var selection = <String>{};
    await tester.pumpWidget(
      _hosted(
        AppMultiSelectChip(
          label: 'Org',
          icon: Icons.business,
          allValues: const ['freepik-company', 'theburrowhub'],
          selected: selection,
          onChanged: (v) => selection = v,
        ),
      ),
    );
    await tester.pumpAndSettle();

    await tester.tap(find.byType(AppMultiSelectChip));
    await tester.pumpAndSettle();

    expect(find.text('Filter by Org'), findsOneWidget);
    expect(find.text('freepik-company'), findsOneWidget);

    await tester.tap(find.text('theburrowhub'));
    await tester.pump();
    await tester.tap(find.widgetWithText(FilledButton, 'Apply (1)'));
    await tester.pumpAndSettle();

    expect(selection, {'theburrowhub'});
  });

  testWidgets('AppMultiSelectChip shows the selection count when non-empty', (
    tester,
  ) async {
    await tester.pumpWidget(
      _hosted(
        AppMultiSelectChip(
          label: 'Repo',
          icon: Icons.folder_outlined,
          allValues: const ['repo-a', 'repo-b'],
          selected: const {'repo-a'},
          onChanged: (_) {},
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('Repo (1)'), findsOneWidget);
  });

  testWidgets('showAppMultiSelect Clear button empties the current selection', (
    tester,
  ) async {
    await tester.pumpWidget(
      _hosted(
        Builder(
          builder: (context) => ElevatedButton(
            onPressed: () => showAppMultiSelect(
              context: context,
              title: 'Type',
              allValues: const ['pr', 'issue'],
              selected: const {'pr'},
            ),
            child: const Text('open'),
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    await tester.tap(find.text('open'));
    await tester.pumpAndSettle();

    expect(find.text('Clear'), findsOneWidget);
    await tester.tap(find.text('Clear'));
    await tester.pump();

    expect(find.widgetWithText(FilledButton, 'Apply'), findsOneWidget);
  });
}

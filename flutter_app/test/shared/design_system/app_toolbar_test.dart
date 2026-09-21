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
  testWidgets('AppToolbar renders leading, filters and trailing zones', (
    tester,
  ) async {
    await tester.pumpWidget(
      _hosted(
        const AppToolbar(
          leading: [Text('Sort')],
          filters: [Text('Search')],
          trailing: [Text('Add PR')],
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('Sort'), findsOneWidget);
    expect(find.text('Search'), findsOneWidget);
    expect(find.text('Add PR'), findsOneWidget);
  });

  testWidgets('AppToolbar renders with no zones populated', (tester) async {
    await tester.pumpWidget(_hosted(const AppToolbar()));
    await tester.pumpAndSettle();

    expect(tester.takeException(), isNull);
  });

  testWidgets(
    'AppToolbar pins the rowKey row flush against the trailing button',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(900, 200));
      addTearDown(() => tester.binding.setSurfaceSize(null));

      await tester.pumpWidget(
        _hosted(
          AppToolbar(
            rowKey: const Key('toolbar-row'),
            filters: const [Text('Search')],
            trailing: [
              FilledButton.icon(
                key: const Key('add-button'),
                icon: const Icon(Icons.add),
                label: const Text('Add PR'),
                onPressed: () {},
              ),
            ],
          ),
        ),
      );
      await tester.pumpAndSettle();

      final row = tester.getRect(find.byKey(const Key('toolbar-row')));
      final button = tester.getRect(find.byKey(const Key('add-button')));
      expect(button.right, moreOrLessEquals(row.right, epsilon: 0.1));
    },
  );
}

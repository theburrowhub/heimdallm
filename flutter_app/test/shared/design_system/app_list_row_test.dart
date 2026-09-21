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
    home: Scaffold(body: Center(child: child)),
  );
}

void main() {
  testWidgets('AppListRow pins trailing content to the row right edge', (
    tester,
  ) async {
    // Regression test for the Activity trailing-alignment bug: a loose
    // Flexible sharing flex with the title's Expanded left a dead gap
    // between the trailing content and the row edge. With a short title,
    // the dismiss icon's right edge must land exactly at the row's content
    // area right edge (the widget's own width, minus its 16px outer margin
    // and 16px inner content padding on that side) — not short of it.
    const totalWidth = 800.0;

    await tester.pumpWidget(
      _hosted(
        SizedBox(
          width: totalWidth,
          child: AppListRow(
            title: const Text('Short'),
            trailing: const [
              Text('Review'),
              Icon(Icons.close, key: Key('dismiss')),
            ],
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    final dismissRight = tester.getRect(find.byKey(const Key('dismiss'))).right;
    const contentRight = totalWidth - 16 - 16;

    expect(dismissRight, closeTo(contentRight, 1));
  });

  testWidgets('AppListRow renders leading, subtitle, accent bar and footer', (
    tester,
  ) async {
    var tapped = false;
    await tester.pumpWidget(
      _hosted(
        AppListRow(
          title: const Text('PR title'),
          subtitle: const Text('repo · #1 · author'),
          accentColor: Colors.blue,
          leading: const [Text('PR'), Text('Open')],
          trailing: const [Text('Review')],
          footer: const Text('footer row'),
          onTap: () => tapped = true,
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('PR title'), findsOneWidget);
    expect(find.text('repo · #1 · author'), findsOneWidget);
    expect(find.text('footer row'), findsOneWidget);

    await tester.tap(find.byType(AppListRow));
    await tester.pump();
    expect(tapped, isTrue);
  });

  testWidgets('AppListRow honors selected and dimmed flags without error', (
    tester,
  ) async {
    await tester.pumpWidget(
      _hosted(
        Column(
          children: [
            AppListRow(title: const Text('Selected'), selected: true),
            AppListRow(title: const Text('Dimmed'), dimmed: true),
          ],
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('Selected'), findsOneWidget);
    expect(find.text('Dimmed'), findsOneWidget);
  });

  testWidgets(
    'AppListRow reflows trailing onto a second line at narrow widths',
    (tester) async {
      await tester.pumpWidget(
        _hosted(
          SizedBox(
            width: 320,
            child: AppListRow(
              title: const Text('A fairly long title that takes real space'),
              trailing: const [Text('LOW'), Text('Review'), Icon(Icons.close)],
            ),
          ),
        ),
      );
      await tester.pumpAndSettle();

      expect(tester.takeException(), isNull);
      expect(find.text('Review'), findsOneWidget);
    },
  );
}

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/shared/design_system/components/components.dart';
import 'package:heimdallm/shared/design_system/theme.dart';
import 'package:heimdallm/shared/widgets/severity_badge.dart';
import 'package:heimdallm/shared/widgets/state_badge.dart';
import 'package:heimdallm/shared/widgets/type_badge.dart';

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

  testWidgets(
    'AppListRow does not overflow with realistic Activity content at a narrow width',
    (tester) async {
      // Regression test for a real overflow the fixed-flex-ratio trailing
      // zone produced: at 375px, two leading badges plus a full
      // single-line severity badge + Review button + dismiss icon exceeded
      // the row's own available width by 46px (`RenderFlex overflowed`).
      await tester.binding.setSurfaceSize(const Size(375, 800));
      addTearDown(() => tester.binding.setSurfaceSize(null));

      await tester.pumpWidget(
        _hosted(
          AppListRow(
            title: const Text('Fix critical bug in payment flow handler'),
            subtitle: const Text('org/repo · #42 · alice'),
            leading: const [
              TypeBadge(type: 'pr'),
              StateBadge(state: 'open'),
            ],
            trailing: [
              const SeverityBadge(severity: 'medium'),
              SizedBox(
                height: 28,
                child: ElevatedButton(
                  style: ElevatedButton.styleFrom(
                    padding: const EdgeInsets.symmetric(horizontal: 10),
                    textStyle: const TextStyle(fontSize: 12),
                  ),
                  onPressed: () {},
                  child: const Text('Review'),
                ),
              ),
              IconButton(
                icon: const Icon(Icons.close, size: 14),
                visualDensity: VisualDensity.compact,
                onPressed: () {},
              ),
            ],
          ),
        ),
      );
      await tester.pump();

      expect(tester.takeException(), isNull);
    },
  );
}

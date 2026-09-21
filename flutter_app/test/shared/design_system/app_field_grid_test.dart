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

Future<void> _setWidth(WidgetTester tester, double width) async {
  tester.view.physicalSize = Size(width, 700);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.resetPhysicalSize);
  addTearDown(tester.view.resetDevicePixelRatio);
}

void main() {
  testWidgets('AppFieldGrid lays out 3 columns at a wide width', (
    tester,
  ) async {
    await _setWidth(tester, 900);

    await tester.pumpWidget(
      _hosted(
        AppFieldGrid(
          children: [
            SizedBox(key: const Key('f1'), height: 40, child: Container()),
            SizedBox(key: const Key('f2'), height: 40, child: Container()),
            SizedBox(key: const Key('f3'), height: 40, child: Container()),
          ],
        ),
      ),
    );
    await tester.pumpAndSettle();

    final f1 = tester.getRect(find.byKey(const Key('f1')));
    final f2 = tester.getRect(find.byKey(const Key('f2')));
    final f3 = tester.getRect(find.byKey(const Key('f3')));

    // Three columns: all on the same row (same top), each narrower than the
    // full 900px width.
    expect(f1.top, f2.top);
    expect(f2.top, f3.top);
    expect(f1.width, lessThan(900));
  });

  testWidgets('AppFieldGrid collapses to a single column at a narrow width', (
    tester,
  ) async {
    await _setWidth(tester, 260);

    await tester.pumpWidget(
      _hosted(
        AppFieldGrid(
          children: [
            SizedBox(key: const Key('f1'), height: 40, child: Container()),
            SizedBox(key: const Key('f2'), height: 40, child: Container()),
          ],
        ),
      ),
    );
    await tester.pumpAndSettle();

    final f1 = tester.getRect(find.byKey(const Key('f1')));
    final f2 = tester.getRect(find.byKey(const Key('f2')));

    expect(f2.top, greaterThan(f1.bottom - 1));
  });

  testWidgets('AppFieldGrid renders nothing for an empty children list', (
    tester,
  ) async {
    await tester.pumpWidget(_hosted(const AppFieldGrid(children: [])));
    await tester.pumpAndSettle();

    expect(tester.takeException(), isNull);
  });
}

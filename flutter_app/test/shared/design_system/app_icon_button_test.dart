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
  testWidgets('AppIconButton fires onPressed and shows its tooltip', (
    tester,
  ) async {
    var tapped = false;
    await tester.pumpWidget(
      _hosted(
        AppIconButton(
          icon: Icons.view_list,
          tooltip: 'List view',
          onPressed: () => tapped = true,
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.byTooltip('List view'), findsOneWidget);
    await tester.tap(find.byType(AppIconButton));
    await tester.pump();
    expect(tapped, isTrue);
  });

  testWidgets('AppIconButton is disabled with a null onPressed', (
    tester,
  ) async {
    await tester.pumpWidget(
      _hosted(const AppIconButton(icon: Icons.close, onPressed: null)),
    );
    await tester.pumpAndSettle();

    final button = tester.widget<IconButton>(find.byType(IconButton));
    expect(button.onPressed, isNull);
  });

  testWidgets('AppIconButton reflects selected state without error', (
    tester,
  ) async {
    await tester.pumpWidget(
      _hosted(
        Column(
          children: [
            AppIconButton(
              icon: Icons.grid_view,
              selected: true,
              onPressed: () {},
            ),
            AppIconButton(
              icon: Icons.grid_view,
              selected: false,
              onPressed: () {},
            ),
          ],
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(tester.takeException(), isNull);
  });
}

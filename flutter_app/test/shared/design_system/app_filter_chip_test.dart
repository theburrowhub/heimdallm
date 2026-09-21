import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/shared/design_system/components/components.dart';
import 'package:heimdallm/shared/design_system/theme.dart';
import 'package:heimdallm/shared/design_system/tokens.dart';

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
  testWidgets('AppFilterChip fires onTap and renders icon/count', (
    tester,
  ) async {
    var tapped = false;
    await tester.pumpWidget(
      _hosted(
        AppFilterChip(
          label: 'Open',
          selected: false,
          icon: Icons.check,
          count: 3,
          onTap: () => tapped = true,
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('Open'), findsOneWidget);
    expect(find.text('3'), findsOneWidget);
    expect(find.byIcon(Icons.check), findsOneWidget);

    await tester.tap(find.byType(AppFilterChip));
    await tester.pump();
    expect(tapped, isTrue);
  });

  testWidgets('AppFilterChip renders selected and unselected without error', (
    tester,
  ) async {
    await tester.pumpWidget(
      _hosted(
        Column(
          children: [
            AppFilterChip(label: 'PR', selected: true, onTap: () {}),
            AppFilterChip(
              label: 'IT',
              selected: false,
              accent: AppColors.featureIssueTracking,
              onTap: () {},
            ),
          ],
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('PR'), findsOneWidget);
    expect(find.text('IT'), findsOneWidget);
    expect(tester.takeException(), isNull);
  });
}

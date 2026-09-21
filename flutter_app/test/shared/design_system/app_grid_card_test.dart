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
    home: Scaffold(
      body: Center(child: SizedBox(width: 220, height: 140, child: child)),
    ),
  );
}

void main() {
  testWidgets('AppGridCard renders header, title, subtitle and footer', (
    tester,
  ) async {
    var tapped = false;
    await tester.pumpWidget(
      _hosted(
        AppGridCard(
          header: const Text('PR'),
          title: const Text('Card title'),
          subtitle: const Text('subtitle'),
          footer: const Text('2m ago'),
          onTap: () => tapped = true,
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('PR'), findsOneWidget);
    expect(find.text('Card title'), findsOneWidget);
    expect(find.text('subtitle'), findsOneWidget);
    expect(find.text('2m ago'), findsOneWidget);

    await tester.tap(find.byType(AppGridCard));
    await tester.pump();
    expect(tapped, isTrue);
  });

  testWidgets('AppGridCard honors selected and dimmed flags without error', (
    tester,
  ) async {
    await tester.pumpWidget(
      _hosted(
        AppGridCard(
          title: const Text('Selected'),
          selected: true,
          dimmed: true,
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('Selected'), findsOneWidget);
    expect(tester.takeException(), isNull);
  });

  test('AppGridDelegate.entities returns the shared grid metrics', () {
    final delegate =
        AppGridDelegate.entities() as SliverGridDelegateWithMaxCrossAxisExtent;
    expect(delegate.maxCrossAxisExtent, 300);
    expect(delegate.childAspectRatio, 1.6);

    final custom =
        AppGridDelegate.entities(maxExtent: 200, aspectRatio: 1.0)
            as SliverGridDelegateWithMaxCrossAxisExtent;
    expect(custom.maxCrossAxisExtent, 200);
    expect(custom.childAspectRatio, 1.0);
  });
}

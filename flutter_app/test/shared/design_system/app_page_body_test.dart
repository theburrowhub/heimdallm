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
  testWidgets('AppPageBody renders toolbar, header and child stacked', (
    tester,
  ) async {
    await tester.pumpWidget(
      _hosted(
        AppPageBody(
          toolbar: const Text('toolbar'),
          header: const Text('header'),
          child: const Text('body'),
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('toolbar'), findsOneWidget);
    expect(find.text('header'), findsOneWidget);
    expect(find.text('body'), findsOneWidget);
  });

  testWidgets(
    'AppPageBody uses the full available width with no toolbar/header',
    (tester) async {
      await tester.binding.setSurfaceSize(const Size(1000, 600));
      addTearDown(() => tester.binding.setSurfaceSize(null));

      await tester.pumpWidget(
        _hosted(AppPageBody(child: Container(key: const Key('body')))),
      );
      await tester.pumpAndSettle();

      final rect = tester.getRect(find.byKey(const Key('body')));
      expect(rect.width, 1000);
    },
  );
}

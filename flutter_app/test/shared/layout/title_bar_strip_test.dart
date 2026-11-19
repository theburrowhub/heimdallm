import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/platform/platform_services_provider.dart';
import 'package:heimdallm/shared/design_system/theme.dart';
import 'package:heimdallm/shared/layout/title_bar_strip.dart';

import '../../core/platform/fake_platform_services.dart';

Widget _host(
  FakePlatformServices platform, {
  ThemeMode mode = ThemeMode.light,
}) => ProviderScope(
  overrides: [platformServicesProvider.overrideWithValue(platform)],
  child: MaterialApp(
    theme: HeimdallmTheme.light(),
    darkTheme: HeimdallmTheme.dark(),
    themeMode: mode,
    builder: (context, child) =>
        TitleBarStrip(child: child ?? const SizedBox.shrink()),
    home: Scaffold(
      appBar: AppBar(title: const Text('Heimdallm')),
      body: const Text('content'),
    ),
  ),
);

final _strip = find.byKey(const Key('title-bar-strip'));

void main() {
  testWidgets('without a strip height the system title bar is kept', (
    tester,
  ) async {
    await tester.pumpWidget(_host(FakePlatformServices()));
    expect(_strip, findsNothing);
    expect(find.text('content'), findsOneWidget);
  });

  testWidgets('macOS gets a strip in the app bar colour above the app bar', (
    tester,
  ) async {
    for (final mode in [ThemeMode.light, ThemeMode.dark]) {
      await tester.pumpWidget(
        _host(FakePlatformServices()..titleBarHeight = 28, mode: mode),
      );
      await tester.pumpAndSettle();
      expect(tester.getSize(_strip).height, 28);
      expect(tester.getTopLeft(find.byType(AppBar)).dy, 28);
      final strip = tester.widget<ColoredBox>(
        find.descendant(of: _strip, matching: find.byType(ColoredBox)),
      );
      final appBarColor = Theme.of(
        tester.element(find.byType(AppBar)),
      ).appBarTheme.backgroundColor;
      expect(strip.color, appBarColor, reason: '$mode');
    }
  });

  testWidgets('dragging the strip moves the window, double-click zooms', (
    tester,
  ) async {
    final platform = FakePlatformServices()..titleBarHeight = 28;
    await tester.pumpWidget(_host(platform));

    await tester.drag(_strip, const Offset(120, 40));
    expect(platform.startWindowDragCalls, 1);

    await tester.tap(_strip);
    await tester.pump(const Duration(milliseconds: 50));
    await tester.tap(_strip);
    await tester.pumpAndSettle();
    expect(platform.toggleWindowZoomCalls, 1);
  });
}

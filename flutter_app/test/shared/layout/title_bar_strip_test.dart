import 'package:flutter/cupertino.dart';
import 'package:flutter/gestures.dart';
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

  // The title bar band is handed to the app as a top inset: the app bar grows
  // into it in its own colour and its toolbar starts below it (#927).
  testWidgets('macOS hands the title bar band to the app as a top inset', (
    tester,
  ) async {
    await tester.pumpWidget(_host(FakePlatformServices()..titleBarHeight = 28));
    await tester.pumpAndSettle();
    expect(tester.getSize(_strip).height, 28);
    expect(tester.getTopLeft(find.byType(AppBar)).dy, 0);
    expect(tester.getTopLeft(find.byType(NavigationToolbar)).dy, 28);
    expect(
      find.descendant(of: _strip, matching: find.byType(ColoredBox)),
      findsNothing,
      reason: 'the strip itself paints nothing',
    );
  });

  testWidgets('dragging the strip moves the window, double-click acts as a '
      'title bar', (tester) async {
    final platform = FakePlatformServices()..titleBarHeight = 28;
    await tester.pumpWidget(_host(platform));

    await tester.drag(_strip, const Offset(120, 40));
    expect(platform.startWindowDragCalls, 1);

    await tester.tap(_strip);
    await tester.pump(const Duration(milliseconds: 50));
    await tester.tap(_strip);
    await tester.pumpAndSettle();
    expect(platform.titleBarDoubleClickCalls, 1);
  });

  // The app gets the whole window with the band as its top padding, so
  // sizes taken from MediaQuery (dialogs, sheets) fit (#902 review, #927).
  testWidgets('the app gets the window with the band as top padding', (
    tester,
  ) async {
    tester.view.physicalSize = const Size(800, 600);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          platformServicesProvider.overrideWithValue(
            FakePlatformServices()..titleBarHeight = 28,
          ),
        ],
        child: MaterialApp(
          builder: (context, child) =>
              TitleBarStrip(child: child ?? const SizedBox.shrink()),
          home: const Text('content'),
        ),
      ),
    );
    final media = MediaQuery.of(tester.element(find.text('content')));
    expect(media.size, const Size(800, 600));
    expect(media.padding.top, 28);
  });

  // In native full screen macOS hides its title bar, so the strip collapses;
  // the app keeps its state across the switch (#902 review).
  testWidgets('full screen collapses the strip and keeps the app state', (
    tester,
  ) async {
    tester.view.physicalSize = const Size(800, 600);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.reset);
    final platform = FakePlatformServices()..titleBarHeight = 28;
    await tester.pumpWidget(
      ProviderScope(
        overrides: [platformServicesProvider.overrideWithValue(platform)],
        child: MaterialApp(
          builder: (context, child) =>
              TitleBarStrip(child: child ?? const SizedBox.shrink()),
          home: const Scaffold(body: TextField()),
        ),
      ),
    );
    await tester.enterText(find.byType(TextField), 'draft');

    platform.fullScreen.value = true;
    await tester.pump();
    expect(tester.getSize(_strip).height, 0);
    expect(MediaQuery.paddingOf(tester.element(find.byType(Scaffold))).top, 0);
    expect(find.text('draft'), findsOneWidget);

    platform.fullScreen.value = false;
    await tester.pump();
    expect(tester.getSize(_strip).height, 28);
    expect(find.text('draft'), findsOneWidget);
  });

  // The strip moves the root overlay 28 px down; a text's right-click menu
  // must still open where it would without it, at the pointer (#902 review).
  testWidgets(
    'a right-click menu opens where it would without the strip',
    (tester) async {
      Future<double> menuOffset(double stripHeight) async {
        await tester.pumpWidget(
          ProviderScope(
            overrides: [
              platformServicesProvider.overrideWithValue(
                FakePlatformServices()..titleBarHeight = stripHeight,
              ),
            ],
            child: MaterialApp(
              builder: (context, child) =>
                  TitleBarStrip(child: child ?? const SizedBox.shrink()),
              home: const Scaffold(
                body: Center(child: SelectableText('Right-click me')),
              ),
            ),
          ),
        );
        await tester.pumpAndSettle();
        final at = tester.getCenter(find.text('Right-click me'));
        final gesture = await tester.startGesture(
          at,
          kind: PointerDeviceKind.mouse,
          buttons: kSecondaryMouseButton,
        );
        await gesture.up();
        await tester.pumpAndSettle();
        final button = find.byType(CupertinoDesktopTextSelectionToolbarButton);
        expect(button, findsWidgets);
        final top = tester.getTopLeft(button.first).dy;
        // Close the menu before the next pump.
        await tester.tapAt(Offset.zero);
        await tester.pumpAndSettle();
        return top - at.dy;
      }

      final without = await menuOffset(0);
      final withStrip = await menuOffset(28);
      expect(withStrip, closeTo(without, 0.5));
    },
    variant: TargetPlatformVariant.only(TargetPlatform.macOS),
  );

  // A translucent window takes the app's light or dark look, so the sidebar
  // material and the traffic lights match the app, not the system (#927).
  testWidgets('a translucent window follows the app brightness', (
    tester,
  ) async {
    final platform = FakePlatformServices()
      ..titleBarHeight = 28
      ..translucentSidebar = true;
    await tester.pumpWidget(_host(platform));
    await tester.pumpWidget(_host(platform));
    expect(platform.windowBrightnessCalls, [Brightness.light]);

    await tester.pumpWidget(_host(platform, mode: ThemeMode.dark));
    await tester.pumpAndSettle();
    expect(platform.windowBrightnessCalls, [Brightness.light, Brightness.dark]);
  });

  testWidgets('an opaque window keeps the system appearance', (tester) async {
    final platform = FakePlatformServices()..titleBarHeight = 28;
    await tester.pumpWidget(_host(platform, mode: ThemeMode.dark));
    await tester.pumpAndSettle();
    expect(platform.windowBrightnessCalls, isEmpty);
  });
}

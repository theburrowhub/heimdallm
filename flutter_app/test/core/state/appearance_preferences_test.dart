import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/state/appearance_preferences.dart';
import 'package:shared_preferences/shared_preferences.dart';

void main() {
  setUp(() {
    SharedPreferences.setMockInitialValues({});
  });

  test('defaults to system before preferences load', () {
    final container = ProviderContainer();
    addTearDown(container.dispose);
    expect(container.read(appearanceProvider), ThemeMode.system);
  });

  test('loads a persisted preference asynchronously', () async {
    SharedPreferences.setMockInitialValues({
      'appearance_theme_mode': 'dark',
    });
    final container = ProviderContainer();
    addTearDown(container.dispose);
    container.read(appearanceProvider); // build() kicks off the async load
    await Future<void>.delayed(Duration.zero);
    await Future<void>.delayed(Duration.zero);
    expect(container.read(appearanceProvider), ThemeMode.dark);
  });

  test('set() persists the new mode for the next launch', () async {
    final container = ProviderContainer();
    addTearDown(container.dispose);
    container.read(appearanceProvider.notifier).set(ThemeMode.light);
    expect(container.read(appearanceProvider), ThemeMode.light);

    await Future<void>.delayed(Duration.zero);
    final prefs = await SharedPreferences.getInstance();
    expect(prefs.getString('appearance_theme_mode'), 'light');
  });

  testWidgets('changing the mode updates MaterialApp.themeMode live', (
    tester,
  ) async {
    final container = ProviderContainer();
    addTearDown(container.dispose);

    await tester.pumpWidget(
      UncontrolledProviderScope(
        container: container,
        child: Consumer(
          builder: (context, ref, _) => MaterialApp(
            themeMode: ref.watch(appearanceProvider),
            theme: ThemeData.light(),
            darkTheme: ThemeData.dark(),
            home: const SizedBox(),
          ),
        ),
      ),
    );

    expect(
      tester
          .widget<MaterialApp>(find.byType(MaterialApp))
          .themeMode,
      ThemeMode.system,
    );

    container.read(appearanceProvider.notifier).set(ThemeMode.dark);
    await tester.pump();

    expect(
      tester
          .widget<MaterialApp>(find.byType(MaterialApp))
          .themeMode,
      ThemeMode.dark,
    );
  });
}

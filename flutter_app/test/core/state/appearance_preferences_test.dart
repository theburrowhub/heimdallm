import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/state/appearance_preferences.dart';
import 'package:shared_preferences/shared_preferences.dart';
import 'package:shared_preferences_platform_interface/shared_preferences_platform_interface.dart';
import 'package:shared_preferences_platform_interface/types.dart';

/// A store whose reads/writes always fail, used to exercise the defensive
/// error handling in [AppearanceNotifier] without touching the real plugin.
class _ThrowingSharedPreferencesStore extends SharedPreferencesStorePlatform {
  @override
  Future<bool> clear() async => throw StateError('preferences unavailable');

  @override
  Future<bool> clearWithParameters(ClearParameters parameters) async =>
      throw StateError('preferences unavailable');

  @override
  Future<Map<String, Object>> getAll() async =>
      throw StateError('preferences unavailable');

  @override
  Future<Map<String, Object>> getAllWithParameters(
    GetAllParameters parameters,
  ) async => throw StateError('preferences unavailable');

  @override
  Future<bool> remove(String key) async =>
      throw StateError('preferences unavailable');

  @override
  Future<bool> setValue(String valueType, String key, Object value) async =>
      throw StateError('preferences unavailable');
}

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

  test('loads a persisted system preference asynchronously', () async {
    SharedPreferences.setMockInitialValues({
      'appearance_theme_mode': 'system',
    });
    final container = ProviderContainer();
    addTearDown(container.dispose);
    container.read(appearanceProvider.notifier).set(ThemeMode.dark);
    container.read(appearanceProvider.notifier).set(ThemeMode.system);
    expect(container.read(appearanceProvider), ThemeMode.system);

    await Future<void>.delayed(Duration.zero);
    final prefs = await SharedPreferences.getInstance();
    expect(prefs.getString('appearance_theme_mode'), 'system');
  });

  test('keeps the default mode when loading the preference fails', () async {
    TestWidgetsFlutterBinding.ensureInitialized();
    final originalStore = SharedPreferencesStorePlatform.instance;
    final originalDebugPrint = debugPrint;
    final messages = <String>[];

    SharedPreferences.resetStatic();
    SharedPreferencesStorePlatform.instance = _ThrowingSharedPreferencesStore();
    debugPrint = (message, {wrapWidth}) {
      if (message != null) messages.add(message);
    };
    addTearDown(() {
      SharedPreferencesStorePlatform.instance = originalStore;
      SharedPreferences.resetStatic();
      debugPrint = originalDebugPrint;
    });

    final container = ProviderContainer();
    addTearDown(container.dispose);

    expect(container.read(appearanceProvider), ThemeMode.system);
    await pumpEventQueue();
    expect(container.read(appearanceProvider), ThemeMode.system);
    expect(
      messages,
      contains(contains('AppearanceNotifier: failed to load preference')),
    );
  });

  test('swallows the error when persisting the preference fails', () async {
    TestWidgetsFlutterBinding.ensureInitialized();
    final originalStore = SharedPreferencesStorePlatform.instance;
    final originalDebugPrint = debugPrint;
    final messages = <String>[];

    SharedPreferences.setMockInitialValues({});
    SharedPreferencesStorePlatform.instance = _ThrowingSharedPreferencesStore();
    debugPrint = (message, {wrapWidth}) {
      if (message != null) messages.add(message);
    };
    addTearDown(() {
      SharedPreferencesStorePlatform.instance = originalStore;
      SharedPreferences.resetStatic();
      debugPrint = originalDebugPrint;
    });

    final container = ProviderContainer();
    addTearDown(container.dispose);

    container.read(appearanceProvider.notifier).set(ThemeMode.dark);
    expect(container.read(appearanceProvider), ThemeMode.dark);
    await pumpEventQueue();
    expect(
      messages,
      contains(contains('AppearanceNotifier: failed to save preference')),
    );
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

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/state/appearance_preferences.dart';
import 'package:shared_preferences/shared_preferences.dart';
// ignore: depend_on_referenced_packages
import 'package:shared_preferences_platform_interface/shared_preferences_platform_interface.dart';

class ThrowingSharedPreferencesStore extends SharedPreferencesStorePlatform {
  ThrowingSharedPreferencesStore({this.getAllError, this.setValueError});

  final Object? getAllError;
  final Object? setValueError;

  @override
  bool get isMock => true;

  @override
  Future<bool> clear() async => true;

  @override
  Future<Map<String, Object>> getAll() async {
    if (getAllError != null) throw getAllError!;
    return <String, Object>{};
  }

  @override
  Future<bool> remove(String key) async => true;

  @override
  Future<bool> setValue(String valueType, String key, Object value) async {
    if (setValueError != null) throw setValueError!;
    return true;
  }
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
    SharedPreferences.setMockInitialValues({'appearance_theme_mode': 'dark'});
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

  test('set() can persist the system mode too', () async {
    final container = ProviderContainer();
    addTearDown(container.dispose);

    container.read(appearanceProvider.notifier).set(ThemeMode.system);
    await Future<void>.delayed(Duration.zero);

    final prefs = await SharedPreferences.getInstance();
    expect(prefs.getString('appearance_theme_mode'), 'system');
  });

  test('load failures are logged and fall back to system mode', () async {
    SharedPreferences.resetStatic();
    final originalStore = SharedPreferencesStorePlatform.instance;
    final messages = <String>[];
    final originalDebugPrint = debugPrint;

    SharedPreferencesStorePlatform.instance = ThrowingSharedPreferencesStore(
      getAllError: StateError('preferences unavailable'),
    );
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
    await Future<void>.delayed(Duration.zero);
    await Future<void>.delayed(Duration.zero);

    expect(
      messages,
      contains(
        predicate<String>(
          (m) => m.startsWith('AppearanceNotifier: failed to load preference:'),
        ),
      ),
    );
  });

  test('save failures are logged instead of throwing', () async {
    SharedPreferences.resetStatic();
    final originalStore = SharedPreferencesStorePlatform.instance;
    final messages = <String>[];
    final originalDebugPrint = debugPrint;

    SharedPreferencesStorePlatform.instance = ThrowingSharedPreferencesStore(
      setValueError: StateError('cannot write preferences'),
    );
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
    await Future<void>.delayed(Duration.zero);
    await Future<void>.delayed(Duration.zero);

    expect(
      messages,
      contains(
        predicate<String>(
          (m) => m.startsWith('AppearanceNotifier: failed to save preference:'),
        ),
      ),
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
      tester.widget<MaterialApp>(find.byType(MaterialApp)).themeMode,
      ThemeMode.system,
    );

    container.read(appearanceProvider.notifier).set(ThemeMode.dark);
    await tester.pump();

    expect(
      tester.widget<MaterialApp>(find.byType(MaterialApp)).themeMode,
      ThemeMode.dark,
    );
  });
}

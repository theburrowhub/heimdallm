import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/state/sidebar_preferences.dart';
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

  group('effectiveSidebarMode', () {
    test('always resolves to hidden below the compact breakpoint', () {
      for (final pref in AppSidebarMode.values) {
        expect(effectiveSidebarMode(pref, 600), AppSidebarMode.hidden);
      }
    });

    test('auto resolves to icons between compact and medium', () {
      expect(
        effectiveSidebarMode(AppSidebarMode.auto, 900),
        AppSidebarMode.icons,
      );
    });

    test('auto resolves to extended at/above medium', () {
      expect(
        effectiveSidebarMode(AppSidebarMode.auto, 1400),
        AppSidebarMode.extended,
      );
    });

    test('an explicit preference wins above the compact breakpoint', () {
      expect(
        effectiveSidebarMode(AppSidebarMode.hidden, 1400),
        AppSidebarMode.hidden,
      );
      expect(
        effectiveSidebarMode(AppSidebarMode.icons, 1400),
        AppSidebarMode.icons,
      );
      expect(
        effectiveSidebarMode(AppSidebarMode.extended, 900),
        AppSidebarMode.extended,
      );
    });
  });

  group('nextSidebarMode', () {
    test('cycles hidden -> icons -> extended -> hidden', () {
      expect(nextSidebarMode(AppSidebarMode.hidden), AppSidebarMode.icons);
      expect(nextSidebarMode(AppSidebarMode.icons), AppSidebarMode.extended);
      expect(nextSidebarMode(AppSidebarMode.extended), AppSidebarMode.hidden);
    });

    test('cycling from auto starts the cycle at hidden', () {
      expect(nextSidebarMode(AppSidebarMode.auto), AppSidebarMode.hidden);
    });
  });

  test('defaults to auto before preferences load', () {
    final container = ProviderContainer();
    addTearDown(container.dispose);
    expect(container.read(sidebarModeProvider), AppSidebarMode.auto);
  });

  test('loads a persisted preference asynchronously', () async {
    SharedPreferences.setMockInitialValues({'sidebar_mode': 'hidden'});
    final container = ProviderContainer();
    addTearDown(container.dispose);
    container.read(sidebarModeProvider);
    await Future<void>.delayed(Duration.zero);
    await Future<void>.delayed(Duration.zero);
    expect(container.read(sidebarModeProvider), AppSidebarMode.hidden);
  });

  test('set() persists the new mode for the next launch', () async {
    final container = ProviderContainer();
    addTearDown(container.dispose);
    container.read(sidebarModeProvider.notifier).set(AppSidebarMode.icons);
    expect(container.read(sidebarModeProvider), AppSidebarMode.icons);

    await Future<void>.delayed(Duration.zero);
    final prefs = await SharedPreferences.getInstance();
    expect(prefs.getString('sidebar_mode'), 'icons');
  });

  test('cycleFrom advances from the given effective mode', () async {
    final container = ProviderContainer();
    addTearDown(container.dispose);
    container
        .read(sidebarModeProvider.notifier)
        .cycleFrom(AppSidebarMode.extended);
    expect(container.read(sidebarModeProvider), AppSidebarMode.hidden);
  });

  test('load failures are logged and fall back to auto', () async {
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

    expect(container.read(sidebarModeProvider), AppSidebarMode.auto);
    await Future<void>.delayed(Duration.zero);
    await Future<void>.delayed(Duration.zero);

    expect(
      messages,
      contains(
        predicate<String>(
          (m) =>
              m.startsWith('SidebarModeNotifier: failed to load preference:'),
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

    container.read(sidebarModeProvider.notifier).set(AppSidebarMode.extended);
    await Future<void>.delayed(Duration.zero);
    await Future<void>.delayed(Duration.zero);

    expect(
      messages,
      contains(
        predicate<String>(
          (m) =>
              m.startsWith('SidebarModeNotifier: failed to save preference:'),
        ),
      ),
    );
  });
}

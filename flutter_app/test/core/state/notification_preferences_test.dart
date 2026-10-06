import 'package:flutter/foundation.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/notification_mode.dart';
import 'package:heimdallm/core/state/notification_preferences.dart';
import 'package:shared_preferences/shared_preferences.dart';
// ignore: depend_on_referenced_packages
import 'package:shared_preferences_platform_interface/shared_preferences_platform_interface.dart';

class _ThrowingStore extends SharedPreferencesStorePlatform {
  @override
  bool get isMock => true;

  @override
  Future<bool> clear() async => true;

  @override
  Future<Map<String, Object>> getAll() async => throw StateError('load');

  @override
  Future<bool> remove(String key) async => true;

  @override
  Future<bool> setValue(String valueType, String key, Object value) async =>
      throw StateError('save');
}

Future<void> _settle() async {
  await Future<void>.delayed(Duration.zero);
  await Future<void>.delayed(Duration.zero);
}

void main() {
  setUp(() {
    SharedPreferences.setMockInitialValues({});
  });

  group('NotificationMode codec', () {
    test('round-trips every mode', () {
      for (final mode in NotificationMode.values) {
        expect(decodeNotificationMode(encodeNotificationMode(mode)), mode);
      }
    });

    test('missing or unknown values keep the historic sound behaviour', () {
      expect(decodeNotificationMode(null), NotificationMode.sound);
      expect(decodeNotificationMode('loud'), NotificationMode.sound);
    });
  });

  test('modeFor resolves each category independently', () {
    const prefs = NotificationPreferences(
      activity: NotificationMode.silent,
      update: NotificationMode.off,
    );
    expect(
      prefs.modeFor(NotificationCategory.activity),
      NotificationMode.silent,
    );
    expect(prefs.modeFor(NotificationCategory.update), NotificationMode.off);
  });

  test('defaults to sound for both categories before loading', () {
    final container = ProviderContainer();
    addTearDown(container.dispose);
    expect(
      container.read(notificationPreferencesProvider),
      const NotificationPreferences(),
    );
  });

  test('loads persisted modes asynchronously', () async {
    SharedPreferences.setMockInitialValues({
      notificationActivityModeKey: 'off',
      notificationUpdateModeKey: 'silent',
    });
    final container = ProviderContainer();
    addTearDown(container.dispose);
    container.read(notificationPreferencesProvider);
    await _settle();
    expect(
      container.read(notificationPreferencesProvider),
      const NotificationPreferences(
        activity: NotificationMode.off,
        update: NotificationMode.silent,
      ),
    );
  });

  test('readNotificationPreferences returns the stored modes', () async {
    SharedPreferences.setMockInitialValues({
      notificationActivityModeKey: 'silent',
    });
    expect(
      await readNotificationPreferences(),
      const NotificationPreferences(activity: NotificationMode.silent),
    );
  });

  test('setters update state and persist each category separately', () async {
    final container = ProviderContainer();
    addTearDown(container.dispose);
    final notifier = container.read(notificationPreferencesProvider.notifier);
    await _settle();

    notifier.setActivity(NotificationMode.silent);
    notifier.setUpdate(NotificationMode.off);
    expect(
      container.read(notificationPreferencesProvider),
      const NotificationPreferences(
        activity: NotificationMode.silent,
        update: NotificationMode.off,
      ),
    );

    await _settle();
    final prefs = await SharedPreferences.getInstance();
    expect(prefs.getString(notificationActivityModeKey), 'silent');
    expect(prefs.getString(notificationUpdateModeKey), 'off');
  });

  test('load and save failures are logged and fall back to sound', () async {
    SharedPreferences.resetStatic();
    final originalStore = SharedPreferencesStorePlatform.instance;
    final originalDebugPrint = debugPrint;
    final messages = <String>[];
    SharedPreferencesStorePlatform.instance = _ThrowingStore();
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
    container.read(notificationPreferencesProvider);
    await _settle();
    expect(
      container.read(notificationPreferencesProvider),
      const NotificationPreferences(),
    );

    container
        .read(notificationPreferencesProvider.notifier)
        .setActivity(NotificationMode.off);
    await _settle();
    expect(
      container.read(notificationPreferencesProvider).activity,
      NotificationMode.off,
    );
    expect(
      messages,
      containsAll([
        predicate<String>((m) => m.contains('failed to load')),
        predicate<String>((m) => m.contains('failed to save')),
      ]),
    );
  });
}

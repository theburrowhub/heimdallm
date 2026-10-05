import 'package:flutter/foundation.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:shared_preferences/shared_preferences.dart';

import '../models/notification_mode.dart';

const notificationActivityModeKey = 'notifications_activity_mode';
const notificationUpdateModeKey = 'notifications_update_mode';

/// Reads the persisted notification modes. Used by the notifier below and by
/// `main()`, which applies them to the platform before any notification can
/// fire (the notifier's own load is asynchronous).
Future<NotificationPreferences> readNotificationPreferences() async {
  final prefs = await SharedPreferences.getInstance();
  return NotificationPreferences(
    activity: decodeNotificationMode(
      prefs.getString(notificationActivityModeKey),
    ),
    update: decodeNotificationMode(prefs.getString(notificationUpdateModeKey)),
  );
}

/// Persists how notifications are presented on this device (sound / silent /
/// off), per [NotificationCategory].
///
/// Like `appearance_preferences.dart` this is a local UI preference, not
/// daemon configuration: notifications are raised by each client, so every
/// desktop app or browser chooses for itself.
class NotificationPreferencesNotifier
    extends Notifier<NotificationPreferences> {
  @override
  NotificationPreferences build() {
    _loadAsync();
    return const NotificationPreferences();
  }

  Future<void> _loadAsync() async {
    try {
      final loaded = await readNotificationPreferences();
      state = loaded;
    } catch (e) {
      debugPrint('NotificationPreferencesNotifier: failed to load: $e');
    }
  }

  void setActivity(NotificationMode mode) {
    state = state.copyWith(activity: mode);
    _persist(notificationActivityModeKey, mode);
  }

  void setUpdate(NotificationMode mode) {
    state = state.copyWith(update: mode);
    _persist(notificationUpdateModeKey, mode);
  }

  void _persist(String key, NotificationMode mode) {
    SharedPreferences.getInstance()
        .then((prefs) => prefs.setString(key, encodeNotificationMode(mode)))
        .catchError((e) {
          debugPrint('NotificationPreferencesNotifier: failed to save: $e');
          return false;
        });
  }
}

final notificationPreferencesProvider =
    NotifierProvider<NotificationPreferencesNotifier, NotificationPreferences>(
      NotificationPreferencesNotifier.new,
    );

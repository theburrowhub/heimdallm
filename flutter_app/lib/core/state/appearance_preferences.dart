import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:shared_preferences/shared_preferences.dart';

const _themeModeKey = 'appearance_theme_mode';

String _encode(ThemeMode mode) => switch (mode) {
  ThemeMode.light => 'light',
  ThemeMode.dark => 'dark',
  ThemeMode.system => 'system',
};

ThemeMode _decode(String? value) => switch (value) {
  'light' => ThemeMode.light,
  'dark' => ThemeMode.dark,
  _ => ThemeMode.system,
};

/// Persists the user's light/dark/system preference across launches.
///
/// This is local UI preference, not daemon configuration — it never touches
/// `AppConfig` or `config.toml` (see `features/config/config_providers.dart`
/// for the daemon-backed settings this is deliberately kept separate from).
class AppearanceNotifier extends Notifier<ThemeMode> {
  @override
  ThemeMode build() {
    _loadAsync();
    return ThemeMode.system;
  }

  Future<void> _loadAsync() async {
    try {
      final prefs = await SharedPreferences.getInstance();
      state = _decode(prefs.getString(_themeModeKey));
    } catch (e) {
      debugPrint('AppearanceNotifier: failed to load preference: $e');
    }
  }

  void set(ThemeMode mode) {
    state = mode;
    SharedPreferences.getInstance()
        .then((prefs) => prefs.setString(_themeModeKey, _encode(mode)))
        .catchError((e) {
          debugPrint('AppearanceNotifier: failed to save preference: $e');
          return false;
        });
  }
}

final appearanceProvider = NotifierProvider<AppearanceNotifier, ThemeMode>(
  AppearanceNotifier.new,
);

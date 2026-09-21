import 'package:flutter/foundation.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:shared_preferences/shared_preferences.dart';

import '../../shared/design_system/components/app_view_toggle.dart';

String _encode(AppViewMode mode) => switch (mode) {
  AppViewMode.list => 'list',
  AppViewMode.grid => 'grid',
};

AppViewMode _decode(String? value) =>
    value == 'grid' ? AppViewMode.grid : AppViewMode.list;

/// Persists a screen's list/grid view-mode preference across launches,
/// keyed by [prefsKey] so each screen that offers the toggle (Merge,
/// Instances, Prompts, CLI Agents, …) remembers its own choice
/// independently.
///
/// Mirrors `AppearanceNotifier`/`SidebarModeNotifier`: synchronous default,
/// async load, failures logged and never thrown. Repositories and Activity
/// keep their own pre-existing, differently-shaped persistence
/// (`repos_view` in `repos_screen.dart`, `activity_view_mode` inside
/// `ActivityFilters`) rather than moving onto this notifier, so their
/// existing tests and state shape are untouched.
class ViewModeNotifier extends Notifier<AppViewMode> {
  ViewModeNotifier(this.prefsKey);

  final String prefsKey;

  @override
  AppViewMode build() {
    _loadAsync();
    return AppViewMode.list;
  }

  Future<void> _loadAsync() async {
    try {
      final prefs = await SharedPreferences.getInstance();
      state = _decode(prefs.getString(prefsKey));
    } catch (e) {
      debugPrint('ViewModeNotifier($prefsKey): failed to load preference: $e');
    }
  }

  void set(AppViewMode mode) {
    state = mode;
    SharedPreferences.getInstance()
        .then((prefs) => prefs.setString(prefsKey, _encode(mode)))
        .catchError((e) {
          debugPrint(
            'ViewModeNotifier($prefsKey): failed to save preference: $e',
          );
          return false;
        });
  }
}

final viewModeProvider =
    NotifierProvider.family<ViewModeNotifier, AppViewMode, String>(
      ViewModeNotifier.new,
    );

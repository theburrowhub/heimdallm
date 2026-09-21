import 'dart:async';

import 'package:flutter/foundation.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:shared_preferences/shared_preferences.dart';

import '../../shared/design_system/tokens.dart';

const _sidebarModeKey = 'sidebar_mode';

/// The sidebar's navigation chrome.
///
/// [auto] is the state before the user has ever touched the toggle: the
/// sidebar falls back to the width-derived behavior the shell always had
/// (extended when wide, collapsed to icons otherwise, a [Drawer] below
/// [AppBreakpoints.compact]). Once the user cycles the toggle, the app
/// remembers an explicit [hidden]/[icons]/[extended] choice instead.
enum AppSidebarMode { auto, hidden, icons, extended }

String _encode(AppSidebarMode mode) => switch (mode) {
  AppSidebarMode.auto => 'auto',
  AppSidebarMode.hidden => 'hidden',
  AppSidebarMode.icons => 'icons',
  AppSidebarMode.extended => 'extended',
};

AppSidebarMode _decode(String? value) => switch (value) {
  'hidden' => AppSidebarMode.hidden,
  'icons' => AppSidebarMode.icons,
  'extended' => AppSidebarMode.extended,
  _ => AppSidebarMode.auto,
};

/// Resolves the [preference] against the window [width] the shell actually
/// has to render into.
///
/// Below [AppBreakpoints.compact] the shell only has room for a [Drawer],
/// so the preference is overridden there regardless of what the user chose
/// — the window is simply too narrow for a rail. Above that, an explicit
/// [AppSidebarMode.hidden]/[icons]/[extended] preference always wins;
/// [AppSidebarMode.auto] keeps the shell's original width-derived behavior
/// (extended at/above [AppBreakpoints.medium], icons-only below it).
AppSidebarMode effectiveSidebarMode(AppSidebarMode preference, double width) {
  if (width < AppBreakpoints.compact) return AppSidebarMode.hidden;
  if (preference != AppSidebarMode.auto) return preference;
  return width >= AppBreakpoints.medium
      ? AppSidebarMode.extended
      : AppSidebarMode.icons;
}

/// hidden -> icons -> extended -> hidden. Cycling from [AppSidebarMode.auto]
/// (which only happens once, before the first toggle) starts from whatever
/// [auto] currently resolves to, so the first click always visibly changes
/// something.
AppSidebarMode nextSidebarMode(AppSidebarMode effective) => switch (effective) {
  AppSidebarMode.hidden => AppSidebarMode.icons,
  AppSidebarMode.icons => AppSidebarMode.extended,
  AppSidebarMode.extended || AppSidebarMode.auto => AppSidebarMode.hidden,
};

/// Persists the user's sidebar preference across launches.
///
/// Local UI preference only — mirrors `AppearanceNotifier`
/// (`core/state/appearance_preferences.dart`): `build()` returns the
/// default synchronously and loads asynchronously so the first frame is
/// never blocked, and a failed read/write is logged, never thrown.
class SidebarModeNotifier extends Notifier<AppSidebarMode> {
  // Guards a narrow but real race: build() fires _loadAsync() and returns
  // immediately, so set()/cycleFrom() can run — and win — before that load
  // resolves. Without this flag, the pending load would then land *after*
  // set() and silently overwrite the user's explicit choice with whatever
  // was last on disk.
  bool _explicitlySet = false;

  @override
  AppSidebarMode build() {
    _loadAsync();
    return AppSidebarMode.auto;
  }

  Future<void> _loadAsync() async {
    try {
      final prefs = await SharedPreferences.getInstance();
      if (_explicitlySet) return;
      state = _decode(prefs.getString(_sidebarModeKey));
    } catch (e) {
      debugPrint('SidebarModeNotifier: failed to load preference: $e');
    }
  }

  void set(AppSidebarMode mode) {
    _explicitlySet = true;
    state = mode;
    unawaited(_persist(mode));
  }

  Future<void> _persist(AppSidebarMode mode) async {
    try {
      final prefs = await SharedPreferences.getInstance();
      await prefs.setString(_sidebarModeKey, _encode(mode));
    } catch (e) {
      debugPrint('SidebarModeNotifier: failed to save preference: $e');
    }
  }

  /// Cycles from the currently [effective] mode (see [effectiveSidebarMode])
  /// rather than from the raw stored preference, so the toggle always
  /// starts from what's on screen right now.
  void cycleFrom(AppSidebarMode effective) => set(nextSidebarMode(effective));
}

final sidebarModeProvider =
    NotifierProvider<SidebarModeNotifier, AppSidebarMode>(
      SidebarModeNotifier.new,
    );

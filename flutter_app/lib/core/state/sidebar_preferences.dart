import 'dart:async';

import 'package:flutter/foundation.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:shared_preferences/shared_preferences.dart';

import '../../shared/design_system/tokens.dart';

const _sidebarModeKey = 'sidebar_mode';

/// The sidebar's navigation chrome: labelled ([extended]) or icons only.
///
/// [auto] is the state before the user has ever touched the toggle: the
/// sidebar falls back to the width-derived behavior the shell always had
/// (extended when wide, collapsed to icons otherwise). Once the user flips
/// the toggle, the app remembers an explicit [icons]/[extended] choice.
///
/// There is deliberately no hidden mode: the toggle lives at the foot of the
/// rail, so a hidden rail would take its own way back with it. Below
/// [AppBreakpoints.compact] the shell shows a [Drawer] instead of a rail,
/// whatever the preference.
enum AppSidebarMode { auto, icons, extended }

String _encode(AppSidebarMode mode) => switch (mode) {
  AppSidebarMode.auto => 'auto',
  AppSidebarMode.icons => 'icons',
  AppSidebarMode.extended => 'extended',
};

AppSidebarMode _decode(String? value) => switch (value) {
  // 'hidden' was a mode until the toggle moved into the rail. Someone who had
  // hidden the sidebar wanted it out of the way, and icons-only is the closest
  // thing that still leaves them a way back.
  'hidden' || 'icons' => AppSidebarMode.icons,
  'extended' => AppSidebarMode.extended,
  _ => AppSidebarMode.auto,
};

/// Resolves the [preference] against the window [width] into the rail the
/// shell draws: never [AppSidebarMode.auto].
///
/// An explicit [AppSidebarMode.icons]/[extended] preference always wins;
/// [AppSidebarMode.auto] keeps the shell's original width-derived behavior
/// (extended at/above [AppBreakpoints.medium], icons-only below it). The
/// compact [Drawer] layout is the shell's call, not this function's.
AppSidebarMode effectiveSidebarMode(AppSidebarMode preference, double width) {
  if (preference != AppSidebarMode.auto) return preference;
  return width >= AppBreakpoints.medium
      ? AppSidebarMode.extended
      : AppSidebarMode.icons;
}

/// icons <-> extended. [effective] is what is on screen (see
/// [effectiveSidebarMode]), so the first click always visibly changes
/// something; an unresolved [AppSidebarMode.auto] is treated as extended.
AppSidebarMode nextSidebarMode(AppSidebarMode effective) => switch (effective) {
  AppSidebarMode.icons => AppSidebarMode.extended,
  AppSidebarMode.extended || AppSidebarMode.auto => AppSidebarMode.icons,
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

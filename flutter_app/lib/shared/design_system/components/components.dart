export 'app_badge.dart';
export 'app_button.dart';
export 'app_surface.dart';
export 'app_text.dart';

import 'app_badge.dart';
import 'app_button.dart';
import 'app_surface.dart';
import 'app_text.dart';

/// The widget types re-exported by this barrel, in export order.
///
/// Exists so a single test can assert this file's public surface without
/// duplicating each widget's own behavioral tests, and so this file keeps at
/// least one executable line (a pure `export`-only library is invisible to
/// Flutter's coverage collector regardless of import reachability).
List<Type> designSystemComponentTypes() => const [
  AppBadge,
  AppButton,
  AppSurface,
  AppText,
];

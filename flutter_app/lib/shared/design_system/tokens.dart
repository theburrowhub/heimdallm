import 'package:mix/mix.dart';

/// Semantic design tokens for Heimdallm.
///
/// Features must style through these roles rather than hard-coding colors,
/// spacing or radii, so light/dark and future palette tweaks happen in one
/// place (`theme.dart`). Values are supplied by a `MixScope` built from the
/// current `ThemeData` — see `HeimdallmTheme.buildTokens`.
class AppColors {
  AppColors._();

  // Surfaces — from lowest to highest elevation.
  static const canvas = ColorToken('app.color.canvas');
  static const surface = ColorToken('app.color.surface');
  static const surfaceRaised = ColorToken('app.color.surfaceRaised');
  static const border = ColorToken('app.color.border');

  // Text.
  static const text = ColorToken('app.color.text');
  static const textMuted = ColorToken('app.color.textMuted');
  static const onAccent = ColorToken('app.color.onAccent');

  // Brand + interaction.
  static const accent = ColorToken('app.color.accent');
  static const accentMuted = ColorToken('app.color.accentMuted');
  static const focus = ColorToken('app.color.focus');

  // Status.
  static const success = ColorToken('app.color.success');
  static const warning = ColorToken('app.color.warning');
  static const danger = ColorToken('app.color.danger');
  static const info = ColorToken('app.color.info');

  // Feature palette — mirrors
  // repositories/widgets/feature_palette.dart so every surface agrees on
  // what "PR Review" purple vs. "Merge Tracking" green means.
  static const featurePrReview = ColorToken('app.color.feature.prReview');
  static const featureIssueTracking = ColorToken(
    'app.color.feature.issueTracking',
  );
  static const featureDevelop = ColorToken('app.color.feature.develop');
  static const featureMergeTracking = ColorToken(
    'app.color.feature.mergeTracking',
  );
  static const featureMixed = ColorToken('app.color.feature.mixed');
  static const featureOffFill = ColorToken('app.color.feature.offFill');
  static const featureOffOutline = ColorToken(
    'app.color.feature.offOutline',
  );
}

/// Spacing scale. Values are in logical pixels.
class AppSpace {
  AppSpace._();

  static const xs = SpaceToken('app.space.xs'); // 4
  static const sm = SpaceToken('app.space.sm'); // 8
  static const md = SpaceToken('app.space.md'); // 12
  static const lg = SpaceToken('app.space.lg'); // 16
  static const xl = SpaceToken('app.space.xl'); // 24
  static const xxl = SpaceToken('app.space.xxl'); // 32
}

/// Corner radius scale.
class AppRadius {
  AppRadius._();

  static const sm = RadiusToken('app.radius.sm'); // 4
  static const md = RadiusToken('app.radius.md'); // 8
  static const lg = RadiusToken('app.radius.lg'); // 12
}

/// Text style roles, bridged from Material's `TextTheme` in `theme.dart`.
class AppTextStyles {
  AppTextStyles._();

  static const pageTitle = TextStyleToken('app.text.pageTitle');
  static const sectionTitle = TextStyleToken('app.text.sectionTitle');
  static const body = TextStyleToken('app.text.body');
  static const bodyMuted = TextStyleToken('app.text.bodyMuted');
  static const label = TextStyleToken('app.text.label');
  static const mono = TextStyleToken('app.text.mono');
}

/// Layout breakpoints shared by the navigation shell and any feature that
/// needs to reflow. Kept independent of Mix's built-in [BreakpointToken]
/// defaults (767/1023) because the approved plan specifies 768/1200.
class AppBreakpoints {
  AppBreakpoints._();

  static const double compact = 768; // < compact: drawer navigation
  static const double medium = 1200; // < medium: rail navigation
  // >= medium: full sidebar navigation
}

import 'package:flutter/material.dart';
import 'package:mix/mix.dart';

import 'tokens.dart';

/// Heimdallm's brand accent — the seed used everywhere the app builds a
/// [ColorScheme]. Keeping one constant means the splash/error/routed apps in
/// `main.dart` and the Mix token bridge below can never disagree about the
/// brand color.
const kBrandSeed = Color(0xFF0969DA);

/// Height of every app bar: a desktop toolbar, shorter than Material's 56.
const kNativeToolbarHeight = 48.0;

/// Builds Heimdallm's Material [ThemeData] and the Mix [MixScope] that sits
/// on top of it. Material stays authoritative for [ColorScheme]/[TextTheme]
/// (so built-in widgets like [TextField], [Dialog] and menus keep looking
/// native); Mix tokens are derived from the same [ThemeData] so custom
/// components never drift from it.
class HeimdallmTheme {
  HeimdallmTheme._();

  static ThemeData light() => _build(Brightness.light);

  static ThemeData dark() => _build(Brightness.dark);

  static ThemeData _build(Brightness brightness) {
    final scheme = ColorScheme.fromSeed(
      seedColor: kBrandSeed,
      brightness: brightness,
    );
    final base = ThemeData(colorScheme: scheme, useMaterial3: true);
    // Native desktop chrome (#902): controls highlight on hover and press but
    // never draw ink ripples; the app bar is a flat toolbar on the surface
    // colour, so it reads as one piece with the title bar strip macOS draws
    // above it (see TitleBarStrip) and never tints when content scrolls under.
    return base.copyWith(
      splashFactory: NoSplash.splashFactory,
      appBarTheme: AppBarTheme(
        backgroundColor: scheme.surface,
        foregroundColor: scheme.onSurface,
        surfaceTintColor: Colors.transparent,
        elevation: 0,
        scrolledUnderElevation: 0,
        centerTitle: false,
        toolbarHeight: kNativeToolbarHeight,
        titleTextStyle: base.textTheme.titleMedium?.copyWith(
          fontSize: 15,
          fontWeight: FontWeight.w600,
          color: scheme.onSurface,
        ),
        shape: Border(bottom: BorderSide(color: scheme.outlineVariant)),
      ),
      scrollbarTheme: const ScrollbarThemeData(
        thickness: WidgetStatePropertyAll(6),
        radius: Radius.circular(3),
      ),
      tooltipTheme: const TooltipThemeData(
        waitDuration: Duration(milliseconds: 500),
      ),
    );
  }

  /// Wraps [child] with a [MixScope] whose tokens are derived from the
  /// nearest [Theme] — call this *inside* the [MaterialApp]/`MaterialApp.router`
  /// builder so `Theme.of(context)` already reflects light/dark/system mode.
  static Widget scope({required Widget child}) {
    return Builder(
      builder: (context) {
        final theme = Theme.of(context);
        final scheme = theme.colorScheme;
        final isDark = theme.brightness == Brightness.dark;

        return MixScope.withMaterial(
          colors: {
            AppColors.canvas: scheme.surface,
            AppColors.surface: isDark
                ? Color.alphaBlend(
                    Colors.white.withValues(alpha: 0.04),
                    scheme.surface,
                  )
                : scheme.surface,
            AppColors.surfaceRaised: isDark
                ? Color.alphaBlend(
                    Colors.white.withValues(alpha: 0.08),
                    scheme.surface,
                  )
                : Colors.white,
            AppColors.border: scheme.outlineVariant,
            AppColors.text: scheme.onSurface,
            AppColors.textMuted: scheme.onSurfaceVariant,
            AppColors.onAccent: scheme.onPrimary,
            AppColors.accent: scheme.primary,
            AppColors.accentMuted: scheme.primaryContainer,
            AppColors.focus: scheme.primary,
            AppColors.success: isDark
                ? const Color(0xFF3FB950)
                : const Color(0xFF2E7D32),
            AppColors.warning: isDark
                ? const Color(0xFFE3B341)
                : const Color(0xFFB26A00),
            AppColors.danger: scheme.error,
            AppColors.info: scheme.secondary,
            // Feature palette — same hex values on both themes today; kept
            // as tokens (not literals) so a future per-theme tweak is a
            // one-line change here, not a search across every feature.
            AppColors.featurePrReview: const Color(0xFF58A6FF),
            AppColors.featureMergeTracking: const Color(0xFF3FB950),
            AppColors.featureMixed: const Color(0xFFE3B341),
            AppColors.featureOffFill: const Color(0xFF2E333B),
            AppColors.featureOffOutline: const Color(0xFF3B424C),
          },
          spaces: {
            AppSpace.xs: 4,
            AppSpace.sm: 8,
            AppSpace.md: 12,
            AppSpace.lg: 16,
            AppSpace.xl: 24,
            AppSpace.xxl: 32,
          },
          radii: {
            AppRadius.sm: const Radius.circular(4),
            AppRadius.md: const Radius.circular(8),
            AppRadius.lg: const Radius.circular(12),
            AppRadius.pill: const Radius.circular(999),
          },
          textStyles: {
            AppTextStyles.pageTitle:
                theme.textTheme.titleLarge ?? const TextStyle(fontSize: 22),
            AppTextStyles.sectionTitle:
                theme.textTheme.titleSmall ?? const TextStyle(fontSize: 14),
            AppTextStyles.body:
                theme.textTheme.bodyMedium ?? const TextStyle(fontSize: 13),
            AppTextStyles.bodyMuted:
                (theme.textTheme.bodyMedium ?? const TextStyle(fontSize: 13))
                    .copyWith(color: scheme.onSurfaceVariant),
            AppTextStyles.label:
                theme.textTheme.labelMedium ?? const TextStyle(fontSize: 11),
            AppTextStyles.mono:
                (theme.textTheme.bodySmall ?? const TextStyle(fontSize: 12))
                    .copyWith(fontFamily: 'monospace'),
          },
          child: child,
        );
      },
    );
  }
}

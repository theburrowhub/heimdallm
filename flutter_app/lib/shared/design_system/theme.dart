import 'package:flutter/foundation.dart' show defaultTargetPlatform, kIsWeb;
import 'package:flutter/material.dart';
import 'package:mix/mix.dart';

import 'tokens.dart';

/// Heimdallm's brand accent in the light theme. The splash, error and routed
/// apps in `main.dart` and the Mix token bridge below all build from the same
/// palette, so they can never disagree about it.
const kBrandSeed = Color(0xFF0068D9);

/// Height of every app bar: a desktop toolbar, shorter than Material's 56.
const kNativeToolbarHeight = 48.0;

/// The app font on Linux and the web (#926), bundled under `assets/fonts/inter`.
/// macOS keeps the system font.
const kAppFontFamily = 'Inter';

/// Heimdallm's colours (#926): neutral greys, one blue accent, and status
/// colours that keep 4.5:1 text contrast on both the canvas and cards.
@immutable
class HeimdallmPalette {
  const HeimdallmPalette({
    required this.canvas,
    required this.raised,
    required this.sidebar,
    required this.text,
    required this.textMuted,
    required this.border,
    required this.accent,
    required this.onAccent,
    required this.accentMuted,
    required this.onAccentMuted,
    required this.success,
    required this.warning,
    required this.danger,
    required this.onDanger,
    required this.dangerMuted,
    required this.onDangerMuted,
  });

  /// The window background behind content.
  final Color canvas;

  /// Cards, fields and menus that sit on the canvas.
  final Color raised;

  /// The app sidebar where the window has no material behind it.
  final Color sidebar;
  final Color text;
  final Color textMuted;
  final Color border;
  final Color accent;
  final Color onAccent;
  final Color accentMuted;
  final Color onAccentMuted;
  final Color success;
  final Color warning;
  final Color danger;
  final Color onDanger;
  final Color dangerMuted;
  final Color onDangerMuted;

  static const light = HeimdallmPalette(
    canvas: Color(0xFFF5F5F7),
    raised: Color(0xFFFFFFFF),
    sidebar: Color(0xFFECECF1),
    text: Color(0xFF1D1D1F),
    textMuted: Color(0xFF6E6E73),
    border: Color(0xFFDCDCE2),
    accent: kBrandSeed,
    onAccent: Color(0xFFFFFFFF),
    accentMuted: Color(0xFFDCEBFF),
    onAccentMuted: Color(0xFF00285A),
    success: Color(0xFF157F3B),
    warning: Color(0xFFA64B00),
    danger: Color(0xFFC4001A),
    onDanger: Color(0xFFFFFFFF),
    dangerMuted: Color(0xFFFFE4E6),
    onDangerMuted: Color(0xFF7A0010),
  );

  static const dark = HeimdallmPalette(
    canvas: Color(0xFF1C1C1E),
    raised: Color(0xFF2C2C2E),
    sidebar: Color(0xFF242426),
    text: Color(0xFFF5F5F7),
    textMuted: Color(0xFFA1A1A6),
    border: Color(0xFF3A3A3C),
    accent: Color(0xFF4DA3FF),
    onAccent: Color(0xFF001C3A),
    accentMuted: Color(0xFF0B3A6E),
    onAccentMuted: Color(0xFFD6E8FF),
    success: Color(0xFF30D158),
    warning: Color(0xFFFFB340),
    danger: Color(0xFFFF6B61),
    onDanger: Color(0xFF3B0000),
    dangerMuted: Color(0xFF4A1512),
    onDangerMuted: Color(0xFFFFDAD6),
  );

  static HeimdallmPalette of(Brightness brightness) =>
      brightness == Brightness.dark ? dark : light;
}

/// Builds Heimdallm's Material [ThemeData] and the Mix [MixScope] that sits
/// on top of it. Material stays authoritative for [ColorScheme]/[TextTheme]
/// (so built-in widgets like [TextField], [Dialog] and menus pick up the
/// palette); Mix tokens are derived from the same palette so custom
/// components never drift from it.
class HeimdallmTheme {
  HeimdallmTheme._();

  static ThemeData light() => _build(Brightness.light);

  static ThemeData dark() => _build(Brightness.dark);

  static ThemeData _build(Brightness brightness) {
    final p = HeimdallmPalette.of(brightness);
    // The seed only fills the roles the palette leaves open (secondary,
    // tertiary); every surface, text and border colour is the palette's, so
    // nothing picks up Material's blue-tinted greys.
    final scheme =
        ColorScheme.fromSeed(
          seedColor: kBrandSeed,
          brightness: brightness,
        ).copyWith(
          primary: p.accent,
          onPrimary: p.onAccent,
          primaryContainer: p.accentMuted,
          onPrimaryContainer: p.onAccentMuted,
          error: p.danger,
          onError: p.onDanger,
          errorContainer: p.dangerMuted,
          onErrorContainer: p.onDangerMuted,
          surface: p.canvas,
          onSurface: p.text,
          onSurfaceVariant: p.textMuted,
          surfaceContainerLowest: p.raised,
          surfaceContainerLow: p.raised,
          surfaceContainer: p.raised,
          surfaceContainerHigh: p.raised,
          surfaceContainerHighest: p.sidebar,
          outline: p.border,
          outlineVariant: p.border,
          surfaceTint: Colors.transparent,
        );
    final fontFamily = kIsWeb || defaultTargetPlatform != TargetPlatform.macOS
        ? kAppFontFamily
        : null;
    final base = ThemeData(
      colorScheme: scheme,
      useMaterial3: true,
      fontFamily: fontFamily,
      scaffoldBackgroundColor: p.canvas,
    );
    // Material 3 tracks body text out for phones; a desktop app reads tighter.
    final textTheme = base.textTheme.copyWith(
      bodyLarge: base.textTheme.bodyLarge?.copyWith(letterSpacing: 0),
      bodyMedium: base.textTheme.bodyMedium?.copyWith(letterSpacing: 0),
      bodySmall: base.textTheme.bodySmall?.copyWith(letterSpacing: 0),
      labelLarge: base.textTheme.labelLarge?.copyWith(letterSpacing: 0),
      labelMedium: base.textTheme.labelMedium?.copyWith(letterSpacing: 0),
      labelSmall: base.textTheme.labelSmall?.copyWith(letterSpacing: 0),
      titleMedium: base.textTheme.titleMedium?.copyWith(letterSpacing: 0),
      titleSmall: base.textTheme.titleSmall?.copyWith(letterSpacing: 0),
      titleLarge: base.textTheme.titleLarge?.copyWith(
        fontWeight: FontWeight.w700,
        letterSpacing: -0.4,
      ),
    );
    final hairline = BorderSide(color: p.border);
    // Native desktop chrome (#902): controls highlight on hover and press but
    // never draw ink ripples; the app bar is a flat toolbar on the surface
    // colour, so it reads as one piece with the title bar strip macOS draws
    // above it (see TitleBarStrip) and never tints when content scrolls under.
    return base.copyWith(
      textTheme: textTheme,
      splashFactory: NoSplash.splashFactory,
      appBarTheme: AppBarTheme(
        backgroundColor: scheme.surface,
        foregroundColor: scheme.onSurface,
        surfaceTintColor: Colors.transparent,
        elevation: 0,
        scrolledUnderElevation: 0,
        centerTitle: false,
        toolbarHeight: kNativeToolbarHeight,
        titleTextStyle: textTheme.titleMedium?.copyWith(
          fontSize: 15,
          fontWeight: FontWeight.w600,
          color: scheme.onSurface,
        ),
        shape: Border(bottom: hairline),
      ),
      cardTheme: CardThemeData(
        color: p.raised,
        elevation: 0,
        margin: EdgeInsets.zero,
        shape: RoundedRectangleBorder(
          borderRadius: BorderRadius.circular(14),
          side: hairline,
        ),
      ),
      dividerTheme: DividerThemeData(color: p.border, space: 1, thickness: 1),
      outlinedButtonTheme: OutlinedButtonThemeData(
        style: OutlinedButton.styleFrom(side: hairline),
      ),
      chipTheme: ChipThemeData(
        shape: const StadiumBorder(),
        side: hairline,
        backgroundColor: p.raised,
      ),
      inputDecorationTheme: InputDecorationTheme(
        filled: true,
        fillColor: p.raised,
        border: OutlineInputBorder(
          borderRadius: BorderRadius.circular(10),
          borderSide: hairline,
        ),
        enabledBorder: OutlineInputBorder(
          borderRadius: BorderRadius.circular(10),
          borderSide: hairline,
        ),
      ),
      dialogTheme: DialogThemeData(
        backgroundColor: p.raised,
        surfaceTintColor: Colors.transparent,
        shape: RoundedRectangleBorder(borderRadius: BorderRadius.circular(16)),
      ),
      popupMenuTheme: PopupMenuThemeData(
        color: p.raised,
        surfaceTintColor: Colors.transparent,
        shape: RoundedRectangleBorder(
          borderRadius: BorderRadius.circular(10),
          side: hairline,
        ),
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
        final p = HeimdallmPalette.of(theme.brightness);

        return MixScope.withMaterial(
          colors: {
            AppColors.canvas: p.canvas,
            AppColors.surface: p.canvas,
            AppColors.surfaceRaised: p.raised,
            AppColors.sidebar: p.sidebar,
            AppColors.border: p.border,
            AppColors.text: p.text,
            AppColors.textMuted: p.textMuted,
            AppColors.onAccent: p.onAccent,
            AppColors.accent: p.accent,
            AppColors.accentMuted: p.accentMuted,
            AppColors.focus: p.accent,
            AppColors.success: p.success,
            AppColors.warning: p.warning,
            AppColors.danger: p.danger,
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
            AppRadius.lg: const Radius.circular(14),
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
                    .copyWith(color: p.textMuted),
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

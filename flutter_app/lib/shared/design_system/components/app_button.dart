import 'package:flutter/widgets.dart';
import 'package:mix/mix.dart';

import '../tokens.dart';
import 'app_text.dart';

/// Visual weight of an [AppButton].
enum AppButtonVariant { primary, secondary, subtle, destructive }

/// A themed, accessible button built on Mix's [PressableBox].
///
/// Handles hover/press/focus/disabled states via Mix variants and exposes
/// keyboard activation (SPACE/ENTER) and semantics through [Pressable].
class AppButton extends StatelessWidget {
  final String label;
  final VoidCallback? onPressed;
  final AppButtonVariant variant;
  final Widget? leading;

  const AppButton({
    super.key,
    required this.label,
    required this.onPressed,
    this.variant = AppButtonVariant.primary,
    this.leading,
  });

  const AppButton.destructive({
    super.key,
    required this.label,
    required this.onPressed,
    this.leading,
  }) : variant = AppButtonVariant.destructive;

  const AppButton.secondary({
    super.key,
    required this.label,
    required this.onPressed,
    this.leading,
  }) : variant = AppButtonVariant.secondary;

  const AppButton.subtle({
    super.key,
    required this.label,
    required this.onPressed,
    this.leading,
  }) : variant = AppButtonVariant.subtle;

  ({ColorToken background, ColorToken foreground, bool bordered}) get _palette =>
      switch (variant) {
        AppButtonVariant.primary => (
          background: AppColors.accent,
          foreground: AppColors.onAccent,
          bordered: false,
        ),
        AppButtonVariant.secondary => (
          background: AppColors.surfaceRaised,
          foreground: AppColors.text,
          bordered: true,
        ),
        AppButtonVariant.subtle => (
          background: AppColors.surface,
          foreground: AppColors.accent,
          bordered: false,
        ),
        AppButtonVariant.destructive => (
          background: AppColors.danger,
          foreground: AppColors.onAccent,
          bordered: false,
        ),
      };

  @override
  Widget build(BuildContext context) {
    final palette = _palette;
    final enabled = onPressed != null;

    var style = BoxStyler()
        .color(palette.background())
        .borderRadiusAll(AppRadius.md())
        .paddingX(AppSpace.lg())
        .paddingY(AppSpace.sm())
        .onHovered(BoxStyler().color(palette.background().withValues(alpha: 0.9)))
        .onDisabled(BoxStyler().color(palette.background().withValues(alpha: 0.4)));

    if (palette.bordered) {
      style = style.borderAll(color: AppColors.border(), width: 1);
    }

    return PressableBox(
      enabled: enabled,
      onPress: onPressed,
      style: style,
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          if (leading != null) ...[
            IconTheme.merge(
              data: IconThemeData(
                color: palette.foreground.resolve(context),
                size: 18,
              ),
              child: leading!,
            ),
            SizedBox(width: AppSpace.sm.resolve(context)),
          ],
          AppText.label(label, color: palette.foreground()),
        ],
      ),
    );
  }
}

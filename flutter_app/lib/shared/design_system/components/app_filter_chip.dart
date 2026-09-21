import 'package:flutter/material.dart';
import 'package:mix/mix.dart';

import '../tokens.dart';
import 'app_badge.dart';
import 'app_text.dart';

/// A selectable pill used in filter toolbars (sort mode, type/state
/// filters, quick date-range switches, …).
///
/// Reproduces Activity's current chip look (`activity_filter_bar.dart`'s
/// `_sortChip`/`_typeChip`/`_stateChip` helpers and `activity_screen.dart`'s
/// `ChoiceChip`/`ActionChip`/`FilterChip` usage) through design-system
/// tokens instead of literal `Colors.grey.shadeNNN`, `fontSize: 11` and
/// `BorderRadius.circular(20)`, so every toolbar in the app gets the same
/// chip. [accent] lets a caller give a chip its own hue (e.g. the PR/IT/DEV
/// type chips, via `AppColors.featurePrReview` etc.) while keeping the same
/// shape as a plain accent-colored chip.
class AppFilterChip extends StatelessWidget {
  final String label;
  final bool selected;
  final VoidCallback onTap;
  final IconData? icon;
  final ColorToken accent;
  final int? count;

  const AppFilterChip({
    super.key,
    required this.label,
    required this.selected,
    required this.onTap,
    this.icon,
    this.accent = AppColors.accent,
    this.count,
  });

  @override
  Widget build(BuildContext context) {
    final accentColor = accent.resolve(context);
    final mutedColor = AppColors.textMuted.resolve(context);
    final color = selected ? accentColor : mutedColor;
    final pillRadius = BorderRadius.all(AppRadius.pill.resolve(context));

    return InkWell(
      onTap: onTap,
      borderRadius: pillRadius,
      child: Box(
        style: BoxStyler()
            .color(
              selected ? accent().withValues(alpha: 0.15) : Colors.transparent,
            )
            .borderAll(
              color: accent().withValues(alpha: selected ? 0.6 : 0.4),
              width: 1,
            )
            .borderRadiusAll(AppRadius.pill())
            .padding(
              EdgeInsetsGeometryMix.value(
                const EdgeInsets.symmetric(horizontal: 10, vertical: 6),
              ),
            ),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            if (icon != null) ...[
              Icon(icon, size: 13, color: color),
              const SizedBox(width: 4),
            ],
            AppText.label(label, color: color),
            if (count != null) ...[
              const SizedBox(width: 6),
              AppBadge(
                label: '$count',
                foreground: color,
                background: (selected ? accentColor : mutedColor).withValues(
                  alpha: 0.15,
                ),
                padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 1),
                radius: 10,
                fontSize: 10,
                letterSpacing: 0,
              ),
            ],
          ],
        ),
      ),
    );
  }
}

import 'package:flutter/widgets.dart';
import 'package:mix/mix.dart';

import '../tokens.dart';

/// A small pill-shaped label used for statuses, feature tags, and counts.
///
/// Replaces the ad-hoc `Container` + `BoxDecoration` combinations previously
/// duplicated across `SeverityBadge`, `StateBadge`, `PRReviewStateBadge`,
/// `TypeBadge`, and `AttentionBadge`.
class AppBadge extends StatelessWidget {
  final String label;
  final Color foreground;
  final Color background;
  final Color? border;
  final Widget? icon;

  /// Overrides for callers that must reproduce an exact legacy pill shape
  /// (padding/radius/typography) while still routing through Mix. Leave
  /// null to get the default design-system pill.
  final EdgeInsetsGeometry? padding;
  final double? radius;
  final double fontSize;
  final FontWeight fontWeight;
  final double letterSpacing;

  const AppBadge({
    super.key,
    required this.label,
    required this.foreground,
    required this.background,
    this.border,
    this.icon,
    this.padding,
    this.radius,
    this.fontSize = 11,
    this.fontWeight = FontWeight.w600,
    this.letterSpacing = 0.5,
  });

  @override
  Widget build(BuildContext context) {
    var style = BoxStyler()
        .color(background)
        .borderRadiusAll(radius != null ? Radius.circular(radius!) : AppRadius.sm())
        .padding(
          EdgeInsetsGeometryMix.value(
            padding ?? const EdgeInsets.symmetric(horizontal: 8, vertical: 4),
          ),
        );

    if (border != null) {
      style = style.borderAll(color: border!, width: 1);
    }

    return Box(
      style: style,
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          if (icon != null) ...[
            IconTheme.merge(
              data: IconThemeData(color: foreground, size: 12),
              child: icon!,
            ),
            const SizedBox(width: 4),
          ],
          StyledText(
            label,
            style: TextStyler()
                .color(foreground)
                .fontSize(fontSize)
                .fontWeight(fontWeight)
                .letterSpacing(letterSpacing),
          ),
        ],
      ),
    );
  }
}

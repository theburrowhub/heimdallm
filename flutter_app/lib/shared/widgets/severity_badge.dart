import 'package:flutter/material.dart';

import '../design_system/components/app_badge.dart';
import '../design_system/color_resolver.dart';
import '../design_system/tokens.dart';

class SeverityBadge extends StatelessWidget {
  final String severity;

  const SeverityBadge({super.key, required this.severity});

  Color _color(BuildContext context) {
    switch (severity.toLowerCase()) {
      case 'high':
        return resolveAppColor(context, AppColors.danger);
      case 'medium':
        return resolveAppColor(context, AppColors.warning);
      default:
        return resolveAppColor(context, AppColors.success);
    }
  }

  @override
  Widget build(BuildContext context) {
    final color = _color(context);
    // Dark mode's status colours are light, so white text on them is hard to
    // read; there the badge is tinted (coloured text on a translucent fill).
    final dark = Theme.of(context).brightness == Brightness.dark;
    return AppBadge(
      label: severity.toUpperCase(),
      foreground: dark ? color : Colors.white,
      background: dark ? color.withValues(alpha: 0.16) : color,
      border: dark ? color.withValues(alpha: 0.4) : null,
      padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 3),
      radius: 4,
    );
  }
}

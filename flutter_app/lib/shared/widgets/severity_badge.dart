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
    return AppBadge(
      label: severity.toUpperCase(),
      foreground: Colors.white,
      background: _color(context),
      padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 3),
      radius: 4,
    );
  }
}

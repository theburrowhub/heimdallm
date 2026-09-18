import 'package:flutter/material.dart';

import '../design_system/components/app_badge.dart';

class SeverityBadge extends StatelessWidget {
  final String severity;

  const SeverityBadge({super.key, required this.severity});

  Color get _color {
    switch (severity.toLowerCase()) {
      case 'high':
        return Colors.red.shade700;
      case 'medium':
        return Colors.orange.shade700;
      default:
        return Colors.green.shade700;
    }
  }

  @override
  Widget build(BuildContext context) {
    return AppBadge(
      label: severity.toUpperCase(),
      foreground: Colors.white,
      background: _color,
      padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 3),
      radius: 4,
    );
  }
}

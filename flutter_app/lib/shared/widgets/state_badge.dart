import 'package:flutter/material.dart';

import '../design_system/components/app_badge.dart';

class StateBadge extends StatelessWidget {
  final String state;
  const StateBadge({super.key, required this.state});

  bool get _isOpen => state == 'open';

  @override
  Widget build(BuildContext context) {
    return AppBadge(
      label: _isOpen ? 'Open' : 'Closed',
      foreground: Colors.white,
      background: _isOpen ? Colors.green.shade700 : Colors.grey.shade600,
      padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 2),
      radius: 10,
      fontSize: 10,
      letterSpacing: 0,
      icon: Icon(_isOpen ? Icons.circle_outlined : Icons.check_circle),
    );
  }
}

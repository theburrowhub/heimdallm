import 'package:flutter/material.dart';

import '../design_system/components/app_badge.dart';
import '../design_system/color_resolver.dart';
import '../design_system/tokens.dart';

/// AttentionBadge surfaces a non-severity terminal state that a user
/// needs to look at — currently the only producer is
/// `auto_implement_no_changes`, where the agent ran to completion but
/// left the working tree untouched (#483). Rendering the row's stored
/// severity here would be misleading (the triage block is empty, so it
/// defaults to LOW/green) and contradicts the SSE event the daemon
/// publishes alongside the row (`issue_review_error`).
class AttentionBadge extends StatelessWidget {
  final String label;

  const AttentionBadge({super.key, this.label = 'NEEDS ATTENTION'});

  @override
  Widget build(BuildContext context) {
    return AppBadge(
      label: label,
      foreground: Colors.white,
      background: resolveAppColor(context, AppColors.warning),
      padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 3),
      radius: 4,
    );
  }
}

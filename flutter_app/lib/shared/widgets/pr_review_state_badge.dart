import 'package:flutter/material.dart';

import '../design_system/components/app_badge.dart';
import '../design_system/color_resolver.dart';
import '../design_system/tokens.dart';

/// PRReviewStateBadge renders the aggregated external review state
/// of the PR auto_implement created from an issue (#482). The state
/// strings mirror GitHub's review API (APPROVED, CHANGES_REQUESTED,
/// COMMENTED) and an internal FIX_PUSHED marker used by phase 3 to
/// indicate the daemon has already pushed a fix and is waiting for
/// the reviewer to re-review.
class PRReviewStateBadge extends StatelessWidget {
  final String state;

  const PRReviewStateBadge({super.key, required this.state});

  ({String label, Color color})? _style(BuildContext context) {
    switch (state) {
      case 'APPROVED':
        return (
          label: 'PR APPROVED',
          color: resolveAppColor(context, AppColors.success),
        );
      case 'CHANGES_REQUESTED':
        return (
          label: 'CHANGES REQUESTED',
          color: resolveAppColor(context, AppColors.danger),
        );
      case 'COMMENTED':
        return (
          label: 'PR COMMENTED',
          color: resolveAppColor(context, AppColors.info),
        );
      case 'FIX_PUSHED':
        return (label: 'FIX PUSHED', color: Colors.purple.shade700);
      default:
        return null;
    }
  }

  @override
  Widget build(BuildContext context) {
    final style = _style(context);
    if (style == null) {
      return const SizedBox.shrink();
    }
    return AppBadge(
      label: style.label,
      foreground: Colors.white,
      background: style.color,
      padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 3),
      radius: 4,
    );
  }
}

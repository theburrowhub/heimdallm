import 'package:flutter/material.dart';

/// PRReviewStateBadge renders the aggregated external review state
/// of the PR auto_implement created from an issue (#482). The state
/// strings mirror GitHub's review API (APPROVED, CHANGES_REQUESTED,
/// COMMENTED) and an internal FIX_PUSHED marker used by phase 3 to
/// indicate the daemon has already pushed a fix and is waiting for
/// the reviewer to re-review.
class PRReviewStateBadge extends StatelessWidget {
  final String state;

  const PRReviewStateBadge({super.key, required this.state});

  ({String label, Color color})? get _style {
    switch (state) {
      case 'APPROVED':
        return (label: 'PR APPROVED', color: Colors.green.shade700);
      case 'CHANGES_REQUESTED':
        return (label: 'CHANGES REQUESTED', color: Colors.red.shade700);
      case 'COMMENTED':
        return (label: 'PR COMMENTED', color: Colors.blue.shade700);
      case 'FIX_PUSHED':
        return (label: 'FIX PUSHED', color: Colors.purple.shade700);
      default:
        return null;
    }
  }

  @override
  Widget build(BuildContext context) {
    final style = _style;
    if (style == null) {
      return const SizedBox.shrink();
    }
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 3),
      decoration: BoxDecoration(
        color: style.color,
        borderRadius: BorderRadius.circular(4),
      ),
      child: Text(
        style.label,
        style: const TextStyle(
          color: Colors.white,
          fontSize: 11,
          fontWeight: FontWeight.w600,
          letterSpacing: 0.5,
        ),
      ),
    );
  }
}

import 'package:flutter/material.dart';

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
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 3),
      decoration: BoxDecoration(
        color: Colors.deepOrange.shade700,
        borderRadius: BorderRadius.circular(4),
      ),
      child: Text(
        label,
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

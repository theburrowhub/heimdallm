import 'package:flutter/material.dart';

/// The merge-tracking phase of a PR, rendered as a compact badge.
///
/// Matches StateBadge's shape so the two read as one family when they sit side
/// by side on a row.
class MergePhaseBadge extends StatelessWidget {
  final String phase;

  const MergePhaseBadge({super.key, required this.phase});

  @override
  Widget build(BuildContext context) {
    final (label, color, icon) = _visuals(context);
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 2),
      decoration: BoxDecoration(
        color: color,
        borderRadius: BorderRadius.circular(10),
      ),
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          Icon(icon, size: 12, color: Colors.white),
          const SizedBox(width: 4),
          Text(
            label,
            style: const TextStyle(
              color: Colors.white,
              fontSize: 10,
              fontWeight: FontWeight.w600,
            ),
          ),
        ],
      ),
    );
  }

  (String, Color, IconData) _visuals(BuildContext context) {
    switch (phase) {
      case 'merged':
        return ('Merged', const Color(0xFF6A1B9A), Icons.merge_type);
      case 'auto_merge_armed':
        return ('Auto-merge on', const Color(0xFF00695C), Icons.schedule_send);
      case 'updating':
        return ('Updating', const Color(0xFF1565C0), Icons.sync);
      case 'resolving':
        return ('Resolving', const Color(0xFF1565C0), Icons.merge);
      case 'merging':
        return ('Merging', const Color(0xFF2E7D32), Icons.merge_type);
      case 'blocked':
        return ('Blocked', Theme.of(context).colorScheme.error, Icons.block);
      case 'abandoned':
        return ('Not tracked', Colors.grey.shade600, Icons.do_not_disturb_on);
      default:
        return ('Tracking', Colors.blueGrey.shade600, Icons.visibility);
    }
  }
}

/// A human-readable rendering of a block reason, for the cases where the
/// daemon's detail text is empty.
///
/// The reason codes are stable identifiers, not prose; showing
/// `blocked_by_protection` to a user is showing them our internal enum.
String humanBlockReason(String reason) {
  switch (reason) {
    case '':
      return '';
    case 'draft':
      return 'Draft — Heimdallm never acts on drafts';
    case 'conflicts':
      return 'Conflicts with the base branch';
    case 'behind_base':
      return 'Behind the base branch';
    case 'changes_requested':
      return 'A reviewer requested changes';
    case 'review_required':
      return 'An approving review is required';
    case 'insufficient_approvals':
      return 'Not enough approvals for the current commit';
    case 'pending_reviewers':
      return 'Waiting on requested reviewers';
    case 'unresolved_threads':
      return 'Unresolved review conversations';
    case 'checks_failing':
      return 'A required check is failing';
    case 'checks_pending':
      return 'Required checks are still running';
    case 'required_check_missing':
      return 'A required check has not reported';
    case 'checks_unknown':
      return 'The check list could not be read in full';
    case 'threads_unknown':
      return 'The review threads could not be read in full';
    case 'mergeability_unknown':
      return 'GitHub is still computing mergeability';
    case 'hooks_pending':
      return 'Repository hooks are still running';
    case 'blocked_by_protection':
      return 'Blocked by branch protection';
    case 'in_merge_queue':
      return 'In the merge queue — GitHub owns the merge';
    case 'merge_queue_configured':
      return 'The base branch uses a merge queue';
    case 'cross_fork':
      return 'The head branch lives in another fork';
    case 'insufficient_permission':
      return 'No write access to this repository';
    case 'merge_method_not_allowed':
      return 'The configured merge method is disabled for this repo';
    case 'automerge_unavailable':
      return 'Auto-merge is not enabled for this repository';
    case 'automerge_waiting':
      return 'Auto-merge armed — waiting for GitHub';
    case 'head_sha_moved':
      return 'A commit landed while Heimdallm was working';
    case 'cooldown':
      return 'Cooling down after a failed attempt';
    case 'attempt_cap_reached':
      return 'Attempt limit reached for this commit';
    case 'already_merged':
      return 'Already merged';
    case 'closed':
      return 'Closed without merging';
    case 'not_tracked':
      return 'No longer yours to merge';
    case 'excluded':
      return 'Excluded from automation';
    case 'disabled':
      return 'Automation disabled for this repository';
    default:
      // A reason the UI has not learned yet still has to render as something
      // legible rather than as a raw identifier.
      return reason.replaceAll('_', ' ');
  }
}

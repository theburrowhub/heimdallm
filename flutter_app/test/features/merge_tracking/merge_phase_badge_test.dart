import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/features/merge_tracking/widgets/merge_phase_badge.dart';

Widget _host(String phase) => MaterialApp(
  home: Scaffold(
    body: Center(child: MergePhaseBadge(phase: phase)),
  ),
);

void main() {
  // The badge is the one thing on a row that is always visible, so every phase
  // the daemon can persist has to render as words rather than as an enum.
  testWidgets('every phase renders a label and an icon', (tester) async {
    const expected = {
      'merged': 'Merged',
      'auto_merge_armed': 'Auto-merge on',
      'updating': 'Updating',
      'update_pending': 'Syncing',
      'resolving': 'Resolving',
      'merging': 'Merging',
      'blocked': 'Blocked',
      'abandoned': 'Not tracked',
      'idle': 'Tracking',
      // A phase this build has not learned about still has to render.
      'something_new': 'Tracking',
    };
    for (final entry in expected.entries) {
      await tester.pumpWidget(_host(entry.key));
      expect(find.text(entry.value), findsOneWidget, reason: entry.key);
      expect(find.byType(Icon), findsOneWidget, reason: entry.key);
    }
  });

  // Reason codes are stable identifiers, not prose. Showing
  // `blocked_by_protection` to a user is showing them our internal enum, so
  // every reason the daemon emits needs a sentence here.
  test('every block reason the daemon emits reads as a sentence', () {
    const reasons = [
      'draft',
      'conflicts',
      'behind_base',
      'changes_requested',
      'review_required',
      'insufficient_approvals',
      'pending_reviewers',
      'unresolved_threads',
      'checks_failing',
      'checks_pending',
      'required_check_missing',
      'checks_unknown',
      'threads_unknown',
      'mergeability_unknown',
      'hooks_pending',
      'blocked_by_protection',
      'in_merge_queue',
      'merge_queue_configured',
      'cross_fork',
      'insufficient_permission',
      'merge_method_not_allowed',
      'automerge_unavailable',
      'automerge_waiting',
      'head_sha_moved',
      'cooldown',
      'attempt_cap_reached',
      'already_merged',
      'closed',
      'not_tracked',
      'excluded',
      'disabled',
    ];
    for (final reason in reasons) {
      final text = humanBlockReason(reason);
      expect(text, isNotEmpty, reason: reason);
      expect(
        text,
        isNot(contains('_')),
        reason: '$reason still reads as an identifier',
      );
      expect(
        text.substring(0, 1),
        text.substring(0, 1).toUpperCase(),
        reason: '$reason should start as a sentence',
      );
    }
  });

  test('no reason renders as nothing, an unknown one renders legibly', () {
    expect(humanBlockReason(''), '');
    // Not a word we own yet, but still not an identifier on screen.
    expect(humanBlockReason('some_new_reason'), 'some new reason');
  });
}

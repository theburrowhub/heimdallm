import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/activity.dart';
import 'package:heimdallm/features/activity/widgets/activity_entry_tile.dart';

ActivityEntry _mk({
  required ActivityAction action,
  String outcome = '',
  Map<String, dynamic> details = const {},
  String itemType = 'pr',
  DateTime? ts,
}) => ActivityEntry(
  id: 1,
  timestamp: ts ?? DateTime(2026, 4, 20, 9, 34, 12),
  org: 'acme',
  repo: 'acme/api',
  itemType: itemType,
  itemNumber: 42,
  itemTitle: 'Fix rate limiter race',
  action: action,
  outcome: outcome,
  details: details,
);

void main() {
  testWidgets('renders repo, number, title, time, outcome for review', (
    tester,
  ) async {
    final entry = _mk(
      action: ActivityAction.review,
      outcome: 'major',
      details: {'cli_used': 'claude'},
    );

    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(
          body: ActivityEntryTile(entry: entry, onTap: () {}),
        ),
      ),
    );

    expect(find.textContaining('acme/api'), findsOneWidget);
    expect(find.textContaining('#42'), findsOneWidget);
    expect(find.textContaining('Fix rate limiter race'), findsOneWidget);
    expect(find.textContaining('09:34:12'), findsOneWidget);
    expect(find.textContaining('major review by claude'), findsOneWidget);
    expect(find.byIcon(Icons.rate_review), findsOneWidget);
  });

  testWidgets('error action shows error icon and outcome text', (tester) async {
    final entry = _mk(action: ActivityAction.error, outcome: 'cli_not_found');
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(body: ActivityEntryTile(entry: entry)),
      ),
    );
    expect(find.byIcon(Icons.error_outline), findsOneWidget);
    expect(find.textContaining('cli_not_found'), findsOneWidget);
  });

  testWidgets('implement with pr_number > 0 shows opened PR text', (
    tester,
  ) async {
    final entry = _mk(
      action: ActivityAction.implement,
      details: {'pr_number': 99},
    );
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(body: ActivityEntryTile(entry: entry)),
      ),
    );
    expect(find.byIcon(Icons.build), findsOneWidget);
    expect(find.textContaining('Opened PR #99'), findsOneWidget);
  });

  testWidgets('implement with pr_number 0 shows failed text', (tester) async {
    final entry = _mk(
      action: ActivityAction.implement,
      details: {'pr_number': 0},
    );
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(body: ActivityEntryTile(entry: entry)),
      ),
    );
    expect(find.textContaining('Implementation failed'), findsOneWidget);
  });

  testWidgets('promote shows from → to outcome', (tester) async {
    final entry = _mk(
      action: ActivityAction.promote,
      outcome: 'blocked → develop',
    );
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(body: ActivityEntryTile(entry: entry)),
      ),
    );
    expect(find.byIcon(Icons.swap_horiz), findsOneWidget);
    expect(find.textContaining('Promoted: blocked → develop'), findsOneWidget);
  });

  testWidgets('triage shows category', (tester) async {
    final entry = _mk(
      action: ActivityAction.triage,
      outcome: 'major',
      details: {'category': 'develop'},
    );
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(body: ActivityEntryTile(entry: entry)),
      ),
    );
    expect(find.byIcon(Icons.label), findsOneWidget);
    expect(find.textContaining('major'), findsOneWidget);
    expect(find.textContaining('(develop)'), findsOneWidget);
  });

  testWidgets('refinement shows completion state', (tester) async {
    final entry = _mk(
      action: ActivityAction.refinement,
      itemType: 'issue',
      details: {'post_ok': true, 'truncated': true},
    );
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(body: ActivityEntryTile(entry: entry)),
      ),
    );
    expect(find.byIcon(Icons.fact_check), findsOneWidget);
    expect(find.text('Refinement'), findsOneWidget);
    expect(
      find.textContaining('Refinement completed (truncated)'),
      findsOneWidget,
    );
  });

  testWidgets('review_skipped shows skipped badge and draft reason', (
    tester,
  ) async {
    final entry = _mk(
      action: ActivityAction.reviewSkipped,
      outcome: 'draft',
      details: {'reason': 'draft'},
    );
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(body: ActivityEntryTile(entry: entry)),
      ),
    );
    expect(find.byIcon(Icons.visibility_off_outlined), findsOneWidget);
    expect(find.text('Skipped'), findsOneWidget);
    expect(find.textContaining('Skipped because PR is draft'), findsOneWidget);
  });

  // peer_published only happens in a cluster: another instance had already
  // published a review for this commit by the time this one reached the
  // publish boundary (theburrowhub/heimdallm#765). The raw reason reads as
  // "peer published" through the fallback, which says nothing useful to
  // someone looking at why their review never appeared.
  testWidgets('review_skipped explains a peer instance having published', (
    tester,
  ) async {
    final entry = _mk(
      action: ActivityAction.reviewSkipped,
      outcome: 'peer_published',
      details: {'reason': 'peer_published'},
    );
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(body: ActivityEntryTile(entry: entry)),
      ),
    );
    expect(
      find.textContaining(
        'Skipped because another instance already reviewed this commit',
      ),
      findsOneWidget,
    );
  });

  // theburrowhub/heimdallm#781: when the daemon recorded who covered the
  // commit, say so. "another instance" alone made a correct skip look like a
  // lost review to the operator who then hit Re-review.
  testWidgets('review_skipped names the peer login and verdict when recorded', (
    tester,
  ) async {
    final entry = _mk(
      action: ActivityAction.reviewSkipped,
      outcome: 'peer_published',
      details: {
        'reason': 'peer_published',
        'peer_login': 'sergiotejon',
        'peer_review_id': 5151568032,
        'peer_state': 'APPROVED',
      },
    );
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(body: ActivityEntryTile(entry: entry)),
      ),
    );
    expect(
      find.textContaining(
        'Skipped because another instance running as @sergiotejon already reviewed this commit (APPROVED, review 5151568032)',
      ),
      findsOneWidget,
    );
  });

  // head_reanchored (theburrowhub/heimdallm#772): GitHub retargeted our own
  // earlier review's commit_id onto the current HEAD — typically an "Update
  // branch" merge commit — so the pipeline treats the commit as already
  // covered instead of reporting no_rereview_request. The raw reason would
  // otherwise read as "head reanchored" through the fallback.
  testWidgets('review_skipped explains a GitHub HEAD reanchor', (
    tester,
  ) async {
    final entry = _mk(
      action: ActivityAction.reviewSkipped,
      outcome: 'head_reanchored',
      details: {'reason': 'head_reanchored'},
    );
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(body: ActivityEntryTile(entry: entry)),
      ),
    );
    expect(
      find.textContaining(
        'Skipped because GitHub retargeted our review onto the current HEAD',
      ),
      findsOneWidget,
    );
  });
}

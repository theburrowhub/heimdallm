import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/merge_tracking.dart';
import 'package:heimdallm/core/models/pr.dart';
import 'package:heimdallm/core/models/review.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/features/merge_tracking/merge_tracking_providers.dart';
import 'package:heimdallm/features/pr_detail/pr_detail_providers.dart';
import 'package:heimdallm/features/pr_detail/pr_detail_screen.dart';

final _pr = PR(
  id: 1,
  githubId: 101,
  repo: 'acme/widgets',
  number: 7,
  title: 'Add widget cache',
  author: 'octocat',
  url: 'https://github.com/acme/widgets/pull/7',
  state: 'open',
  updatedAt: DateTime.utc(2026, 8, 28),
);

final _review = Review(
  id: 1,
  prId: 1,
  cliUsed: 'claude',
  summary: 'Overall looks good',
  issues: const [],
  severity: 'low',
  createdAt: DateTime.utc(2026, 8, 28),
);

/// Mounts the PR detail screen with the merge-tracking detail the daemon would
/// serve — or with a failure, for the paths where it serves none.
Widget _host({MergeTrackingEntry? tracked, Object? detailError}) => ProviderScope(
  overrides: [
    prDetailProvider(1).overrideWith(
      (_) => Future.value({
        'pr': _pr,
        'reviews': [_review],
      }),
    ),
    sseStreamProvider.overrideWith((ref) => const Stream.empty()),
    mergeTrackingDetailProvider.overrideWith((ref, id) async {
      if (detailError != null) throw detailError;
      return tracked!;
    }),
  ],
  child: MaterialApp.router(
    routerConfig: GoRouter(
      initialLocation: '/prs/1',
      routes: [
        GoRoute(path: '/', builder: (_, _) => const SizedBox()),
        GoRoute(
          path: '/prs/:id',
          builder: (ctx, state) =>
              PRDetailScreen(prId: int.parse(state.pathParameters['id']!)),
        ),
      ],
    ),
  ),
);

MergeTrackingEntry _entry({
  String phase = 'blocked',
  String blockReason = 'checks_failing',
  String blockDetail = '1 required check is failing: build (GitHub Actions)',
  MergeDecision? decision,
  int failing = 1,
}) => MergeTrackingEntry(
  prId: 1,
  repo: 'acme/widgets',
  number: 7,
  phase: phase,
  blockReason: blockReason,
  blockDetail: blockDetail,
  checksRequiredFailing: failing,
  decision: decision,
);

final _failingDecision = MergeDecision(
  ready: false,
  blocks: const [MergeBlock(reason: 'checks_failing', detail: 'build failed')],
  checks: const [
    MergeCheck(
      name: 'build',
      state: 'failure',
      required: true,
      app: 'GitHub Actions',
      url: 'https://ci.example/build',
    ),
    MergeCheck(name: 'coverage', state: 'failure'),
  ],
  checksSummary: const MergeChecksSummary(
    total: 2,
    requiredTotal: 1,
    requiredFailing: 1,
    optionalFailing: 1,
  ),
);

void main() {
  // A red check is more urgent than the review, so the panel sits above it and
  // has to name the check rather than report a count.
  testWidgets('a failing check is explained in the PR detail', (tester) async {
    await tester.pumpWidget(_host(tracked: _entry(decision: _failingDecision)));
    await tester.pumpAndSettle();

    expect(find.text('Merge status'), findsOneWidget);
    expect(find.text('Blocked'), findsOneWidget);
    expect(find.textContaining('build (GitHub Actions)'), findsWidgets);
    expect(find.text('build'), findsOneWidget);
  });

  // A block that has nothing to do with CI still needs its sentence; the
  // banner is for checks only.
  testWidgets('a non-check block reads as a line, not a banner', (tester) async {
    await tester.pumpWidget(
      _host(
        tracked: _entry(
          blockReason: 'draft',
          blockDetail: '',
          failing: 0,
          decision: const MergeDecision(ready: false, blocks: []),
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('Draft — Heimdallm never acts on drafts'), findsOneWidget);
  });

  // A merged PR is not blocked by anything, whatever the last counts were.
  testWidgets('a merged PR shows its phase and no blocker', (tester) async {
    await tester.pumpWidget(
      _host(
        tracked: _entry(
          phase: 'merged',
          blockReason: '',
          blockDetail: '',
          failing: 0,
          decision: const MergeDecision(ready: true, blocks: []),
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('Merged'), findsOneWidget);
  });

  // A PR nobody tracks answers 404. Showing nothing is right — an error banner
  // on every untracked PR would be noise.
  testWidgets('an untracked PR shows no merge panel at all', (tester) async {
    await tester.pumpWidget(_host(detailError: ApiException('not found')));
    await tester.pumpAndSettle();

    expect(find.text('Merge status'), findsNothing);
    // The rest of the screen is unaffected.
    expect(find.text('Overall looks good'), findsOneWidget);
  });

  // Tracked but never evaluated: there is no decision to render.
  testWidgets('a tracked PR with no decision yet shows no panel', (tester) async {
    await tester.pumpWidget(_host(tracked: _entry()));
    await tester.pumpAndSettle();

    expect(find.text('Merge status'), findsNothing);
  });
}

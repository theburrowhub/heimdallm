import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/merge_tracking.dart';
import 'package:heimdallm/features/merge_tracking/merge_tracking_providers.dart';
import 'package:heimdallm/features/merge_tracking/merge_tracking_screen.dart';

MergeTrackingEntry _entry({
  int prId = 1,
  String phase = 'blocked',
  String blockReason = '',
  String blockDetail = '',
  int failing = 0,
  int pending = 0,
  String title = 'Add widget cache',
  bool isAuthor = true,
}) => MergeTrackingEntry(
  prId: prId,
  repo: 'acme/widgets',
  number: prId + 6,
  title: title,
  url: 'https://github.com/acme/widgets/pull/${prId + 6}',
  author: 'octocat',
  phase: phase,
  blockReason: blockReason,
  blockDetail: blockDetail,
  isAuthor: isAuthor,
  checksRequiredFailing: failing,
  checksRequiredPending: pending,
);

Widget _host(List<MergeTrackingEntry> entries) => ProviderScope(
  overrides: [
    mergeTrackingProvider.overrideWith((ref) async => entries),
    // The listener subscribes to the SSE stream, which has no daemon in a
    // widget test; overriding it to a no-op keeps the test hermetic.
    mergeTrackingSseListenerProvider.overrideWithValue(null),
  ],
  child: const MaterialApp(home: Scaffold(body: MergeTrackingScreen())),
);

void main() {
  testWidgets('empty state explains what the tab tracks', (tester) async {
    await tester.pumpWidget(_host(const []));
    await tester.pumpAndSettle();

    expect(find.text('No pull requests tracked yet'), findsOneWidget);
    expect(find.textContaining('authored or are assigned to'), findsOneWidget);
  });

  // The whole point of the check warning: it must show the daemon's detail
  // text, which names the failing check, not a bare count.
  testWidgets('a failing check shows a prominent warning naming the check', (
    tester,
  ) async {
    // Disposed explicitly at the end of the test: addTearDown runs after the
    // framework's end-of-test handle check and would fail it.
    final semantics = tester.ensureSemantics();
    await tester.pumpWidget(
      _host([
        _entry(
          blockReason: 'checks_failing',
          blockDetail: '1 required check is failing: build (GitHub Actions)',
          failing: 1,
        ),
      ]),
    );
    await tester.pumpAndSettle();

    expect(
      find.text('1 required check is failing: build (GitHub Actions)'),
      findsOneWidget,
    );
    // The counter chip makes the state legible even when the text is clipped.
    expect(find.bySemanticsLabel('1 required checks failing'), findsOneWidget);
    semantics.dispose();
  });

  testWidgets('pending checks are shown as waiting, not as a failure', (
    tester,
  ) async {
    // Disposed explicitly at the end of the test: addTearDown runs after the
    // framework's end-of-test handle check and would fail it.
    final semantics = tester.ensureSemantics();
    await tester.pumpWidget(
      _host([
        _entry(
          blockReason: 'checks_pending',
          blockDetail: '2 required checks are still running: build, lint',
          pending: 2,
        ),
      ]),
    );
    await tester.pumpAndSettle();

    expect(
      find.text('2 required checks are still running: build, lint'),
      findsOneWidget,
    );
    expect(find.bySemanticsLabel('2 required checks running'), findsOneWidget);
    expect(find.bySemanticsLabel('0 required checks failing'), findsNothing);
    semantics.dispose();
  });

  // A non-check block still has to be legible; the reason code is an internal
  // identifier, not something to put in front of a user.
  testWidgets('a non-check block renders as a readable sentence', (
    tester,
  ) async {
    await tester.pumpWidget(_host([_entry(blockReason: 'unresolved_threads')]));
    await tester.pumpAndSettle();

    expect(find.text('Unresolved review conversations'), findsOneWidget);
    expect(find.text('unresolved_threads'), findsNothing);
  });

  testWidgets('the daemon detail wins over the generic reason text', (
    tester,
  ) async {
    await tester.pumpWidget(
      _host([
        _entry(
          blockReason: 'changes_requested',
          blockDetail: 'alice requested changes',
        ),
      ]),
    );
    await tester.pumpAndSettle();

    expect(find.text('alice requested changes'), findsOneWidget);
  });

  testWidgets('phase is shown as a badge', (tester) async {
    await tester.pumpWidget(
      _host([_entry(phase: 'auto_merge_armed', blockReason: '')]),
    );
    await tester.pumpAndSettle();

    expect(find.text('Auto-merge on'), findsOneWidget);
  });

  testWidgets('a merged PR shows no block line', (tester) async {
    await tester.pumpWidget(
      _host([_entry(phase: 'merged', blockReason: 'already_merged')]),
    );
    await tester.pumpAndSettle();

    expect(find.text('Merged'), findsOneWidget);
    expect(find.text('Already merged'), findsNothing);
  });

  testWidgets('a PR assigned but not authored says so', (tester) async {
    await tester.pumpWidget(
      _host([
        MergeTrackingEntry(
          prId: 1,
          repo: 'acme/widgets',
          number: 7,
          title: 'Someone else PR',
          isAuthor: false,
          isAssignee: true,
        ),
      ]),
    );
    await tester.pumpAndSettle();

    expect(find.textContaining('assigned to you'), findsOneWidget);
  });

  testWidgets('the check-problem count only counts live PRs', (tester) async {
    final container = ProviderContainer(
      overrides: [
        mergeTrackingProvider.overrideWith(
          (ref) async => [
            _entry(prId: 1, failing: 1),
            _entry(prId: 2, pending: 3),
            // Terminal rows are history; badging them would keep the tab red
            // forever after a merge.
            _entry(prId: 3, phase: 'merged', failing: 5),
            _entry(prId: 4),
          ],
        ),
      ],
    );
    addTearDown(container.dispose);

    await container.read(mergeTrackingProvider.future);
    expect(container.read(mergeTrackingCheckProblemCountProvider), 2);
  });
}

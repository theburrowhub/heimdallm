import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/merge_tracking.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/features/merge_tracking/merge_tracking_providers.dart';
import 'package:heimdallm/features/merge_tracking/merge_tracking_screen.dart';
import 'package:heimdallm/shared/design_system/theme.dart';
import 'package:mocktail/mocktail.dart';

class _MockApiClient extends Mock implements ApiClient {}

MergeTrackingEntry _entry({
  int prId = 1,
  String phase = 'blocked',
  String blockReason = '',
  String blockDetail = '',
  int failing = 0,
  int pending = 0,
  String title = 'Add widget cache',
  bool isAuthor = true,
  String attention = 'none',
  bool stale = false,
  DateTime? lastActivityAt,
  DateTime? terminalAt,
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
  attention: attention,
  stale: stale,
  lastActivityAt: lastActivityAt,
  terminalAt: terminalAt,
);

Widget _host(List<MergeTrackingEntry> entries, {ApiClient? api}) =>
    ProviderScope(
      overrides: [
        mergeTrackingProvider.overrideWith((ref) async => entries),
        // The listener subscribes to the SSE stream, which has no daemon in a
        // widget test; overriding it to a no-op keeps the test hermetic.
        mergeTrackingSseListenerProvider.overrideWithValue(null),
        if (api != null) apiClientProvider.overrideWithValue(api),
      ],
      child: const MaterialApp(
        builder: _withMixScope,
        home: Scaffold(body: MergeTrackingScreen()),
      ),
    );

void main() {
  testWidgets('empty state explains what the tab tracks', (tester) async {
    await tester.pumpWidget(_host(const []));
    await tester.pumpAndSettle();

    expect(_text('No open pull requests of yours'), findsOneWidget);
    expect(_textContaining('authored or are assigned to'), findsOneWidget);
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
      _text('1 required check is failing: build (GitHub Actions)'),
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
      _text('2 required checks are still running: build, lint'),
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

    expect(_text('Unresolved review conversations'), findsOneWidget);
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

    expect(_text('alice requested changes'), findsOneWidget);
  });

  // A PR that is both behind its base and failing a check reports
  // `behind_base` as its primary blocker — correct, that is the next action —
  // but the failing check is the part a human has to fix, so it must still get
  // the prominent warning rather than being buried.
  testWidgets('a failing check warns even when it is not the primary blocker', (
    tester,
  ) async {
    await tester.pumpWidget(
      _host([
        _entry(
          blockReason: 'behind_base',
          blockDetail: 'the head branch is behind main',
          failing: 1,
        ),
      ]),
    );
    await tester.pumpAndSettle();

    // Both facts are present: the check warning and the primary blocker.
    expect(_textContaining('1 required check is failing'), findsOneWidget);
    expect(_text('the head branch is behind main'), findsOneWidget);
  });

  // A merged PR keeps no warning, whatever its last recorded counts were.
  testWidgets('a terminal PR shows no check warning', (tester) async {
    await tester.pumpWidget(
      _host([
        _entry(phase: 'merged', blockReason: 'already_merged', failing: 3),
      ]),
    );
    await tester.pumpAndSettle();

    expect(find.textContaining('required check'), findsNothing);
  });

  testWidgets('phase is shown as a badge', (tester) async {
    await tester.pumpWidget(
      _host([_entry(phase: 'auto_merge_armed', blockReason: '')]),
    );
    await tester.pumpAndSettle();

    expect(_text('Auto-merge on'), findsOneWidget);
  });

  testWidgets('a row with no title falls back to repo and number', (
    tester,
  ) async {
    await tester.pumpWidget(_host([_entry(title: '')]));
    await tester.pumpAndSettle();

    expect(_text('acme/widgets #7'), findsOneWidget);
  });

  testWidgets('a merged PR shows no block line', (tester) async {
    await tester.pumpWidget(
      _host([_entry(phase: 'merged', blockReason: 'already_merged')]),
    );
    await tester.pumpAndSettle();
    // Finished PRs sit in the collapsed "recently merged" section.
    await tester.tap(_textContaining('Recently merged or closed'));
    await tester.pumpAndSettle();

    expect(_text('Merged'), findsOneWidget);
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

    expect(_textContaining('assigned to you'), findsOneWidget);
  });

  testWidgets('the badge counts PRs that need the operator, once each', (
    tester,
  ) async {
    final container = ProviderContainer(
      overrides: [
        mergeTrackingProvider.overrideWith(
          (ref) async => [
            _entry(prId: 1, attention: 'action', failing: 1),
            // Needs action AND stale: one PR, counted once.
            _entry(prId: 2, attention: 'action', stale: true),
            _entry(prId: 3, attention: 'ready'),
            _entry(prId: 4, attention: 'waiting', stale: true),
            // Waiting on CI is not the operator's problem: no badge for it.
            _entry(prId: 5, attention: 'waiting', pending: 3),
            // Terminal rows are history; badging them would keep the tab red
            // forever after a merge.
            _entry(prId: 6, phase: 'merged', attention: 'action', failing: 5),
          ],
        ),
      ],
    );
    addTearDown(container.dispose);

    await container.read(mergeTrackingProvider.future);
    expect(container.read(myPrsAttentionCountProvider), 4);
    final summary = container.read(myPrsSummaryProvider);
    expect(summary.needAction, 2);
    expect(summary.ready, 1);
    expect(summary.stale, 2);
  });

  testWidgets('PRs are grouped by who they are waiting on', (tester) async {
    await tester.pumpWidget(
      _host([
        _entry(prId: 1, title: 'Waiting one', attention: 'waiting'),
        _entry(prId: 2, title: 'Broken one', attention: 'action'),
        _entry(prId: 3, title: 'Ready one', attention: 'ready'),
      ]),
    );
    await tester.pumpAndSettle();

    final action = tester.getTopLeft(_textContaining('Needs your action')).dy;
    final ready = tester.getTopLeft(_textContaining('Ready to merge')).dy;
    final waiting = tester.getTopLeft(_textContaining('Waiting on others')).dy;
    expect(action < ready && ready < waiting, isTrue);
    expect(
      tester.getTopLeft(_text('Broken one')).dy <
          tester.getTopLeft(_text('Ready one')).dy,
      isTrue,
    );
    expect(_textContaining('Recently merged'), findsNothing);
  });

  testWidgets('recently merged PRs are collapsed until asked for', (
    tester,
  ) async {
    await tester.pumpWidget(
      _host([
        _entry(prId: 1, title: 'Live one', attention: 'action'),
        _entry(
          prId: 2,
          title: 'Merged one',
          phase: 'merged',
          terminalAt: DateTime.now().subtract(const Duration(hours: 2)),
        ),
      ]),
    );
    await tester.pumpAndSettle();

    expect(_textContaining('Recently merged or closed · 1'), findsOneWidget);
    expect(_text('Merged one'), findsNothing);

    await tester.tap(_textContaining('Recently merged or closed'));
    await tester.pumpAndSettle();
    expect(_text('Merged one'), findsOneWidget);

    await tester.tap(_textContaining('Recently merged or closed'));
    await tester.pumpAndSettle();
    expect(_text('Merged one'), findsNothing);
  });

  testWidgets('a stale PR shows how long it has been idle', (tester) async {
    await tester.pumpWidget(
      _host([
        _entry(
          attention: 'waiting',
          stale: true,
          lastActivityAt: DateTime.now().subtract(const Duration(days: 4)),
        ),
      ]),
    );
    await tester.pumpAndSettle();

    expect(find.byKey(const Key('stale-chip')), findsOneWidget);
    expect(_textContaining('Stale · 4d'), findsOneWidget);
  });

  testWidgets('a re-check shows a busy spinner while the request is running', (
    tester,
  ) async {
    final api = _MockApiClient();
    final completer = Completer<MergeTrackingEntry>();
    when(
      () => api.evaluateMergeTracking(any(), dryRun: any(named: 'dryRun')),
    ).thenAnswer((_) => completer.future);

    await tester.pumpWidget(_host([_entry()], api: api));
    await tester.pumpAndSettle();

    await tester.tap(find.text('Re-check'));
    await tester.pump();

    expect(find.byType(CircularProgressIndicator), findsOneWidget);

    completer.complete(_entry());
    await tester.pump();
    await tester.pumpWidget(const SizedBox.shrink());
  });

  testWidgets('the GitHub button launches the PR URL', (tester) async {
    const channel = MethodChannel('plugins.flutter.io/url_launcher');
    final messenger =
        TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger;
    final launchedUrls = <String>[];
    messenger.setMockMethodCallHandler(channel, (call) async {
      if (call.method == 'launch') {
        launchedUrls.add((call.arguments as Map)['url'] as String);
        return true;
      }
      if (call.method == 'canLaunch') return true;
      return null;
    });
    addTearDown(() => messenger.setMockMethodCallHandler(channel, null));

    await tester.pumpWidget(_host([_entry()]));
    await tester.pumpAndSettle();

    await tester.tap(find.byTooltip('Open on GitHub'));
    await tester.pumpAndSettle();

    expect(launchedUrls, contains('https://github.com/acme/widgets/pull/7'));
  });

  testWidgets('an expanded row renders detail-load failures inline', (
    tester,
  ) async {
    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          mergeTrackingProvider.overrideWith((ref) async => [_entry()]),
          mergeTrackingSseListenerProvider.overrideWithValue(null),
          mergeTrackingDetailProvider.overrideWith(
            (ref, id) async => throw ApiException('detail unavailable'),
          ),
        ],
        child: const MaterialApp(
          builder: _withMixScope,
          home: Scaffold(body: MergeTrackingScreen()),
        ),
      ),
    );
    await tester.pumpAndSettle();

    await tester.tap(find.text('Show checks'));
    await tester.pumpAndSettle();

    expect(
      _textContaining(
        'Could not load checks: ApiException: detail unavailable',
      ),
      findsOneWidget,
    );
  });
}

Widget _withMixScope(BuildContext context, Widget? child) =>
    HeimdallmTheme.scope(child: child ?? const SizedBox.shrink());

Finder _text(String value) => find.text(value, findRichText: true);

Finder _textContaining(String value) =>
    find.textContaining(value, findRichText: true);

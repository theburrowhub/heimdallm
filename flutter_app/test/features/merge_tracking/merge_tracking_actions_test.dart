import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/merge_tracking.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/features/merge_tracking/merge_tracking_providers.dart';
import 'package:heimdallm/features/merge_tracking/merge_tracking_screen.dart';
import 'package:mocktail/mocktail.dart';

class _MockApiClient extends Mock implements ApiClient {}

MergeTrackingEntry _entry({
  String phase = 'blocked',
  String blockReason = 'checks_failing',
  String blockDetail = '1 required check is failing: build (GitHub Actions)',
  String lastError = '',
  bool excluded = false,
  int failing = 1,
}) => MergeTrackingEntry(
  prId: 1,
  repo: 'acme/widgets',
  number: 7,
  title: 'Add widget cache',
  url: 'https://github.com/acme/widgets/pull/7',
  author: 'octocat',
  phase: phase,
  blockReason: blockReason,
  blockDetail: blockDetail,
  lastError: lastError,
  excluded: excluded,
  isAuthor: true,
  checksRequiredFailing: failing,
);

final _decision = MergeDecision(
  ready: false,
  blocks: const [MergeBlock(reason: 'checks_failing', detail: 'build failed')],
  checks: const [
    MergeCheck(name: 'build', state: 'failure', required: true),
    MergeCheck(name: 'coverage', state: 'success'),
  ],
  checksSummary: const MergeChecksSummary(
    total: 2,
    requiredTotal: 1,
    requiredFailing: 1,
  ),
);

Widget _host(
  List<MergeTrackingEntry> entries, {
  ApiClient? api,
  MergeTrackingEntry? detail,
}) => ProviderScope(
  overrides: [
    mergeTrackingProvider.overrideWith((ref) async => entries),
    mergeTrackingSseListenerProvider.overrideWithValue(null),
    if (api != null) apiClientProvider.overrideWithValue(api),
    if (detail != null)
      mergeTrackingDetailProvider.overrideWith((ref, id) async => detail),
  ],
  child: const MaterialApp(home: Scaffold(body: MergeTrackingScreen())),
);

void main() {
  setUpAll(() => registerFallbackValue(<String, dynamic>{}));

  // The check breakdown is loaded on demand so the listing stays small; the
  // toggle is the only way to reach it.
  testWidgets('showing checks loads the breakdown on demand', (tester) async {
    await tester.pumpWidget(
      _host(
        [_entry()],
        detail: MergeTrackingEntry(
          prId: 1,
          repo: 'acme/widgets',
          number: 7,
          phase: 'blocked',
          decision: _decision,
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('Show checks'), findsOneWidget);
    expect(find.text('build'), findsNothing);

    await tester.tap(find.text('Show checks'));
    await tester.pumpAndSettle();

    expect(find.text('Hide checks'), findsOneWidget);
    expect(find.text('build'), findsOneWidget);

    await tester.tap(find.text('Hide checks'));
    await tester.pumpAndSettle();
    expect(find.text('build'), findsNothing);
  });

  // A PR the daemon has never evaluated has no decision to show, and saying so
  // beats an empty panel that reads as "no checks".
  testWidgets('an unevaluated PR says so rather than showing nothing', (
    tester,
  ) async {
    await tester.pumpWidget(
      _host(
        [_entry()],
        detail: const MergeTrackingEntry(prId: 1, repo: 'acme/widgets', number: 7),
      ),
    );
    await tester.pumpAndSettle();
    await tester.tap(find.text('Show checks'));
    await tester.pumpAndSettle();

    expect(find.textContaining('has not evaluated this PR yet'), findsOneWidget);
  });

  // Re-check is a question, never an action: it must not authorise a merge the
  // operator did not configure.
  testWidgets('re-check asks the daemon for a dry run', (tester) async {
    final api = _MockApiClient();
    when(
      () => api.evaluateMergeTracking(any(), dryRun: any(named: 'dryRun')),
    ).thenAnswer((_) async => _entry());

    await tester.pumpWidget(_host([_entry()], api: api));
    await tester.pumpAndSettle();

    await tester.tap(find.text('Re-check'));
    await tester.pumpAndSettle();

    verify(() => api.evaluateMergeTracking(1, dryRun: true)).called(1);
  });

  testWidgets('a failed re-check is reported rather than swallowed', (
    tester,
  ) async {
    final api = _MockApiClient();
    when(
      () => api.evaluateMergeTracking(any(), dryRun: any(named: 'dryRun')),
    ).thenThrow(ApiException('daemon is down'));

    await tester.pumpWidget(_host([_entry()], api: api));
    await tester.pumpAndSettle();

    await tester.tap(find.text('Re-check'));
    await tester.pumpAndSettle();

    expect(find.textContaining('Re-check failed'), findsOneWidget);
  });

  // Excluding is the per-PR escape hatch that needs no config edit, so it has
  // to work in both directions from the row itself.
  testWidgets('a tracked PR can be excluded from automation', (tester) async {
    final api = _MockApiClient();
    when(
      () => api.setMergeTrackingExcluded(any(), any()),
    ).thenAnswer((_) async {});

    await tester.pumpWidget(_host([_entry()], api: api));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Exclude'));
    await tester.pumpAndSettle();
    verify(() => api.setMergeTrackingExcluded(1, true)).called(1);
  });

  testWidgets('an excluded PR offers the way back in', (tester) async {
    final api = _MockApiClient();
    when(
      () => api.setMergeTrackingExcluded(any(), any()),
    ).thenAnswer((_) async {});

    await tester.pumpWidget(_host([_entry(excluded: true)], api: api));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Include'));
    await tester.pumpAndSettle();
    verify(() => api.setMergeTrackingExcluded(1, false)).called(1);
  });

  testWidgets('a failed exclusion is reported', (tester) async {
    final api = _MockApiClient();
    when(
      () => api.setMergeTrackingExcluded(any(), any()),
    ).thenThrow(ApiException('nope'));

    await tester.pumpWidget(_host([_entry()], api: api));
    await tester.pumpAndSettle();
    await tester.tap(find.text('Exclude'));
    await tester.pumpAndSettle();

    expect(find.textContaining('Failed:'), findsOneWidget);
  });

  // The daemon records the error that parked a row. Hiding it leaves the
  // operator with a blocked PR and no explanation.
  testWidgets('the last error is shown on the row', (tester) async {
    await tester.pumpWidget(
      _host([_entry(lastError: 'GitHub rejected the merge: the head had moved')]),
    );
    await tester.pumpAndSettle();

    expect(find.textContaining('the head had moved'), findsOneWidget);
  });

  testWidgets('a load failure explains itself', (tester) async {
    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          mergeTrackingProvider.overrideWith(
            (ref) async => throw ApiException('daemon is down'),
          ),
          mergeTrackingSseListenerProvider.overrideWithValue(null),
        ],
        child: const MaterialApp(home: Scaffold(body: MergeTrackingScreen())),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.textContaining('Error loading merge tracking'), findsOneWidget);
  });
}

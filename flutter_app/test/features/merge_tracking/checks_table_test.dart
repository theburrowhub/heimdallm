import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/merge_tracking.dart';
import 'package:heimdallm/features/merge_tracking/widgets/checks_table.dart';

MergeCheck _check({
  required String name,
  String state = 'success',
  bool required = true,
  String app = '',
  String url = '',
  DateTime? startedAt,
  DateTime? completedAt,
}) => MergeCheck(
  name: name,
  state: state,
  required: required,
  app: app,
  url: url,
  startedAt: startedAt,
  completedAt: completedAt,
);

Widget _host(MergeDecision decision, {void Function(String)? onOpenUrl}) =>
    MaterialApp(
      home: Scaffold(
        body: SingleChildScrollView(
          child: ChecksTable(decision: decision, onOpenUrl: onOpenUrl),
        ),
      ),
    );

void main() {
  _optionalOnlyRegression();

  // The sentence is the point: a grid of coloured dots does not tell anyone
  // what to do next.
  testWidgets('a failing required check produces a plain-language headline', (
    tester,
  ) async {
    await tester.pumpWidget(
      _host(
        MergeDecision(
          checks: [
            _check(name: 'build', state: 'failure', app: 'GitHub Actions'),
            _check(name: 'lint'),
            _check(name: 'test'),
            _check(name: 'vet'),
          ],
          checksSummary: const MergeChecksSummary(
            total: 4,
            requiredTotal: 4,
            requiredFailing: 1,
            requiredSuccess: 3,
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(
      find.text(
        'This PR cannot be merged: 1 of the 4 required checks is failing.',
      ),
      findsOneWidget,
    );
    expect(find.text('build'), findsOneWidget);
    // The app is what disambiguates "build" in a repo with several providers.
    expect(find.textContaining('GitHub Actions'), findsOneWidget);
  });

  testWidgets('pending checks say the PR will merge on its own', (
    tester,
  ) async {
    await tester.pumpWidget(
      _host(
        MergeDecision(
          checks: [_check(name: 'build', state: 'pending')],
          checksSummary: const MergeChecksSummary(
            total: 1,
            requiredTotal: 1,
            requiredPending: 1,
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(
      find.textContaining('The PR merges on its own once they pass.'),
      findsOneWidget,
    );
  });

  // An optional red check is the single most confusing thing on this screen if
  // it is not explained: the reader sees red and assumes it is the blocker.
  testWidgets('an optional failure is stated as not blocking, and collapsed', (
    tester,
  ) async {
    await tester.pumpWidget(
      _host(
        MergeDecision(
          ready: true,
          checks: [
            _check(name: 'build'),
            _check(name: 'coverage', state: 'failure', required: false),
          ],
          checksSummary: const MergeChecksSummary(
            total: 2,
            requiredTotal: 1,
            requiredSuccess: 1,
            optionalFailing: 1,
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(
      find.textContaining('which does not block the merge'),
      findsOneWidget,
    );
    // Collapsed by default so its noise cannot hide what is blocking.
    expect(find.text('coverage'), findsNothing);
    expect(
      find.text('1 optional check (do not block the merge)'),
      findsOneWidget,
    );

    await tester.tap(find.text('1 optional check (do not block the merge)'));
    await tester.pumpAndSettle();
    expect(find.text('coverage'), findsOneWidget);
  });

  testWidgets(
    'required checks carry a Required chip and optional ones do not',
    (tester) async {
      await tester.pumpWidget(
        _host(
          MergeDecision(
            checks: [
              _check(name: 'build'),
              _check(name: 'coverage', required: false),
            ],
            checksSummary: const MergeChecksSummary(
              total: 2,
              requiredTotal: 1,
              requiredSuccess: 1,
            ),
          ),
        ),
      );
      await tester.pumpAndSettle();

      expect(find.text('Required'), findsOneWidget);
    },
  );

  // A required context that never reported shows nothing red anywhere in
  // GitHub's UI, so it needs its own callout.
  testWidgets('required checks that never reported are called out', (
    tester,
  ) async {
    await tester.pumpWidget(
      _host(
        MergeDecision(
          checks: [_check(name: 'build')],
          checksSummary: const MergeChecksSummary(
            total: 1,
            requiredTotal: 2,
            requiredSuccess: 1,
            missingRequired: ['e2e'],
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('Required checks that have not reported'), findsOneWidget);
    expect(find.text('• e2e'), findsOneWidget);
    expect(find.textContaining('has not run yet'), findsOneWidget);
  });

  testWidgets('a truncated check list is reported, never treated as green', (
    tester,
  ) async {
    await tester.pumpWidget(
      _host(
        MergeDecision(
          checks: [_check(name: 'build')],
          checksSummary: const MergeChecksSummary(
            total: 1,
            requiredTotal: 1,
            requiredSuccess: 1,
            truncated: true,
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(
      find.textContaining('merge state cannot be confirmed'),
      findsOneWidget,
    );
  });

  testWidgets('all green says so', (tester) async {
    await tester.pumpWidget(
      _host(
        MergeDecision(
          ready: true,
          checks: [
            _check(name: 'build'),
            _check(name: 'lint'),
          ],
          checksSummary: const MergeChecksSummary(
            total: 2,
            requiredTotal: 2,
            requiredSuccess: 2,
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('All 2 checks passed.'), findsOneWidget);
  });

  testWidgets('no checks configured is stated rather than left blank', (
    tester,
  ) async {
    await tester.pumpWidget(
      _host(
        const MergeDecision(ready: true, checksSummary: MergeChecksSummary()),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('This PR has no checks configured.'), findsOneWidget);
  });

  testWidgets('a check with a log URL opens it', (tester) async {
    String? opened;
    await tester.pumpWidget(
      _host(
        MergeDecision(
          checks: [
            _check(
              name: 'build',
              state: 'failure',
              url: 'https://ci.example.test/build/1',
            ),
          ],
          checksSummary: const MergeChecksSummary(
            total: 1,
            requiredTotal: 1,
            requiredFailing: 1,
          ),
        ),
        onOpenUrl: (url) => opened = url,
      ),
    );
    await tester.pumpAndSettle();

    await tester.tap(find.byTooltip('Open the check log'));
    await tester.pumpAndSettle();
    expect(opened, 'https://ci.example.test/build/1');
  });

  testWidgets('a check duration is shown when GitHub reported both ends', (
    tester,
  ) async {
    await tester.pumpWidget(
      _host(
        MergeDecision(
          checks: [
            _check(
              name: 'build',
              startedAt: DateTime.utc(2026, 8, 28, 10, 0),
              completedAt: DateTime.utc(2026, 8, 28, 10, 5, 30),
            ),
          ],
          checksSummary: const MergeChecksSummary(
            total: 1,
            requiredTotal: 1,
            requiredSuccess: 1,
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('5m 30s'), findsOneWidget);
  });
}

// Regression from the second review of PR #738: a repo with no required checks
// and a red optional one fell through to the last branch and announced "All N
// checks passed" over a failure.
void _optionalOnlyRegression() {
  testWidgets('a red optional check is not reported as everything passing', (
    tester,
  ) async {
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(
          body: ChecksTable(
            decision: const MergeDecision(
              ready: true,
              checks: [
                MergeCheck(name: 'coverage', state: 'failure'),
                MergeCheck(name: 'lint', state: 'success'),
              ],
              checksSummary: MergeChecksSummary(
                total: 2,
                requiredTotal: 0,
                optionalFailing: 1,
              ),
            ),
            onOpenUrl: _noop,
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.textContaining('All 2 checks passed'), findsNothing);
    expect(find.textContaining('No required checks are configured'), findsOneWidget);
    expect(find.textContaining('does not block the merge'), findsOneWidget);
  });
}

void _noop(String _) {}

import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/tracked_issue.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/features/issues/issues_providers.dart';
import 'package:heimdallm/features/issues/issues_screen.dart';
import 'package:heimdallm/shared/design_system/theme.dart';
import 'package:heimdallm/shared/widgets/attention_badge.dart';
import 'package:heimdallm/shared/widgets/pr_review_state_badge.dart';
import 'package:heimdallm/shared/widgets/severity_badge.dart';
import 'package:mocktail/mocktail.dart';

class _MockApiClient extends Mock implements ApiClient {}

TrackedIssue _issue(
  int id,
  String repo,
  int number,
  String title, {
  String author = 'alice',
  List<dynamic> labels = const [],
  TrackedIssueReview? latestReview,
  TrackedIssueLinkedPR? linkedPR,
}) => TrackedIssue(
  id: id,
  githubId: 2000 + id,
  repo: repo,
  number: number,
  title: title,
  body: '',
  author: author,
  assignees: const [],
  labels: labels,
  state: 'open',
  createdAt: DateTime(2026, 9, 1),
  fetchedAt: DateTime(2026, 9, 1),
  latestReview: latestReview,
  linkedPR: linkedPR,
);

TrackedIssueReview _review(
  int id, {
  String actionTaken = 'review_only',
  String severity = 'high',
}) => TrackedIssueReview(
  id: id,
  issueId: 0,
  cliUsed: 'claude',
  summary: 'Looks risky',
  triage: {'severity': severity},
  nextSteps: const [],
  actionTaken: actionTaken,
  prCreated: 0,
  createdAt: DateTime(2026, 9, 1),
);

TrackedIssueLinkedPR _linkedPr({
  String state = 'APPROVED',
  String reviewer = 'bob',
}) => TrackedIssueLinkedPR(
  number: 41,
  url: 'https://github.com/acme/widgets/pull/41',
  state: 'open',
  externalReviewState: state,
  externalReviewer: reviewer,
  externalReviewAt: DateTime(2026, 9, 1),
);

Widget _host({required Object issues, ApiClient? api}) {
  Future<List<TrackedIssue>> resolve() async {
    if (issues is Exception) throw issues;
    return issues as List<TrackedIssue>;
  }

  return ProviderScope(
    overrides: [
      issuesProvider.overrideWith((ref) => resolve()),
      apiClientProvider.overrideWithValue(api ?? _MockApiClient()),
      sseStreamProvider.overrideWith((ref) => const Stream.empty()),
    ],
    child: MaterialApp.router(
      builder: (context, child) =>
          HeimdallmTheme.scope(child: child ?? const SizedBox.shrink()),
      routerConfig: GoRouter(
        routes: [
          GoRoute(
            path: '/',
            builder: (_, _) => const Scaffold(body: IssuesScreen()),
          ),
          GoRoute(
            path: '/issues/:id',
            builder: (_, state) => Scaffold(
              body: Text('Issue detail ${state.pathParameters['id']}'),
            ),
          ),
        ],
      ),
    ),
  );
}

void main() {
  testWidgets('an error state shows the retry action', (tester) async {
    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          issuesProvider.overrideWith(
            (ref) => Future<List<TrackedIssue>>.error(
              ApiException('daemon offline'),
            ),
          ),
          sseStreamProvider.overrideWith((ref) => const Stream.empty()),
        ],
        child: MaterialApp(
          builder: (context, child) =>
              HeimdallmTheme.scope(child: child ?? const SizedBox.shrink()),
          home: const Scaffold(body: IssuesScreen()),
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('Retry'), findsOneWidget);
    await tester.tap(find.text('Retry'));
    await tester.pump();
  });

  testWidgets('repo filtering updates the visible issue count', (tester) async {
    await tester.pumpWidget(
      _host(
        issues: [
          _issue(1, 'zeta/api', 9, 'Zulu issue', labels: const ['bug']),
          _issue(2, 'acme/api', 7, 'Alpha issue', labels: const ['infra']),
        ],
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('2 issues', findRichText: true), findsOneWidget);
    expect(find.text('Alpha issue', findRichText: true), findsOneWidget);
    expect(find.text('Zulu issue', findRichText: true), findsOneWidget);

    await tester.tap(find.text('All'));
    await tester.pumpAndSettle();
    expect(find.text('acme/api').last, findsOneWidget);
    expect(find.text('zeta/api').last, findsOneWidget);

    await tester.tap(find.text('acme/api').last);
    await tester.pumpAndSettle();

    expect(find.text('1 issue', findRichText: true), findsOneWidget);
    expect(find.text('Alpha issue', findRichText: true), findsOneWidget);
    expect(find.text('Zulu issue', findRichText: true), findsNothing);
    expect(find.text('bug'), findsNothing);
    expect(find.text('infra'), findsOneWidget);
  });

  testWidgets('a pending issue shows its labels and navigates to detail', (
    tester,
  ) async {
    await tester.pumpWidget(
      _host(
        issues: [
          _issue(
            1,
            'acme/api',
            7,
            'Pending issue',
            labels: const ['bug', 'mix', 'urgent', 'ignored'],
          ),
        ],
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('PENDING'), findsOneWidget);
    expect(find.text('bug'), findsOneWidget);
    expect(find.text('mix'), findsOneWidget);
    expect(find.text('urgent'), findsOneWidget);
    expect(find.text('ignored'), findsNothing);

    await tester.tap(find.text('Pending issue', findRichText: true));
    await tester.pumpAndSettle();

    expect(find.text('Issue detail 1'), findsOneWidget);
  });

  testWidgets(
    'a review request failure clears its spinner and surfaces the error',
    (tester) async {
      final api = _MockApiClient();
      when(
        () => api.triggerIssueReview(any()),
      ).thenThrow(ApiException('no daemon'));

      await tester.pumpWidget(
        _host(issues: [_issue(1, 'acme/api', 7, 'Needs review')], api: api),
      );
      await tester.pumpAndSettle();

      await tester.tap(find.text('Review'));
      await tester.pumpAndSettle();

      verify(() => api.triggerIssueReview(1)).called(1);
      expect(find.byType(CircularProgressIndicator), findsNothing);
      expect(
        find.textContaining('Error: ApiException: no daemon'),
        findsOneWidget,
      );
    },
  );

  testWidgets('a successful review request leaves the row in reviewing state', (
    tester,
  ) async {
    final api = _MockApiClient();
    final completer = Completer<void>();
    when(
      () => api.triggerIssueReview(any()),
    ).thenAnswer((_) => completer.future);

    await tester.pumpWidget(
      _host(issues: [_issue(1, 'acme/api', 7, 'Needs review')], api: api),
    );
    await tester.pumpAndSettle();

    await tester.tap(find.text('Review'));
    await tester.pump();

    expect(find.byType(CircularProgressIndicator), findsOneWidget);
    completer.complete();
    await tester.pump();
    await tester.pumpWidget(const SizedBox.shrink());
  });

  testWidgets('reviewed issues can be promoted and show a severity badge', (
    tester,
  ) async {
    final api = _MockApiClient();
    when(() => api.promoteIssue(any())).thenAnswer((_) async {});

    await tester.pumpWidget(
      _host(
        issues: [
          _issue(
            1,
            'acme/api',
            7,
            'Reviewed issue',
            latestReview: _review(
              9,
              actionTaken: 'refinement',
              severity: 'medium',
            ),
          ),
        ],
        api: api,
      ),
    );
    await tester.pumpAndSettle();

    expect(find.byType(SeverityBadge), findsOneWidget);
    expect(find.text('Promote to Dev'), findsOneWidget);

    await tester.tap(find.text('Promote to Dev'));
    await tester.pumpAndSettle();

    verify(() => api.promoteIssue(1)).called(1);
    expect(find.text('Stage promotion requested'), findsOneWidget);
  });

  testWidgets('a promotion in progress uses the secondary spinner color', (
    tester,
  ) async {
    final api = _MockApiClient();
    final completer = Completer<void>();
    when(() => api.promoteIssue(any())).thenAnswer((_) => completer.future);

    await tester.pumpWidget(
      _host(
        issues: [
          _issue(1, 'acme/api', 7, 'Reviewed issue', latestReview: _review(9)),
        ],
        api: api,
      ),
    );
    await tester.pumpAndSettle();

    await tester.tap(find.text('Promote'));
    await tester.pump();

    final spinner = tester.widget<CircularProgressIndicator>(
      find.byType(CircularProgressIndicator),
    );
    expect(spinner.color, isNotNull);

    completer.complete();
    await tester.pump();
    await tester.pumpWidget(const SizedBox.shrink());
  });

  testWidgets('linked PRs and no-change reviews render the right badges', (
    tester,
  ) async {
    await tester.pumpWidget(
      _host(
        issues: [
          _issue(
            1,
            'acme/api',
            7,
            'Linked PR issue',
            latestReview: _review(9),
            linkedPR: _linkedPr(),
          ),
          _issue(
            2,
            'acme/api',
            8,
            'Needs human attention',
            latestReview: _review(
              10,
              actionTaken: 'auto_implement_no_changes',
              severity: 'low',
            ),
          ),
        ],
      ),
    );
    await tester.pumpAndSettle();

    expect(find.byType(PRReviewStateBadge), findsOneWidget);
    expect(find.text('PR APPROVED'), findsOneWidget);
    expect(find.byType(AttentionBadge), findsOneWidget);
    expect(find.text('Promote'), findsOneWidget);
  });

  testWidgets('dismissing an issue offers undo and calls the API both ways', (
    tester,
  ) async {
    final api = _MockApiClient();
    when(() => api.dismissIssue(any())).thenAnswer((_) async {});
    when(() => api.undismissIssue(any())).thenAnswer((_) async {});

    await tester.pumpWidget(
      _host(issues: [_issue(1, 'acme/api', 7, 'Disposable issue')], api: api),
    );
    await tester.pumpAndSettle();

    await tester.tap(find.byTooltip('Dismiss issue'));
    await tester.pumpAndSettle();

    verify(() => api.dismissIssue(1)).called(1);
    expect(find.text('Issue #7 dismissed'), findsOneWidget);

    await tester.tap(find.text('Undo'));
    await tester.pumpAndSettle();

    verify(() => api.undismissIssue(1)).called(1);
  });
}

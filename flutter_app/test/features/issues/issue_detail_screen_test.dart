import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/tracked_issue.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/features/issues/issue_detail_screen.dart';
import 'package:heimdallm/features/issues/issues_providers.dart';
import 'package:heimdallm/shared/design_system/theme.dart';
import 'package:heimdallm/shared/widgets/attention_badge.dart';
import 'package:heimdallm/shared/widgets/pr_review_state_badge.dart';
import 'package:heimdallm/shared/widgets/severity_badge.dart';
import 'package:mocktail/mocktail.dart';

class _MockApiClient extends Mock implements ApiClient {}

TrackedIssue _issue({
  TrackedIssueLinkedPR? linkedPR,
  List<dynamic> assignees = const ['alice', 'bob'],
  List<dynamic> labels = const ['bug', 'mix'],
}) => TrackedIssue(
  id: 9,
  githubId: 2009,
  repo: 'acme/widgets',
  number: 41,
  title: 'Fix the flaky pilot banner',
  body: '',
  author: 'octocat',
  assignees: assignees,
  labels: labels,
  state: 'open',
  createdAt: DateTime(2026, 9, 1, 10, 30),
  fetchedAt: DateTime(2026, 9, 1, 10, 30),
  linkedPR: linkedPR,
);

TrackedIssueReview _review({
  String actionTaken = 'review_only',
  Map<String, dynamic> triage = const {
    'severity': 'high',
    'category': 'develop',
  },
  List<dynamic> nextSteps = const [
    'Re-run pilot flow',
    'Add a regression test',
  ],
}) => TrackedIssueReview(
  id: 12,
  issueId: 9,
  cliUsed: 'claude',
  summary: 'The pilot flow still misses a stale state reset.',
  triage: triage,
  nextSteps: nextSteps,
  actionTaken: actionTaken,
  prCreated: 0,
  createdAt: DateTime(2026, 9, 1, 11),
);

TrackedIssueLinkedPR _linkedPr({String reviewer = 'alice'}) =>
    TrackedIssueLinkedPR(
      number: 88,
      url: 'https://github.com/acme/widgets/pull/88',
      state: 'open',
      externalReviewState: 'APPROVED',
      externalReviewer: reviewer,
      externalReviewAt: DateTime(2026, 9, 1, 11, 30),
    );

Widget _host({
  required TrackedIssue issue,
  required List<TrackedIssueReview> reviews,
  ApiClient? api,
}) {
  return ProviderScope(
    overrides: [
      apiClientProvider.overrideWithValue(api ?? _MockApiClient()),
      issueDetailProvider.overrideWith(
        (ref, key) async => {'issue': issue, 'reviews': reviews},
      ),
      sseStreamProvider.overrideWith((ref) => const Stream.empty()),
    ],
    child: MaterialApp.router(
      builder: (context, child) =>
          HeimdallmTheme.scope(child: child ?? const SizedBox.shrink()),
      routerConfig: GoRouter(
        initialLocation: '/issues/9',
        routes: [
          GoRoute(path: '/', builder: (_, _) => const SizedBox()),
          GoRoute(
            path: '/issues/:id',
            builder: (_, state) => IssueDetailScreen(
              issueId: int.parse(state.pathParameters['id']!),
            ),
          ),
        ],
      ),
    ),
  );
}

void _setWideSurface(WidgetTester tester) {
  tester.view.physicalSize = const Size(1400, 1200);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.resetPhysicalSize);
  addTearDown(tester.view.resetDevicePixelRatio);
}

void main() {
  testWidgets(
    'detail view renders linked PR state, review notes and metadata',
    (tester) async {
      _setWideSurface(tester);
      await tester.pumpWidget(
        _host(
          issue: _issue(linkedPR: _linkedPr()),
          reviews: [_review()],
        ),
      );
      await tester.pumpAndSettle();

      expect(
        find.text('Fix the flaky pilot banner', findRichText: true),
        findsOneWidget,
      );
      expect(find.byType(PRReviewStateBadge), findsOneWidget);
      expect(find.text('PR APPROVED'), findsOneWidget);
      expect(find.text('alice on PR #88'), findsOneWidget);
      expect(
        find.text('Reviewed by claude', findRichText: true),
        findsOneWidget,
      );
      expect(find.byType(SeverityBadge), findsOneWidget);
      expect(find.text('Classification', findRichText: true), findsOneWidget);
      expect(
        find.text('Category: develop', findRichText: true),
        findsOneWidget,
      );
      expect(find.text('Next steps', findRichText: true), findsOneWidget);
      expect(find.byIcon(Icons.lightbulb_outline), findsNWidgets(2));
      expect(find.text('Assignees:', findRichText: true), findsOneWidget);
      expect(find.text('alice, bob', findRichText: true), findsOneWidget);
      expect(find.text('bug'), findsOneWidget);
      expect(find.text('mix'), findsOneWidget);
    },
  );

  testWidgets(
    'a no-review issue shows the fallback linked PR label and launches GitHub',
    (tester) async {
      _setWideSurface(tester);
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

      await tester.pumpWidget(
        _host(
          issue: _issue(
            linkedPR: _linkedPr(reviewer: ''),
            assignees: const [],
            labels: const [],
          ),
          reviews: const [],
        ),
      );
      await tester.pumpAndSettle();

      expect(find.text('PR #88'), findsOneWidget);
      expect(find.text('No reviews yet.', findRichText: true), findsOneWidget);

      await tester.tap(find.text('Open on GitHub'));
      await tester.pumpAndSettle();

      expect(
        launchedUrls,
        contains('https://github.com/acme/widgets/issues/41'),
      );
    },
  );

  testWidgets(
    'auto-implement-without-changes reviews show the attention badge',
    (tester) async {
      _setWideSurface(tester);
      await tester.pumpWidget(
        _host(
          issue: _issue(),
          reviews: [
            _review(
              actionTaken: 'auto_implement_no_changes',
              triage: const {},
              nextSteps: const [],
            ),
          ],
        ),
      );
      await tester.pumpAndSettle();

      expect(find.byType(AttentionBadge), findsOneWidget);
    },
  );
}

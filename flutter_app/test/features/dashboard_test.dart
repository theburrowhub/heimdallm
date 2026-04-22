import 'dart:async';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:heimdallm/core/models/pr.dart';
import 'package:heimdallm/core/models/review.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/features/dashboard/dashboard_screen.dart';

PR _pr({
  int id = 1,
  String repo = 'org/repo',
  int number = 42,
  Review? latestReview,
}) =>
    PR(
      id: id,
      githubId: 1000 + id,
      repo: repo,
      number: number,
      title: 't',
      author: 'a',
      url: 'u',
      state: 'open',
      updatedAt: DateTime.utc(2026, 1, 1),
      latestReview: latestReview,
    );

Review _review(int id) => Review(
      id: id,
      prId: 1,
      cliUsed: 'claude',
      summary: '',
      issues: const [],
      suggestions: const [],
      severity: 'low',
      createdAt: DateTime.utc(2026, 1, 1),
    );

void main() {
  group('reconcileReviewing', () {
    test('drops entry when first review lands (baseline=0, latestReview present)', () {
      final pr = _pr(repo: 'org/repo', number: 1, latestReview: _review(42));
      final out = reconcileReviewing({'org/repo:1': 0}, [pr]);
      expect(out, isEmpty);
    });

    test('keeps entry when review still pending (baseline=0, no latestReview yet)', () {
      final pr = _pr(repo: 'org/repo', number: 1);
      final out = reconcileReviewing({'org/repo:1': 0}, [pr]);
      expect(out, equals({'org/repo:1': 0}));
    });

    test('keeps entry during re-review (baseline matches current id)', () {
      final pr = _pr(repo: 'org/repo', number: 1, latestReview: _review(42));
      final out = reconcileReviewing({'org/repo:1': 42}, [pr]);
      expect(out, equals({'org/repo:1': 42}));
    });

    test('drops entry when re-review completes (baseline older than current id)', () {
      final pr = _pr(repo: 'org/repo', number: 1, latestReview: _review(43));
      final out = reconcileReviewing({'org/repo:1': 42}, [pr]);
      expect(out, isEmpty);
    });

    test('preserves entry for PR not in current list', () {
      final out = reconcileReviewing({'org/other:9': 5}, const []);
      expect(out, equals({'org/other:9': 5}));
    });

    test('reconciles a mixed set (drops stale, keeps in-progress)', () {
      final stale = _pr(repo: 'org/a', number: 1, latestReview: _review(100));
      final fresh = _pr(id: 2, repo: 'org/b', number: 2, latestReview: _review(200));
      final out = reconcileReviewing(
        {'org/a:1': 0, 'org/b:2': 200},
        [stale, fresh],
      );
      expect(out, equals({'org/b:2': 200}));
    });
  });

  testWidgets('DashboardScreen shows PR title', (tester) async {
    final pr = PR(
      id: 1, githubId: 101, repo: 'org/repo', number: 42,
      title: 'Fix critical bug', author: 'alice', url: 'https://github.com',
      state: 'open', updatedAt: DateTime.now(),
    );

    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          prsProvider.overrideWith((ref) => Future.value([pr])),
          sseStreamProvider.overrideWith((ref) => const Stream.empty()),
        ],
        child: MaterialApp.router(
          routerConfig: GoRouter(
            routes: [GoRoute(path: '/', builder: (_, __) => const DashboardScreen())],
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.textContaining('Fix critical bug'), findsOneWidget);
    expect(find.textContaining('org/repo'), findsOneWidget);
  });

  testWidgets('DashboardScreen shows loading indicator while fetching', (tester) async {
    final completer = Completer<List<PR>>();
    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          prsProvider.overrideWith((ref) => completer.future),
          sseStreamProvider.overrideWith((ref) => const Stream.empty()),
        ],
        child: MaterialApp.router(
          routerConfig: GoRouter(
            routes: [GoRoute(path: '/', builder: (_, __) => const DashboardScreen())],
          ),
        ),
      ),
    );
    await tester.pump();
    expect(find.byType(CircularProgressIndicator), findsOneWidget);
  });
}

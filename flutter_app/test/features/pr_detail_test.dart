import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:mocktail/mocktail.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/pr.dart';
import 'package:heimdallm/core/models/review.dart';
import 'package:heimdallm/core/models/review_status.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/features/pr_detail/pr_detail_providers.dart';
import 'package:heimdallm/features/pr_detail/pr_detail_screen.dart';

class MockDetailApiClient extends Mock implements ApiClient {}

void main() {
  testWidgets('PRDetailScreen shows review summary', (tester) async {
    final pr = PR(id: 1, githubId: 101, repo: 'org/repo', number: 42,
      title: 'Fix bug', author: 'alice', url: 'https://github.com',
      state: 'open', updatedAt: DateTime.now());
    final review = Review(id: 1, prId: 1, cliUsed: 'claude',
      summary: 'Overall looks good', issues: [],
      severity: 'low', createdAt: DateTime.now());

    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          prDetailProvider(1).overrideWith((_) => Future.value({'pr': pr, 'reviews': [review]})),
          sseStreamProvider.overrideWith((ref) => const Stream.empty()),
        ],
        child: MaterialApp.router(
          routerConfig: GoRouter(routes: [
            GoRoute(path: '/', builder: (_, _) => const SizedBox()),
            GoRoute(path: '/prs/:id', builder: (ctx, state) =>
                PRDetailScreen(prId: int.parse(state.pathParameters['id']!))),
          ], initialLocation: '/prs/1'),
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('Fix bug'), findsOneWidget);
    expect(find.text('Overall looks good'), findsOneWidget);
  });

  testWidgets('active daemon status offers scoped cancellation from detail', (
    tester,
  ) async {
    final api = MockDetailApiClient();
    when(() => api.cancelReview(1)).thenAnswer((_) async {});
    final pr = PR(
      id: 1,
      githubId: 101,
      repo: 'org/repo',
      number: 42,
      title: 'Fix bug',
      author: 'alice',
      url: 'https://github.com',
      state: 'open',
      updatedAt: DateTime.now(),
      reviewStatus: ReviewExecutionStatus(
        headSha: 'abc',
        attempts: 1,
        failedAt: DateTime(2026, 8, 24, 10, 30),
        retryAt: DateTime(2026, 8, 24, 10, 35),
        error: '',
        active: true,
      ),
    );

    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          apiClientProvider.overrideWithValue(api),
          prDetailProvider(1).overrideWith(
            (_) => Future.value({'pr': pr, 'reviews': <Review>[]}),
          ),
          sseStreamProvider.overrideWith((ref) => const Stream.empty()),
        ],
        child: MaterialApp.router(
          routerConfig: GoRouter(
            routes: [
              GoRoute(path: '/', builder: (_, _) => const SizedBox()),
              GoRoute(
                path: '/prs/:id',
                builder: (_, state) => PRDetailScreen(
                  prId: int.parse(state.pathParameters['id']!),
                ),
              ),
            ],
            initialLocation: '/prs/1',
          ),
        ),
      ),
    );
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 100));

    expect(find.text('Cancel'), findsOneWidget);
    await tester.tap(find.text('Cancel'));
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 300));
    expect(find.text('Cancel this review?'), findsOneWidget);
    await tester.tap(find.text('Cancel review'));
    await tester.pump();

    verify(() => api.cancelReview(1)).called(1);
  });

  testWidgets('cancelled detail says cancelled and offers Retry', (
    tester,
  ) async {
    final pr = PR(
      id: 1,
      githubId: 101,
      repo: 'org/repo',
      number: 42,
      title: 'Fix bug',
      author: 'alice',
      url: 'https://github.com',
      state: 'open',
      updatedAt: DateTime.now(),
      reviewStatus: ReviewExecutionStatus(
        headSha: 'abc',
        attempts: 1,
        failedAt: DateTime(2026, 8, 24, 10, 30),
        retryAt: DateTime(2099, 8, 24, 10, 35),
        error: ReviewExecutionStatus.manualCancellationError,
        active: false,
      ),
    );

    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          prDetailProvider(1).overrideWith(
            (_) => Future.value({'pr': pr, 'reviews': <Review>[]}),
          ),
          sseStreamProvider.overrideWith((ref) => const Stream.empty()),
        ],
        child: MaterialApp.router(
          routerConfig: GoRouter(
            routes: [
              GoRoute(path: '/', builder: (_, _) => const SizedBox()),
              GoRoute(
                path: '/prs/:id',
                builder: (_, state) => PRDetailScreen(
                  prId: int.parse(state.pathParameters['id']!),
                ),
              ),
            ],
            initialLocation: '/prs/1',
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('Retry'), findsOneWidget);
    expect(find.textContaining('cancelled 2026-08-24 10:30'), findsOneWidget);
    expect(find.textContaining('failed 2026-08-24'), findsNothing);
  });
}

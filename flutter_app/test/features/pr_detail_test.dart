import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:heimdallr/core/models/pr.dart';
import 'package:heimdallr/core/models/review.dart';
import 'package:heimdallr/features/dashboard/dashboard_providers.dart';
import 'package:heimdallr/features/pr_detail/pr_detail_providers.dart';
import 'package:heimdallr/features/pr_detail/pr_detail_screen.dart';

void main() {
  testWidgets('PRDetailScreen shows review summary', (tester) async {
    final pr = PR(id: 1, githubId: 101, repo: 'org/repo', number: 42,
      title: 'Fix bug', author: 'alice', url: 'https://github.com',
      state: 'open', updatedAt: DateTime.now());
    final review = Review(id: 1, prId: 1, cliUsed: 'claude',
      summary: 'Overall looks good', issues: [], suggestions: ['add tests'],
      severity: 'low', createdAt: DateTime.now());

    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          prDetailProvider(1).overrideWith((_) => Future.value({'pr': pr, 'reviews': [review]})),
          sseStreamProvider.overrideWith((ref) => const Stream.empty()),
        ],
        child: MaterialApp.router(
          routerConfig: GoRouter(routes: [
            GoRoute(path: '/', builder: (_, __) => const SizedBox()),
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
}

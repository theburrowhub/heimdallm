import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/agent.dart';
import 'package:heimdallm/features/agents/agents_screen.dart';
import 'package:heimdallm/features/config/config_providers.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/features/repositories/repo_detail_screen.dart';
import 'package:mocktail/mocktail.dart';

class MockApiClient extends Mock implements ApiClient {}

void main() {
  testWidgets('RepoDetailScreen hides Organizations filter in Issue Tracking', (
    tester,
  ) async {
    const repoName = 'theburrowhub/heimdallm';
    final mockApi = MockApiClient();

    when(() => mockApi.fetchConfig()).thenAnswer(
      (_) async => {
        'repositories': [repoName],
        'server_port': 1,
        'poll_interval': '60s',
        'retention_days': 30,
        'ai_primary': 'claude',
        'ai_fallback': '',
        'review_mode': 'single',
        'issue_tracking': {'enabled': true},
      },
    );
    when(
      () => mockApi.fetchRepoLabels(repoName),
    ).thenAnswer((_) async => <String>[]);
    when(
      () => mockApi.fetchRepoCollaborators(repoName),
    ).thenAnswer((_) async => <String>[]);

    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          apiClientProvider.overrideWithValue(mockApi),
          configNotifierProvider.overrideWith(ConfigNotifier.new),
          agentsProvider.overrideWith((_) async => <ReviewPrompt>[]),
        ],
        child: const MaterialApp(home: RepoDetailScreen(repoName: repoName)),
      ),
    );
    await tester.pumpAndSettle();

    // Regression guard: Organizations field must not appear at repo scope.
    expect(find.text('Organizations'), findsNothing);

    // Sanity: surrounding Issue Tracking fields still render.
    expect(find.text('Review-only labels'), findsOneWidget);
    expect(find.text('Refinement labels'), findsOneWidget);
    expect(find.text('Skip labels'), findsOneWidget);
    expect(find.text('Filter mode'), findsOneWidget);
    expect(find.text('Default action'), findsOneWidget);
    expect(find.text('Assignees'), findsOneWidget);
    expect(find.text('Prompt'), findsWidgets);
  });
}

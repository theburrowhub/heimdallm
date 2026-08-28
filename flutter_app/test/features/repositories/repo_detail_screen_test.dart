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

const _repoName = 'theburrowhub/heimdallm';

Map<String, dynamic> _configJson({
  bool globalMtEnabled = false,
  bool? repoMtEnabled,
  bool monitored = true,
}) => {
  'repositories': [if (monitored) _repoName],
  'non_monitored': [if (!monitored) _repoName],
  'server_port': 1,
  'poll_interval': '60s',
  'retention_days': 30,
  'ai_primary': 'claude',
  'ai_fallback': '',
  'review_mode': 'single',
  'issue_tracking': {'enabled': true},
  'merge_tracking': {
    'enabled': globalMtEnabled,
    if (repoMtEnabled != null)
      'repos': {
        _repoName: {'enabled': repoMtEnabled},
      },
  },
};

Future<MockApiClient> _mountMergeTrackingDetail(
  WidgetTester tester, {
  bool globalMtEnabled = false,
  bool? repoMtEnabled,
  bool monitored = true,
}) async {
  final mockApi = MockApiClient();
  when(() => mockApi.fetchConfig()).thenAnswer(
    (_) async => _configJson(
      globalMtEnabled: globalMtEnabled,
      repoMtEnabled: repoMtEnabled,
      monitored: monitored,
    ),
  );
  when(
    () => mockApi.fetchRepoLabels(_repoName),
  ).thenAnswer((_) async => <String>[]);
  when(
    () => mockApi.fetchRepoCollaborators(_repoName),
  ).thenAnswer((_) async => <String>[]);
  when(() => mockApi.patchMergeTrackingRepoConfig(_repoName, any())).thenAnswer(
    (invocation) async {
      final patch = invocation.positionalArguments[1] as Map<String, dynamic>;
      return _configJson(
        globalMtEnabled: globalMtEnabled,
        repoMtEnabled: patch['enabled'] as bool?,
        monitored: monitored,
      );
    },
  );

  await tester.pumpWidget(
    ProviderScope(
      overrides: [
        apiClientProvider.overrideWithValue(mockApi),
        configNotifierProvider.overrideWith(ConfigNotifier.new),
        agentsProvider.overrideWith((_) async => <ReviewPrompt>[]),
      ],
      child: const MaterialApp(home: RepoDetailScreen(repoName: _repoName)),
    ),
  );
  await tester.pumpAndSettle();
  await tester.scrollUntilVisible(
    find.text('Track my pull requests'),
    300,
    scrollable: find.byType(Scrollable).first,
  );
  await tester.pumpAndSettle();
  return mockApi;
}

void main() {
  setUpAll(() => registerFallbackValue(<String, dynamic>{}));

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

    // Regression guard: the Issue Tracking "Organizations" filter field must
    // not appear at repo scope (it is a global/org-only filter). Target its
    // unique helper text so the guard is not confused by the unrelated
    // "Organizations" review-policy section header on this screen.
    expect(
      find.textContaining('Limit to issues from these orgs'),
      findsNothing,
    );

    // Sanity: surrounding Issue Tracking fields still render.
    expect(find.text('Review-only labels'), findsOneWidget);
    expect(find.text('Refinement labels'), findsOneWidget);
    expect(find.text('Skip labels'), findsOneWidget);
    expect(find.text('Filter mode'), findsOneWidget);
    expect(find.text('Default action'), findsOneWidget);
    expect(find.text('Assignees'), findsOneWidget);
    expect(find.text('Prompt'), findsWidgets);
  });

  testWidgets('merge-tracking switch persists through its scoped endpoint', (
    tester,
  ) async {
    final mockApi = await _mountMergeTrackingDetail(tester);

    await tester.tap(find.byKey(const Key('repo_merge_tracking_switch')));
    await tester.pump(const Duration(milliseconds: 801));
    await tester.pumpAndSettle();

    final patches = verify(
      () => mockApi.patchMergeTrackingRepoConfig(_repoName, captureAny()),
    ).captured;
    expect(patches, [
      <String, dynamic>{'enabled': true},
    ]);
    verifyNever(() => mockApi.patchRepoConfig(any(), any()));
    expect(find.text('Saved'), findsOneWidget);

    await tester.pump(const Duration(seconds: 4));
  });

  testWidgets('resetting merge tracking removes the repo override', (
    tester,
  ) async {
    final mockApi = await _mountMergeTrackingDetail(
      tester,
      repoMtEnabled: true,
    );

    await tester.tap(find.byKey(const Key('repo_merge_tracking_reset')));
    await tester.pump(const Duration(milliseconds: 801));
    await tester.pumpAndSettle();

    final patches = verify(
      () => mockApi.patchMergeTrackingRepoConfig(_repoName, captureAny()),
    ).captured;
    expect(patches, [
      <String, dynamic>{'enabled': null},
    ]);
    expect(find.byKey(const Key('repo_merge_tracking_reset')), findsNothing);
    expect(find.text('Saved'), findsOneWidget);

    await tester.pump(const Duration(seconds: 4));
  });

  testWidgets(
    'a non-monitored repo does not show inherited merge tracking as active',
    (tester) async {
      await _mountMergeTrackingDetail(
        tester,
        globalMtEnabled: true,
        monitored: false,
      );

      final switchFinder = find.descendant(
        of: find.byKey(const Key('repo_merge_tracking_switch')),
        matching: find.byType(Switch),
      );
      expect(tester.widget<Switch>(switchFinder).value, isFalse);
      expect(find.textContaining('not monitored'), findsOneWidget);

      await tester.pump(const Duration(seconds: 4));
    },
  );
}

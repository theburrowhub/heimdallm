import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/agent.dart';
import 'package:heimdallm/core/models/config_model.dart';
import 'package:heimdallm/features/agents/agents_screen.dart';
import 'package:heimdallm/features/config/config_providers.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/features/repositories/repo_detail_screen.dart';
import 'package:heimdallm/shared/design_system/theme.dart';
import 'package:heimdallm/shared/widgets/override_field.dart';
import 'package:mocktail/mocktail.dart';

class MockApiClient extends Mock implements ApiClient {}

class _ErrorConfigNotifier extends ConfigNotifier {
  @override
  Future<AppConfig> build() async => throw Exception('boom');
}

const _repoName = 'theburrowhub/heimdallm';
const _orgName = 'theburrowhub';

Map<String, dynamic> _configJson({
  bool globalMtEnabled = false,
  Map<String, dynamic> globalMergeTracking = const {},
  Map<String, dynamic> repoMergeTracking = const {},
  Map<String, dynamic> orgMergeTracking = const {},
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
  'merge_tracking': {
    'enabled': globalMtEnabled,
    ...globalMergeTracking,
    if (orgMergeTracking.isNotEmpty) 'orgs': {_orgName: orgMergeTracking},
    if (repoMergeTracking.isNotEmpty) 'repos': {_repoName: repoMergeTracking},
  },
};

Future<MockApiClient> _mountMergeTrackingDetail(
  WidgetTester tester, {
  bool globalMtEnabled = false,
  Map<String, dynamic> globalMergeTracking = const {},
  Map<String, dynamic> repoMergeTracking = const {},
  Map<String, dynamic> orgMergeTracking = const {},
  bool monitored = true,
}) async {
  final mockApi = MockApiClient();
  final currentRepoMergeTracking = Map<String, dynamic>.from(repoMergeTracking);
  when(() => mockApi.fetchConfig()).thenAnswer(
    (_) async => _configJson(
      globalMtEnabled: globalMtEnabled,
      globalMergeTracking: globalMergeTracking,
      repoMergeTracking: currentRepoMergeTracking,
      orgMergeTracking: orgMergeTracking,
      monitored: monitored,
    ),
  );
  when(() => mockApi.patchMergeTrackingRepoConfig(_repoName, any())).thenAnswer(
    (invocation) async {
      final patch = invocation.positionalArguments[1] as Map<String, dynamic>;
      for (final entry in patch.entries) {
        if (entry.value == null) {
          currentRepoMergeTracking.remove(entry.key);
        } else {
          currentRepoMergeTracking[entry.key] = entry.value;
        }
      }
      return _configJson(
        globalMtEnabled: globalMtEnabled,
        globalMergeTracking: globalMergeTracking,
        repoMergeTracking: currentRepoMergeTracking,
        orgMergeTracking: orgMergeTracking,
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
      child: MaterialApp(
        theme: HeimdallmTheme.light(),
        builder: (context, navigatorChild) => HeimdallmTheme.scope(
          child: navigatorChild ?? const SizedBox.shrink(),
        ),
        home: const RepoDetailScreen(repoName: _repoName),
      ),
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

  testWidgets('RepoDetailScreen shows a config error state', (tester) async {
    final mockApi = MockApiClient();

    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          apiClientProvider.overrideWithValue(mockApi),
          configNotifierProvider.overrideWith(_ErrorConfigNotifier.new),
          agentsProvider.overrideWith((_) async => <ReviewPrompt>[]),
        ],
        child: MaterialApp(
          theme: HeimdallmTheme.light(),
          builder: (context, navigatorChild) => HeimdallmTheme.scope(
            child: navigatorChild ?? const SizedBox.shrink(),
          ),
          home: const RepoDetailScreen(repoName: _repoName),
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('Could not load config'), findsOneWidget);
  });

  testWidgets('shows the auto-detected local directory helper', (tester) async {
    final mockApi = MockApiClient();
    when(() => mockApi.fetchConfig()).thenAnswer(
      (_) async => {
        ..._configJson(),
        'local_dirs_detected': {_repoName: '/repos/heimdallm'},
      },
    );

    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          apiClientProvider.overrideWithValue(mockApi),
          configNotifierProvider.overrideWith(ConfigNotifier.new),
          agentsProvider.overrideWith((_) async => <ReviewPrompt>[]),
        ],
        child: MaterialApp(
          theme: HeimdallmTheme.light(),
          builder: (context, navigatorChild) => HeimdallmTheme.scope(
            child: navigatorChild ?? const SizedBox.shrink(),
          ),
          home: const RepoDetailScreen(repoName: _repoName),
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(
      find.textContaining('Leave empty to use the auto-detected path above'),
      findsOneWidget,
    );
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

  testWidgets('merge tracking exposes every granular and advanced control', (
    tester,
  ) async {
    await _mountMergeTrackingDetail(tester);

    for (final suffix in [
      'switch',
      'include_assigned',
      'enable_auto_merge',
      'update_branch',
      'resolve_conflicts',
      'merge',
      'require_approval',
      'merge_method',
      'resolve_timeout',
      'resolve_effort',
      'max_update_attempts',
      'max_resolve_attempts',
      'max_merge_attempts',
      'action_cooldown',
    ]) {
      expect(
        find.byKey(Key('repo_merge_tracking_$suffix')),
        findsOneWidget,
        reason: 'missing repo merge-tracking control: $suffix',
      );
    }
    expect(find.text('Advanced limits'), findsOneWidget);

    await tester.pump(const Duration(seconds: 4));
  });

  testWidgets('rapid merge-tracking changes are saved together', (
    tester,
  ) async {
    final mockApi = await _mountMergeTrackingDetail(tester);
    final autoMerge = find.byKey(
      const Key('repo_merge_tracking_enable_auto_merge'),
    );
    final updateBranch = find.byKey(
      const Key('repo_merge_tracking_update_branch'),
    );
    await tester.scrollUntilVisible(
      autoMerge,
      300,
      scrollable: find.byType(Scrollable).first,
    );

    await tester.tap(autoMerge);
    await tester.pump();
    await tester.tap(updateBranch);
    await tester.pump(const Duration(milliseconds: 801));
    await tester.pumpAndSettle();

    final patches = verify(
      () => mockApi.patchMergeTrackingRepoConfig(_repoName, captureAny()),
    ).captured;
    expect(patches, [
      <String, dynamic>{'enable_auto_merge': true, 'update_branch': true},
    ]);
    verifyNever(() => mockApi.patchRepoConfig(any(), any()));

    await tester.pump(const Duration(seconds: 4));
  });

  testWidgets('resetting one merge-tracking field preserves its siblings', (
    tester,
  ) async {
    final mockApi = await _mountMergeTrackingDetail(
      tester,
      repoMergeTracking: const {
        'enable_auto_merge': true,
        'update_branch': true,
      },
    );
    final resetAutoMerge = find.byKey(
      const Key('repo_merge_tracking_enable_auto_merge_reset'),
    );
    await tester.scrollUntilVisible(
      resetAutoMerge,
      300,
      scrollable: find.byType(Scrollable).first,
    );

    await tester.tap(resetAutoMerge);
    await tester.pump(const Duration(milliseconds: 801));
    await tester.pumpAndSettle();

    final patches = verify(
      () => mockApi.patchMergeTrackingRepoConfig(_repoName, captureAny()),
    ).captured;
    expect(patches, [
      <String, dynamic>{'enable_auto_merge': null},
    ]);
    expect(
      find.byKey(const Key('repo_merge_tracking_update_branch_reset')),
      findsOneWidget,
    );
    expect(
      tester
          .widget<Switch>(
            find.byKey(const Key('repo_merge_tracking_update_branch')),
          )
          .value,
      isTrue,
    );

    await tester.pump(const Duration(seconds: 4));
  });

  testWidgets('repo fields can inherit their effective value from the org', (
    tester,
  ) async {
    await _mountMergeTrackingDetail(
      tester,
      globalMergeTracking: const {'enable_auto_merge': false},
      orgMergeTracking: const {'enable_auto_merge': true},
    );

    expect(
      tester
          .widget<Switch>(
            find.byKey(const Key('repo_merge_tracking_enable_auto_merge')),
          )
          .value,
      isTrue,
    );
    expect(find.text('Inherited from org: $_orgName'), findsOneWidget);
    expect(
      find.byKey(const Key('repo_merge_tracking_enable_auto_merge_reset')),
      findsNothing,
    );

    await tester.pump(const Duration(seconds: 4));
  });

  testWidgets('resetting merge tracking removes the repo override', (
    tester,
  ) async {
    final mockApi = await _mountMergeTrackingDetail(
      tester,
      repoMergeTracking: const {'enabled': true},
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

      final switchFinder = find.byKey(const Key('repo_merge_tracking_switch'));
      expect(tester.widget<Switch>(switchFinder).value, isFalse);
      expect(find.text('Inherited from repository monitoring'), findsOneWidget);

      await tester.pump(const Duration(seconds: 4));
    },
  );

  group('clone directory override', () {
    Future<MockApiClient> mount(WidgetTester tester) async {
      final mockApi = MockApiClient();
      final json = {
        ..._configJson(),
        'clone_dir': '/work/global',
        'repo_overrides': {
          _repoName: {'clone_dir': '/work/repo'},
        },
      };
      when(() => mockApi.fetchConfig()).thenAnswer((_) async => json);
      when(
        () => mockApi.patchRepoConfig(_repoName, any()),
      ).thenAnswer((_) async => json);
      when(
        () => mockApi.deleteRepoField(_repoName, any()),
      ).thenAnswer((_) async => _configJson());
      await tester.pumpWidget(
        ProviderScope(
          overrides: [
            apiClientProvider.overrideWithValue(mockApi),
            configNotifierProvider.overrideWith(ConfigNotifier.new),
            agentsProvider.overrideWith((_) async => <ReviewPrompt>[]),
          ],
          child: MaterialApp(
            theme: HeimdallmTheme.light(),
            builder: (context, navigatorChild) => HeimdallmTheme.scope(
              child: navigatorChild ?? const SizedBox.shrink(),
            ),
            home: const RepoDetailScreen(repoName: _repoName),
          ),
        ),
      );
      await tester.pumpAndSettle();
      return mockApi;
    }

    Finder cloneDirField() =>
        find.widgetWithText(OverrideTextField, 'Clone directory');

    testWidgets('an edit is autosaved as clone_dir', (tester) async {
      final mockApi = await mount(tester);

      await tester.enterText(
        find.descendant(
          of: cloneDirField(),
          matching: find.byType(TextFormField),
        ),
        '/work/elsewhere',
      );
      await tester.pump(const Duration(milliseconds: 801));
      await tester.pumpAndSettle();

      final captured = verify(
        () => mockApi.patchRepoConfig(_repoName, captureAny()),
      ).captured;
      expect(captured.single, {'clone_dir': '/work/elsewhere'});
    });

    testWidgets('reset deletes the repo clone_dir override', (tester) async {
      final mockApi = await mount(tester);

      await tester.tap(
        find.descendant(of: cloneDirField(), matching: find.text('\u00d7 reset')),
      );
      await tester.pumpAndSettle();

      verify(() => mockApi.deleteRepoField(_repoName, 'clone_dir')).called(1);
    });
  });
}

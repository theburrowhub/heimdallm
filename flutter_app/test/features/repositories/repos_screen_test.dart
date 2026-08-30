import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/api/sse_client.dart';
import 'package:heimdallm/core/models/config_model.dart';
import 'package:heimdallm/core/platform/platform_services_provider.dart';
import 'package:heimdallm/features/config/config_providers.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/features/repositories/repos_screen.dart';
import 'package:heimdallm/features/repositories/widgets/bulk_actions_bar.dart';
import 'package:heimdallm/features/repositories/widgets/repo_grid_tile.dart';
import 'package:heimdallm/features/repositories/widgets/repo_list_tile.dart';
import 'package:shared_preferences/shared_preferences.dart';
import 'package:mocktail/mocktail.dart';
import '../../core/platform/fake_platform_services.dart';

Widget _host(AppConfig cfg) => ProviderScope(
  overrides: [
    platformServicesProvider.overrideWithValue(FakePlatformServices()),
    configNotifierProvider.overrideWith(() => _FakeConfig(cfg)),
    sseStreamProvider.overrideWith((_) => const Stream<SseEvent>.empty()),
  ],
  child: const MaterialApp(home: Scaffold(body: ReposScreen())),
);

Widget _hostWithConfig(_FakeConfig config, {ApiClient? api}) => ProviderScope(
  overrides: [
    platformServicesProvider.overrideWithValue(FakePlatformServices()),
    configNotifierProvider.overrideWith(() => config),
    sseStreamProvider.overrideWith((_) => const Stream<SseEvent>.empty()),
    if (api != null) apiClientProvider.overrideWithValue(api),
  ],
  child: const MaterialApp(home: Scaffold(body: ReposScreen())),
);

class _FakeConfig extends ConfigNotifier {
  _FakeConfig(this.initial, {this.saveResponse});
  final AppConfig initial;
  final AppConfig? saveResponse;
  final List<AppConfig> saves = [];
  Completer<void>? saveGate;
  @override
  Future<AppConfig> build() async => initial;
  @override
  Future<void> save(AppConfig next) async {
    saves.add(next);
    final gate = saveGate;
    saveGate = null;
    if (gate != null) await gate.future;
    state = AsyncData(saveResponse ?? next);
  }

  void replace(AppConfig next) {
    state = AsyncData(next);
  }
}

class _MockApiClient extends Mock implements ApiClient {}

Finder _bulkPrSwitch() => find
    .descendant(of: find.byType(BulkActionsBar), matching: find.byType(Switch))
    .first;

AppConfig _cfg() => const AppConfig(
  serverPort: 1,
  pollInterval: '60s',
  retentionDays: 30,
  aiPrimary: 'claude',
  aiFallback: '',
  reviewMode: 'single',
  repoConfigs: {
    'a/one': RepoConfig(prEnabled: true),
    'a/two': RepoConfig(prEnabled: true),
  },
  issueTracking: IssueTrackingConfig(),
);

void main() {
  setUpAll(() => registerFallbackValue(<String, dynamic>{}));

  testWidgets('bulk bar appears when a repo is selected', (tester) async {
    await tester.pumpWidget(_host(_cfg()));
    await tester.pumpAndSettle();

    expect(find.byType(BulkActionsBar), findsNothing);

    await tester.tap(find.byKey(const Key('RepoListTile_checkbox')).first);
    await tester.pump();

    expect(find.byType(BulkActionsBar), findsOneWidget);
    expect(find.text('1 selected'), findsOneWidget);
  });

  testWidgets('Clear dismisses the bulk bar', (tester) async {
    await tester.pumpWidget(_host(_cfg()));
    await tester.pumpAndSettle();

    await tester.tap(find.byKey(const Key('RepoListTile_checkbox')).first);
    await tester.pump();
    await tester.tap(find.text('Clear'));
    await tester.pump();

    expect(find.byType(BulkActionsBar), findsNothing);
  });

  testWidgets('selecting Monitored hides non-monitored repos', (tester) async {
    const cfg = AppConfig(
      serverPort: 1,
      pollInterval: '60s',
      retentionDays: 30,
      aiPrimary: 'claude',
      aiFallback: '',
      reviewMode: 'single',
      repoConfigs: {
        'a/one': RepoConfig(prEnabled: true),
        'a/two': RepoConfig(prEnabled: false),
      },
      issueTracking: IssueTrackingConfig(),
    );
    await tester.pumpWidget(_host(cfg));
    await tester.pumpAndSettle();

    expect(find.text('a/one'), findsOneWidget);
    expect(find.text('a/two'), findsOneWidget);

    await tester.tap(find.text('Monitored'));
    await tester.pumpAndSettle();

    expect(find.text('a/one'), findsOneWidget);
    expect(find.text('a/two'), findsNothing);
  });

  testWidgets('view toggle persists choice in SharedPreferences', (
    tester,
  ) async {
    SharedPreferences.setMockInitialValues({});
    await tester.pumpWidget(_host(_cfg()));
    await tester.pumpAndSettle();

    await tester.tap(find.byKey(const Key('repos_view_toggle_grid')));
    await tester.pumpAndSettle();

    final prefs = await SharedPreferences.getInstance();
    expect(prefs.getString('repos_view'), 'grid');
  });

  testWidgets('grid view renders RepoGridTile instead of RepoListTile', (
    tester,
  ) async {
    SharedPreferences.setMockInitialValues({'repos_view': 'grid'});
    await tester.pumpWidget(_host(_cfg()));
    await tester.pumpAndSettle();

    expect(find.byType(RepoGridTile), findsWidgets);
    expect(find.byType(RepoListTile), findsNothing);
  });

  testWidgets(
    'provider refresh moves a mounted repo from not monitored to monitored',
    (tester) async {
      SharedPreferences.setMockInitialValues({'repos_view': 'grid'});
      const initial = AppConfig(
        repoConfigs: {'a/repo': RepoConfig(prEnabled: false)},
      );
      final config = _FakeConfig(initial);

      await tester.pumpWidget(_hostWithConfig(config));
      await tester.pumpAndSettle();

      final screen = tester.element(find.byType(ReposScreen));
      expect(find.text('NOT MONITORED · 1'), findsOneWidget);
      expect(find.text('MONITORED · 1'), findsNothing);

      config.replace(
        initial.copyWith(
          repoConfigs: const {'a/repo': RepoConfig(prEnabled: true)},
        ),
      );
      await tester.pump();

      expect(
        identical(tester.element(find.byType(ReposScreen)), screen),
        isTrue,
      );
      expect(find.text('MONITORED · 1'), findsOneWidget);
      expect(find.text('NOT MONITORED · 1'), findsNothing);
    },
  );

  testWidgets(
    'provider refresh moves a mounted repo from monitored to not monitored',
    (tester) async {
      SharedPreferences.setMockInitialValues({'repos_view': 'grid'});
      const initial = AppConfig(
        repoConfigs: {'a/repo': RepoConfig(prEnabled: true)},
      );
      final config = _FakeConfig(initial);

      await tester.pumpWidget(_hostWithConfig(config));
      await tester.pumpAndSettle();

      final screen = tester.element(find.byType(ReposScreen));
      expect(find.text('MONITORED · 1'), findsOneWidget);
      expect(find.text('NOT MONITORED · 1'), findsNothing);

      config.replace(
        initial.copyWith(
          repoConfigs: const {'a/repo': RepoConfig(prEnabled: false)},
        ),
      );
      await tester.pump();

      expect(
        identical(tester.element(find.byType(ReposScreen)), screen),
        isTrue,
      );
      expect(find.text('NOT MONITORED · 1'), findsOneWidget);
      expect(find.text('MONITORED · 1'), findsNothing);
    },
  );

  testWidgets(
    'provider refresh removes selections for repos no longer present',
    (tester) async {
      SharedPreferences.setMockInitialValues({'repos_view': 'list'});
      const initial = AppConfig(
        repoConfigs: {'a/repo': RepoConfig(prEnabled: true)},
      );
      final config = _FakeConfig(initial);

      await tester.pumpWidget(_hostWithConfig(config));
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('RepoListTile_checkbox')));
      await tester.pump();
      expect(find.text('1 selected'), findsOneWidget);

      config.replace(initial.copyWith(repoConfigs: const {}));
      await tester.pump();

      expect(find.byType(BulkActionsBar), findsNothing);
    },
  );

  testWidgets(
    'provider refresh merges discovery without overwriting a dirty bulk edit',
    (tester) async {
      SharedPreferences.setMockInitialValues({'repos_view': 'list'});
      const initial = AppConfig(
        repoConfigs: {'a/existing': RepoConfig(prEnabled: false)},
      );
      final config = _FakeConfig(initial);

      await tester.pumpWidget(_hostWithConfig(config));
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('RepoListTile_checkbox')));
      await tester.pump();

      // The first bulk switch is PR Review. Toggle it, but refresh the
      // provider before the 400 ms auto-save debounce fires.
      await tester.tap(_bulkPrSwitch());
      await tester.pump();
      config.replace(
        initial.copyWith(
          repoConfigs: const {
            'a/existing': RepoConfig(prEnabled: false),
            'a/discovered': RepoConfig(prEnabled: false),
          },
        ),
      );
      await tester.pump();

      // skipOffstage: false because the assertion is about the merged config,
      // not about what fits on screen: the bulk bar grew a row when merge
      // tracking joined the feature set, and the second tile now starts just
      // below the fold on this surface.
      final existing = tester.widget<RepoListTile>(
        find.widgetWithText(RepoListTile, 'a/existing', skipOffstage: false),
      );
      final discovered = tester.widget<RepoListTile>(
        find.widgetWithText(RepoListTile, 'a/discovered', skipOffstage: false),
      );
      expect(existing.config.prEnabled, isTrue);
      expect(discovered.config.prEnabled, isFalse);

      // Let the pending save and saved-status timer finish cleanly.
      await tester.pump(const Duration(milliseconds: 401));
      await tester.pump();
      await tester.pump(const Duration(seconds: 2));
    },
  );

  testWidgets('a delayed save does not overwrite a newer bulk edit', (
    tester,
  ) async {
    SharedPreferences.setMockInitialValues({'repos_view': 'list'});
    const initial = AppConfig(
      repoConfigs: {'a/existing': RepoConfig(prEnabled: false)},
    );
    final firstSave = Completer<void>();
    final config = _FakeConfig(initial)..saveGate = firstSave;

    await tester.pumpWidget(_hostWithConfig(config));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const Key('RepoListTile_checkbox')));
    await tester.pump();

    await tester.tap(_bulkPrSwitch());
    await tester.pump(const Duration(milliseconds: 401));
    expect(config.saves, hasLength(1));
    expect(config.saves.first.repoConfigs['a/existing']?.prEnabled, isTrue);

    // Make a second edit while the first daemon response is still pending.
    await tester.tap(_bulkPrSwitch());
    await tester.pump();
    firstSave.complete();
    await tester.pump();

    final afterFirstResponse = tester.widget<RepoListTile>(
      find.widgetWithText(RepoListTile, 'a/existing'),
    );
    expect(afterFirstResponse.config.prEnabled, isFalse);

    await tester.pump(const Duration(milliseconds: 401));
    await tester.pump();
    expect(config.saves, hasLength(2));
    expect(config.saves.last.repoConfigs['a/existing']?.prEnabled, isFalse);

    await tester.pump(const Duration(seconds: 2));
  });

  testWidgets(
    'bulk merge tracking keeps the scoped endpoint response as authoritative',
    (tester) async {
      SharedPreferences.setMockInitialValues({'repos_view': 'list'});
      const initial = AppConfig(
        repoConfigs: {
          'a/existing': RepoConfig(prEnabled: false, mtEnabled: false),
        },
      );
      const staleGlobalResponse = AppConfig(
        repoConfigs: {
          'a/existing': RepoConfig(prEnabled: true, mtEnabled: false),
        },
      );
      final config = _FakeConfig(initial, saveResponse: staleGlobalResponse);
      final api = _MockApiClient();
      when(
        () => api.patchMergeTrackingRepoConfig('a/existing', any()),
      ).thenAnswer(
        (_) async => {
          'repositories': ['a/existing'],
          'merge_tracking': {
            'enabled': false,
            'repos': {
              'a/existing': {'enabled': true},
            },
          },
        },
      );

      await tester.pumpWidget(_hostWithConfig(config, api: api));
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const Key('RepoListTile_checkbox')));
      await tester.pump();

      final bulkSwitches = find.descendant(
        of: find.byType(BulkActionsBar),
        matching: find.byType(Switch),
      );
      await tester.tap(bulkSwitches.first); // PR Review: changes membership.
      await tester.tap(bulkSwitches.last); // Merge Tracking: scoped endpoint.
      await tester.pump(const Duration(milliseconds: 401));
      await tester.pumpAndSettle();

      final patches = verify(
        () => api.patchMergeTrackingRepoConfig('a/existing', captureAny()),
      ).captured;
      expect(patches, [
        <String, dynamic>{'enabled': true},
      ]);
      final tile = tester.widget<RepoListTile>(
        find.widgetWithText(RepoListTile, 'a/existing'),
      );
      expect(tile.config.prEnabled, isTrue);
      expect(tile.config.mtEnabled, isTrue);

      await tester.pump(const Duration(seconds: 2));
    },
  );
}

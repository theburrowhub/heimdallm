import 'dart:async';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:mocktail/mocktail.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/config_model.dart';
import 'package:heimdallm/core/platform/platform_services_provider.dart';
import 'package:heimdallm/features/config/config_providers.dart';
import 'package:heimdallm/features/config/config_screen.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import '../core/platform/fake_platform_services.dart';

class MockApiClient extends Mock implements ApiClient {}

void main() {
  setUpAll(() {
    registerFallbackValue(<String, dynamic>{});
  });

  testWidgets('ConfigScreen shows current poll interval', (tester) async {
    const config = AppConfig(
      pollInterval: '5m',
      aiPrimary: 'claude',
      repoConfigs: {'org/repo': RepoConfig(prEnabled: true)},
    );

    final mockApi = MockApiClient();
    when(() => mockApi.fetchConfig()).thenAnswer((_) async => config.toJson());
    when(() => mockApi.updateConfig(any())).thenAnswer((_) async {});
    when(() => mockApi.checkHealth()).thenAnswer((_) async => false);

    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          apiClientProvider.overrideWithValue(mockApi),
          configNotifierProvider.overrideWith(ConfigNotifier.new),
          platformServicesProvider.overrideWithValue(FakePlatformServices()),
        ],
        child: MaterialApp.router(
          routerConfig: GoRouter(
            routes: [
              GoRoute(path: '/', builder: (_, __) => const ConfigScreen()),
            ],
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    // Poll interval is still shown in Settings
    expect(find.text('5m'), findsAtLeastNWidgets(1));
    // Primary agent ('claude') moved to Agents tab — no longer in ConfigScreen
  });

  test('RepoConfig parses first_seen_at when provided', () {
    final json = {
      'repositories': ['a/b'],
      'repo_overrides': {
        'a/b': {'first_seen_at': 1234567890},
      },
      'server_port': 1,
      'poll_interval': '60s',
      'retention_days': 30,
      'ai_primary': 'claude',
      'ai_fallback': '',
      'review_mode': 'single',
      'issue_tracking': {'enabled': false},
    };
    final cfg = AppConfig.fromJson(json);
    expect(
      cfg.repoConfigs['a/b']!.firstSeenAt,
      DateTime.fromMillisecondsSinceEpoch(1234567890 * 1000),
    );
  });

  test('AppConfig parses organization overrides', () {
    final json = {
      'repositories': <String>[],
      'non_monitored': ['acme/api'],
      'server_port': 1,
      'poll_interval': '60s',
      'retention_days': 30,
      'ai_primary': 'claude',
      'ai_fallback': '',
      'review_mode': 'single',
      'triage_owner': 'global-owner',
      'clone_dir': '/work/global',
      'auto_promote_triage': true,
      'auto_promote_refinement': false,
      'generate_pr_description': true,
      'issue_tracking': {'enabled': false},
      'org_overrides': {
        'acme': {
          'primary': 'gemini',
          'issue_prompt': 'org-issue',
          'triage_owner': 'alice',
          'clone_dir': '/work/acme',
          'auto_promote_triage': false,
          'auto_promote_refinement': true,
          'generate_pr_description': false,
          'pr_reviewers': ['alice'],
          'issue_tracking': {
            'develop_labels': ['ready'],
          },
        },
      },
    };

    final cfg = AppConfig.fromJson(json);
    final org = cfg.orgConfigs['acme']!;
    expect(org.aiPrimary, 'gemini');
    expect(org.issuePromptId, 'org-issue');
    expect(org.triageOwner, 'alice');
    expect(org.cloneDir, '/work/acme');
    expect(org.autoPromoteTriage, isFalse);
    expect(org.autoPromoteRefinement, isTrue);
    expect(org.generatePRDescription, isFalse);
    expect(org.prReviewers, ['alice']);
    expect(org.itEnabled, isNull);
    expect(org.devEnabled, isTrue);
    expect(org.developLabels, ['ready']);
    expect(cfg.globalTriageOwner, 'global-owner');
    expect(cfg.globalCloneDir, '/work/global');
    expect(cfg.globalAutoPromoteTriage, isTrue);
    expect(cfg.globalAutoPromoteRefinement, isFalse);
    expect(cfg.globalGeneratePRDescription, isTrue);
  });

  test('AppConfig preserves scoped false and empty-list overrides', () {
    final json = {
      'repositories': <String>[],
      'non_monitored': ['acme/api'],
      'server_port': 1,
      'poll_interval': '60s',
      'retention_days': 30,
      'ai_primary': 'claude',
      'ai_fallback': '',
      'review_mode': 'single',
      'issue_tracking': {
        'enabled': true,
        'review_only_labels': ['global-review'],
      },
      'repo_overrides': {
        'acme/api': {
          'implement_prompt': 'repo-impl',
          'triage_owner': 'repo-owner',
          'clone_dir': '/work/repo',
          'auto_promote_triage': false,
          'auto_promote_refinement': true,
          'generate_pr_description': true,
          'issue_tracking': {
            'enabled': false,
            'review_only_labels': <String>[],
          },
        },
      },
      'org_overrides': {
        'acme': {
          'issue_tracking': {'enabled': false, 'develop_labels': <String>[]},
        },
      },
    };

    final cfg = AppConfig.fromJson(json);
    final repo = cfg.repoConfigs['acme/api']!;
    expect(repo.itEnabled, isFalse);
    expect(repo.reviewOnlyLabels, isEmpty);
    expect(repo.developPromptId, 'repo-impl');
    expect(repo.triageOwner, 'repo-owner');
    expect(repo.cloneDir, '/work/repo');
    expect(repo.autoPromoteTriage, isFalse);
    expect(repo.autoPromoteRefinement, isTrue);
    expect(repo.generatePRDescription, isTrue);
    expect(repo.isMonitored, isFalse);

    final org = cfg.orgConfigs['acme']!;
    expect(org.itEnabled, isFalse);
    expect(org.developLabels, isEmpty);
  });

  test('OrgConfig derives enabled switches from label overrides', () {
    final cfg = AppConfig.fromJson({
      'repositories': <String>[],
      'server_port': 1,
      'poll_interval': '60s',
      'retention_days': 30,
      'ai_primary': 'claude',
      'ai_fallback': '',
      'review_mode': 'single',
      'issue_tracking': {'enabled': false},
      'org_overrides': {
        'acme': {
          'issue_tracking': {
            'review_only_labels': ['needs-triage'],
            'develop_labels': ['ready'],
          },
        },
      },
    });

    final org = cfg.orgConfigs['acme']!;
    expect(org.itEnabled, isTrue);
    expect(org.devEnabled, isTrue);
  });

  testWidgets('saveAndStartDaemon calls platform.spawnDaemon', (tester) async {
    final platform = FakePlatformServices(
      daemonBinaryPath: '/fake/bin/heimdalld',
      githubToken: 'fake-token',
    );
    final container = ProviderContainer(
      overrides: [platformServicesProvider.overrideWithValue(platform)],
    );
    addTearDown(container.dispose);

    // Call saveAndStartDaemon via the notifier. We don't verify daemon health
    // (the fake's ApiClient isn't wired), but we do verify the spawn reached
    // the platform layer at least once before the health-check loop timed out.
    final notifier = container.read(configNotifierProvider.notifier);

    // Run in real-async mode so Future.delayed works without fake-async leaks.
    await tester.runAsync(() async {
      unawaited(
        notifier.saveAndStartDaemon(
          token: 'fake-gh-token',
          config: const AppConfig(),
          daemonBinaryPath: '/fake/bin/heimdalld',
        ),
      );
      // Allow the microtasks that lead to the first spawnDaemon to run.
      await Future.delayed(const Duration(milliseconds: 50));
    });

    expect(platform.spawnedDaemons, contains('/fake/bin/heimdalld'));
  });

  testWidgets(
    'saveAndStartDaemon routes daemon spawn through PlatformServices',
    (tester) async {
      final platform = FakePlatformServices(
        daemonBinaryPath: '/fake/bin/heimdalld',
        githubToken: 'fake-token',
      );
      final container = ProviderContainer(
        overrides: [platformServicesProvider.overrideWithValue(platform)],
      );
      addTearDown(container.dispose);

      // Call saveAndStartDaemon via the notifier. We don't verify daemon health
      // (the fake's ApiClient isn't wired), but we do verify the spawn reached
      // the platform layer at least once before the health-check loop timed out.
      final notifier = container.read(configNotifierProvider.notifier);
      // Kick off the call but ignore its completion — we only care about the
      // side-effect of calling spawnDaemon.
      await tester.runAsync(() async {
        unawaited(
          notifier.saveAndStartDaemon(
            token: 'fake-gh-token',
            config: const AppConfig(),
            daemonBinaryPath: '/fake/bin/heimdalld',
          ),
        );
        // Allow the microtasks that lead to the first spawnDaemon to run.
        await Future.delayed(const Duration(milliseconds: 50));
      });

      expect(platform.spawnedDaemons, contains('/fake/bin/heimdalld'));
    },
  );
}

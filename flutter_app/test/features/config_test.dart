import 'dart:async';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:mocktail/mocktail.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/config_model.dart';
import 'package:heimdallm/core/platform/platform_services_provider.dart';
import 'package:heimdallm/core/setup/first_run_setup.dart';
import 'package:heimdallm/features/config/config_providers.dart'
    show ConfigNotifier, configNotifierProvider, computeGlobalDiffForTest;
import 'package:heimdallm/features/config/config_screen.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/features/repositories/repo_diff.dart';
import '../core/platform/fake_platform_services.dart';

// Expose _computeGlobalDiff for testing via the public save path.
// We drive it indirectly through ConfigNotifier.save and inspect patchConfig calls.

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
              GoRoute(path: '/', builder: (_, _) => const ConfigScreen()),
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

  testWidgets('AI defaults expose the never-approve severity threshold', (
    tester,
  ) async {
    const config = AppConfig(
      pollInterval: '5m',
      aiPrimary: 'claude',
      globalNeverApproveWithIssues: true,
      globalNeverApproveMinSeverity: 'high',
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
              GoRoute(path: '/', builder: (_, _) => const ConfigScreen()),
            ],
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('Never approve — minimum severity'), findsOneWidget);
    // Shows the configured threshold, and is enabled because the toggle is on.
    final dropdown = tester.widget<DropdownButtonFormField<String>>(
      find.byWidgetPredicate(
        (w) => w is DropdownButtonFormField<String> && w.initialValue == 'high',
      ),
    );
    expect(dropdown.onChanged, isNotNull);
  });

  testWidgets('severity threshold is disabled when never-approve is off', (
    tester,
  ) async {
    // The threshold has no effect with the toggle off; a live-looking control
    // that changes nothing would read as a bug.
    const config = AppConfig(
      pollInterval: '5m',
      aiPrimary: 'claude',
      globalNeverApproveWithIssues: false,
      globalNeverApproveMinSeverity: 'medium',
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
              GoRoute(path: '/', builder: (_, _) => const ConfigScreen()),
            ],
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    final dropdown = tester.widget<DropdownButtonFormField<String>>(
      find.byWidgetPredicate(
        (w) =>
            w is DropdownButtonFormField<String> &&
            w.initialValue == 'medium' &&
            w.decoration.labelText == 'Never approve — minimum severity',
      ),
    );
    expect(dropdown.onChanged, isNull);
  });

  testWidgets('Save is gated on a valid poll interval', (tester) async {
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
              GoRoute(path: '/', builder: (_, _) => const ConfigScreen()),
            ],
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    final pollField = find.ancestor(
      of: find.text('Poll interval'),
      matching: find.byType(TextField),
    );
    final saveButton = find.widgetWithText(
      FilledButton,
      'Save and start Heimdallm',
    );

    // Valid default → Save is enabled.
    expect(tester.widget<FilledButton>(saveButton).onPressed, isNotNull);

    // Out-of-range value → Save is disabled (no round-trip to a daemon 400).
    await tester.enterText(pollField, '30s');
    await tester.pumpAndSettle();
    expect(tester.widget<FilledButton>(saveButton).onPressed, isNull);

    // Back to a valid value → Save is re-enabled.
    await tester.enterText(pollField, '90m');
    await tester.pumpAndSettle();
    expect(tester.widget<FilledButton>(saveButton).onPressed, isNotNull);
  });

  group('validatePollInterval', () {
    test('accepts arbitrary durations within [1m, 24h]', () {
      for (final v in ['1m', '5m', '3m', '90m', '1h', '1h30m', '1.5h', '24h']) {
        expect(validatePollInterval(v), isNull, reason: v);
      }
    });

    test('rejects values below the 1m floor', () {
      for (final v in ['59s', '30s', '0s', '1ms']) {
        expect(validatePollInterval(v), 'Must be between 1m and 24h', reason: v);
      }
    });

    test('rejects values above the 24h ceiling', () {
      // Parseable but out of range → the range message, not a parse error.
      for (final v in ['25h', '1441m']) {
        expect(validatePollInterval(v), 'Must be between 1m and 24h', reason: v);
      }
    });

    test('exercises the accumulation path near the upper bound', () {
      // Compound durations must sum correctly: 23h59m is under the ceiling,
      // 24h1m is just over it. Guards against regressions in the micros sum.
      expect(validatePollInterval('23h59m'), isNull);
      expect(validatePollInterval('24h1m'), 'Must be between 1m and 24h');
    });

    test('rejects empty and unparseable input', () {
      // '2d' is rejected here (Go has no 'd' unit → unparseable), not as an
      // out-of-range value.
      for (final v in ['', '  ', 'abc', '5', '5 m', '5minutes', '2d']) {
        expect(validatePollInterval(v), isNotNull, reason: '"$v"');
      }
    });
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
            'refinement_labels': ['needs-plan'],
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
    expect(org.itEnabled, isTrue);
    expect(org.devEnabled, isTrue);
    expect(org.developLabels, ['ready']);
    expect(org.refinementLabels, ['needs-plan']);
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
            'refinement_labels': <String>[],
          },
        },
      },
      'org_overrides': {
        'acme': {
          'issue_tracking': {
            'enabled': false,
            'develop_labels': <String>[],
            'refinement_labels': <String>[],
          },
        },
      },
    };

    final cfg = AppConfig.fromJson(json);
    final repo = cfg.repoConfigs['acme/api']!;
    expect(repo.itEnabled, isFalse);
    expect(repo.reviewOnlyLabels, isEmpty);
    expect(repo.refinementLabels, isEmpty);
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
    expect(org.refinementLabels, isEmpty);
  });

  test('AppConfig parses refinement labels at global, org, and repo scope', () {
    final cfg = AppConfig.fromJson({
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
        'review_only_labels': ['global-triage'],
        'refinement_labels': ['global-refine'],
        'develop_labels': ['global-dev'],
      },
      'org_overrides': {
        'acme': {
          'issue_tracking': {
            'refinement_labels': ['org-refine'],
          },
        },
      },
      'repo_overrides': {
        'acme/api': {
          'issue_tracking': {
            'refinement_labels': ['repo-refine'],
          },
        },
      },
    });

    expect(cfg.issueTracking.refinementLabels, ['global-refine']);
    expect(cfg.issueTracking.toJson()['refinement_labels'], ['global-refine']);
    expect(cfg.orgConfigs['acme']!.refinementLabels, ['org-refine']);
    expect(cfg.orgConfigs['acme']!.itEnabled, isTrue);
    expect(cfg.repoConfigs['acme/api']!.refinementLabels, ['repo-refine']);
    expect(cfg.repoConfigs['acme/api']!.itEnabled, isTrue);
    expect(cfg.repoConfigs['acme/api']!.isMonitored, isTrue);
  });

  test('FirstRunSetup serializes refinement labels in all scopes', () {
    final toml = FirstRunSetup.buildTomlForTesting(
      const AppConfig(
        issueTracking: IssueTrackingConfig(
          enabled: true,
          refinementLabels: ['global-refine'],
        ),
        orgConfigs: {
          'acme': OrgConfig(refinementLabels: ['org-refine']),
        },
        repoConfigs: {
          'acme/api': RepoConfig(refinementLabels: ['repo-refine']),
        },
      ),
    );

    expect(toml, contains('refinement_labels = ["global-refine"]'));
    expect(toml, contains('[ai.orgs."acme".issue_tracking]'));
    expect(toml, contains('refinement_labels = ["org-refine"]'));
    expect(toml, contains('[ai.repos."acme/api".issue_tracking]'));
    expect(toml, contains('refinement_labels = ["repo-refine"]'));
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
            'refinement_labels': ['needs-plan'],
            'develop_labels': ['ready'],
          },
        },
      },
    });

    final org = cfg.orgConfigs['acme']!;
    expect(org.itEnabled, isTrue);
    expect(org.devEnabled, isTrue);
    expect(org.refinementLabels, ['needs-plan']);
  });

  testWidgets('ConfigScreen exposes global refinement labels', (tester) async {
    final mockApi = MockApiClient();
    when(() => mockApi.fetchConfig()).thenAnswer(
      (_) async => const AppConfig(
        issueTracking: IssueTrackingConfig(enabled: true),
      ).toJson(),
    );
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
              GoRoute(path: '/', builder: (_, _) => const ConfigScreen()),
            ],
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('Review-only labels'), findsOneWidget);
    expect(find.text('Refinement labels'), findsOneWidget);
    expect(find.text('Develop labels'), findsNothing);
  });

  test('AppConfig exposes known autocomplete options', () {
    const cfg = AppConfig(
      repoConfigs: {
        'acme/api': RepoConfig(
          issueOrganizations: ['security'],
          issueAssignees: ['repo-assignee'],
          prReviewers: ['repo-reviewer'],
          prAssignee: 'repo-owner',
        ),
      },
      orgConfigs: {
        'platform': OrgConfig(
          issueOrganizations: ['external'],
          issueAssignees: ['org-assignee'],
          prReviewers: ['org-reviewer'],
          prAssignee: 'org-owner',
        ),
      },
      issueTracking: IssueTrackingConfig(
        organizations: ['global-org'],
        assignees: ['global-assignee'],
      ),
      globalPRReviewers: ['global-reviewer'],
      globalPRAssignee: 'global-owner',
    );

    expect(cfg.knownOrganizations, [
      'acme',
      'external',
      'global-org',
      'platform',
      'security',
    ]);
    expect(cfg.knownGitHubUsers, [
      'global-assignee',
      'global-owner',
      'global-reviewer',
      'org-assignee',
      'org-owner',
      'org-reviewer',
      'repo-assignee',
      'repo-owner',
      'repo-reviewer',
    ]);
  });

  // ── PollingConfig tests ────────────────────────────────────────────────────

  test('PollingConfig.fromJson round-trips via toJson', () {
    final json = {
      'adaptive': true,
      'poll_interval': '2m',
      'min_interval': '30s',
      'max_interval': '10m',
      'discovery_interval': '3m',
      'tier3_interval': '1m',
      'rate_limit_safety_threshold': 200,
      'use_etag': false,
      'use_graphql': true,
    };
    final cfg = PollingConfig.fromJson(json);
    expect(cfg.adaptive, isTrue);
    expect(cfg.pollInterval, '2m');
    expect(cfg.minInterval, '30s');
    expect(cfg.maxInterval, '10m');
    expect(cfg.discoveryInterval, '3m');
    expect(cfg.tier3Interval, '1m');
    expect(cfg.rateLimitSafetyThreshold, 200);
    expect(cfg.useEtag, isFalse);
    expect(cfg.useGraphql, isTrue);

    final roundTrip = PollingConfig.fromJson(cfg.toJson());
    expect(roundTrip.adaptive, cfg.adaptive);
    expect(roundTrip.pollInterval, cfg.pollInterval);
    expect(roundTrip.minInterval, cfg.minInterval);
    expect(roundTrip.maxInterval, cfg.maxInterval);
    expect(roundTrip.discoveryInterval, cfg.discoveryInterval);
    expect(roundTrip.tier3Interval, cfg.tier3Interval);
    expect(roundTrip.rateLimitSafetyThreshold, cfg.rateLimitSafetyThreshold);
    expect(roundTrip.useEtag, cfg.useEtag);
    expect(roundTrip.useGraphql, cfg.useGraphql);
  });

  test('PollingConfig.fromJson applies defaults for missing keys', () {
    final cfg = PollingConfig.fromJson({});
    expect(cfg.adaptive, isFalse);
    expect(cfg.pollInterval, '');
    expect(cfg.minInterval, '1m');
    expect(cfg.maxInterval, '15m');
    expect(cfg.discoveryInterval, '5m');
    expect(cfg.tier3Interval, '30s');
    expect(cfg.rateLimitSafetyThreshold, 100);
    expect(cfg.useEtag, isTrue);
    expect(cfg.useGraphql, isFalse);
  });

  test('PollingConfig.fromJson parses rate_limit_safety_threshold as int via num', () {
    // The daemon may send it as a JSON number (double or int)
    final cfg = PollingConfig.fromJson({'rate_limit_safety_threshold': 150});
    expect(cfg.rateLimitSafetyThreshold, 150);
  });

  test('AppConfig.fromJson parses nested polling object', () {
    final json = {
      'repositories': <String>[],
      'server_port': 7842,
      'poll_interval': '5m',
      'retention_days': 90,
      'ai_primary': 'claude',
      'ai_fallback': '',
      'review_mode': 'single',
      'issue_tracking': {'enabled': false},
      'polling': {
        'adaptive': true,
        'poll_interval': '1m',
        'min_interval': '30s',
        'max_interval': '10m',
        'discovery_interval': '4m',
        'tier3_interval': '45s',
        'rate_limit_safety_threshold': 50,
        'use_etag': true,
        'use_graphql': false,
      },
    };
    final cfg = AppConfig.fromJson(json);
    expect(cfg.polling.adaptive, isTrue);
    expect(cfg.polling.pollInterval, '1m');
    expect(cfg.polling.minInterval, '30s');
    expect(cfg.polling.rateLimitSafetyThreshold, 50);
  });

  test('AppConfig.fromJson uses PollingConfig defaults when polling key absent', () {
    final json = {
      'repositories': <String>[],
      'server_port': 7842,
      'poll_interval': '5m',
      'retention_days': 90,
      'ai_primary': 'claude',
      'ai_fallback': '',
      'review_mode': 'single',
      'issue_tracking': {'enabled': false},
    };
    final cfg = AppConfig.fromJson(json);
    expect(cfg.polling.adaptive, isFalse);
    expect(cfg.polling.useEtag, isTrue);
  });

  test('_computeGlobalDiff emits polling diff only for changed fields', () {
    const old = AppConfig();
    // Change two fields: adaptive and use_graphql
    final updated = old.copyWith(
      polling: const PollingConfig(adaptive: true, useGraphql: true),
    );
    final diff = computeGlobalDiffForTest(old, updated);
    expect(diff.containsKey('polling'), isTrue);
    final pd = diff['polling'] as Map<String, dynamic>;
    expect(pd['adaptive'], isTrue);
    expect(pd['use_graphql'], isTrue);
    // Unchanged fields must not appear
    expect(pd.containsKey('use_etag'), isFalse);
    expect(pd.containsKey('min_interval'), isFalse);
    expect(pd.containsKey('poll_interval'), isFalse);
  });

  test('_computeGlobalDiff emits no polling diff when polling unchanged', () {
    const cfg = AppConfig();
    final diff = computeGlobalDiffForTest(cfg, cfg);
    expect(diff.containsKey('polling'), isFalse);
  });

  // ── saveAndStartDaemon tests ───────────────────────────────────────────────

  testWidgets('saveAndStartDaemon calls platform.spawnDaemon', (tester) async {
    final platform = FakePlatformServices(
      daemonBinaryPath: '/fake/bin/heimdalld',
      githubToken: 'fake-token',
    );
    final container = ProviderContainer(
      // Riverpod 3 retries failed providers by default; saveAndStartDaemon's
      // health check is expected to fail in this test, so disable retries to
      // avoid pending timers after the test body returns.
      retry: (_, _) => null,
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
        // Riverpod 3 retries failed providers by default; saveAndStartDaemon's
        // health check is expected to fail in this test, so disable retries to
        // avoid pending timers after the test body returns.
        retry: (_, _) => null,
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

  // ── AutonomousConfig ───────────────────────────────────────────────────────

  test('AutonomousConfig.fromJson round-trip preserves all fields', () {
    final json = {
      'enabled': true,
      'auto_merge': true,
      'merge_method': 'rebase',
      'take_others_tasks': true,
      'reassign_on_take': true,
      'dev_max_turns': 10,
      'dev_effort': 'max',
      'dev_timeout': '30m',
      'claim_lease': '1h',
    };

    final cfg = AutonomousConfig.fromJson(json);
    expect(cfg.enabled, isTrue);
    expect(cfg.autoMerge, isTrue);
    expect(cfg.mergeMethod, 'rebase');
    expect(cfg.takeOthersTasks, isTrue);
    expect(cfg.reassignOnTake, isTrue);
    expect(cfg.devMaxTurns, 10);
    expect(cfg.devEffort, 'max');
    expect(cfg.devTimeout, '30m');
    expect(cfg.claimLease, '1h');

    final roundTrip = AutonomousConfig.fromJson(cfg.toJson());
    expect(roundTrip.enabled, cfg.enabled);
    expect(roundTrip.autoMerge, cfg.autoMerge);
    expect(roundTrip.mergeMethod, cfg.mergeMethod);
    expect(roundTrip.takeOthersTasks, cfg.takeOthersTasks);
    expect(roundTrip.reassignOnTake, cfg.reassignOnTake);
    expect(roundTrip.devMaxTurns, cfg.devMaxTurns);
    expect(roundTrip.devEffort, cfg.devEffort);
    expect(roundTrip.devTimeout, cfg.devTimeout);
    expect(roundTrip.claimLease, cfg.claimLease);
  });

  test('AutonomousConfig.fromJson uses defaults for missing fields', () {
    final cfg = AutonomousConfig.fromJson({});
    expect(cfg.enabled, isFalse);
    expect(cfg.autoMerge, isFalse);
    expect(cfg.mergeMethod, 'squash');
    expect(cfg.takeOthersTasks, isFalse);
    expect(cfg.reassignOnTake, isFalse);
    expect(cfg.devMaxTurns, 0);
    expect(cfg.devEffort, 'high');
    expect(cfg.devTimeout, '45m');
    expect(cfg.claimLease, '2h');
  });

  // ── CircuitBreakerConfig ───────────────────────────────────────────────────

  test('CircuitBreakerConfig.fromJson round-trip preserves all fields', () {
    final json = {
      'per_pr_24h': 5,
      'per_repo_hr': 15,
      'per_issue_24h': 7,
      'per_issue_repo_hr': 8,
      'per_impl_repo_hr': 3,
    };

    final cfg = CircuitBreakerConfig.fromJson(json);
    expect(cfg.perPr24h, 5);
    expect(cfg.perRepoHr, 15);
    expect(cfg.perIssue24h, 7);
    expect(cfg.perIssueRepoHr, 8);
    expect(cfg.perImplRepoHr, 3);

    final roundTrip = CircuitBreakerConfig.fromJson(cfg.toJson());
    expect(roundTrip.perPr24h, cfg.perPr24h);
    expect(roundTrip.perRepoHr, cfg.perRepoHr);
    expect(roundTrip.perIssue24h, cfg.perIssue24h);
    expect(roundTrip.perIssueRepoHr, cfg.perIssueRepoHr);
    expect(roundTrip.perImplRepoHr, cfg.perImplRepoHr);
  });

  test('CircuitBreakerConfig.fromJson uses defaults for missing fields', () {
    final cfg = CircuitBreakerConfig.fromJson({});
    expect(cfg.perPr24h, 3);
    expect(cfg.perRepoHr, 20);
    expect(cfg.perIssue24h, 3);
    expect(cfg.perIssueRepoHr, 10);
    expect(cfg.perImplRepoHr, 5);
  });

  // ── AppConfig parses autonomous / circuit_breaker ──────────────────────────

  test('AppConfig.fromJson parses autonomous and circuit_breaker blocks', () {
    final json = {
      'repositories': <String>[],
      'server_port': 7842,
      'poll_interval': '5m',
      'retention_days': 90,
      'ai_primary': 'claude',
      'ai_fallback': '',
      'review_mode': 'single',
      'issue_tracking': {'enabled': false},
      'autonomous': {
        'enabled': true,
        'merge_method': 'merge',
        'dev_effort': 'low',
      },
      'circuit_breaker': {
        'per_pr_24h': 10,
        'per_repo_hr': 50,
      },
    };

    final cfg = AppConfig.fromJson(json);
    expect(cfg.autonomous.enabled, isTrue);
    expect(cfg.autonomous.mergeMethod, 'merge');
    expect(cfg.autonomous.devEffort, 'low');
    expect(cfg.circuitBreaker.perPr24h, 10);
    expect(cfg.circuitBreaker.perRepoHr, 50);
    // Unspecified fields get defaults
    expect(cfg.circuitBreaker.perIssue24h, 3);
  });

  test('AppConfig.fromJson uses defaults when autonomous/circuit_breaker absent', () {
    final json = {
      'repositories': <String>[],
      'server_port': 7842,
      'poll_interval': '5m',
      'retention_days': 90,
      'ai_primary': 'claude',
      'ai_fallback': '',
      'review_mode': 'single',
      'issue_tracking': {'enabled': false},
    };

    final cfg = AppConfig.fromJson(json);
    expect(cfg.autonomous.enabled, isFalse);
    expect(cfg.autonomous.mergeMethod, 'squash');
    expect(cfg.circuitBreaker.perPr24h, 3);
  });

  // ── _computeGlobalDiff via ConfigNotifier.save ────────────────────────────

  test('_computeGlobalDiff emits autonomous diff when enabled changes', () async {
    final mockApi = MockApiClient();
    Map<String, dynamic>? capturedPatch;

    const initialConfig = AppConfig();
    when(() => mockApi.fetchConfig()).thenAnswer((_) async => initialConfig.toJson());
    when(() => mockApi.patchConfig(any())).thenAnswer((invocation) async {
      capturedPatch = invocation.positionalArguments[0] as Map<String, dynamic>;
      return initialConfig.copyWith(
        autonomous: const AutonomousConfig(enabled: true),
      ).toJson();
    });

    final container = ProviderContainer(
      overrides: [apiClientProvider.overrideWithValue(mockApi)],
    );
    addTearDown(container.dispose);

    // Wait for the provider to load
    await container.read(configNotifierProvider.future);

    final updated = initialConfig.copyWith(
      autonomous: const AutonomousConfig(enabled: true),
    );
    await container.read(configNotifierProvider.notifier).save(updated);

    expect(capturedPatch, isNotNull);
    expect(capturedPatch!['autonomous'], isNotNull);
    expect(capturedPatch!['autonomous']['enabled'], isTrue);
  });

  test('_computeGlobalDiff emits circuit_breaker diff when a field changes', () async {
    final mockApi = MockApiClient();
    Map<String, dynamic>? capturedPatch;

    const initialConfig = AppConfig();
    when(() => mockApi.fetchConfig()).thenAnswer((_) async => initialConfig.toJson());
    when(() => mockApi.patchConfig(any())).thenAnswer((invocation) async {
      capturedPatch = invocation.positionalArguments[0] as Map<String, dynamic>;
      return initialConfig.copyWith(
        circuitBreaker: const CircuitBreakerConfig(perPr24h: 10),
      ).toJson();
    });

    final container = ProviderContainer(
      overrides: [apiClientProvider.overrideWithValue(mockApi)],
    );
    addTearDown(container.dispose);

    await container.read(configNotifierProvider.future);

    final updated = initialConfig.copyWith(
      circuitBreaker: const CircuitBreakerConfig(perPr24h: 10),
    );
    await container.read(configNotifierProvider.notifier).save(updated);

    expect(capturedPatch, isNotNull);
    expect(capturedPatch!['circuit_breaker'], isNotNull);
    expect(capturedPatch!['circuit_breaker']['per_pr_24h'], 10);
  });

  test('never_approve_with_issues round-trips (global + repo override)', () {
    final json = {
      'never_approve_with_issues': true,
      'repositories': <String>[],
      'repo_overrides': {
        'org/repo1': {'never_approve_with_issues': false},
      },
    };
    final cfg = AppConfig.fromJson(json);
    expect(cfg.globalNeverApproveWithIssues, isTrue);
    expect(cfg.repoConfigs['org/repo1']!.neverApproveWithIssues, isFalse);

    // Global survives toJson round-trip.
    expect(cfg.toJson()['never_approve_with_issues'], isTrue);

    // copyWith sentinel: repo override can be cleared to inherit (null).
    final cleared = cfg.repoConfigs['org/repo1']!.copyWith(
      neverApproveWithIssues: null,
    );
    expect(cleared.neverApproveWithIssues, isNull);
  });

  test('OrgConfig.hasOverride true when only never_approve_with_issues set', () {
    const org = OrgConfig(neverApproveWithIssues: true);
    expect(org.hasOverride, isTrue);
  });

  test('repo diff includes never_approve_with_issues when set', () {
    const oldCfg = RepoConfig();
    final updated = oldCfg.copyWith(neverApproveWithIssues: true);
    final diff = computeRepoDiff(oldCfg, updated);
    expect(diff['never_approve_with_issues'], isTrue);
  });

  test('never_approve_min_severity round-trips (global + org + repo)', () {
    final json = {
      'never_approve_min_severity': 'high',
      'repositories': <String>[],
      'repo_overrides': {
        'org/repo1': {'never_approve_min_severity': 'low'},
      },
      'org_overrides': {
        'org': {'never_approve_min_severity': 'medium'},
      },
    };
    final cfg = AppConfig.fromJson(json);
    expect(cfg.globalNeverApproveMinSeverity, 'high');
    expect(cfg.repoConfigs['org/repo1']!.neverApproveMinSeverity, 'low');
    expect(cfg.orgConfigs['org']!.neverApproveMinSeverity, 'medium');
    expect(cfg.toJson()['never_approve_min_severity'], 'high');
  });

  test('unset never_approve_min_severity surfaces the daemon default', () {
    // The daemon serves "" when the operator never set the key; the dropdown
    // must show the threshold actually in effect, not a blank option.
    final cfg = AppConfig.fromJson({'repositories': <String>[]});
    expect(cfg.globalNeverApproveMinSeverity, defaultNeverApproveMinSeverity);
    expect(defaultNeverApproveMinSeverity, 'medium');

    final empty = AppConfig.fromJson({
      'never_approve_min_severity': '',
      'repositories': <String>[],
    });
    expect(empty.globalNeverApproveMinSeverity, 'medium');
  });

  test('empty scoped never_approve_min_severity stays null (inherit)', () {
    final cfg = AppConfig.fromJson({
      'repositories': <String>[],
      'repo_overrides': {
        'org/repo1': {'never_approve_min_severity': ''},
      },
      'org_overrides': {
        'org': {'never_approve_min_severity': ''},
      },
    });
    expect(cfg.repoConfigs['org/repo1']!.neverApproveMinSeverity, isNull);
    expect(cfg.orgConfigs['org']!.neverApproveMinSeverity, isNull);
  });

  test('never_approve_min_severity marks scoped overrides as present', () {
    const org = OrgConfig(neverApproveMinSeverity: 'high');
    expect(org.hasOverride, isTrue);
    const repo = RepoConfig(neverApproveMinSeverity: 'high');
    expect(repo.hasAiOverride, isTrue);
  });

  test('repo diff carries never_approve_min_severity, clearing with ""', () {
    const oldCfg = RepoConfig();
    final set = oldCfg.copyWith(neverApproveMinSeverity: 'high');
    expect(computeRepoDiff(oldCfg, set)['never_approve_min_severity'], 'high');

    // String overrides emit '' to clear (same contract as review_mode).
    final cleared = set.copyWith(neverApproveMinSeverity: null);
    expect(computeRepoDiff(set, cleared)['never_approve_min_severity'], '');
  });

  test('repo diff includes Pipeline overrides when set', () {
    const oldCfg = RepoConfig();
    final updated = oldCfg.copyWith(
      triageOwner: 'alice',
      cloneDir: '/work/x',
      autoPromoteTriage: true,
      autoPromoteRefinement: false,
      generatePRDescription: true,
    );
    final diff = computeRepoDiff(oldCfg, updated);
    expect(diff['triage_owner'], 'alice');
    expect(diff['clone_dir'], '/work/x');
    expect(diff['auto_promote_triage'], isTrue);
    expect(diff['auto_promote_refinement'], isFalse);
    expect(diff['generate_pr_description'], isTrue);
  });

  test('repo diff includes issue_tracking organizations when set', () {
    const oldCfg = RepoConfig();
    final updated = oldCfg.copyWith(issueOrganizations: ['acme']);
    final diff = computeRepoDiff(oldCfg, updated);
    expect(
      (diff['issue_tracking'] as Map)['organizations'],
      ['acme'],
    );
  });
}

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:mocktail/mocktail.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/config_model.dart';
import 'package:heimdallm/core/models/review_limit_status.dart';
import 'package:heimdallm/features/config/config_providers.dart'
    show computeGlobalDiffForTest;
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/features/repositories/repo_diff.dart';
import 'package:heimdallm/features/review_limits/review_limits_usage.dart';
import 'package:heimdallm/shared/design_system/theme.dart';
import 'package:heimdallm/shared/widgets/review_limits_editor.dart';

class _MockApiClient extends Mock implements ApiClient {}

Widget _hosted(Widget child, {ApiClient? api}) {
  return ProviderScope(
    overrides: [if (api != null) apiClientProvider.overrideWithValue(api)],
    child: MaterialApp(
      theme: HeimdallmTheme.light(),
      home: HeimdallmTheme.scope(child: Scaffold(body: child)),
    ),
  );
}

void main() {
  group('ReviewLimits model', () {
    test('parses and serialises every window', () {
      final l = ReviewLimits.fromJson({
        'per_minute': 2,
        'per_hour': 30,
        'per_day': 200,
      });
      expect(l, const ReviewLimits(perMinute: 2, perHour: 30, perDay: 200));
      expect(l.toJson(), {'per_minute': 2, 'per_hour': 30, 'per_day': 200});
      expect(l.isEmpty, isFalse);
      expect(const ReviewLimits().isEmpty, isTrue);
      expect(ReviewLimits.maybeFromJson(null), isNull);
      expect(ReviewLimits.maybeFromJson('x'), isNull);
      expect(
        l.hashCode,
        const ReviewLimits(perMinute: 2, perHour: 30, perDay: 200).hashCode,
      );
    });

    test('AppConfig.fromJson reads global, org, repo and agent budgets', () {
      final cfg = AppConfig.fromJson({
        'repositories': ['acme/api'],
        'review_limits': {'per_minute': 1, 'per_hour': 0, 'per_day': 50},
        'repo_overrides': {
          'acme/api': {
            'review_limits': {'per_minute': 0, 'per_hour': 0, 'per_day': 5},
          },
        },
        'org_overrides': {
          'acme': {
            'review_limits': {'per_minute': 0, 'per_hour': 10, 'per_day': 0},
          },
        },
        'agent_configs': {
          'claude': {
            'review_limits': {'per_minute': 0, 'per_hour': 3, 'per_day': 0},
          },
          'codex': {'model': 'gpt-5.5'},
        },
      });
      expect(cfg.reviewLimits, const ReviewLimits(perMinute: 1, perDay: 50));
      expect(
        cfg.repoConfigs['acme/api']!.reviewLimits,
        const ReviewLimits(perDay: 5),
      );
      expect(
        cfg.orgConfigs['acme']!.reviewLimits,
        const ReviewLimits(perHour: 10),
      );
      expect(cfg.orgConfigs['acme']!.hasOverride, isTrue);
      expect(
        cfg.agentConfigs['claude']!.reviewLimits,
        const ReviewLimits(perHour: 3),
      );
      expect(cfg.agentConfigs['claude']!.hasConfig, isTrue);
      expect(cfg.agentConfigs['codex']!.reviewLimits.isEmpty, isTrue);
    });

    test('missing review_limits means no limits', () {
      final cfg = AppConfig.fromJson({});
      expect(cfg.reviewLimits.isEmpty, isTrue);
    });

    test('copyWith carries review limits forward', () {
      const repo = RepoConfig(reviewLimits: ReviewLimits(perDay: 3));
      expect(
        repo.copyWith(localDir: '/x').reviewLimits,
        const ReviewLimits(perDay: 3),
      );
      expect(repo.copyWith(reviewLimits: null).reviewLimits, isNull);
      const org = OrgConfig(reviewLimits: ReviewLimits(perHour: 4));
      expect(
        org.copyWith(cloneDir: '/y').reviewLimits,
        const ReviewLimits(perHour: 4),
      );
      const agent = CLIAgentConfig();
      expect(
        agent
            .copyWith(reviewLimits: const ReviewLimits(perMinute: 1))
            .reviewLimits,
        const ReviewLimits(perMinute: 1),
      );
    });
  });

  group('diffs', () {
    test('global diff sends review_limits only when it changed', () {
      const base = AppConfig();
      expect(
        computeGlobalDiffForTest(base, base).containsKey('review_limits'),
        isFalse,
      );
      final diff = computeGlobalDiffForTest(
        base,
        base.copyWith(reviewLimits: const ReviewLimits(perHour: 12)),
      );
      expect(diff['review_limits'], {
        'per_minute': 0,
        'per_hour': 12,
        'per_day': 0,
      });
    });

    test('agent diff sends the agent budget', () {
      const base = AppConfig(agentConfigs: {'claude': CLIAgentConfig()});
      final diff = computeGlobalDiffForTest(
        base,
        base.copyWith(
          agentConfigs: {
            'claude': const CLIAgentConfig(
              reviewLimits: ReviewLimits(perDay: 7),
            ),
          },
        ),
      );
      expect(diff['ai']['agents']['claude']['review_limits'], {
        'per_minute': 0,
        'per_hour': 0,
        'per_day': 7,
      });
    });

    test('repo diff patches a present budget and leaves removal to DELETE', () {
      const none = RepoConfig();
      const some = RepoConfig(reviewLimits: ReviewLimits(perMinute: 2));
      expect(computeRepoDiff(none, some)['review_limits'], {
        'per_minute': 2,
        'per_hour': 0,
        'per_day': 0,
      });
      expect(computeRepoDiff(some, none).containsKey('review_limits'), isFalse);
    });
  });

  group('ReviewLimitStatus', () {
    test('parses usage and treats Go zero time as unset', () {
      final s = ReviewLimitStatus.fromJson({
        'kind': 'repo',
        'key': 'acme/api',
        'windows': [
          {
            'window': 'hour',
            'used': 3,
            'limit': 3,
            'reset_at': '2026-10-08T12:00:00Z',
          },
          {
            'window': 'day',
            'used': 0,
            'limit': 9,
            'reset_at': '0001-01-01T00:00:00Z',
          },
        ],
      });
      expect(s.label, 'acme/api');
      expect(s.exhausted, isTrue);
      expect(s.windows.first.resetAt, DateTime.utc(2026, 10, 8, 12));
      expect(s.windows.last.resetAt, isNull);
      expect(s.windows.last.exhausted, isFalse);
      expect(
        const ReviewLimitStatus(kind: 'global', key: '', windows: []).label,
        'All reviews',
      );
      expect(
        const ReviewLimitStatus(kind: 'org', key: 'acme', windows: []).label,
        'Org acme',
      );
      expect(
        const ReviewLimitStatus(
          kind: 'agent',
          key: 'claude',
          windows: [],
        ).label,
        'Agent claude',
      );
    });
  });

  group('ReviewLimitsFields', () {
    testWidgets('emits each window and clamps to the daemon maximum', (
      tester,
    ) async {
      ReviewLimits? last;
      await tester.pumpWidget(
        _hosted(
          ReviewLimitsFields(
            keyPrefix: 'global',
            value: const ReviewLimits(perHour: 5),
            onChanged: (v) => last = v,
          ),
        ),
      );
      expect(find.text('5'), findsOneWidget);
      await tester.enterText(
        find.byKey(const ValueKey('global-review-limit-minute')),
        '3',
      );
      expect(last, const ReviewLimits(perMinute: 3, perHour: 5));
      await tester.enterText(
        find.byKey(const ValueKey('global-review-limit-day')),
        '999999',
      );
      expect(last!.perDay, ReviewLimits.maxValue);
      await tester.enterText(
        find.byKey(const ValueKey('global-review-limit-hour')),
        '',
      );
      expect(last!.perHour, 0);
    });
  });

  group('ReviewLimitsOverrideEditor', () {
    testWidgets('adds a budget and removes it', (tester) async {
      ReviewLimits? changed;
      var removed = false;
      Widget build(ReviewLimits? value) => _hosted(
        ReviewLimitsOverrideEditor(
          keyPrefix: 'org',
          scopeLabel: 'organization',
          value: value,
          onChanged: (v) => changed = v,
          onRemove: () => removed = true,
        ),
      );

      await tester.pumpWidget(build(null));
      expect(find.textContaining('No organization limit'), findsOneWidget);
      await tester.tap(find.byKey(const ValueKey('org-review-limits-add')));
      expect(changed, const ReviewLimits(perHour: 10));

      await tester.pumpWidget(build(const ReviewLimits(perHour: 10)));
      expect(
        find.byKey(const ValueKey('org-review-limit-hour')),
        findsOneWidget,
      );
      await tester.tap(find.byKey(const ValueKey('org-review-limits-remove')));
      expect(removed, isTrue);
    });
  });

  group('ReviewLimitsUsageCard', () {
    testWidgets('shows each budget and flags a full window', (tester) async {
      final api = _MockApiClient();
      when(() => api.fetchReviewLimits()).thenAnswer(
        (_) async => [
          ReviewLimitStatus(
            kind: 'global',
            key: '',
            windows: [
              ReviewLimitWindowUsage(
                window: 'minute',
                used: 1,
                limit: 1,
                resetAt: DateTime.now().add(const Duration(seconds: 40)),
              ),
            ],
          ),
          const ReviewLimitStatus(
            kind: 'agent',
            key: 'codex',
            windows: [
              ReviewLimitWindowUsage(window: 'day', used: 2, limit: 10),
            ],
          ),
        ],
      );
      await tester.pumpWidget(_hosted(const ReviewLimitsUsageCard(), api: api));
      await tester.pump();
      expect(find.text('Review limits'), findsOneWidget);
      expect(find.text('All reviews'), findsOneWidget);
      expect(find.text('1 / 1'), findsOneWidget);
      expect(find.textContaining('slot in'), findsOneWidget);
      expect(find.text('Agent codex'), findsOneWidget);
      expect(find.text('2 / 10'), findsOneWidget);
      await tester.tap(find.byTooltip('Refresh'));
      await tester.pump();
      verify(() => api.fetchReviewLimits()).called(greaterThanOrEqualTo(2));
      await tester.pumpWidget(const SizedBox.shrink());
    });

    testWidgets('renders nothing without budgets or on an old daemon', (
      tester,
    ) async {
      final api = _MockApiClient();
      when(() => api.fetchReviewLimits()).thenAnswer((_) async => const []);
      await tester.pumpWidget(_hosted(const ReviewLimitsUsageCard(), api: api));
      await tester.pump();
      expect(find.text('Review limits'), findsNothing);

      when(() => api.fetchReviewLimits()).thenThrow(ApiException('404'));
      await tester.pumpWidget(
        _hosted(const ReviewLimitsUsageCard(kinds: {'repo'}), api: api),
      );
      await tester.pump();
      expect(find.text('Review limits'), findsNothing);
      await tester.pumpWidget(const SizedBox.shrink());
    });
  });

  test('slot labels and unknown scope kinds', () {
    final now = DateTime.utc(2026, 10, 8, 12);
    expect(reviewLimitSlotLabel(null, now), '');
    expect(reviewLimitSlotLabel(now.subtract(const Duration(seconds: 1)), now), 'slot free now');
    expect(reviewLimitSlotLabel(now.add(const Duration(seconds: 30)), now), 'slot in 30s');
    expect(reviewLimitSlotLabel(now.add(const Duration(minutes: 12)), now), 'slot in 12m');
    expect(reviewLimitSlotLabel(now.add(const Duration(hours: 5)), now), 'slot in 5h');
    expect(const ReviewLimitStatus(kind: 'future', key: '', windows: []).label, 'future');
    expect(const ReviewLimitStatus(kind: 'future', key: 'x', windows: []).label, 'future x');
  });
}

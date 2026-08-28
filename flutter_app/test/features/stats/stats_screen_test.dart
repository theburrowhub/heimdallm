import 'dart:async';

import 'package:clock/clock.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/pr.dart';
import 'package:heimdallm/core/models/tracked_issue.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/features/issues/issues_providers.dart';
import 'package:heimdallm/features/stats/stats_screen.dart';

typedef _MapLoader = Future<Map<String, dynamic>> Function();

Widget _host({
  required _MapLoader loadStats,
  required _MapLoader loadRateLimits,
  List<PR> prs = const [],
  List<TrackedIssue> issues = const [],
}) => ProviderScope(
  overrides: [
    statsProvider.overrideWith((ref) => loadStats()),
    githubRateLimitProvider.overrideWith((ref) => loadRateLimits()),
    prsProvider.overrideWith((ref) async => prs),
    issuesProvider.overrideWith((ref) async => issues),
  ],
  child: const MaterialApp(home: Scaffold(body: StatsScreen())),
);

PR _pr({required int id, required String repo}) => PR(
  id: id,
  githubId: 1000 + id,
  repo: repo,
  number: id,
  title: 'PR $id',
  author: 'octocat',
  url: 'https://example.test/pr/$id',
  state: 'open',
  updatedAt: DateTime.utc(2026, 8, 1),
);

TrackedIssue _issue({required int id, required String repo}) => TrackedIssue(
  id: id,
  githubId: 2000 + id,
  repo: repo,
  number: id,
  title: 'Issue $id',
  body: 'Body',
  author: 'octocat',
  assignees: const [],
  labels: const [],
  state: 'open',
  createdAt: DateTime.utc(2026, 8, 1),
  fetchedAt: DateTime.utc(2026, 8, 1),
);

Finder _filterChip(String label) => find.widgetWithText(Chip, label);

Finder _option(String label) => find.widgetWithText(CheckboxListTile, label);

Finder _refreshButton() => find.byWidgetPredicate(
  (widget) => widget is IconButton && widget.tooltip == 'Refresh',
);

void _expectStatCard(String label, String value) {
  final card = find.ancestor(of: find.text(label), matching: find.byType(Card));
  expect(card, findsOneWidget);
  expect(find.descendant(of: card, matching: find.text(value)), findsOneWidget);
}

Future<void> _useWideViewport(WidgetTester tester) async {
  await tester.binding.setSurfaceSize(const Size(1400, 1000));
  addTearDown(() => tester.binding.setSurfaceSize(null));
}

Map<String, dynamic> _fullStats() => <String, dynamic>{
  'total_reviews': 17,
  'avg_issues_per_review': 1.75,
  'by_severity': <String, dynamic>{
    'high': 3,
    'medium': 4,
    'low': 9,
    'unknown': 1,
  },
  'by_cli': <String, dynamic>{'claude': 8, 'gemini': 4, 'codex': 3, 'other': 2},
  'top_repos': <Map<String, dynamic>>[
    {'repo': 'acme/api', 'count': 8},
    {'repo': 'globex/web', 'count': 4},
  ],
  'reviews_last_7_days': <Map<String, dynamic>>[
    {'day': '2026-08-01', 'count': 2},
    {'day': '2026-08-02', 'count': 5},
  ],
  'review_timing': <String, dynamic>{
    'sample_count': 17,
    'avg_seconds': 65,
    'median_seconds': 60,
    'min_seconds': 12,
    'max_seconds': 367,
    'bucket_fast': 4,
    'bucket_medium': 6,
    'bucket_slow': 5,
    'bucket_very_slow': 2,
  },
};

Map<String, dynamic> _rateLimits({required int load}) {
  final now = DateTime.now().millisecondsSinceEpoch ~/ 1000;
  if (load == 1) {
    return <String, dynamic>{
      'core': <String, dynamic>{
        'limit': 5000,
        'remaining': 4500,
        'reset': now - 60,
      },
      'search': <String, dynamic>{
        'limit': 30,
        'remaining': 2,
        'reset': now + 45,
      },
      'graphql': <String, dynamic>{
        'limit': 0,
        'remaining': 0,
        'reset': now + 3 * 60 * 60,
      },
    };
  }
  return <String, dynamic>{
    'core': <String, dynamic>{
      'limit': 5000,
      'remaining': 4400,
      'reset': now + 15 * 60,
    },
    'search': <String, dynamic>{'limit': 30, 'remaining': 1, 'reset': now - 60},
    'graphql': <String, dynamic>{
      'limit': 5000,
      'remaining': 3000,
      'reset': now + 3 * 60 * 60,
    },
  };
}

void main() {
  testWidgets('stats can remain loading while rate limits fail', (
    tester,
  ) async {
    final pendingStats = Completer<Map<String, dynamic>>();

    await tester.pumpWidget(
      _host(
        loadStats: () => pendingStats.future,
        loadRateLimits: () async => throw StateError('rate limit unavailable'),
      ),
    );
    await tester.pump();
    await tester.pump();

    expect(find.byType(CircularProgressIndicator), findsOneWidget);
    expect(find.textContaining('Could not load rate limits:'), findsOneWidget);
    expect(find.textContaining('rate limit unavailable'), findsOneWidget);
  });

  testWidgets('rate limits can remain loading while stats fail', (
    tester,
  ) async {
    final pendingRateLimits = Completer<Map<String, dynamic>>();

    await tester.pumpWidget(
      _host(
        loadStats: () async => throw StateError('stats unavailable'),
        loadRateLimits: () => pendingRateLimits.future,
      ),
    );
    await tester.pump();
    await tester.pump();

    expect(find.textContaining('Error loading stats:'), findsOneWidget);
    expect(find.textContaining('stats unavailable'), findsOneWidget);
    expect(find.byType(CircularProgressIndicator), findsOneWidget);
  });

  testWidgets(
    'partial payload renders safe defaults and omits empty sections',
    (tester) async {
      await _useWideViewport(tester);

      await tester.pumpWidget(
        _host(
          loadStats: () async => <String, dynamic>{
            'review_timing': <String, dynamic>{'sample_count': 1},
          },
          loadRateLimits: () async => <String, dynamic>{
            'core': <String, dynamic>{'limit': 0, 'remaining': 0, 'reset': 0},
            'search': 'unavailable',
          },
        ),
      );
      await tester.pumpAndSettle();

      _expectStatCard('Total Reviews', '0');
      _expectStatCard('Avg Issues / Review', '0.0');
      _expectStatCard('High Severity', '0');
      _expectStatCard('Low Severity', '0');
      expect(find.text('Review Duration'), findsOneWidget);
      expect(find.text('0s'), findsNWidgets(4));
      expect(find.text('Distribution (1 reviews)'), findsOneWidget);
      expect(find.text('By Severity'), findsNothing);
      expect(find.text('By AI Agent'), findsNothing);
      expect(find.text('Reviews Last 7 Days'), findsNothing);
      expect(find.text('Top Repos by Reviews'), findsNothing);
      expect(find.text('Core (REST)'), findsOneWidget);
      expect(find.text('0 / 0'), findsOneWidget);
      expect(find.text('Search'), findsNothing);
      expect(find.text('GraphQL'), findsNothing);
    },
  );

  testWidgets('rate limits count down and refresh automatically', (
    tester,
  ) async {
    await _useWideViewport(tester);
    var rateLimitLoads = 0;

    await tester.pumpWidget(
      _host(
        loadStats: () async => <String, dynamic>{},
        loadRateLimits: () async {
          rateLimitLoads++;
          final reset = clock.now().millisecondsSinceEpoch ~/ 1000 + 91;
          return <String, dynamic>{
            'core': <String, dynamic>{
              'limit': 5000,
              'remaining': 5000 - rateLimitLoads,
              'reset': reset,
            },
          };
        },
      ),
    );
    await tester.pumpAndSettle();

    expect(rateLimitLoads, 1);
    expect(find.text('4999 / 5000'), findsOneWidget);
    expect(find.text('resets 1m'), findsOneWidget);

    await tester.pump(const Duration(seconds: 31));
    final resetLabels = tester
        .widgetList<Text>(find.byType(Text))
        .map((widget) => widget.data)
        .whereType<String>()
        .where((text) => text.startsWith('resets '))
        .toList();
    expect(resetLabels, hasLength(1));
    expect(resetLabels.single, matches(RegExp(r'^resets \d+s$')));
    expect(rateLimitLoads, 1);

    await tester.pump(const Duration(seconds: 29));
    await tester.pump();
    expect(rateLimitLoads, 2);
    expect(find.text('4998 / 5000'), findsOneWidget);
  });

  testWidgets(
    'full payload renders stats, repo sources, limits, and refreshes',
    (tester) async {
      await _useWideViewport(tester);
      var rateLimitLoads = 0;

      await tester.pumpWidget(
        _host(
          loadStats: () async => _fullStats(),
          loadRateLimits: () async {
            rateLimitLoads++;
            return _rateLimits(load: rateLimitLoads);
          },
          prs: [
            _pr(id: 1, repo: 'acme/api'),
            _pr(id: 2, repo: ''),
          ],
          issues: [_issue(id: 3, repo: 'globex/web')],
        ),
      );
      await tester.pumpAndSettle();

      _expectStatCard('Total Reviews', '17');
      _expectStatCard('Avg Issues / Review', '1.8');
      _expectStatCard('High Severity', '3');
      _expectStatCard('Low Severity', '9');
      expect(find.text('Review Duration'), findsOneWidget);
      expect(find.text('1m 5s'), findsOneWidget);
      expect(find.text('1m'), findsOneWidget);
      expect(find.text('12s'), findsOneWidget);
      expect(find.text('6m 7s'), findsOneWidget);
      expect(find.text('Distribution (17 reviews)'), findsOneWidget);
      expect(find.text('Reviews Last 7 Days'), findsOneWidget);
      expect(find.text('08-01'), findsOneWidget);
      expect(find.text('08-02'), findsOneWidget);
      expect(find.text('By Severity'), findsOneWidget);
      expect(find.text('unknown  1'), findsOneWidget);
      expect(find.text('By AI Agent'), findsOneWidget);
      expect(find.text('other  2'), findsOneWidget);
      expect(find.text('Top Repos by Reviews'), findsOneWidget);
      expect(find.text('acme/api'), findsOneWidget);
      expect(find.text('globex/web'), findsOneWidget);

      expect(rateLimitLoads, 1);
      expect(find.text('Core (REST)'), findsOneWidget);
      expect(find.text('Search'), findsOneWidget);
      expect(find.text('GraphQL'), findsOneWidget);
      expect(find.text('4500 / 5000'), findsOneWidget);
      expect(find.text('2 / 30'), findsOneWidget);
      expect(find.text('0 / 0'), findsOneWidget);
      expect(find.text('reset now'), findsOneWidget);

      await tester.tap(_filterChip('Repo'));
      await tester.pumpAndSettle();
      expect(_option('acme/api'), findsOneWidget);
      expect(_option('globex/web'), findsOneWidget);
      expect(find.byType(CheckboxListTile), findsNWidgets(2));
      await tester.tap(find.widgetWithText(TextButton, 'Cancel'));
      await tester.pumpAndSettle();

      expect(_refreshButton(), findsOneWidget);
      await tester.tap(_refreshButton());
      await tester.pumpAndSettle();

      expect(rateLimitLoads, 2);
      expect(find.text('4400 / 5000'), findsOneWidget);
      expect(find.text('1 / 30'), findsOneWidget);
      expect(find.text('3000 / 5000'), findsOneWidget);
    },
  );
}

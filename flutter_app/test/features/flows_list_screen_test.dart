import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:mocktail/mocktail.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/config_model.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/config/config_providers.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/features/flows/flows_list_screen.dart';
import 'package:heimdallm/shared/design_system/theme.dart';

class _MockApiClient extends Mock implements ApiClient {}

class _FixedConfig extends ConfigNotifier {
  @override
  Future<AppConfig> build() async => const AppConfig();
}

const _listingJson = {
  'selected': 'weekday',
  'default_id': 'default',
  'agents': [
    {'id': 'claude', 'installed': true, 'write_capable': true},
    {'id': 'codex', 'installed': true, 'write_capable': true},
  ],
  'write_capable': ['claude', 'codex'],
  'flows': {
    'default': {
      'name': 'Primary and fallback',
      'start': 'a1',
      'nodes': {
        'a1': {'type': 'agent', 'agent': 'claude', 'next': 'a2'},
        'a2': {'type': 'agent', 'agent': 'codex'},
      },
    },
    'weekday': {
      'name': 'Weekday',
      'start': 'c1',
      'nodes': {
        'c1': {'type': 'condition', 'yes': 'b', 'no': 'a'},
        'b': {'type': 'agent', 'agent': 'copilot'},
        'a': {'type': 'agent', 'agent': 'claude'},
        'z': {'type': 'agent', 'agent': 'claude'},
      },
    },
  },
};

Future<GoRouter> _pump(WidgetTester tester) async {
  tester.view.physicalSize = const Size(1400, 2400);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.resetPhysicalSize);
  addTearDown(tester.view.resetDevicePixelRatio);
  final api = _MockApiClient();
  when(
    () => api.fetchFlows(),
  ).thenAnswer((_) async => FlowListing.fromJson(_listingJson));
  final router = GoRouter(
    initialLocation: '/flows',
    routes: [
      GoRoute(
        path: '/flows',
        builder: (_, _) => const Scaffold(body: FlowsScreen()),
        routes: [
          GoRoute(
            path: ':id',
            builder: (_, s) =>
                Scaffold(body: Text('Flow ${s.pathParameters['id']}')),
          ),
        ],
      ),
    ],
  );
  await tester.pumpWidget(
    ProviderScope(
      overrides: [
        apiClientProvider.overrideWithValue(api),
        configNotifierProvider.overrideWith(_FixedConfig.new),
      ],
      child: MaterialApp.router(
        theme: HeimdallmTheme.light(),
        builder: (context, child) => HeimdallmTheme.scope(child: child!),
        routerConfig: router,
      ),
    ),
  );
  await tester.pumpAndSettle();
  return router;
}

void main() {
  test('compare keys and percent formatting', () {
    expect(compareFlowKeys('a', '1'), greaterThan(0));
    expect(compareFlowKeys('b', 'a'), greaterThan(0));
    expect(compareFlowKeys('10', '010'), greaterThan(0));
    expect(formatPercent(49.5), '49.5');
    expect(formatPercent(50), '50');
  });

  test('condition descriptions', () {
    expect(
      const ScheduleCondition(days: [], tz: '').describe(),
      'every day 08:00–15:00 daemon time',
    );
    expect(
      const ScheduleCondition(
        days: ['sat', 'sun'],
        from: '00:00',
        to: '00:00',
        tz: '',
      ).describe(),
      'sat, sun all day',
    );
    expect(
      const ScheduleCondition(days: [], from: '07:00', to: '07:00').describe(),
      'always',
    );
    expect(
      const ScheduleCondition(
        days: ['sat'],
        from: '00:00',
        to: '00:00',
        tz: 'UTC',
      ).describe(),
      'sat all day UTC',
    );
    expect(
      const QuotaCondition(agent: 'x', op: 'above', percent: 90).describe(),
      'x session quota > 90%',
    );
  });

  test('quota and decision payloads', () {
    final q = AgentQuota.fromJson({
      'agent': 'claude',
      'available': true,
      'windows': [
        {
          'kind': 'session',
          'used_percent': 34.4,
          'resets_at': '2026-10-08T15:00:00Z',
        },
        {
          'kind': 'model',
          'label': 'gemini-2.5-pro',
          'used_percent': 10,
          'resets_at': '0001-01-01T00:00:00Z',
        },
      ],
    });
    expect(q.summary, '5h 34% · gemini-2.5-pro 10%');
    expect(q.windows[1].resetsAt, isNull);
    expect(const AgentQuota(agent: 'x', available: true).summary, 'no limits');
    expect(const AgentQuota(agent: 'x').summary, '');
    for (final kind in ['weekly', 'monthly', 'credit', 'other']) {
      expect(QuotaWindow(kind: kind, usedPercent: 1).shortLabel, isNotEmpty);
    }
    final d = FlowDecision.fromJson(const {});
    expect(d.candidates, isEmpty);
    final copy = const QuotaCondition(
      agent: 'a',
    ).copyWith(agent: 'b', window: 'weekly', op: 'above', percent: 3);
    expect(copy.toJson(), {
      'agent': 'b',
      'window': 'weekly',
      'op': 'above',
      'percent': 3.0,
    });
    expect(const ScheduleCondition().copyWith(tz: 'UTC').toJson()['tz'], 'UTC');
  });

  testWidgets('list shows flows and creates a new one', (tester) async {
    final router = await _pump(tester);
    expect(find.text('Weekday'), findsOneWidget);
    expect(find.text('Used globally'), findsOneWidget);
    expect(find.text('Primary / fallback'), findsOneWidget);
    expect(find.text('2 steps · claude, codex'), findsOneWidget);
    expect(find.text('4 steps · claude, copilot'), findsOneWidget);

    await tester.tap(find.byKey(const ValueKey('flows-new')));
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const ValueKey('new-flow-id')),
      'weekday',
    );
    await tester.tap(find.byKey(const ValueKey('new-flow-create')));
    await tester.pumpAndSettle();
    expect(find.textContaining('already exists'), findsOneWidget);

    await tester.tap(find.byKey(const ValueKey('flows-new')));
    await tester.pumpAndSettle();
    await tester.enterText(find.byKey(const ValueKey('new-flow-id')), 'night');
    await tester.tap(find.byKey(const ValueKey('new-flow-create')));
    await tester.pumpAndSettle();
    expect(router.state.uri.path, '/flows/night');
  });
}

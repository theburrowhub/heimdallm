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
import 'package:heimdallm/features/flows/flows_screen.dart';
import 'package:heimdallm/shared/design_system/theme.dart';

class _MockApiClient extends Mock implements ApiClient {}

class _FixedConfig extends ConfigNotifier {
  @override
  Future<AppConfig> build() async => const AppConfig();
}

const _listingJson = {
  'selected': 'weekday',
  'default_id': 'default',
  'agents': ['claude', 'codex', 'copilot', 'gemini'],
  'write_capable': ['claude', 'codex', 'gemini'],
  'flows': {
    'default': {
      'name': 'Primary and fallback',
      'rules': {
        '10': {'agent': 'claude'},
        '20': {'agent': 'codex'},
      },
    },
    'weekday': {
      'name': 'Weekday',
      'rules': {
        '100': {'agent': 'codex'},
        '10': {
          'agent': 'claude',
          'quota': {
            'q1': {
              'agent': 'claude',
              'window': 'session',
              'op': 'below',
              'percent': 50,
            },
          },
        },
        '20': {
          'agent': 'copilot',
          'match': 'any',
          'schedule': {
            's1': {
              'days': ['mon', 'fri'],
              'from': '08:00',
              'to': '15:00',
              'tz': 'Europe/Madrid',
            },
          },
        },
      },
    },
  },
};

Future<(_MockApiClient, GoRouter)> _pump(
  WidgetTester tester, {
  String location = '/flows',
}) async {
  tester.view.physicalSize = const Size(1400, 2400);
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.resetPhysicalSize);
  addTearDown(tester.view.resetDevicePixelRatio);
  final api = _MockApiClient();
  when(
    () => api.fetchFlows(),
  ).thenAnswer((_) async => FlowListing.fromJson(_listingJson));
  when(() => api.putFlow(any(), any())).thenAnswer((_) async => {});
  when(() => api.deleteFlow(any())).thenAnswer((_) async => {});
  when(() => api.patchConfig(any())).thenAnswer((_) async => {});
  when(() => api.simulateFlow(flow: any(named: 'flow'))).thenAnswer(
    (_) async => FlowDecision.fromJson({
      'flow_id': 'weekday',
      'candidates': ['copilot', 'codex'],
      'rules': [
        {
          'key': '10',
          'agent': 'claude',
          'matched': false,
          'available': true,
          'reasons': [
            'claude session quota below 50% does not hold (used 70%)',
          ],
        },
        {
          'key': '20',
          'agent': 'copilot',
          'matched': true,
          'available': true,
          'reasons': ['schedule holds'],
        },
      ],
    }),
  );
  final router = GoRouter(
    initialLocation: location,
    routes: [
      GoRoute(
        path: '/flows',
        builder: (_, _) => const Scaffold(body: FlowsScreen()),
        routes: [
          GoRoute(
            path: ':id',
            builder: (_, s) => Scaffold(
              body: FlowEditorScreen(flowId: s.pathParameters['id']!),
            ),
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
  return (api, router);
}

void main() {
  setUpAll(() {
    registerFallbackValue(<String, dynamic>{});
    registerFallbackValue(const ReviewFlow());
  });

  group('models', () {
    test('flows parse in numeric rule order and serialise renumbered', () {
      final listing = FlowListing.fromJson(_listingJson);
      expect(listing.ids, ['default', 'weekday']);
      final f = listing.flows['weekday']!;
      expect(f.rules.map((r) => r.agent), ['claude', 'copilot', 'codex']);
      expect(f.rules[1].match, 'any');
      expect(f.rules[1].schedule.single.tz, 'Europe/Madrid');
      expect(f.rules[2].isCatchAll, isTrue);
      final json = f.toJson();
      expect((json['rules'] as Map).keys, ['10', '20', '30']);
      final back = ReviewFlow.fromJson(json);
      expect(back.rules[0].quota.single.percent, 50);
      expect(back.rules[1].toJson()['match'], 'any');
      expect(back.rules[0].toJson().containsKey('match'), isFalse);
      expect(compareFlowKeys('a', '1'), greaterThan(0));
      expect(compareFlowKeys('b', 'a'), greaterThan(0));
      expect(compareFlowKeys('10', '010'), greaterThan(0));
      expect(formatPercent(49.5), '49.5');
      expect(formatPercent(50), '50');
      expect(ReviewFlow.fromJson(const {}).rules, isEmpty);
    });

    test('descriptions', () {
      final f = FlowListing.fromJson(_listingJson).flows['weekday']!;
      expect(
        describeRule(f.rules[0]),
        'claude — when claude session quota < 50%',
      );
      expect(
        describeRule(f.rules[1]),
        'copilot — when mon, fri 08:00–15:00 Europe/Madrid',
      );
      expect(describeRule(f.rules[2]), 'codex — always (fallback)');
      expect(
        const ScheduleCondition(days: [], tz: '').describe(),
        'every day 08:00–15:00 daemon time',
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
      expect(
        const AgentQuota(agent: 'x', available: true).summary,
        'no limits',
      );
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
      expect(
        const ScheduleCondition().copyWith(tz: 'UTC').toJson()['tz'],
        'UTC',
      );
    });
  });

  testWidgets('list shows flows and creates a new one', (tester) async {
    final (_, router) = await _pump(tester);
    expect(find.text('Weekday'), findsOneWidget);
    expect(find.text('Used globally'), findsOneWidget);
    expect(find.text('Primary / fallback'), findsOneWidget);
    expect(
      find.text('2. copilot — when mon, fri 08:00–15:00 Europe/Madrid'),
      findsOneWidget,
    );

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

  testWidgets('editing a new flow builds rules and saves them', (tester) async {
    final (api, _) = await _pump(tester, location: '/flows/night');
    await tester.tap(find.byKey(const ValueKey('flow-save')));
    await tester.pumpAndSettle();
    expect(find.text('Add at least one rule'), findsOneWidget);

    await tester.enterText(find.byKey(const ValueKey('flow-name')), 'Night');
    await tester.tap(find.byKey(const ValueKey('flow-add-rule')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const ValueKey('rule-0-add-schedule')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const ValueKey('rule-0-schedule-0-day-sat')));
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const ValueKey('rule-0-schedule-0-from')),
      '22:00',
    );
    await tester.enterText(
      find.byKey(const ValueKey('rule-0-schedule-0-to')),
      '06:00',
    );
    await tester.enterText(
      find.byKey(const ValueKey('rule-0-schedule-0-tz')),
      'UTC',
    );
    await tester.pump();
    await tester.tap(find.byKey(const ValueKey('rule-0-add-quota')));
    await tester.pumpAndSettle();
    await tester.enterText(
      find.byKey(const ValueKey('rule-0-quota-0-percent')),
      '30',
    );
    await tester.pumpAndSettle();
    await tester.tap(find.text('Any').first);
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const ValueKey('flow-add-rule')));
    await tester.pumpAndSettle();

    await tester.tap(find.byKey(const ValueKey('flow-save')));
    await tester.pumpAndSettle();
    final saved =
        verify(() => api.putFlow('night', captureAny())).captured.single
            as ReviewFlow;
    expect(saved.name, 'Night');
    expect(saved.rules, hasLength(2));
    final r = saved.rules.first;
    expect(r.match, 'any');
    expect(r.schedule.single.days, contains('sat'));
    expect(r.schedule.single.from, '22:00');
    expect(r.schedule.single.to, '06:00');
    expect(r.schedule.single.tz, 'UTC');
    expect(r.quota.single.percent, 30);
    expect(find.text('Flow saved'), findsOneWidget);

    // Remove conditions and the second rule.
    await tester.tap(find.byKey(const ValueKey('rule-0-quota-0-delete')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const ValueKey('rule-0-schedule-0-delete')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const ValueKey('rule-1-delete')));
    await tester.pumpAndSettle();
    expect(
      find.text('No conditions: this rule always applies.'),
      findsOneWidget,
    );
  });

  testWidgets('existing flow: simulate, use globally, delete', (tester) async {
    final (api, router) = await _pump(tester, location: '/flows/weekday');
    expect(find.text('Weekday'), findsOneWidget);
    await tester.tap(find.byKey(const ValueKey('flow-simulate')));
    await tester.pumpAndSettle();
    expect(
      find.text(
        'Reviews now with copilot, then codex if it runs out of quota.',
      ),
      findsOneWidget,
    );
    expect(find.textContaining('used 70%'), findsOneWidget);

    await tester.tap(find.byKey(const ValueKey('flow-use-globally')));
    await tester.pumpAndSettle();
    final patch =
        verify(() => api.patchConfig(captureAny())).captured.last
            as Map<String, dynamic>;
    expect(patch, {
      'ai': {'flow': ''},
    });

    await tester.tap(find.byKey(const ValueKey('flow-delete')));
    await tester.pumpAndSettle();
    await tester.tap(find.byKey(const ValueKey('flow-delete-confirm')));
    await tester.pumpAndSettle();
    verify(() => api.deleteFlow('weekday')).called(1);
    expect(router.state.uri.path, '/flows');
  });

  testWidgets('default flow edits primary and fallback', (tester) async {
    final (api, _) = await _pump(tester, location: '/flows/default');
    expect(find.textContaining('classic primary/fallback'), findsOneWidget);
    await tester.tap(find.byKey(const ValueKey('default-flow-fallback')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('none').last);
    await tester.pumpAndSettle();
    final patch =
        verify(() => api.patchConfig(captureAny())).captured.last
            as Map<String, dynamic>;
    expect(patch, {
      'ai': {'fallback': ''},
    });
    await tester.tap(find.byKey(const ValueKey('default-flow-primary')));
    await tester.pumpAndSettle();
    await tester.tap(find.text('gemini').last);
    await tester.pumpAndSettle();
    expect(
      (verify(() => api.patchConfig(captureAny())).captured.last
          as Map<String, dynamic>)['ai']['primary'],
      'gemini',
    );
  });

  testWidgets('errors surface as toasts', (tester) async {
    final (api, _) = await _pump(tester, location: '/flows/weekday');
    when(
      () => api.simulateFlow(flow: any(named: 'flow')),
    ).thenThrow(ApiException('sim down'));
    when(() => api.putFlow(any(), any())).thenThrow(ApiException('bad flow'));
    when(() => api.patchConfig(any())).thenThrow(ApiException('patch down'));
    await tester.tap(find.byKey(const ValueKey('flow-simulate')));
    await tester.pumpAndSettle();
    expect(find.textContaining('sim down'), findsOneWidget);
    await tester.tap(find.byKey(const ValueKey('flow-save')));
    await tester.pumpAndSettle();
    expect(find.textContaining('bad flow'), findsOneWidget);
    await tester.tap(find.byKey(const ValueKey('flow-use-globally')));
    await tester.pumpAndSettle();
    expect(find.textContaining('patch down'), findsOneWidget);
  });
}

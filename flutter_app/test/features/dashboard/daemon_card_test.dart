import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/config/config_providers.dart';
import 'package:heimdallm/features/dashboard/daemon_card.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/shared/design_system/theme.dart';
import 'package:mocktail/mocktail.dart';

class _MockApiClient extends Mock implements ApiClient {}

/// The event stream's connection without its watchdog timer; tests flip it.
class _Connection extends DaemonConnectionNotifier {
  @override
  DaemonConnectionStatus build() =>
      const DaemonConnectionStatus(phase: DaemonConnectionPhase.connected);

  void set(DaemonConnectionPhase phase) =>
      state = DaemonConnectionStatus(phase: phase);
}

Future<void> _settle(WidgetTester tester) async {
  for (var i = 0; i < 5; i++) {
    await tester.pump(const Duration(milliseconds: 200));
  }
}

Future<(ProviderContainer, _MockApiClient)> _pump(
  WidgetTester tester, {
  bool running = true,
  Future<bool> Function()? power,
  bool compact = false,
  void Function(_MockApiClient api)? stub,
}) async {
  final api = _MockApiClient();
  stub?.call(api);
  final container = ProviderContainer(
    overrides: [
      apiClientProvider.overrideWithValue(api),
      daemonHealthProvider.overrideWith(
        (ref) => power?.call() ?? Future.value(running),
      ),
      sseStreamProvider.overrideWith((ref) => const Stream.empty()),
      daemonConnectionProvider.overrideWith(_Connection.new),
    ],
  );
  addTearDown(container.dispose);
  await tester.pumpWidget(
    UncontrolledProviderScope(
      container: container,
      child: MaterialApp.router(
        builder: (context, child) =>
            HeimdallmTheme.scope(child: child ?? const SizedBox.shrink()),
        routerConfig: GoRouter(
          routes: [
            GoRoute(
              path: '/',
              builder: (_, _) => Scaffold(
                body: Center(
                  child: SizedBox(
                    width: 200,
                    child: DaemonCard(compact: compact),
                  ),
                ),
              ),
            ),
            GoRoute(
              path: '/server',
              builder: (_, _) => const Scaffold(body: Text('Server screen')),
            ),
          ],
        ),
      ),
    ),
  );
  await _settle(tester);
  return (container, api);
}

void _stubRunning(_MockApiClient api, {Map<String, dynamic>? health}) {
  when(
    () => api.fetchHealth(),
  ).thenAnswer((_) async => health ?? {'status': 'ok', 'version': '0.10.8'});
  when(() => api.fetchQuotas()).thenAnswer(
    (_) async => [
      AgentQuota(
        agent: 'claude',
        available: true,
        windows: [
          QuotaWindow(
            kind: 'session',
            usedPercent: 42,
            resetsAt: DateTime(2026, 11, 19, 14, 30),
          ),
          const QuotaWindow(kind: 'weekly', usedPercent: 91),
          const QuotaWindow(kind: 'model', label: 'opus', usedPercent: 75),
        ],
      ),
      const AgentQuota(agent: 'codex', error: 'not_configured'),
    ],
  );
  when(() => api.fetchStats()).thenAnswer(
    (_) async => {
      'reviews_last_7_days': [
        {'day': '2026-11-18', 'count': 3},
        {'day': '2026-11-19', 'count': 2},
      ],
      'tokens_last_7_days': {'cost_usd': 18.4},
    },
  );
}

void main() {
  testWidgets('shows the daemon, its subscription windows and the week', (
    tester,
  ) async {
    await _pump(tester, stub: _stubRunning);

    expect(find.text('Daemon running'), findsOneWidget);
    expect(find.text('v0.10.8'), findsOneWidget);
    expect(find.text('claude 5h'), findsOneWidget);
    expect(find.text('42%'), findsOneWidget);
    expect(find.text('claude 7d'), findsOneWidget);
    expect(find.text('91%'), findsOneWidget);
    // Only the plan windows, and no dollar figure: a subscription's cost is
    // its usage window, not the API price.
    expect(find.text('claude opus'), findsNothing);
    expect(find.textContaining(r'$'), findsNothing);
    expect(find.text('Last 7 days · 5 reviews'), findsOneWidget);
    expect(
      find.byTooltip('claude 5h window resets 19/11 14:30'),
      findsOneWidget,
    );

    await tester.tap(find.byKey(const Key('daemon-card')));
    await _settle(tester);
    expect(find.text('Server screen'), findsOneWidget);
  });

  testWidgets('a stopped daemon says so; its quotas and stats are not read', (
    tester,
  ) async {
    final (_, api) = await _pump(
      tester,
      running: false,
      stub: (api) =>
          when(() => api.fetchHealth()).thenAnswer((_) async => null),
    );

    expect(find.text('Daemon stopped'), findsOneWidget);
    verifyNever(() => api.fetchQuotas());
    verifyNever(() => api.fetchStats());
  });

  testWidgets('it says checking until the daemon answers', (tester) async {
    final never = Completer<Map<String, dynamic>?>();
    await _pump(
      tester,
      stub: (api) =>
          when(() => api.fetchHealth()).thenAnswer((_) => never.future),
    );
    expect(find.text('Checking daemon…'), findsOneWidget);
  });

  testWidgets('no answer while the power check loads is not "stopped"', (
    tester,
  ) async {
    final pending = Completer<bool>();
    await _pump(
      tester,
      power: () => pending.future,
      stub: (api) =>
          when(() => api.fetchHealth()).thenAnswer((_) async => null),
    );
    expect(find.text('Daemon stopped'), findsNothing);
    expect(find.text('Daemon not reachable'), findsOneWidget);
  });

  testWidgets('a daemon started after the app shows up without a restart', (
    tester,
  ) async {
    // The power button's check ran before the daemon (a CLI or compose start)
    // and still says false.
    Map<String, dynamic>? health;
    final (container, api) = await _pump(
      tester,
      running: false,
      stub: (api) {
        _stubRunning(api);
        when(() => api.fetchHealth()).thenAnswer((_) async => health);
      },
    );
    expect(find.text('Daemon stopped'), findsOneWidget);

    health = {'status': 'ok', 'version': '0.10.8'};
    (container.read(daemonConnectionProvider.notifier) as _Connection).set(
      DaemonConnectionPhase.connecting,
    );
    await _settle(tester);
    expect(find.text('Daemon running'), findsOneWidget);
    expect(find.text('Last 7 days · 5 reviews'), findsOneWidget);
    verify(() => api.fetchQuotas()).called(1);
  });

  testWidgets('checks the daemon again when the event stream drops', (
    tester,
  ) async {
    Map<String, dynamic>? health = {'status': 'ok'};
    final (container, _) = await _pump(
      tester,
      stub: (api) {
        _stubRunning(api);
        when(() => api.fetchHealth()).thenAnswer((_) async => health);
      },
    );
    expect(find.text('Daemon running'), findsOneWidget);

    health = null;
    (container.read(daemonConnectionProvider.notifier) as _Connection).set(
      DaemonConnectionPhase.offline,
    );
    await _settle(tester);
    expect(find.text('Daemon not reachable'), findsOneWidget);

    health = {'status': 'degraded'};
    (container.read(daemonConnectionProvider.notifier) as _Connection).set(
      DaemonConnectionPhase.connected,
    );
    await _settle(tester);
    expect(find.text('Daemon degraded'), findsOneWidget);
  });

  testWidgets('collapsed, it is a dot with everything in its tooltip', (
    tester,
  ) async {
    await _pump(tester, compact: true, stub: _stubRunning);

    expect(find.text('Daemon running'), findsNothing);
    expect(
      find.byTooltip('Daemon running · 0.10.8 · claude 5h 42% · claude 7d 91%'),
      findsOneWidget,
    );

    await tester.tap(find.byKey(const Key('daemon-card')));
    await _settle(tester);
    expect(find.text('Server screen'), findsOneWidget);
  });
}

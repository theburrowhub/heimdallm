import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:mocktail/mocktail.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/instances/aggregation.dart';
import 'package:heimdallm/core/instances/instances_providers.dart';
import 'package:heimdallm/core/instances/models.dart';
import 'package:heimdallm/core/platform/platform_services_provider.dart';
import 'package:heimdallm/core/models/pr.dart';
import 'package:heimdallm/core/models/tracked_issue.dart';
import 'package:heimdallm/core/state/local_state_notifier.dart';
import 'package:heimdallm/features/config/config_providers.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/features/issues/issues_providers.dart';
import 'package:heimdallm/features/merge_tracking/merge_tracking_providers.dart';
import 'package:heimdallm/shared/layout/app_shell.dart';
import 'package:heimdallm/shared/router.dart';

import '../../core/platform/fake_platform_services.dart';

class _MockApiClient extends Mock implements ApiClient {}

/// Fixes [daemonConnectionProvider] to a status chosen by the test instead
/// of driving it through the real SSE-listening state machine.
class _FixedConnectionNotifier extends DaemonConnectionNotifier {
  _FixedConnectionNotifier(this._status);
  final DaemonConnectionStatus _status;

  @override
  DaemonConnectionStatus build() => _status;
}

/// Common overrides so the `/` (dashboard) branch that `AppShell` wraps
/// renders without touching the network: an empty single-daemon install
/// with no PRs/issues, matching the pattern in `dashboard_test.dart`.
List<dynamic> _baseOverrides({ApiClient? api, FakePlatformServices? platform}) => [
  if (api != null) apiClientProvider.overrideWithValue(api),
  if (platform != null) platformServicesProvider.overrideWithValue(platform),
  prsByInstanceProvider.overrideWith(
    (ref) => Future.value(singleInstanceResult(const <PR>[])),
  ),
  issuesByInstanceProvider.overrideWith(
    (ref) async => singleInstanceResult(const <TrackedIssue>[]),
  ),
  sseStreamProvider.overrideWith((ref) => const Stream.empty()),
  daemonInstancesProvider.overrideWith(
    (ref) async => ClusterRegistry.fromJson({
      'role': 'hub',
      'self_id': 'hub-1',
      'self_name': 'Local hub',
      'instances': const [],
    }),
  ),
];

Future<void> _pumpShell(
  WidgetTester tester,
  List<dynamic> extraOverrides, {
  ApiClient? api,
  FakePlatformServices? platform,
  Size size = const Size(1400, 900),
}) async {
  tester.view.physicalSize = size;
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.resetPhysicalSize);
  addTearDown(tester.view.resetDevicePixelRatio);
  await tester.pumpWidget(
    ProviderScope(
      overrides: [
        ..._baseOverrides(api: api, platform: platform),
        ...extraOverrides,
      ].cast(),
      child: MaterialApp.router(routerConfig: createRouter()),
    ),
  );
  await tester.pump();
}

void main() {
  group('ConnectionBanner', () {
    Future<void> pumpPhase(
      WidgetTester tester,
      DaemonConnectionPhase phase, {
      VoidCallback? onRestart,
    }) => tester.pumpWidget(
      MaterialApp(
        home: ConnectionBanner(
          status: DaemonConnectionStatus(phase: phase),
          onRestart: onRestart ?? () {},
        ),
      ),
    );

    testWidgets('shows the connected label with no restart action', (
      tester,
    ) async {
      await pumpPhase(tester, DaemonConnectionPhase.connected);
      expect(find.text('Connected'), findsOneWidget);
      expect(find.widgetWithText(TextButton, 'Restart'), findsNothing);
    });

    testWidgets('shows the stale label with no restart action', (
      tester,
    ) async {
      await pumpPhase(tester, DaemonConnectionPhase.stale);
      expect(find.text('No events received — reconnecting'), findsOneWidget);
      expect(find.widgetWithText(TextButton, 'Restart'), findsNothing);
    });

    testWidgets('shows the connecting label with no restart action', (
      tester,
    ) async {
      await pumpPhase(tester, DaemonConnectionPhase.connecting);
      expect(find.text('Connecting'), findsOneWidget);
      expect(find.widgetWithText(TextButton, 'Restart'), findsNothing);
    });

    testWidgets('shows the offline label with a working restart action', (
      tester,
    ) async {
      var restarted = false;
      await pumpPhase(
        tester,
        DaemonConnectionPhase.offline,
        onRestart: () => restarted = true,
      );
      expect(find.text('Server unavailable'), findsOneWidget);

      final restart = find.widgetWithText(TextButton, 'Restart');
      expect(restart, findsOneWidget);
      await tester.tap(restart);
      expect(restarted, isTrue);
    });
  });

  testWidgets('the merge destination badges its icon with the check count', (
    tester,
  ) async {
    await _pumpShell(tester, [
      mergeTrackingCheckProblemCountProvider.overrideWithValue(3),
    ]);

    expect(find.text('3'), findsOneWidget);
  });

  testWidgets(
    'the merge destination shows a plain icon when there are no problems',
    (tester) async {
      await _pumpShell(tester, [
        mergeTrackingCheckProblemCountProvider.overrideWithValue(0),
      ]);

      expect(find.text('0'), findsNothing);
    },
  );

  testWidgets('a circuit breaker message renders as a dismissible banner', (
    tester,
  ) async {
    await _pumpShell(tester, [
      circuitBreakerProvider.overrideWith(
        () => LocalStateNotifier<String?>('Cost spike detected'),
      ),
    ]);

    expect(find.textContaining('Cost spike detected'), findsOneWidget);

    final container = ProviderScope.containerOf(
      tester.element(find.byType(AppShell)),
      listen: false,
    );
    // Drive the dismiss action the same way the banner's own close button
    // does, without depending on that widget's internal layout.
    container.read(circuitBreakerProvider.notifier).set(null);
    await tester.pump();

    expect(find.textContaining('Cost spike detected'), findsNothing);
  });

  testWidgets(
    'a non-connected daemon renders a connection banner with a working restart',
    (tester) async {
      final api = _MockApiClient();
      final platform = FakePlatformServices(daemonBinaryPath: '/tmp/heimdallm');
      when(() => api.shutdownDaemon()).thenAnswer((_) async {});
      when(() => api.daemonReachable()).thenAnswer((_) async => PortOwner.none);
      when(() => api.checkHealth()).thenAnswer((_) async => true);
      when(() => api.daemonPort).thenReturn(7842);

      await _pumpShell(
        tester,
        [
          daemonHealthProvider.overrideWith((ref) async => true),
          daemonConnectionProvider.overrideWith(
            () => _FixedConnectionNotifier(
              const DaemonConnectionStatus(
                phase: DaemonConnectionPhase.offline,
              ),
            ),
          ),
        ],
        api: api,
        platform: platform,
      );

      expect(find.text('Server unavailable'), findsOneWidget);

      await tester.tap(find.widgetWithText(TextButton, 'Restart'));
      await tester.pumpAndSettle(const Duration(milliseconds: 500));
    },
  );

  testWidgets('stopping a running daemon asks for confirmation first', (
    tester,
  ) async {
    final api = _MockApiClient();
    when(() => api.shutdownDaemon()).thenAnswer((_) async {});
    when(() => api.daemonReachable()).thenAnswer((_) async => PortOwner.none);
    when(() => api.checkHealth()).thenAnswer((_) async => false);

    await _pumpShell(
      tester,
      [daemonHealthProvider.overrideWith((ref) async => true)],
      api: api,
      platform: FakePlatformServices(),
    );

    expect(find.byTooltip('Stop Server'), findsOneWidget);
    await tester.tap(find.byTooltip('Stop Server'));
    await tester.pump();

    expect(find.text('Stop Server?'), findsOneWidget);
    await tester.tap(find.text('Cancel'));
    await tester.pumpAndSettle();

    verifyNever(() => api.shutdownDaemon());
  });

  testWidgets('starting a stopped daemon uses the configured binary', (
    tester,
  ) async {
    final platform = FakePlatformServices(daemonBinaryPath: '/tmp/heimdallm');
    final api = _MockApiClient();
    when(() => api.daemonReachable()).thenAnswer((_) async => PortOwner.none);
    when(() => api.checkHealth()).thenAnswer((_) async => true);

    await _pumpShell(
      tester,
      [daemonHealthProvider.overrideWith((ref) async => false)],
      api: api,
      platform: platform,
    );

    expect(find.byTooltip('Start Server'), findsOneWidget);
    await tester.tap(find.byTooltip('Start Server'));
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 100));
    await tester.pump();

    expect(platform.spawnedDaemons, equals(['/tmp/heimdallm']));
  });

GoRouter minimalRouter() => GoRouter(
  initialLocation: '/',
  routes: [
    StatefulShellRoute.indexedStack(
      builder: (context, state, navigationShell) =>
          AppShell(navigationShell: navigationShell),
      branches: [
        for (var i = 0; i < 9; i++)
          StatefulShellBranch(
            routes: [
              GoRoute(
                path: i == 0 ? '/' : '/branch-$i',
                builder: (context, state) => Text('Branch $i'),
              ),
            ],
          ),
      ],
    ),
    GoRoute(
      path: '/server',
      builder: (context, state) => const Text('Server placeholder'),
    ),
    GoRoute(
      path: '/config',
      builder: (context, state) => const Text('Config placeholder'),
    ),
  ],
);

  testWidgets('the server action navigates to the server screen', (
    tester,
  ) async {
    tester.view.physicalSize = const Size(1400, 900);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);
    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          ..._baseOverrides(),
          daemonHealthProvider.overrideWith((ref) async => false),
        ].cast(),
        child: MaterialApp.router(routerConfig: minimalRouter()),
      ),
    );
    await tester.pump();

    await tester.tap(find.byTooltip('Server'));
    await tester.pumpAndSettle();
    expect(find.text('Server placeholder'), findsOneWidget);
  });

  testWidgets('the settings action navigates to the config screen', (
    tester,
  ) async {
    tester.view.physicalSize = const Size(1400, 900);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);
    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          ..._baseOverrides(),
          daemonHealthProvider.overrideWith((ref) async => false),
        ].cast(),
        child: MaterialApp.router(routerConfig: minimalRouter()),
      ),
    );
    await tester.pump();

    await tester.tap(find.byIcon(Icons.settings));
    await tester.pumpAndSettle();
    expect(find.text('Config placeholder'), findsOneWidget);
  });

  testWidgets(
    'the refresh action invalidates the aggregating providers without error',
    (tester) async {
      await _pumpShell(tester, [
        daemonHealthProvider.overrideWith((ref) async => false),
      ]);

      await tester.tap(find.byIcon(Icons.refresh));
      await tester.pump();

      expect(tester.takeException(), isNull);
    },
  );

  testWidgets(
    'a compact layout puts the destinations behind a Drawer and can navigate',
    (tester) async {
      await _pumpShell(
        tester,
        [daemonHealthProvider.overrideWith((ref) async => false)],
        size: const Size(600, 900),
      );

      expect(find.byType(NavigationRail), findsNothing);

      final scaffoldState = tester.state<ScaffoldState>(
        find.byType(Scaffold).first,
      );
      scaffoldState.openDrawer();
      await tester.pumpAndSettle();

      expect(find.text('Heimdallm'), findsWidgets);
      final instancesTile = find.widgetWithText(ListTile, 'Instances');
      expect(instancesTile, findsOneWidget);

      await tester.tap(instancesTile);
      await tester.pumpAndSettle();

      expect(scaffoldState.isDrawerOpen, isFalse);
    },
  );
}

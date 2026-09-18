import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/instances/aggregation.dart';
import 'package:heimdallm/core/instances/instances_providers.dart';
import 'package:heimdallm/core/instances/models.dart';
import 'package:heimdallm/core/models/activity.dart';
import 'package:heimdallm/core/models/merge_tracking.dart';
import 'package:heimdallm/core/models/pr.dart';
import 'package:heimdallm/core/models/tracked_issue.dart';
import 'package:heimdallm/core/platform/platform_services_provider.dart';
import 'package:heimdallm/core/state/local_state_notifier.dart';
import 'package:heimdallm/features/activity/activity_providers.dart';
import 'package:heimdallm/features/config/config_providers.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/features/issues/issues_providers.dart';
import 'package:heimdallm/features/merge_tracking/merge_tracking_providers.dart';
import 'package:heimdallm/shared/design_system/theme.dart';
import 'package:heimdallm/shared/layout/app_shell.dart';
import 'package:mocktail/mocktail.dart';

import '../../core/platform/fake_platform_services.dart';

class _MockApiClient extends Mock implements ApiClient {}

class TestDaemonConnectionNotifier extends DaemonConnectionNotifier {
  TestDaemonConnectionNotifier(this.initialStatus);

  final DaemonConnectionStatus initialStatus;

  @override
  DaemonConnectionStatus build() => initialStatus;
}

GoRouter minimalRouter({String initialLocation = '/'}) => GoRouter(
  initialLocation: initialLocation,
  routes: [
    StatefulShellRoute.indexedStack(
      builder: (context, state, navigationShell) =>
          AppShell(navigationShell: navigationShell),
      branches: [
        StatefulShellBranch(
          routes: [
            GoRoute(
              path: '/',
              builder: (_, _) =>
                  const Scaffold(body: Text('Dashboard content')),
            ),
          ],
        ),
        StatefulShellBranch(
          routes: [
            GoRoute(
              path: '/activity',
              builder: (_, _) =>
                  const Scaffold(body: Text('Activity log content')),
            ),
          ],
        ),
        StatefulShellBranch(
          routes: [
            GoRoute(
              path: '/merge',
              builder: (_, _) => const Scaffold(body: Text('Merge content')),
            ),
          ],
        ),
        StatefulShellBranch(
          routes: [
            GoRoute(
              path: '/repos',
              builder: (_, _) =>
                  const Scaffold(body: Text('Repositories content')),
            ),
          ],
        ),
        StatefulShellBranch(
          routes: [
            GoRoute(
              path: '/orgs',
              builder: (_, _) =>
                  const Scaffold(body: Text('Organizations content')),
            ),
          ],
        ),
        StatefulShellBranch(
          routes: [
            GoRoute(
              path: '/prompts',
              builder: (_, _) => const Scaffold(body: Text('Prompts content')),
            ),
          ],
        ),
        StatefulShellBranch(
          routes: [
            GoRoute(
              path: '/cli-agents',
              builder: (_, _) => const Scaffold(body: Text('Agents content')),
            ),
          ],
        ),
        StatefulShellBranch(
          routes: [
            GoRoute(
              path: '/stats',
              builder: (_, _) => const Scaffold(body: Text('Stats content')),
            ),
          ],
        ),
        StatefulShellBranch(
          routes: [
            GoRoute(
              path: '/instances',
              builder: (_, _) =>
                  const Scaffold(body: Text('Instances content')),
            ),
          ],
        ),
      ],
    ),
    GoRoute(
      path: '/server',
      builder: (_, _) => const Scaffold(body: Text('Server placeholder')),
    ),
    GoRoute(
      path: '/config',
      builder: (_, _) => const Scaffold(body: Text('Config placeholder')),
    ),
  ],
);

List<dynamic> _baseOverrides({
  required FakePlatformServices platform,
  required ApiClient api,
  bool daemonRunning = false,
  bool daemonStarting = false,
  int mergeCount = 0,
  DaemonConnectionStatus? connection,
  AggregatedResult<PR>? prs,
  Future<AggregatedResult<TrackedIssue>> Function(Ref ref)? issuesByInstance,
  Future<AggregatedResult<MergeTrackingEntry>> Function(Ref ref)?
  mergeTrackingByInstance,
  Future<AggregatedResult<Map<String, dynamic>>> Function(Ref ref)?
  statsByInstance,
  Future<Map<String, dynamic>> Function(Ref ref)? rateLimit,
  Future<ActivityPage> Function(Ref ref)? activityEntries,
  Future<ActivityPage> Function(Ref ref)? activityOptions,
}) {
  return [
    platformServicesProvider.overrideWithValue(platform),
    apiClientProvider.overrideWithValue(api),
    daemonHealthProvider.overrideWith((ref) async => daemonRunning),
    daemonInstancesProvider.overrideWith((ref) async => ClusterRegistry.empty),
    prsByInstanceProvider.overrideWith(
      (ref) async => prs ?? singleInstanceResult<PR>(const []),
    ),
    issuesByInstanceProvider.overrideWith(
      issuesByInstance ??
          (ref) async => singleInstanceResult<TrackedIssue>(const []),
    ),
    mergeTrackingByInstanceProvider.overrideWith(
      mergeTrackingByInstance ??
          (ref) async => singleInstanceResult<MergeTrackingEntry>(const []),
    ),
    statsByInstanceProvider.overrideWith(
      statsByInstance ??
          (ref) async => singleInstanceResult<Map<String, dynamic>>(const []),
    ),
    githubRateLimitProvider.overrideWith(rateLimit ?? (ref) async => const {}),
    activityEntriesProvider.overrideWith(
      activityEntries ??
          (ref) async =>
              const ActivityPage(entries: [], truncated: false, count: 0),
    ),
    activityOptionsProvider.overrideWith(
      activityOptions ??
          (ref) async =>
              const ActivityPage(entries: [], truncated: false, count: 0),
    ),
    daemonStartingProvider.overrideWith(
      () => LocalStateNotifier<bool>(daemonStarting),
    ),
    mergeTrackingCheckProblemCountProvider.overrideWith((ref) => mergeCount),
    sseStreamProvider.overrideWith((ref) => const Stream.empty()),
    if (connection != null)
      daemonConnectionProvider.overrideWith(
        () => TestDaemonConnectionNotifier(connection),
      ),
  ];
}

Future<void> _pumpShell(
  WidgetTester tester, {
  required List<dynamic> overrides,
  Size size = const Size(1400, 1200),
  String initialLocation = '/',
  String? circuitMessage,
}) async {
  tester.view.physicalSize = size;
  tester.view.devicePixelRatio = 1;
  addTearDown(tester.view.resetPhysicalSize);
  addTearDown(tester.view.resetDevicePixelRatio);

  final container = ProviderContainer(overrides: overrides.cast());
  addTearDown(container.dispose);
  if (circuitMessage != null) {
    container.read(circuitBreakerProvider.notifier).set(circuitMessage);
  }

  await tester.pumpWidget(
    UncontrolledProviderScope(
      container: container,
      child: MaterialApp.router(
        builder: (context, child) =>
            HeimdallmTheme.scope(child: child ?? const SizedBox.shrink()),
        routerConfig: minimalRouter(initialLocation: initialLocation),
      ),
    ),
  );
  await tester.pumpAndSettle();
}

void main() {
  testWidgets('ConnectionBanner renders connecting and stale states', (
    tester,
  ) async {
    Future<void> pumpFor(DaemonConnectionStatus status) async {
      await tester.pumpWidget(
        MaterialApp(
          builder: (context, child) =>
              HeimdallmTheme.scope(child: child ?? const SizedBox.shrink()),
          home: Scaffold(
            body: ConnectionBanner(status: status, onRestart: () {}),
          ),
        ),
      );
      await tester.pumpAndSettle();
    }

    await pumpFor(
      const DaemonConnectionStatus(phase: DaemonConnectionPhase.connecting),
    );
    expect(find.text('Connecting'), findsOneWidget);
    expect(find.text('Restart'), findsNothing);

    await pumpFor(
      const DaemonConnectionStatus(phase: DaemonConnectionPhase.stale),
    );
    expect(find.text('No events received — reconnecting'), findsOneWidget);
  });

  testWidgets('ConnectionBanner offline state offers Restart', (tester) async {
    var restarted = false;
    await tester.pumpWidget(
      MaterialApp(
        builder: (context, child) =>
            HeimdallmTheme.scope(child: child ?? const SizedBox.shrink()),
        home: Scaffold(
          body: ConnectionBanner(
            status: const DaemonConnectionStatus(
              phase: DaemonConnectionPhase.offline,
            ),
            onRestart: () => restarted = true,
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('Server unavailable'), findsOneWidget);
    await tester.tap(find.text('Restart'));
    await tester.pump();
    expect(restarted, isTrue);
  });

  testWidgets('AppShell dismisses the circuit-breaker banner', (tester) async {
    final platform = FakePlatformServices();
    final api = _MockApiClient();

    await _pumpShell(
      tester,
      overrides: _baseOverrides(platform: platform, api: api),
      circuitMessage: 'Spend limit exceeded',
    );

    expect(
      find.textContaining('Review circuit breaker tripped'),
      findsOneWidget,
    );

    await tester.tap(find.text('Dismiss'));
    await tester.pumpAndSettle();

    expect(find.textContaining('Review circuit breaker tripped'), findsNothing);
  });

  testWidgets('wide layouts show the merge badge and navigate with the rail', (
    tester,
  ) async {
    final platform = FakePlatformServices();
    final api = _MockApiClient();

    await _pumpShell(
      tester,
      overrides: _baseOverrides(platform: platform, api: api, mergeCount: 3),
    );

    expect(find.byType(NavigationRail), findsOneWidget);
    expect(find.text('Dashboard content'), findsOneWidget);
    expect(find.text('3'), findsOneWidget);

    await tester.tap(find.text('Merge'));
    await tester.pumpAndSettle();

    expect(find.text('Merge content'), findsOneWidget);
  });

  testWidgets('when stopped, the toolbar start action is wired', (
    tester,
  ) async {
    final platform = FakePlatformServices();
    final api = _MockApiClient();

    await _pumpShell(
      tester,
      overrides: _baseOverrides(platform: platform, api: api),
    );

    await tester.tap(find.byTooltip('Start Server'));
    await tester.pumpAndSettle();

    expect(find.text('Daemon binary not found'), findsOneWidget);
  });

  testWidgets('when running, the toolbar stop action asks for confirmation', (
    tester,
  ) async {
    final platform = FakePlatformServices();
    final api = _MockApiClient();

    await _pumpShell(
      tester,
      overrides: _baseOverrides(
        platform: platform,
        api: api,
        daemonRunning: true,
        connection: const DaemonConnectionStatus(
          phase: DaemonConnectionPhase.connected,
        ),
      ),
    );

    await tester.tap(find.byTooltip('Stop Server'));
    await tester.pumpAndSettle();

    expect(find.text('Stop Server?'), findsOneWidget);
    await tester.tap(find.text('Cancel'));
    await tester.pumpAndSettle();
    expect(find.text('Stop Server?'), findsNothing);
  });

  testWidgets('the server button pushes the server route', (tester) async {
    final platform = FakePlatformServices();
    final api = _MockApiClient();

    await _pumpShell(
      tester,
      overrides: _baseOverrides(platform: platform, api: api),
    );

    await tester.tap(find.byTooltip('Server'));
    await tester.pumpAndSettle();

    expect(find.text('Server placeholder'), findsOneWidget);
  });

  testWidgets('the settings button pushes the config route', (tester) async {
    final platform = FakePlatformServices();
    final api = _MockApiClient();

    await _pumpShell(
      tester,
      overrides: _baseOverrides(platform: platform, api: api),
    );

    await tester.tap(find.byIcon(Icons.settings));
    await tester.pumpAndSettle();

    expect(find.text('Config placeholder'), findsOneWidget);
  });

  testWidgets('the refresh button invalidates all dashboard providers', (
    tester,
  ) async {
    final platform = FakePlatformServices();
    final api = _MockApiClient();

    await _pumpShell(
      tester,
      overrides: _baseOverrides(platform: platform, api: api),
    );

    await tester.tap(find.byIcon(Icons.refresh));
    await tester.pumpAndSettle();

    expect(find.text('Dashboard content'), findsOneWidget);
  });

  testWidgets('compact layouts use the drawer for navigation', (tester) async {
    final platform = FakePlatformServices();
    final api = _MockApiClient();

    await _pumpShell(
      tester,
      size: const Size(600, 1000),
      overrides: _baseOverrides(platform: platform, api: api),
    );

    final scaffold = tester.state<ScaffoldState>(find.byType(Scaffold).first);
    scaffold.openDrawer();
    await tester.pumpAndSettle();

    await tester.tap(find.text('Instances'));
    await tester.pumpAndSettle();

    expect(find.text('Instances content'), findsOneWidget);
  });

  testWidgets('partial-read failures surface in the shell banner', (
    tester,
  ) async {
    final platform = FakePlatformServices();
    final api = _MockApiClient();

    await _pumpShell(
      tester,
      overrides: _baseOverrides(
        platform: platform,
        api: api,
        prs: const AggregatedResult<PR>(
          failures: [
            InstanceFailure(
              instanceId: 'srv-a',
              instanceName: 'Server A',
              error: 'offline',
            ),
          ],
        ),
      ),
    );

    expect(
      find.text('Showing partial data — Server A could not be reached.'),
      findsOneWidget,
    );
  });

  testWidgets('offline daemon banner triggers the restart flow', (
    tester,
  ) async {
    final platform = FakePlatformServices();
    final api = _MockApiClient();
    when(() => api.shutdownDaemon()).thenAnswer((_) async {});
    when(
      () => api.daemonReachable(),
    ).thenAnswer((_) async => PortOwner.foreign);
    when(() => api.daemonPort).thenReturn(7842);

    await _pumpShell(
      tester,
      overrides: _baseOverrides(
        platform: platform,
        api: api,
        daemonRunning: true,
        connection: const DaemonConnectionStatus(
          phase: DaemonConnectionPhase.offline,
        ),
      ),
    );

    expect(find.text('Server unavailable'), findsOneWidget);

    await tester.tap(find.text('Restart'));
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 250));
    await tester.pumpAndSettle();

    expect(
      find.textContaining('restart cancelled', findRichText: true),
      findsOneWidget,
    );
  });
}

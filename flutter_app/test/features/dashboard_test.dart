import 'dart:async';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:mocktail/mocktail.dart';
import 'package:shared_preferences/shared_preferences.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/daemon/daemon_startup.dart';
import 'package:heimdallm/core/models/pr.dart';
import 'package:heimdallm/core/models/review.dart';
import 'package:heimdallm/core/models/review_status.dart';
import 'package:heimdallm/core/platform/platform_services_provider.dart';
import 'package:heimdallm/features/config/config_providers.dart';
import 'package:heimdallm/features/dashboard/activity_filters.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/features/dashboard/dashboard_screen.dart';
import 'package:heimdallm/features/issues/issues_providers.dart';
import 'package:heimdallm/features/server/server_actions.dart';
import '../core/platform/fake_platform_services.dart';

class MockApiClient extends Mock implements ApiClient {}

class ThrowingPlatformServices extends FakePlatformServices {
  ThrowingPlatformServices({
    required this.spawnError,
    required super.daemonBinaryPath,
  });

  final Object spawnError;

  @override
  Future<void> spawnDaemon(String binaryPath) async {
    spawnedDaemons.add(binaryPath);
    throw spawnError;
  }
}

PR _pr({
  int id = 1,
  String repo = 'org/repo',
  int number = 42,
  Review? latestReview,
  ReviewExecutionStatus? reviewStatus,
}) => PR(
  id: id,
  githubId: 1000 + id,
  repo: repo,
  number: number,
  title: 't',
  author: 'a',
  url: 'u',
  state: 'open',
  updatedAt: DateTime.utc(2026, 1, 1),
  latestReview: latestReview,
  reviewStatus: reviewStatus,
);

Review _review(int id) => Review(
  id: id,
  prId: 1,
  cliUsed: 'claude',
  summary: '',
  issues: const [],
  severity: 'low',
  createdAt: DateTime.utc(2026, 1, 1),
);

Future<void> _pumpOfflineDashboard(
  WidgetTester tester, {
  required MockApiClient api,
  required FakePlatformServices platform,
}) async {
  await tester.pumpWidget(
    ProviderScope(
      overrides: [
        apiClientProvider.overrideWithValue(api),
        platformServicesProvider.overrideWithValue(platform),
        daemonHealthProvider.overrideWith((ref) => Future.value(false)),
        prsProvider.overrideWith((ref) => Future.error(Exception('offline'))),
        issuesProvider.overrideWith(
          (ref) => Future.error(Exception('offline')),
        ),
        sseStreamProvider.overrideWith((ref) => const Stream.empty()),
      ],
      child: MaterialApp.router(
        routerConfig: GoRouter(
          routes: [
            GoRoute(path: '/', builder: (_, _) => const DashboardScreen()),
          ],
        ),
      ),
    ),
  );
  await tester.pumpAndSettle();
}

Future<void> _pumpRestartHarness(
  WidgetTester tester, {
  required MockApiClient api,
  required FakePlatformServices platform,
}) async {
  await tester.pumpWidget(
    ProviderScope(
      overrides: [
        apiClientProvider.overrideWithValue(api),
        platformServicesProvider.overrideWithValue(platform),
      ],
      child: MaterialApp(
        home: Consumer(
          builder: (context, ref, _) => Scaffold(
            body: FilledButton(
              onPressed: () => restartDaemon(
                context,
                ref,
                portReleaseDelays: const [Duration.zero],
              ),
              child: const Text('Restart harness'),
            ),
          ),
        ),
      ),
    ),
  );
}

void main() {
  test('daemon startup failure messages cover every guarded outcome', () {
    expect(
      daemonStartupFailureMessage(
        const DaemonStartupResult(DaemonStartupOutcome.portOccupied),
        9000,
      ),
      contains('Port 9000'),
    );
    expect(
      daemonStartupFailureMessage(
        const DaemonStartupResult(DaemonStartupOutcome.daemonPresent),
        9000,
      ),
      contains('already running'),
    );
    expect(
      daemonStartupFailureMessage(
        const DaemonStartupResult(DaemonStartupOutcome.spawnFailedRetryable),
        9000,
      ),
      contains('unknown error'),
    );
    expect(
      daemonStartupFailureMessage(
        const DaemonStartupResult(DaemonStartupOutcome.spawnBudgetExhausted),
        9000,
      ),
      contains('exhausted'),
    );
    expect(
      daemonStartupFailureMessage(
        const DaemonStartupResult(DaemonStartupOutcome.spawned),
        9000,
      ),
      isEmpty,
    );
  });

  test(
    'restart waits for daemon port release, not health degradation',
    () async {
      final api = MockApiClient();
      final owners = <PortOwner>[
        PortOwner.daemon,
        PortOwner.daemon,
        PortOwner.none,
      ];
      when(
        () => api.daemonReachable(),
      ).thenAnswer((_) async => owners.removeAt(0));

      final owner = await waitForDaemonPortRelease(
        api,
        delays: const [Duration.zero, Duration.zero, Duration.zero],
      );

      expect(owner, PortOwner.none);
      verify(() => api.daemonReachable()).called(3);
      verifyNever(() => api.checkHealth());
    },
  );

  testWidgets(
    'restart accepts a LaunchAgent reappearance without detached spawn',
    (tester) async {
      final platform = FakePlatformServices(daemonBinaryPath: '/tmp/heimdallm');
      final api = MockApiClient();
      final owners = <PortOwner>[PortOwner.none, PortOwner.daemon];
      when(() => api.shutdownDaemon()).thenAnswer((_) async {});
      when(
        () => api.daemonReachable(),
      ).thenAnswer((_) async => owners.removeAt(0));
      when(() => api.checkHealth()).thenAnswer((_) async => true);

      await tester.pumpWidget(
        ProviderScope(
          overrides: [
            apiClientProvider.overrideWithValue(api),
            platformServicesProvider.overrideWithValue(platform),
          ],
          child: MaterialApp(
            home: Consumer(
              builder: (context, ref, _) => Scaffold(
                body: FilledButton(
                  onPressed: () => restartDaemon(context, ref),
                  child: const Text('Restart test daemon'),
                ),
              ),
            ),
          ),
        ),
      );

      await tester.tap(find.text('Restart test daemon'));
      await tester.pump();
      await tester.pump(const Duration(milliseconds: 200));
      await tester.pump();
      await tester.pump(const Duration(milliseconds: 100));
      await tester.pump();

      expect(platform.spawnedDaemons, isEmpty);
      expect(find.text('Server restarted'), findsOneWidget);
      verify(() => api.daemonReachable()).called(2);
      verify(() => api.checkHealth()).called(1);
    },
  );

  testWidgets(
    'restart accepts a supervised daemon that reappears before port release',
    (tester) async {
      final platform = FakePlatformServices();
      final api = MockApiClient();
      when(() => api.shutdownDaemon()).thenAnswer((_) async {});
      when(
        () => api.daemonReachable(),
      ).thenAnswer((_) async => PortOwner.daemon);
      when(() => api.checkHealth()).thenAnswer((_) async => true);

      await tester.pumpWidget(
        ProviderScope(
          overrides: [
            apiClientProvider.overrideWithValue(api),
            platformServicesProvider.overrideWithValue(platform),
          ],
          child: MaterialApp(
            home: Consumer(
              builder: (context, ref, _) => Scaffold(
                body: FilledButton(
                  onPressed: () => restartDaemon(context, ref),
                  child: const Text('Restart supervised daemon'),
                ),
              ),
            ),
          ),
        ),
      );

      await tester.tap(find.text('Restart supervised daemon'));
      await tester.pump();
      for (final delay in const [
        Duration(milliseconds: 200),
        Duration(milliseconds: 300),
        Duration(milliseconds: 500),
        Duration(milliseconds: 800),
        Duration(milliseconds: 1200),
        Duration(seconds: 2),
      ]) {
        await tester.pump(delay);
        await tester.pump();
      }
      await tester.pump(const Duration(milliseconds: 100));
      await tester.pump();

      expect(platform.spawnedDaemons, isEmpty);
      expect(find.text('Server restarted'), findsOneWidget);
      verify(() => api.daemonReachable()).called(6);
      verify(() => api.checkHealth()).called(1);
    },
  );

  testWidgets('restart cancels when a foreign process claims the port', (
    tester,
  ) async {
    final api = MockApiClient();
    when(() => api.shutdownDaemon()).thenAnswer((_) async {});
    when(
      () => api.daemonReachable(),
    ).thenAnswer((_) async => PortOwner.foreign);
    when(() => api.daemonPort).thenReturn(8123);
    final platform = FakePlatformServices(
      daemonBinaryPath: '/tmp/heimdallm',
    );
    await _pumpRestartHarness(tester, api: api, platform: platform);

    await tester.tap(find.text('Restart harness'));
    await tester.pumpAndSettle();

    expect(find.textContaining('port 8123 is now occupied'), findsOneWidget);
    expect(platform.spawnedDaemons, isEmpty);
  });

  testWidgets('restart ignores a concurrent second request', (tester) async {
    final api = MockApiClient();
    final releaseShutdown = Completer<void>();
    when(() => api.shutdownDaemon()).thenAnswer((_) => releaseShutdown.future);
    when(() => api.daemonReachable()).thenAnswer((_) async => PortOwner.none);
    when(() => api.checkHealth()).thenAnswer((_) async => true);
    final platform = FakePlatformServices(daemonBinaryPath: '/tmp/heimdallm');
    await _pumpRestartHarness(tester, api: api, platform: platform);

    await tester.tap(find.text('Restart harness'));
    await tester.pump();
    await tester.tap(find.text('Restart harness'));
    await tester.pump();

    verify(() => api.shutdownDaemon()).called(1);
    releaseShutdown.complete();
    await tester.pumpAndSettle();
    expect(platform.spawnedDaemons, ['/tmp/heimdallm']);
  });

  testWidgets('restart reuses a daemon that still owns the port', (
    tester,
  ) async {
    final api = MockApiClient();
    when(() => api.shutdownDaemon()).thenAnswer((_) async {});
    when(() => api.daemonReachable()).thenAnswer((_) async => PortOwner.daemon);
    when(() => api.checkHealth()).thenAnswer((_) async => true);
    final platform = FakePlatformServices(daemonBinaryPath: '/tmp/heimdallm');
    await _pumpRestartHarness(tester, api: api, platform: platform);

    await tester.tap(find.text('Restart harness'));
    await tester.pumpAndSettle();

    expect(find.text('Server restarted'), findsOneWidget);
    expect(platform.spawnedDaemons, isEmpty);
  });

  testWidgets('restart reports a missing binary after the port is released', (
    tester,
  ) async {
    final api = MockApiClient();
    when(() => api.shutdownDaemon()).thenAnswer((_) async {});
    when(() => api.daemonReachable()).thenAnswer((_) async => PortOwner.none);
    final platform = FakePlatformServices();
    await _pumpRestartHarness(tester, api: api, platform: platform);

    await tester.tap(find.text('Restart harness'));
    await tester.pumpAndSettle();

    expect(find.text('Daemon binary not found'), findsOneWidget);
    expect(platform.spawnedDaemons, isEmpty);
  });

  testWidgets('restart surfaces a guarded startup refusal', (tester) async {
    final api = MockApiClient();
    final owners = [PortOwner.none, PortOwner.foreign];
    when(() => api.shutdownDaemon()).thenAnswer((_) async {});
    when(
      () => api.daemonReachable(),
    ).thenAnswer((_) async => owners.removeAt(0));
    when(() => api.daemonPort).thenReturn(8123);
    final platform = FakePlatformServices(daemonBinaryPath: '/tmp/heimdallm');
    await _pumpRestartHarness(tester, api: api, platform: platform);

    await tester.tap(find.text('Restart harness'));
    await tester.pumpAndSettle();

    expect(
      find.text('Port 8123 is occupied; no daemon was started.'),
      findsOneWidget,
    );
    expect(platform.spawnedDaemons, isEmpty);
  });

  test('SortNotifier handles preference load failures', () async {
    TestWidgetsFlutterBinding.ensureInitialized();
    const channel = MethodChannel('plugins.flutter.io/shared_preferences');
    final messenger =
        TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger;
    final messages = <String>[];
    final originalDebugPrint = debugPrint;

    SharedPreferences.resetStatic();
    messenger.setMockMethodCallHandler(
      channel,
      (_) async => throw StateError('preferences unavailable'),
    );
    debugPrint = (message, {wrapWidth}) {
      if (message != null) messages.add(message);
    };
    addTearDown(() {
      messenger.setMockMethodCallHandler(channel, null);
      SharedPreferences.resetStatic();
      debugPrint = originalDebugPrint;
    });

    final container = ProviderContainer();
    addTearDown(container.dispose);

    expect(container.read(reviewsSortProvider), SortMode.priority);
    await pumpEventQueue();

    final sortMessages = messages
        .where((message) => message.startsWith('SortNotifier:'))
        .toList();
    expect(sortMessages, hasLength(1));
    expect(
      sortMessages.single,
      startsWith('SortNotifier: failed to load preference:'),
    );
    expect(sortMessages.single, contains('preferences unavailable'));
  });

  group('reconcileReviewing', () {
    test(
      'drops entry when first review lands (baseline=0, latestReview present)',
      () {
        final pr = _pr(repo: 'org/repo', number: 1, latestReview: _review(42));
        final out = reconcileReviewing({'org/repo:1': 0}, [pr]);
        expect(out, isEmpty);
      },
    );

    test(
      'keeps entry when review still pending (baseline=0, no latestReview yet)',
      () {
        final pr = _pr(repo: 'org/repo', number: 1);
        final out = reconcileReviewing({'org/repo:1': 0}, [pr]);
        expect(out, equals({'org/repo:1': 0}));
      },
    );

    test('keeps entry during re-review (baseline matches current id)', () {
      final pr = _pr(repo: 'org/repo', number: 1, latestReview: _review(42));
      final out = reconcileReviewing({'org/repo:1': 42}, [pr]);
      expect(out, equals({'org/repo:1': 42}));
    });

    test(
      'drops entry when re-review completes (baseline older than current id)',
      () {
        final pr = _pr(repo: 'org/repo', number: 1, latestReview: _review(43));
        final out = reconcileReviewing({'org/repo:1': 42}, [pr]);
        expect(out, isEmpty);
      },
    );

    test('preserves entry for PR not in current list', () {
      final out = reconcileReviewing({'org/other:9': 5}, const []);
      expect(out, equals({'org/other:9': 5}));
    });

    test('drops optimistic entry when daemon reports a terminal failure', () {
      final pr = _pr(
        repo: 'org/repo',
        number: 1,
        reviewStatus: ReviewExecutionStatus(
          headSha: 'abc',
          attempts: 1,
          failedAt: DateTime(2026, 8, 24, 10, 30),
          retryAt: DateTime(2026, 8, 24, 10, 35),
          error: 'Review timed out before completion.',
          active: false,
        ),
      );
      final out = reconcileReviewing({'org/repo:1': 0}, [pr]);
      expect(out, isEmpty);
    });

    test('reconciles a mixed set (drops stale, keeps in-progress)', () {
      final stale = _pr(repo: 'org/a', number: 1, latestReview: _review(100));
      final fresh = _pr(
        id: 2,
        repo: 'org/b',
        number: 2,
        latestReview: _review(200),
      );
      final out = reconcileReviewing(
        {'org/a:1': 0, 'org/b:2': 200},
        [stale, fresh],
      );
      expect(out, equals({'org/b:2': 200}));
    });
  });

  testWidgets('DashboardScreen shows PR title', (tester) async {
    await tester.binding.setSurfaceSize(const Size(1800, 700));
    addTearDown(() => tester.binding.setSurfaceSize(null));

    final pr = PR(
      id: 1,
      githubId: 101,
      repo: 'org/repo',
      number: 42,
      title: 'Fix critical bug',
      author: 'alice',
      url: 'https://github.com',
      state: 'open',
      updatedAt: DateTime.now(),
    );

    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          prsProvider.overrideWith((ref) => Future.value([pr])),
          sseStreamProvider.overrideWith((ref) => const Stream.empty()),
        ],
        child: MaterialApp.router(
          routerConfig: GoRouter(
            routes: [
              GoRoute(path: '/', builder: (_, _) => const DashboardScreen()),
            ],
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.textContaining('Fix critical bug'), findsOneWidget);
    expect(find.textContaining('org/repo'), findsOneWidget);

    final toolbarFinder = find.byKey(
      const Key('activity-filter-toolbar-row'),
    );
    final toolbar = tester.getRect(toolbarFinder);
    final addButton = tester.getRect(
      find.byKey(const Key('dashboard-add-pr-button')),
    );
    final searchField = tester.getRect(
      find.byWidgetPredicate(
        (widget) =>
            widget is TextField && widget.decoration?.hintText == 'Search...',
      ),
    );

    expect(addButton.right, closeTo(toolbar.right, 0.1));
    expect(addButton.left, greaterThan(searchField.right));
    for (final label in [
      'Priority',
      'Newest',
      'PR',
      'IT',
      'DEV',
      'Open',
      'Closed',
      'Org',
      'Repo',
    ]) {
      final control = tester.getRect(
        find.descendant(of: toolbarFinder, matching: find.text(label)),
      );
      expect(
        (control.center.dy - addButton.center.dy).abs(),
        lessThan(10),
        reason: '$label and Add PR must share the toolbar row',
      );
    }
  });

  testWidgets(
    'main Activity list Add PR control opens, validates, submits, and refreshes',
    (tester) async {
      final api = MockApiClient();
      final platform = FakePlatformServices();
      var prLoads = 0;
      when(() => api.addPRByUrl(any())).thenAnswer((_) async => 73);

      await tester.pumpWidget(
        ProviderScope(
          overrides: [
            apiClientProvider.overrideWithValue(api),
            platformServicesProvider.overrideWithValue(platform),
            daemonHealthProvider.overrideWith((ref) async => false),
            prsProvider.overrideWith((ref) async {
              prLoads++;
              return <PR>[];
            }),
            issuesProvider.overrideWith((ref) async => []),
            sseStreamProvider.overrideWith((ref) => const Stream.empty()),
          ],
          child: MaterialApp.router(
            routerConfig: GoRouter(
              routes: [
                GoRoute(path: '/', builder: (_, _) => const DashboardScreen()),
              ],
            ),
          ),
        ),
      );
      await tester.pumpAndSettle();

      final addControl = find.byKey(const Key('dashboard-add-pr-button'));
      expect(addControl, findsOneWidget);
      expect(find.text('Add PR'), findsOneWidget);
      expect(find.text('No activity yet'), findsOneWidget);

      await tester.tap(addControl);
      await tester.pumpAndSettle();
      expect(find.text('Add a pull request'), findsOneWidget);

      final urlField = find.byKey(const Key('add-pr-url-field'));
      await tester.enterText(urlField, 'not a GitHub PR');
      await tester.tap(find.text('Add & review'));
      await tester.pump();
      expect(find.textContaining('Enter a GitHub PR link'), findsOneWidget);
      verifyNever(() => api.addPRByUrl(any()));

      await tester.enterText(
        urlField,
        'https://github.com/acme/widgets/pull/73',
      );
      await tester.tap(find.text('Add & review'));
      await tester.pumpAndSettle();

      verify(
        () => api.addPRByUrl('https://github.com/acme/widgets/pull/73'),
      ).called(1);
      expect(prLoads, 2);
      expect(find.text('Add a pull request'), findsNothing);
      expect(
        find.text('PR added — repository monitored and review started.'),
        findsOneWidget,
      );
    },
  );

  testWidgets('failed review is visible with timestamp and retry action', (
    tester,
  ) async {
    final pr = _pr(
      reviewStatus: ReviewExecutionStatus(
        headSha: 'abc',
        attempts: 2,
        failedAt: DateTime(2026, 8, 24, 10, 35),
        retryAt: DateTime(2099, 8, 24, 10, 45),
        error: 'Review timed out before completion.',
        active: false,
      ),
    );

    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          prsProvider.overrideWith((ref) => Future.value([pr])),
          sseStreamProvider.overrideWith((ref) => const Stream.empty()),
        ],
        child: MaterialApp.router(
          routerConfig: GoRouter(
            routes: [
              GoRoute(path: '/', builder: (_, _) => const DashboardScreen()),
            ],
          ),
        ),
      ),
    );
    await tester.pumpAndSettle();

    expect(find.text('FAILED'), findsOneWidget);
    expect(find.text('Retry'), findsOneWidget);
    expect(find.text('PENDING'), findsNothing);
    expect(
      find.textContaining('Review timed out before completion.'),
      findsOneWidget,
    );
    expect(find.textContaining('failed 2026-08-24 10:35'), findsOneWidget);
  });

  testWidgets('active daemon status offers scoped cancellation from PR list', (
    tester,
  ) async {
    final api = MockApiClient();
    when(() => api.cancelReview(1)).thenAnswer((_) async {});
    final pr = _pr(
      reviewStatus: ReviewExecutionStatus(
        headSha: 'abc',
        attempts: 1,
        failedAt: DateTime(2026, 8, 24, 10, 30),
        retryAt: DateTime(2026, 8, 24, 10, 35),
        error: '',
        active: true,
      ),
    );

    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          apiClientProvider.overrideWithValue(api),
          prsProvider.overrideWith((ref) => Future.value([pr])),
          sseStreamProvider.overrideWith((ref) => const Stream.empty()),
        ],
        child: MaterialApp.router(
          routerConfig: GoRouter(
            routes: [
              GoRoute(path: '/', builder: (_, _) => const DashboardScreen()),
            ],
          ),
        ),
      ),
    );
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 100));

    expect(find.text('Cancel'), findsOneWidget);
    expect(find.text('PENDING'), findsNothing);
    await tester.tap(find.text('Cancel'));
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 300));
    expect(find.text('Cancel this review?'), findsOneWidget);
    await tester.tap(find.text('Cancel review'));
    await tester.pump();

    verify(() => api.cancelReview(1)).called(1);
  });

  testWidgets('DashboardScreen shows loading indicator while fetching', (
    tester,
  ) async {
    final completer = Completer<List<PR>>();
    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          prsProvider.overrideWith((ref) => completer.future),
          sseStreamProvider.overrideWith((ref) => const Stream.empty()),
        ],
        child: MaterialApp.router(
          routerConfig: GoRouter(
            routes: [
              GoRoute(path: '/', builder: (_, _) => const DashboardScreen()),
            ],
          ),
        ),
      ),
    );
    await tester.pump();
    expect(find.byKey(const Key('dashboard-add-pr-button')), findsOneWidget);
    expect(find.byType(CircularProgressIndicator), findsOneWidget);
  });

  testWidgets('offline dashboard can start daemon', (tester) async {
    final platform = FakePlatformServices(daemonBinaryPath: '/tmp/heimdallm');
    final api = MockApiClient();
    var healthChecks = 0;
    when(() => api.daemonReachable()).thenAnswer((_) async => PortOwner.none);
    when(() => api.checkHealth()).thenAnswer((_) async {
      healthChecks++;
      return healthChecks == 3;
    });

    await _pumpOfflineDashboard(tester, api: api, platform: platform);

    expect(find.byKey(const Key('dashboard-add-pr-button')), findsOneWidget);
    expect(find.text('Start Server'), findsOneWidget);
    await tester.tap(find.text('Start Server'));
    await tester.pump();
    expect(find.text('Starting...'), findsOneWidget);

    await tester.pump(const Duration(milliseconds: 100));
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 100));
    await tester.pump();
    await tester.pump(const Duration(milliseconds: 100));
    await tester.pump();

    expect(platform.spawnedDaemons, equals(['/tmp/heimdallm']));
    verify(() => api.checkHealth()).called(3);
    expect(find.text('Server started'), findsOneWidget);
    expect(find.text('Starting...'), findsNothing);

    final container = ProviderScope.containerOf(
      tester.element(find.byType(DashboardScreen)),
      listen: false,
    );
    expect(container.read(daemonStartingProvider), isFalse);
  });

  testWidgets('offline dashboard reports missing daemon binary', (
    tester,
  ) async {
    final platform = FakePlatformServices();
    final api = MockApiClient();

    await _pumpOfflineDashboard(tester, api: api, platform: platform);

    await tester.tap(find.text('Start Server'));
    await tester.pumpAndSettle();

    expect(platform.spawnedDaemons, isEmpty);
    verifyNever(() => api.checkHealth());
    expect(find.text('Daemon binary not found'), findsOneWidget);

    final container = ProviderScope.containerOf(
      tester.element(find.byType(DashboardScreen)),
      listen: false,
    );
    expect(container.read(daemonStartingProvider), isFalse);
  });

  testWidgets('offline dashboard accepts an already-present daemon', (
    tester,
  ) async {
    final platform = FakePlatformServices(daemonBinaryPath: '/tmp/heimdallm');
    final api = MockApiClient();
    when(() => api.daemonReachable()).thenAnswer((_) async => PortOwner.daemon);
    when(() => api.daemonPort).thenReturn(7842);

    await _pumpOfflineDashboard(tester, api: api, platform: platform);

    await tester.tap(find.text('Start Server'));
    await tester.pumpAndSettle();

    expect(platform.spawnedDaemons, isEmpty);
    expect(find.text('Server is already running'), findsOneWidget);
    verifyNever(() => api.checkHealth());
  });

  testWidgets('offline dashboard resets start state when spawn fails', (
    tester,
  ) async {
    final platform = ThrowingPlatformServices(
      daemonBinaryPath: '/tmp/heimdallm',
      spawnError: Exception('boom'),
    );
    final api = MockApiClient();
    when(() => api.daemonReachable()).thenAnswer((_) async => PortOwner.none);
    when(() => api.daemonPort).thenReturn(7842);

    await _pumpOfflineDashboard(tester, api: api, platform: platform);

    await tester.tap(find.text('Start Server'));
    await tester.pumpAndSettle();

    expect(platform.spawnedDaemons, equals(['/tmp/heimdallm']));
    verifyNever(() => api.checkHealth());
    expect(
      find.text('Could not start Heimdallm: Exception: boom'),
      findsOneWidget,
    );

    final container = ProviderScope.containerOf(
      tester.element(find.byType(DashboardScreen)),
      listen: false,
    );
    expect(container.read(daemonStartingProvider), isFalse);
  });

  testWidgets('offline dashboard reports daemon start timeout', (tester) async {
    final platform = FakePlatformServices(daemonBinaryPath: '/tmp/heimdallm');
    final api = MockApiClient();
    when(() => api.daemonReachable()).thenAnswer((_) async => PortOwner.none);
    when(() => api.checkHealth()).thenAnswer((_) async => false);

    await _pumpOfflineDashboard(tester, api: api, platform: platform);

    await tester.tap(find.text('Start Server'));
    await tester.pump();
    expect(find.text('Starting...'), findsOneWidget);

    for (var i = 0; i < 80; i++) {
      await tester.pump(const Duration(milliseconds: 100));
      await tester.pump();
    }

    expect(platform.spawnedDaemons, equals(['/tmp/heimdallm']));
    verify(() => api.checkHealth()).called(80);
    expect(
      find.text('Heimdallm could not start. Check the app installation.'),
      findsOneWidget,
    );

    final container = ProviderScope.containerOf(
      tester.element(find.byType(DashboardScreen)),
      listen: false,
    );
    expect(container.read(daemonStartingProvider), isFalse);
  });
}

import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/config_model.dart';
import 'package:heimdallm/main.dart';
import 'package:mocktail/mocktail.dart';

import 'core/platform/fake_platform_services.dart';

class _MockApiClient extends Mock implements ApiClient {}

class _ThrowingPlatformServices extends FakePlatformServices {
  _ThrowingPlatformServices({
    required this.error,
    required super.githubToken,
    required super.daemonBinaryPath,
  });

  final Object error;

  @override
  Future<void> spawnDaemon(String binaryPath) async {
    spawnedDaemons.add(binaryPath);
    throw error;
  }
}

class _DuplicatePlatformServices extends FakePlatformServices {
  @override
  Future<bool> ensureSingleInstance() async {
    ensureSingleInstanceCalls++;
    return false;
  }
}

class _UpdaterFailurePlatformServices extends FakePlatformServices {
  @override
  Future<void> setupAppUpdater() async {
    setupAppUpdaterCalls++;
    throw StateError('updater unavailable');
  }
}

GoRouter _router() => GoRouter(
  routes: [
    GoRoute(
      path: '/',
      builder: (_, _) => const Scaffold(body: Text('Dashboard target')),
    ),
    GoRoute(
      path: '/config',
      builder: (_, _) => const Scaffold(body: Text('Config target')),
    ),
  ],
);

Future<void> _pumpUntil(
  WidgetTester tester,
  Finder finder, {
  int frames = 30,
}) async {
  for (var i = 0; i < frames; i++) {
    await tester.pump(Duration.zero);
    if (finder.evaluate().isNotEmpty) return;
  }
  fail('Widget never appeared: $finder');
}

void main() {
  test(
    'platform initialization terminates a duplicate before updater setup',
    () async {
      final platform = _DuplicatePlatformServices();

      expect(await initializePlatformForApp(platform), isFalse);
      expect(platform.ensureSingleInstanceCalls, 1);
      expect(platform.quitDuplicateInstanceCalls, 1);
      expect(platform.setupAppUpdaterCalls, 0);
    },
  );

  test(
    'updater initialization failure does not block normal startup',
    () async {
      final platform = _UpdaterFailurePlatformServices();

      expect(await initializePlatformForApp(platform), isTrue);
      expect(platform.ensureSingleInstanceCalls, 1);
      expect(platform.setupAppUpdaterCalls, 1);
      expect(platform.quitDuplicateInstanceCalls, 0);
    },
  );

  testWidgets('reachable daemon enters the application without spawning', (
    tester,
  ) async {
    final api = _MockApiClient();
    final platform = FakePlatformServices();
    when(() => api.daemonReachable()).thenAnswer((_) async => PortOwner.daemon);

    await tester.pumpWidget(
      buildBootstrapAppForTest(
        router: _router(),
        platform: platform,
        apiClient: api,
      ),
    );
    await _pumpUntil(tester, find.text('Dashboard target'));

    expect(platform.spawnedDaemons, isEmpty);
  });

  testWidgets('completed native update requires matching daemon version', (
    tester,
  ) async {
    final api = _MockApiClient();
    final platform = FakePlatformServices(pendingUpdateVersion: '0.8.4');
    when(() => api.daemonReachable()).thenAnswer((_) async => PortOwner.daemon);
    when(
      () => api.fetchHealth(),
    ).thenAnswer((_) async => {'status': 'ok', 'version': '0.8.4'});

    await tester.pumpWidget(
      buildBootstrapAppForTest(
        router: _router(),
        platform: platform,
        apiClient: api,
      ),
    );
    await _pumpUntil(tester, find.text('Dashboard target'));

    expect(platform.completeAppUpdateCalls, 1);
    expect(platform.pendingUpdateVersion, isNull);
  });

  testWidgets('pending update waits when the daemon appears during startup', (
    tester,
  ) async {
    final api = _MockApiClient();
    final platform = FakePlatformServices(
      pendingUpdateVersion: '0.8.4',
      githubToken: 'token',
      daemonBinaryPath: '/bundled/heimdalld',
    );
    final owners = [PortOwner.none, PortOwner.daemon];
    when(
      () => api.daemonReachable(),
    ).thenAnswer((_) async => owners.removeAt(0));
    when(() => api.daemonPort).thenReturn(7842);
    when(() => api.checkHealth()).thenAnswer((_) async => true);
    when(
      () => api.fetchHealth(),
    ).thenAnswer((_) async => {'status': 'ok', 'version': '0.8.4'});

    await tester.pumpWidget(
      buildBootstrapAppForTest(
        router: _router(),
        platform: platform,
        apiClient: api,
      ),
    );
    await _pumpUntil(tester, find.text('Dashboard target'));

    expect(platform.spawnedDaemons, isEmpty);
    expect(platform.completeAppUpdateCalls, 1);
  });

  testWidgets('mixed app and daemon versions fail closed after update', (
    tester,
  ) async {
    final api = _MockApiClient();
    final platform = FakePlatformServices(pendingUpdateVersion: '0.8.4');
    when(() => api.daemonReachable()).thenAnswer((_) async => PortOwner.daemon);
    when(
      () => api.fetchHealth(),
    ).thenAnswer((_) async => {'status': 'ok', 'version': '0.8.3'});

    await tester.pumpWidget(
      buildBootstrapAppForTest(
        router: _router(),
        platform: platform,
        apiClient: api,
      ),
    );
    await _pumpUntil(tester, find.text('Update validation failed'));

    expect(find.textContaining('0.8.3'), findsOneWidget);
    // Native completion owns the sealed version/PID check before Dart performs
    // its independent post-release health assertion.
    expect(platform.completeAppUpdateCalls, 1);
    expect(find.text('Dashboard target'), findsNothing);
  });

  testWidgets('failed update acknowledgement retains the recovery marker', (
    tester,
  ) async {
    final api = _MockApiClient();
    final platform = FakePlatformServices(
      pendingUpdateVersion: '0.8.4',
      completeUpdateError: StateError('lease owner mismatch'),
    );
    when(() => api.daemonReachable()).thenAnswer((_) async => PortOwner.daemon);
    when(
      () => api.fetchHealth(),
    ).thenAnswer((_) async => {'status': 'ok', 'version': '0.8.4'});

    await tester.pumpWidget(
      buildBootstrapAppForTest(
        router: _router(),
        platform: platform,
        apiClient: api,
      ),
    );
    await _pumpUntil(tester, find.text('Update acknowledgement failed'));

    expect(platform.completeAppUpdateCalls, 1);
    expect(platform.pendingUpdateVersion, '0.8.4');
    expect(find.textContaining('lease owner mismatch'), findsOneWidget);
    expect(find.text('Dashboard target'), findsNothing);
  });

  testWidgets('failed native update recovery blocks daemon startup', (
    tester,
  ) async {
    final api = _MockApiClient();
    final platform = FakePlatformServices(
      pendingUpdateError: StateError('LaunchAgent restore failed'),
    );

    await tester.pumpWidget(
      buildBootstrapAppForTest(
        router: _router(),
        platform: platform,
        apiClient: api,
      ),
    );
    await _pumpUntil(tester, find.text('Update recovery failed'));

    expect(find.textContaining('LaunchAgent restore failed'), findsOneWidget);
    expect(platform.spawnedDaemons, isEmpty);
    verifyNever(() => api.daemonReachable());
  });

  testWidgets('foreign port shows diagnostics and Retry probes again', (
    tester,
  ) async {
    final api = _MockApiClient();
    final platform = FakePlatformServices();
    final owners = [PortOwner.foreign, PortOwner.daemon];
    when(
      () => api.daemonReachable(),
    ).thenAnswer((_) async => owners.removeAt(0));
    when(() => api.daemonPort).thenReturn(7842);

    await tester.pumpWidget(
      buildBootstrapAppForTest(
        router: _router(),
        platform: platform,
        apiClient: api,
      ),
    );
    await _pumpUntil(tester, find.text('Port 7842 is already occupied'));
    expect(find.textContaining('No daemon was started'), findsOneWidget);
    expect(find.textContaining('lsof -nP'), findsOneWidget);

    await tester.tap(find.text('Retry'));
    await _pumpUntil(tester, find.text('Dashboard target'));
    expect(platform.spawnedDaemons, isEmpty);
  });

  testWidgets('relative endpoint fails closed with proxy diagnostics', (
    tester,
  ) async {
    final api = _MockApiClient();
    when(
      () => api.daemonReachable(),
    ).thenAnswer((_) async => PortOwner.foreign);
    when(() => api.daemonPort).thenReturn(0);

    await tester.pumpWidget(
      buildBootstrapAppForTest(
        router: _router(),
        platform: FakePlatformServices(apiBaseUrl: '/api'),
        apiClient: api,
      ),
    );
    await _pumpUntil(tester, find.text('Daemon endpoint unavailable'));

    expect(find.textContaining('reverse-proxy logs'), findsOneWidget);
  });

  testWidgets('missing credentials routes into the application setup', (
    tester,
  ) async {
    final api = _MockApiClient();
    when(() => api.daemonReachable()).thenAnswer((_) async => PortOwner.none);

    await tester.pumpWidget(
      buildBootstrapAppForTest(
        router: _router(),
        platform: FakePlatformServices(),
        apiClient: api,
      ),
    );
    await _pumpUntil(tester, find.text('Config target'));

    verify(() => api.daemonReachable()).called(1);
  });

  testWidgets('missing bundled daemon reports an actionable install error', (
    tester,
  ) async {
    final api = _MockApiClient();
    when(() => api.daemonReachable()).thenAnswer((_) async => PortOwner.none);

    await tester.pumpWidget(
      buildBootstrapAppForTest(
        router: _router(),
        platform: FakePlatformServices(githubToken: 'token'),
        apiClient: api,
      ),
    );
    await _pumpUntil(tester, find.text('Daemon binary not found'));

    expect(find.textContaining('installation is incomplete'), findsOneWidget);
    expect(find.textContaining('xattr -cr'), findsOneWidget);
  });

  testWidgets('first run writes discovered repos and starts one daemon', (
    tester,
  ) async {
    final api = _MockApiClient();
    final owners = [PortOwner.none, PortOwner.none];
    when(
      () => api.daemonReachable(),
    ).thenAnswer((_) async => owners.removeAt(0));
    when(() => api.daemonPort).thenReturn(7842);
    when(() => api.checkHealth()).thenAnswer((_) async => true);
    final platform = FakePlatformServices(
      githubToken: 'token',
      configExistsValue: false,
      daemonBinaryPath: '/fake/heimdalld',
      env: {'HEIMDALLM_LOCAL_DIR_BASE': '/one, /two'},
    )..discoveredRepos = const ['org/one', 'org/two'];

    await tester.pumpWidget(
      buildBootstrapAppForTest(
        router: _router(),
        platform: platform,
        apiClient: api,
      ),
    );
    await _pumpUntil(tester, find.text('Dashboard target'));

    expect(platform.spawnedDaemons, ['/fake/heimdalld']);
    expect(platform.writtenConfigs, hasLength(1));
    final AppConfig config = platform.writtenConfigs.single;
    expect(config.repoConfigs.keys, containsAll(['org/one', 'org/two']));
    expect(config.localDirBase, ['/one', '/two']);
  });

  testWidgets('daemon appearing during setup is reused', (tester) async {
    final api = _MockApiClient();
    final owners = [PortOwner.none, PortOwner.daemon];
    when(
      () => api.daemonReachable(),
    ).thenAnswer((_) async => owners.removeAt(0));
    when(() => api.daemonPort).thenReturn(7842);
    final platform = FakePlatformServices(
      githubToken: 'token',
      daemonBinaryPath: '/fake/heimdalld',
    );

    await tester.pumpWidget(
      buildBootstrapAppForTest(
        router: _router(),
        platform: platform,
        apiClient: api,
      ),
    );
    await _pumpUntil(tester, find.text('Dashboard target'));

    expect(platform.spawnedDaemons, isEmpty);
    verifyNever(() => api.checkHealth());
  });

  testWidgets('port claimed during setup aborts before spawn', (tester) async {
    final api = _MockApiClient();
    final owners = [PortOwner.none, PortOwner.foreign];
    when(
      () => api.daemonReachable(),
    ).thenAnswer((_) async => owners.removeAt(0));
    when(() => api.daemonPort).thenReturn(7842);
    final platform = FakePlatformServices(
      githubToken: 'token',
      daemonBinaryPath: '/fake/heimdalld',
    );

    await tester.pumpWidget(
      buildBootstrapAppForTest(
        router: _router(),
        platform: platform,
        apiClient: api,
      ),
    );
    await _pumpUntil(tester, find.text('Port 7842 is already occupied'));

    expect(platform.spawnedDaemons, isEmpty);
  });

  testWidgets('retryable spawn failure becomes a terminal error', (
    tester,
  ) async {
    final api = _MockApiClient();
    when(() => api.daemonReachable()).thenAnswer((_) async => PortOwner.none);
    when(() => api.daemonPort).thenReturn(7842);
    when(() => api.checkHealth()).thenAnswer((_) async => false);
    final platform = _ThrowingPlatformServices(
      error: StateError('cannot execute'),
      githubToken: 'token',
      daemonBinaryPath: '/fake/heimdalld',
    );

    await tester.pumpWidget(
      buildBootstrapAppForTest(
        router: _router(),
        platform: platform,
        apiClient: api,
        maxSpawnAttempts: 2,
      ),
    );
    await _pumpUntil(tester, find.text('Could not start daemon'));

    expect(find.textContaining('cannot execute'), findsOneWidget);
    expect(platform.spawnedDaemons, hasLength(2));
  });

  testWidgets('successful process start cannot exceed its spawn budget', (
    tester,
  ) async {
    final api = _MockApiClient();
    when(() => api.daemonReachable()).thenAnswer((_) async => PortOwner.none);
    when(() => api.daemonPort).thenReturn(7842);
    when(() => api.checkHealth()).thenAnswer((_) async => false);
    final platform = FakePlatformServices(
      githubToken: 'token',
      daemonBinaryPath: '/fake/heimdalld',
    );

    await tester.pumpWidget(
      buildBootstrapAppForTest(
        router: _router(),
        platform: platform,
        apiClient: api,
        maxSpawnAttempts: 1,
      ),
    );
    await _pumpUntil(tester, find.text('Daemon failed to start'));

    expect(platform.spawnedDaemons, ['/fake/heimdalld']);
    expect(find.textContaining('exhausted'), findsOneWidget);
  });
}

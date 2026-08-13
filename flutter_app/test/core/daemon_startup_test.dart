import 'dart:async';

import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/daemon/daemon_startup.dart';
import 'package:heimdallm/core/platform/platform_services.dart';
import 'platform/fake_platform_services.dart';

class _ThrowingPlatformServices extends FakePlatformServices {
  _ThrowingPlatformServices({required this.spawnError});

  final Object spawnError;

  @override
  Future<void> spawnDaemon(String binaryPath) async {
    spawnedDaemons.add(binaryPath);
    throw spawnError;
  }
}

ApiClient _apiFor(
  FakePlatformServices platform,
  Future<http.Response> Function() response,
) => ApiClient(
  httpClient: MockClient((_) => response()),
  platform: platform,
  daemonReachabilityTimeout: const Duration(milliseconds: 20),
);

void main() {
  const daemonHeaders = {'x-heimdallm-daemon': '1'};

  test('occupied-port failures are actionable', () {
    expect(
      const DaemonPortOccupiedException(8123).toString(),
      'Port 8123 is already occupied; no daemon was started.',
    );
  });

  test('an existing degraded daemon is never spawned again', () async {
    final platform = FakePlatformServices();
    final coordinator = DaemonStartupCoordinator(
      api: _apiFor(
        platform,
        () async => http.Response(
          '{"status":"degraded","checks":{}}',
          503,
          headers: daemonHeaders,
        ),
      ),
      platform: platform,
      binaryPath: '/daemon',
    );

    final result = await coordinator.ensureAvailable();

    expect(result.outcome, DaemonStartupOutcome.daemonPresent);
    expect(platform.spawnedDaemons, isEmpty);
  });

  test('a proven-closed port permits exactly one spawn attempt', () async {
    final platform = FakePlatformServices();
    final coordinator = DaemonStartupCoordinator(
      api: _apiFor(
        platform,
        () async => throw http.ClientException('Connection refused'),
      ),
      platform: platform,
      binaryPath: '/daemon',
    );

    final result = await coordinator.ensureAvailable();

    expect(result.outcome, DaemonStartupOutcome.spawned);
    expect(platform.spawnedDaemons, ['/daemon']);
    expect(coordinator.spawnAttempts, 1);
  });

  test('concurrent callers share one ownership probe and one spawn', () async {
    final platform = FakePlatformServices();
    var probes = 0;
    final releaseProbe = Completer<void>();
    final coordinator = DaemonStartupCoordinator(
      api: _apiFor(platform, () async {
        probes++;
        await releaseProbe.future;
        throw http.ClientException('Connection refused');
      }),
      platform: platform,
      binaryPath: '/daemon',
    );

    final first = coordinator.ensureAvailable();
    final second = coordinator.ensureAvailable();
    releaseProbe.complete();
    final results = await Future.wait([first, second]);

    expect(
      results.map((r) => r.outcome),
      everyElement(DaemonStartupOutcome.spawned),
    );
    expect(probes, 1);
    expect(platform.spawnedDaemons, ['/daemon']);
    expect(coordinator.spawnAttempts, 1);
  });

  test(
    'the final spawn guard maps an occupied port without starting',
    () async {
      final platform = _ThrowingPlatformServices(
        spawnError: const DaemonPortOccupiedException(7842),
      );
      final coordinator = DaemonStartupCoordinator(
        api: _apiFor(
          platform,
          () async => throw http.ClientException('timed out'),
        ),
        platform: platform,
        binaryPath: '/daemon',
      );

      final result = await coordinator.ensureAvailable();

      expect(result.outcome, DaemonStartupOutcome.portOccupied);
      expect(platform.spawnedDaemons, ['/daemon']);
    },
  );

  test(
    'persistent spawn failures consume a bounded budget and keep the error',
    () async {
      final failure = StateError('binary is not executable');
      final platform = _ThrowingPlatformServices(spawnError: failure);
      final coordinator = DaemonStartupCoordinator(
        api: _apiFor(
          platform,
          () async => throw http.ClientException('Connection refused'),
        ),
        platform: platform,
        binaryPath: '/daemon',
        maxSpawnAttempts: 3,
      );

      expect(
        (await coordinator.ensureAvailable()).outcome,
        DaemonStartupOutcome.spawnFailedRetryable,
      );
      expect(
        (await coordinator.ensureAvailable()).outcome,
        DaemonStartupOutcome.spawnFailedRetryable,
      );
      final terminal = await coordinator.ensureAvailable();

      expect(terminal.outcome, DaemonStartupOutcome.spawnFailedTerminal);
      expect(terminal.error, same(failure));
      expect(platform.spawnedDaemons, ['/daemon', '/daemon', '/daemon']);
    },
  );

  test('reachability wins over an exhausted spawn budget', () async {
    var daemonNowOwnsPort = false;
    final platform = FakePlatformServices();
    final api = _apiFor(platform, () async {
      if (daemonNowOwnsPort) {
        return http.Response(
          '{"status":"starting"}',
          503,
          headers: daemonHeaders,
        );
      }
      throw http.ClientException('Connection refused');
    });
    final coordinator = DaemonStartupCoordinator(
      api: api,
      platform: platform,
      binaryPath: '/daemon',
      maxSpawnAttempts: 1,
    );

    expect(
      (await coordinator.ensureAvailable()).outcome,
      DaemonStartupOutcome.spawned,
    );
    daemonNowOwnsPort = true;
    expect(
      (await coordinator.ensureAvailable()).outcome,
      DaemonStartupOutcome.daemonPresent,
    );
    expect(platform.spawnedDaemons, ['/daemon']);
  });

  test(
    'successful spawns that never bind exhaust the budget without an N+1 call',
    () async {
      final platform = FakePlatformServices();
      final coordinator = DaemonStartupCoordinator(
        api: _apiFor(
          platform,
          () async => throw http.ClientException('Connection refused'),
        ),
        platform: platform,
        binaryPath: '/daemon',
        maxSpawnAttempts: 2,
      );

      expect(
        (await coordinator.ensureAvailable()).outcome,
        DaemonStartupOutcome.spawned,
      );
      expect(
        (await coordinator.ensureAvailable()).outcome,
        DaemonStartupOutcome.spawned,
      );
      expect(
        (await coordinator.ensureAvailable()).outcome,
        DaemonStartupOutcome.spawnBudgetExhausted,
      );
      expect(platform.spawnedDaemons, ['/daemon', '/daemon']);
    },
  );
}

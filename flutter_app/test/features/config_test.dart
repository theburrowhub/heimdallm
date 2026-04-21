import 'dart:async';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:mocktail/mocktail.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/config_model.dart';
import 'package:heimdallm/core/platform/platform_services_provider.dart';
import 'package:heimdallm/features/config/config_providers.dart';
import 'package:heimdallm/features/config/config_screen.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import '../core/platform/fake_platform_services.dart';

class MockApiClient extends Mock implements ApiClient {}

void main() {
  setUpAll(() {
    registerFallbackValue(<String, dynamic>{});
  });

  testWidgets('ConfigScreen shows current poll interval', (tester) async {
    const config = AppConfig(
      pollInterval: '5m',
      aiPrimary: 'claude',
      repoConfigs: {'org/repo': RepoConfig(prEnabled: true)},
    );

    final mockApi = MockApiClient();
    when(() => mockApi.fetchConfig()).thenAnswer((_) async => config.toJson());
    when(() => mockApi.updateConfig(any())).thenAnswer((_) async {});
    when(() => mockApi.checkHealth()).thenAnswer((_) async => false);

    await tester.pumpWidget(
      ProviderScope(
        overrides: [
          apiClientProvider.overrideWithValue(mockApi),
          configNotifierProvider.overrideWith(ConfigNotifier.new),
          platformServicesProvider.overrideWithValue(
            FakePlatformServices(),
          ),
        ],
        child: MaterialApp.router(
          routerConfig: GoRouter(routes: [
            GoRoute(path: '/', builder: (_, __) => const ConfigScreen()),
          ]),
        ),
      ),
    );
    await tester.pumpAndSettle();

    // Poll interval is still shown in Settings
    expect(find.text('5m'), findsAtLeastNWidgets(1));
    // Primary agent ('claude') moved to Agents tab — no longer in ConfigScreen
  });

  testWidgets('saveAndStartDaemon calls platform.spawnDaemon', (tester) async {
    final platform = FakePlatformServices(
      daemonBinaryPath: '/fake/bin/heimdalld',
      githubToken: 'fake-token',
    );
    final container = ProviderContainer(overrides: [
      platformServicesProvider.overrideWithValue(platform),
    ]);
    addTearDown(container.dispose);

    // Call saveAndStartDaemon via the notifier. We don't verify daemon health
    // (the fake's ApiClient isn't wired), but we do verify the spawn reached
    // the platform layer at least once before the health-check loop timed out.
    final notifier = container.read(configNotifierProvider.notifier);

    // Run in real-async mode so Future.delayed works without fake-async leaks.
    await tester.runAsync(() async {
      unawaited(notifier.saveAndStartDaemon(
        token: 'fake-gh-token',
        config: const AppConfig(),
        daemonBinaryPath: '/fake/bin/heimdalld',
      ));
      // Allow the microtasks that lead to the first spawnDaemon to run.
      await Future.delayed(const Duration(milliseconds: 50));
    });

    expect(platform.spawnedDaemons, contains('/fake/bin/heimdalld'));
  });

  testWidgets('saveAndStartDaemon routes daemon spawn through PlatformServices', (tester) async {
    final platform = FakePlatformServices(
      daemonBinaryPath: '/fake/bin/heimdalld',
      githubToken: 'fake-token',
    );
    final container = ProviderContainer(overrides: [
      platformServicesProvider.overrideWithValue(platform),
    ]);
    addTearDown(container.dispose);

    // Call saveAndStartDaemon via the notifier. We don't verify daemon health
    // (the fake's ApiClient isn't wired), but we do verify the spawn reached
    // the platform layer at least once before the health-check loop timed out.
    final notifier = container.read(configNotifierProvider.notifier);
    // Kick off the call but ignore its completion — we only care about the
    // side-effect of calling spawnDaemon.
    await tester.runAsync(() async {
      unawaited(notifier.saveAndStartDaemon(
        token: 'fake-gh-token',
        config: const AppConfig(),
        daemonBinaryPath: '/fake/bin/heimdalld',
      ));
      // Allow the microtasks that lead to the first spawnDaemon to run.
      await Future.delayed(const Duration(milliseconds: 50));
    });

    expect(platform.spawnedDaemons, contains('/fake/bin/heimdalld'));
  });
}

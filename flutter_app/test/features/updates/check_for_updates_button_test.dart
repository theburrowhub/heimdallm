import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/platform/platform_services.dart';
import 'package:heimdallm/core/platform/platform_services_provider.dart';
import 'package:heimdallm/features/updates/check_for_updates_button.dart';

import '../../core/platform/fake_platform_services.dart';

void main() {
  testWidgets('is hidden when native updates are unavailable', (tester) async {
    final platform = FakePlatformServices();
    await tester.pumpWidget(_app(platform));
    expect(find.byKey(const Key('check-for-updates')), findsNothing);
  });

  testWidgets('banner stays hidden when no update is actionable', (
    tester,
  ) async {
    final platform = FakePlatformServices(
      appUpdateSupport: AppUpdateSupport.native,
    );

    await tester.pumpWidget(_bannerApp(platform));
    await tester.pump();

    expect(find.byKey(const Key('app-update-banner')), findsNothing);
  });

  for (final phase in [AppUpdatePhase.installing, AppUpdatePhase.restarting]) {
    testWidgets('banner renders the busy $phase lifecycle without an action', (
      tester,
    ) async {
      final platform = FakePlatformServices(
        appUpdateSupport: AppUpdateSupport.native,
        appUpdateStatus: AppUpdateStatus(phase: phase),
      );

      await tester.pumpWidget(_bannerApp(platform));
      await tester.pump();

      expect(find.byKey(const Key('app-update-banner')), findsOneWidget);
      expect(find.byType(CircularProgressIndicator), findsOneWidget);
      expect(find.text('Heimdallm  is available.'), findsOneWidget);
      expect(find.byKey(const Key('install-app-update')), findsNothing);
    });
  }

  testWidgets('opens the native updater and prevents duplicate checks', (
    tester,
  ) async {
    final completion = Completer<void>();
    final platform = _BlockingUpdatePlatform(completion);
    await tester.pumpWidget(_app(platform));

    await tester.tap(find.byKey(const Key('check-for-updates')));
    await tester.pump();
    expect(platform.checkForAppUpdatesCalls, 1);
    expect(find.byType(CircularProgressIndicator), findsOneWidget);

    await tester.tap(find.byKey(const Key('check-for-updates')));
    expect(platform.checkForAppUpdatesCalls, 1);

    completion.complete();
    platform.emitAppUpdateStatus(const AppUpdateStatus.idle());
    await tester.pumpAndSettle();
    expect(find.byIcon(Icons.system_update_alt), findsOneWidget);
  });

  testWidgets('reports native updater failures and becomes retryable', (
    tester,
  ) async {
    final platform = _FailingUpdatePlatform();
    await tester.pumpWidget(_app(platform));

    await tester.tap(find.byKey(const Key('check-for-updates')));
    await tester.pumpAndSettle();

    expect(
      find.text('Could not check for updates: Bad state: bridge unavailable'),
      findsNothing,
    );
    expect(
      find.text('Could not update Heimdallm: Bad state: bridge unavailable'),
      findsOneWidget,
    );
    expect(find.byIcon(Icons.system_update_alt), findsOneWidget);
    expect(platform.checkForAppUpdatesCalls, 1);
  });

  testWidgets('banner and app-bar action install an available update', (
    tester,
  ) async {
    final platform = FakePlatformServices(
      appUpdateSupport: AppUpdateSupport.native,
      appUpdateStatus: const AppUpdateStatus(
        phase: AppUpdatePhase.available,
        version: '1.2.3',
        message: 'Heimdallm 1.2.3 is ready to install.',
      ),
    );
    await tester.pumpWidget(
      ProviderScope(
        overrides: [platformServicesProvider.overrideWithValue(platform)],
        child: const MaterialApp(
          home: Scaffold(
            body: Column(
              children: [AppUpdateBanner(), CheckForUpdatesButton()],
            ),
          ),
        ),
      ),
    );
    await tester.pump();

    expect(find.byKey(const Key('app-update-banner')), findsOneWidget);
    expect(find.text('Heimdallm 1.2.3 is ready to install.'), findsOneWidget);
    await tester.tap(find.byKey(const Key('install-app-update')));
    await tester.pump();
    expect(platform.installAppUpdateCalls, 1);

    await tester.tap(find.byKey(const Key('check-for-updates')));
    await tester.pump();
    expect(platform.installAppUpdateCalls, 2);
  });

  testWidgets('banner surfaces installation failures', (tester) async {
    final platform = _FailingInstallPlatform();
    await tester.pumpWidget(_bannerApp(platform));

    await tester.tap(find.byKey(const Key('install-app-update')));
    await tester.pumpAndSettle();

    expect(platform.installAppUpdateCalls, 1);
    expect(
      find.text('Update failed: Bad state: replacement failed'),
      findsOneWidget,
    );
  });
}

Widget _app(FakePlatformServices platform) => ProviderScope(
  overrides: [platformServicesProvider.overrideWithValue(platform)],
  child: const MaterialApp(home: Scaffold(body: CheckForUpdatesButton())),
);

Widget _bannerApp(FakePlatformServices platform) => ProviderScope(
  overrides: [platformServicesProvider.overrideWithValue(platform)],
  child: const MaterialApp(home: Scaffold(body: AppUpdateBanner())),
);

class _BlockingUpdatePlatform extends FakePlatformServices {
  _BlockingUpdatePlatform(this.completion)
    : super(appUpdateSupport: AppUpdateSupport.native);

  final Completer<void> completion;

  @override
  Future<void> checkForAppUpdates() {
    checkForAppUpdatesCalls++;
    emitAppUpdateStatus(
      const AppUpdateStatus(
        phase: AppUpdatePhase.checking,
        message: 'Checking for updates…',
      ),
    );
    return completion.future;
  }
}

class _FailingUpdatePlatform extends FakePlatformServices {
  _FailingUpdatePlatform() : super(appUpdateSupport: AppUpdateSupport.native);

  @override
  Future<void> checkForAppUpdates() async {
    checkForAppUpdatesCalls++;
    throw StateError('bridge unavailable');
  }
}

class _FailingInstallPlatform extends FakePlatformServices {
  _FailingInstallPlatform()
    : super(
        appUpdateSupport: AppUpdateSupport.native,
        appUpdateStatus: const AppUpdateStatus(
          phase: AppUpdatePhase.available,
          version: '1.2.3',
        ),
      );

  @override
  Future<void> installAppUpdate() async {
    installAppUpdateCalls++;
    throw StateError('replacement failed');
  }
}

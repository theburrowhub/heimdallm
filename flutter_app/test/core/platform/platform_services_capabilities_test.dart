import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/platform/platform_services.dart';

import 'fake_platform_services.dart';

void main() {
  test(
    'update status maps native values and fails closed on unknown phases',
    () {
      final installing = AppUpdateStatus.fromMap({
        'phase': 'installing',
        'version': 123,
        'message': StringBuffer('Replacing application'),
      });

      expect(installing.phase, AppUpdatePhase.installing);
      expect(installing.version, '123');
      expect(installing.message, 'Replacing application');
      expect(installing.busy, isTrue);

      final unknown = AppUpdateStatus.fromMap(const {'phase': 'future-phase'});
      expect(unknown.phase, AppUpdatePhase.idle);
      expect(unknown.version, isNull);
      expect(unknown.message, isNull);
    },
  );

  group('optional platform capabilities', () {
    test('deployment-managed platforms get safe updater defaults', () async {
      final PlatformServices platform = _PlatformOnly();

      expect(platform.appUpdateSupport, AppUpdateSupport.unavailable);
      expect(
        platform.appUpdateUnavailableReason,
        contains('package or deployment'),
      );
      expect(platform.appUpdateStatus.phase, AppUpdatePhase.idle);
      expect(await platform.appUpdateEvents.toList(), isEmpty);
      await platform.setupAppUpdater();
      expect(await platform.pendingAppUpdateVersion(), isNull);
      await platform.completeAppUpdate();
      await platform.finalizeAppUpdate();
      await expectLater(
        platform.checkForAppUpdates(),
        throwsA(isA<UnsupportedError>()),
      );
      await expectLater(
        platform.installAppUpdate(),
        throwsA(isA<UnsupportedError>()),
      );

      // Unsupported platforms have no desktop duplicate to terminate.
      platform.quitDuplicateInstance();
    });

    test(
      'desktop capabilities dispatch through a PlatformServices reference',
      () async {
        final fake = FakePlatformServices(
          appUpdateSupport: AppUpdateSupport.native,
          pendingUpdateVersion: '0.8.4',
        );
        final PlatformServices platform = fake;

        expect(platform.appUpdateSupport, AppUpdateSupport.native);
        expect(platform.appUpdateUnavailableReason, isNull);
        await platform.setupAppUpdater();
        await platform.checkForAppUpdates();
        await platform.installAppUpdate();
        expect(await platform.pendingAppUpdateVersion(), '0.8.4');
        await platform.completeAppUpdate();
        await platform.finalizeAppUpdate();
        platform.quitDuplicateInstance();

        expect(fake.setupAppUpdaterCalls, 1);
        expect(fake.checkForAppUpdatesCalls, 1);
        expect(fake.installAppUpdateCalls, 1);
        expect(fake.completeAppUpdateCalls, 1);
        expect(fake.finalizeAppUpdateCalls, 1);
        expect(fake.pendingUpdateVersion, isNull);
        expect(fake.quitDuplicateInstanceCalls, 1);
      },
    );
  });
}

class _PlatformOnly implements PlatformServices {
  @override
  dynamic noSuchMethod(Invocation invocation) => super.noSuchMethod(invocation);
}

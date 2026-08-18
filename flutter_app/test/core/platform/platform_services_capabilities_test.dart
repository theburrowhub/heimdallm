import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/platform/platform_services.dart';

import 'fake_platform_services.dart';

void main() {
  group('optional platform capabilities', () {
    test('deployment-managed platforms get safe updater defaults', () async {
      final PlatformServices platform = _PlatformOnly();

      expect(platform.appUpdateSupport, AppUpdateSupport.unavailable);
      await platform.setupAppUpdater();
      expect(await platform.pendingAppUpdateVersion(), isNull);
      await platform.completeAppUpdate();
      await expectLater(
        platform.checkForAppUpdates(),
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
        await platform.setupAppUpdater();
        await platform.checkForAppUpdates();
        expect(await platform.pendingAppUpdateVersion(), '0.8.4');
        await platform.completeAppUpdate();
        platform.quitDuplicateInstance();

        expect(fake.setupAppUpdaterCalls, 1);
        expect(fake.checkForAppUpdatesCalls, 1);
        expect(fake.completeAppUpdateCalls, 1);
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

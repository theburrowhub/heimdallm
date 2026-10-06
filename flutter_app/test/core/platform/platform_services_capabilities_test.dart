import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/merge_tracking.dart';
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
    test('notification modes reach only platforms that support them', () {
      const prefs = NotificationPreferences(update: NotificationMode.off);
      final fake = FakePlatformServices();
      (fake as PlatformServices).setNotificationModes(prefs);
      expect(fake.setNotificationModesCalls, [prefs]);

      // A platform without the capability (web) simply ignores them.
      expect(
        () => _PlatformOnly().setNotificationModes(prefs),
        returnsNormally,
      );
    });

    test('the tray My PRs section is a no-op without a tray', () async {
      final PlatformServices platform = _PlatformOnly();
      await platform.setTrayMyPrs(const [
        MergeTrackingEntry(prId: 1, repo: 'a/b', number: 1),
      ]);
    });

    test('a platform with a tray receives the My PRs section', () async {
      final fake = FakePlatformServices();
      final PlatformServices platform = fake;
      const entry = MergeTrackingEntry(prId: 1, repo: 'a/b', number: 1);
      await platform.setTrayMyPrs(const [entry]);
      expect(fake.trayMyPrs.single.single.number, 1);
    });

    test('deployment-managed platforms get safe updater defaults', () async {
      final PlatformServices platform = _PlatformOnly();

      expect((await platform.loadAppVersion()).version, 'unknown');
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

        expect((await platform.loadAppVersion()).version, '0.8.4');
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
        expect(fake.loadAppVersionCalls, 1);
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

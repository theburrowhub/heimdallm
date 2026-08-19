import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/pr.dart';
import 'package:heimdallm/core/platform/platform_services.dart';
import 'package:heimdallm/core/tray/tray_menu.dart';
import 'package:mocktail/mocktail.dart';
import 'package:tray_manager/tray_manager.dart';

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  const trayChannel = MethodChannel('tray_manager');
  late List<MethodCall> trayCalls;

  setUp(() {
    trayCalls = [];
    TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
        .setMockMethodCallHandler(trayChannel, (call) async {
          trayCalls.add(call);
          return null;
        });
  });

  tearDown(() {
    TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
        .setMockMethodCallHandler(trayChannel, null);
  });

  test('Quit delegates to the platform-owned termination callback', () {
    var quitCalls = 0;
    TrayMenu.instance.init(
      apiClient: _MockApiClient(),
      onNavigate: (_) {},
      onQuit: () => quitCalls++,
    );

    TrayMenu.instance.onTrayMenuItemClick(MenuItem(key: 'quit'));

    expect(quitCalls, 1);
  });

  test(
    'rebuild creates pending-review entries and the overflow action',
    () async {
      final prs = List.generate(
        8,
        (index) => PR(
          id: index + 1,
          githubId: 1000 + index,
          repo: 'theburrowhub/repo-$index',
          number: 700 + index,
          title: 'Pull request $index',
          author: 'contributor-$index',
          url:
              'https://github.com/theburrowhub/repo-$index/pull/${700 + index}',
          state: 'open',
          updatedAt: DateTime.utc(2026, 8, 18),
        ),
      );

      await TrayMenuRef.rebuild(prs: prs, me: 'reviewer');

      expect(
        trayCalls.where((call) => call.method == 'setContextMenu'),
        hasLength(1),
      );
    },
  );

  test('available update invokes the one-click tray action', () async {
    var installCalls = 0;
    TrayMenu.instance.init(
      apiClient: _MockApiClient(),
      onNavigate: (_) {},
      onQuit: () {},
      onCheckForUpdates: () {},
      onInstallUpdate: () => installCalls++,
    );

    await TrayMenu.instance.setUpdateState(
      const AppUpdateStatus(
        phase: AppUpdatePhase.available,
        version: '1.2.3',
        message: 'Heimdallm 1.2.3 is ready to install.',
      ),
    );
    TrayMenu.instance.onTrayMenuItemClick(MenuItem(key: 'update_now'));

    expect(installCalls, 1);
    expect(
      trayCalls.where((call) => call.method == 'setContextMenu'),
      hasLength(1),
    );
  });
}

class _MockApiClient extends Mock implements ApiClient {}

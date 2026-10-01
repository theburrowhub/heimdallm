import 'package:flutter/services.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/merge_tracking.dart';
import 'package:heimdallm/core/models/pr.dart';
import 'package:heimdallm/core/platform/platform_services.dart';
import 'package:heimdallm/core/tray/tray_menu.dart';
import 'package:mocktail/mocktail.dart';
import 'package:tray_manager/tray_manager.dart';

void main() {
  TestWidgetsFlutterBinding.ensureInitialized();

  const trayChannel = MethodChannel('tray_manager');
  const windowChannel = MethodChannel('window_manager');
  late List<MethodCall> trayCalls;
  late List<MethodCall> windowCalls;

  setUp(() {
    trayCalls = [];
    windowCalls = [];
    TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
        .setMockMethodCallHandler(trayChannel, (call) async {
          trayCalls.add(call);
          return null;
        });
    TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
        .setMockMethodCallHandler(windowChannel, (call) async {
          windowCalls.add(call);
          if (call.method == 'isMinimized') return false;
          return null;
        });
  });

  tearDown(() {
    TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
        .setMockMethodCallHandler(trayChannel, null);
    TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
        .setMockMethodCallHandler(windowChannel, null);
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

  test(
    'enabled updater exposes a check action and dispatches it',
    () async {
      var checkCalls = 0;
      TrayMenu.instance.init(
        apiClient: _MockApiClient(),
        onNavigate: (_) {},
        onQuit: () {},
        onCheckForUpdates: () => checkCalls++,
        onInstallUpdate: () {},
        currentVersion: '0.8.4 (build 546)',
      );

      await TrayMenu.instance.setUpdateState(const AppUpdateStatus.idle());
      final items = _latestMenuItems(trayCalls);

      expect(
        items,
        contains(containsPair('label', 'Heimdallm 0.8.4 (build 546)')),
      );
      expect(items, contains(containsPair('label', 'Check for Updates…')));
      TrayMenu.instance.onTrayMenuItemClick(MenuItem(key: 'check_updates'));
      expect(checkCalls, 1);
    },
  );

  test('tray keeps the successful up-to-date result visible', () async {
    TrayMenu.instance.init(
      apiClient: _MockApiClient(),
      onNavigate: (_) {},
      onQuit: () {},
      onCheckForUpdates: () {},
      onInstallUpdate: () {},
      currentVersion: '0.8.4',
    );

    await TrayMenu.instance.setUpdateState(
      const AppUpdateStatus.idle(message: 'Heimdallm is up to date.'),
    );
    final items = _latestMenuItems(trayCalls);

    expect(
      items,
      contains(containsPair('label', '✓  Heimdallm is up to date.')),
    );
    expect(items, contains(containsPair('label', 'Check for Updates…')));
  });

  test(
    'tray keeps an update check error visible and retryable',
    () async {
      TrayMenu.instance.init(
        apiClient: _MockApiClient(),
        onNavigate: (_) {},
        onQuit: () {},
        onCheckForUpdates: () {},
        onInstallUpdate: () {},
        currentVersion: '0.8.4',
      );

      await TrayMenu.instance.setUpdateState(
        const AppUpdateStatus(
          phase: AppUpdatePhase.error,
          message: 'Could not load the signed update feed (HTTP 404).',
        ),
      );
      final items = _latestMenuItems(trayCalls);

      expect(
        items,
        contains(
          containsPair(
            'label',
            '⚠  Could not load the signed update feed (HTTP 404).',
          ),
        ),
      );
      expect(items, contains(containsPair('key', 'check_updates')));
    },
  );

  test('macOS installation exposes normal update controls in the tray', () async {
    var checkCalls = 0;
    TrayMenu.instance.init(
      apiClient: _MockApiClient(),
      onNavigate: (_) {},
      onQuit: () {},
      onCheckForUpdates: () => checkCalls++,
      onInstallUpdate: () {},
      currentVersion: '0.8.4',
    );

    await TrayMenu.instance.setUpdateState(const AppUpdateStatus.idle());
    final items = _latestMenuItems(trayCalls);

    expect(items, contains(containsPair('label', 'Heimdallm 0.8.4')));
    expect(items, contains(containsPair('key', 'check_updates')));
    expect(items, isNot(contains(containsPair('key', 'updates_unavailable'))));

    TrayMenu.instance.onTrayMenuItemClick(MenuItem(key: 'check_updates'));
    expect(checkCalls, 1);
  });

  test('tray explains why application updates are unavailable', () async {
    TrayMenu.instance.init(
      apiClient: _MockApiClient(),
      onNavigate: (_) {},
      onQuit: () {},
      currentVersion: '0.8.4',
      updateUnavailableReason: 'Updater initialization failed.',
    );

    await TrayMenu.instance.setUpdateState(const AppUpdateStatus.idle());
    final items = _latestMenuItems(trayCalls);

    expect(
      items,
      contains(
        containsPair(
          'label',
          'Updates unavailable — Updater initialization failed.',
        ),
      ),
    );
  });

  test('busy updater exposes a disabled progress item', () async {
    TrayMenu.instance.init(
      apiClient: _MockApiClient(),
      onNavigate: (_) {},
      onQuit: () {},
      onCheckForUpdates: () {},
      onInstallUpdate: () {},
    );

    await TrayMenu.instance.setUpdateState(
      const AppUpdateStatus(phase: AppUpdatePhase.checking),
    );
    final items = _latestMenuItems(trayCalls);

    expect(
      items,
      contains(
        allOf(
          containsPair('key', 'update_busy'),
          containsPair('label', 'Updating Heimdallm…'),
          containsPair('disabled', true),
        ),
      ),
    );
  });

  group('Your PRs', () {
    MergeTrackingEntry pr(int n, String attention, {String reason = ''}) =>
        MergeTrackingEntry(
          prId: n,
          repo: 'acme/widgets',
          number: n,
          title: 'PR $n',
          url: 'https://github.com/acme/widgets/pull/$n',
          attention: attention,
          blockReason: reason,
        );

    setUp(() {
      TrayMenu.instance.init(
        apiClient: _MockApiClient(),
        onNavigate: (_) {},
        onQuit: () {},
      );
    });
    tearDown(() => TrayMenu.instance.setMyPrs(const []));

    test('lists the PRs that need the operator with a summary line', () async {
      await TrayMenu.instance.setMyPrs([
        pr(1, 'action', reason: 'checks_failing'),
        pr(2, 'ready'),
      ]);

      final labels = _latestMenuItems(trayCalls).map((i) => i['label']).toList();
      expect(labels, contains('⚑  Your PRs: 1 needs your action · 1 ready to merge'));
      expect(labels, contains('●   #1  widgets  —  CI failing'));
      expect(labels, contains('●   #2  widgets  —  ready to merge'));
    });

    test('caps the list and offers the tab for the rest', () async {
      final navigated = <String>[];
      TrayMenu.instance.init(
        apiClient: _MockApiClient(),
        onNavigate: navigated.add,
        onQuit: () {},
      );
      await TrayMenu.instance.setMyPrs([
        for (var i = 1; i <= 7; i++) pr(i, 'action'),
      ]);

      final items = _latestMenuItems(trayCalls);
      expect(items.where((i) => '${i['key']}'.startsWith('mypr_')), hasLength(5));
      expect(items.map((i) => i['label']), contains('   + 2 more…'));

      TrayMenu.instance.onTrayMenuItemClick(MenuItem(key: 'open_my_prs'));
      await Future<void>.delayed(const Duration(milliseconds: 250));
      expect(navigated, ['/merge']);
    });

    test('no section when nothing needs the operator', () async {
      await TrayMenu.instance.setMyPrs(const []);
      final labels = _latestMenuItems(trayCalls).map((i) => '${i['label']}');
      expect(labels.where((l) => l.contains('Your PRs')), isEmpty);
    });

    test('clicking a PR opens it on GitHub', () async {
      const channel = MethodChannel('plugins.flutter.io/url_launcher');
      final launched = <String>[];
      TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
          .setMockMethodCallHandler(channel, (call) async {
            if (call.method == 'launch') {
              launched.add((call.arguments as Map)['url'] as String);
            }
            return true;
          });
      addTearDown(
        () => TestDefaultBinaryMessengerBinding.instance.defaultBinaryMessenger
            .setMockMethodCallHandler(channel, null),
      );
      await TrayMenu.instance.setMyPrs([pr(9, 'ready')]);

      TrayMenu.instance.onTrayMenuItemClick(MenuItem(key: 'mypr_0'));
      TrayMenu.instance.onTrayMenuItemClick(MenuItem(key: 'mypr_5'));
      await Future<void>.delayed(Duration.zero);
      expect(launched, ['https://github.com/acme/widgets/pull/9']);
    });
  });

  test('Open shows and focuses the desktop window', () async {
    TrayMenu.instance.onTrayMenuItemClick(MenuItem(key: 'open'));
    await Future<void>.delayed(Duration.zero);

    expect(
      windowCalls.map((call) => call.method),
      containsAll(['show', 'focus']),
    );
  });
}

List<Map<Object?, Object?>> _latestMenuItems(List<MethodCall> calls) {
  final call = calls.lastWhere((item) => item.method == 'setContextMenu');
  final arguments = call.arguments as Map<Object?, Object?>;
  final menu = arguments['menu'] as Map<Object?, Object?>;
  return (menu['items'] as List<Object?>).cast<Map<Object?, Object?>>();
}

class _MockApiClient extends Mock implements ApiClient {}

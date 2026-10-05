import 'dart:async';

import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/api/sse_client.dart';
import 'package:heimdallm/core/models/config_model.dart';
import 'package:heimdallm/core/models/merge_tracking.dart';
import 'package:heimdallm/core/models/notification_mode.dart';
import 'package:heimdallm/core/platform/platform_services_provider.dart';
import 'package:heimdallm/features/config/config_providers.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/features/merge_tracking/merge_tracking_providers.dart';
import 'package:shared_preferences/shared_preferences.dart';

import '../../core/platform/fake_platform_services.dart';

class _FixedConfig extends ConfigNotifier {
  _FixedConfig(this.config);
  final AppConfig config;

  @override
  Future<AppConfig> build() async => config;
}

MergeTrackingEntry _pr(
  int n, {
  String attention = 'none',
  bool stale = false,
  String phase = 'blocked',
  String blockReason = '',
  DateTime? lastActivityAt,
}) => MergeTrackingEntry(
  prId: n,
  repo: 'acme/widgets',
  number: n,
  title: 'PR $n',
  url: 'https://github.com/acme/widgets/pull/$n',
  phase: phase,
  attention: attention,
  stale: stale,
  blockReason: blockReason,
  lastActivityAt: lastActivityAt,
);

ProviderContainer _container({
  required FakePlatformServices platform,
  MyPrsConfig myPrs = const MyPrsConfig(),
  List<MergeTrackingEntry> entries = const [],
  DateTime? now,
  Stream<SseEvent>? sse,
}) {
  final container = ProviderContainer(
    overrides: [
      platformServicesProvider.overrideWithValue(platform),
      configNotifierProvider.overrideWith(
        () => _FixedConfig(AppConfig(myPrs: myPrs)),
      ),
      mergeTrackingProvider.overrideWith((ref) async => entries),
      if (now != null) myPrsClockProvider.overrideWithValue(() => now),
      sseStreamProvider.overrideWith((ref) => sse ?? const Stream.empty()),
    ],
  );
  addTearDown(container.dispose);
  return container;
}

Future<void> _settle() => Future<void>.delayed(const Duration(milliseconds: 20));

void main() {
  setUp(() => SharedPreferences.setMockInitialValues({}));

  group('transition notifications', () {
    Future<FakePlatformServices> deliver(
      SseEvent event, {
      bool notify = true,
      bool enabled = true,
      NotificationMode mode = NotificationMode.sound,
    }) async {
      final platform = FakePlatformServices()
        ..setNotificationModes(NotificationPreferences(activity: mode));
      final controller = StreamController<SseEvent>();
      addTearDown(controller.close);
      final container = _container(
        platform: platform,
        myPrs: MyPrsConfig(enabled: enabled, notifyTransitions: notify),
        sse: controller.stream,
      );
      await container.read(configNotifierProvider.future);
      container.listen(mergeTrackingSseListenerProvider, (_, _) {});
      controller.add(event);
      await _settle();
      return platform;
    }

    test('a PR that needs the operator raises a notification', () async {
      final platform = await deliver(
        const SseEvent(
          type: 'my_pr_attention',
          data:
              '{"repo":"acme/widgets","number":7,"title":"Add cache",'
              '"attention":"action","reason":"changes_requested"}',
        ),
      );
      expect(platform.notifications, hasLength(1));
      expect(platform.notifications.single.title, 'Changes requested on your PR');
      expect(platform.notifications.single.body, 'acme/widgets#7 — Add cache');
    });

    test('ready and stale transitions have their own wording', () async {
      final ready = await deliver(
        const SseEvent(
          type: 'my_pr_attention',
          data: '{"repo":"a/b","number":1,"attention":"ready"}',
        ),
      );
      expect(ready.notifications.single.title, 'Your PR is ready to merge');

      final stale = await deliver(
        const SseEvent(
          type: 'my_pr_stale',
          data: '{"repo":"a/b","number":2,"title":"T","idle_seconds":345600}',
        ),
      );
      expect(stale.notifications.single.title, 'Your PR has gone quiet for 4d');
    });

    test('nothing is sent unless the operator opted in', () async {
      const event = SseEvent(
        type: 'my_pr_attention',
        data: '{"repo":"a/b","number":1,"attention":"action"}',
      );
      expect((await deliver(event, notify: false)).notifications, isEmpty);
      expect((await deliver(event, enabled: false)).notifications, isEmpty);
    });

    test('the device notification mode applies to My PRs alerts', () async {
      const event = SseEvent(
        type: 'my_pr_attention',
        data: '{"repo":"a/b","number":1,"attention":"action"}',
      );
      final silent = await deliver(event, mode: NotificationMode.silent);
      expect(silent.notifications.single.silent, isTrue);
      expect(
        silent.notifications.single.category,
        NotificationCategory.activity,
      );
      expect(
        (await deliver(event, mode: NotificationMode.off)).notifications,
        isEmpty,
      );
    });

    test('a malformed payload is ignored', () {
      expect(
        myPrTransitionNotification(
          const SseEvent(type: 'my_pr_attention', data: 'not json'),
        ),
        isNull,
      );
    });

    test('my_pr events refresh the listing', () async {
      final controller = StreamController<SseEvent>();
      addTearDown(controller.close);
      final container = _container(
        platform: FakePlatformServices(),
        sse: controller.stream,
      );
      container.listen(mergeTrackingSseListenerProvider, (_, _) {});
      for (final type in ['my_pr_attention', 'my_pr_stale']) {
        final before = container.read(mergeTrackingRefreshProvider);
        controller.add(SseEvent(type: type, data: '{"repo":"a/b","number":1}'));
        await _settle();
        expect(
          container.read(mergeTrackingRefreshProvider),
          isNot(before),
          reason: type,
        );
      }
    });
  });

  group('daily digest', () {
    final entries = [
      _pr(1, attention: 'action', blockReason: 'checks_failing'),
      _pr(2, attention: 'ready'),
      _pr(
        3,
        attention: 'waiting',
        stale: true,
        lastActivityAt: DateTime(2026, 9, 26, 9),
      ),
      _pr(4, attention: 'waiting'),
    ];

    Future<(ProviderContainer, FakePlatformServices)> digestAt(
      DateTime now, {
      MyPrsConfig myPrs = const MyPrsConfig(),
      List<MergeTrackingEntry>? list,
    }) async {
      final platform = FakePlatformServices();
      final container = _container(
        platform: platform,
        myPrs: myPrs,
        entries: list ?? entries,
        now: now,
      );
      await container.read(configNotifierProvider.future);
      await container.read(mergeTrackingProvider.future);
      container.listen(myPrsDigestProvider, (_, _) {});
      await container.read(myPrsDigestProvider.notifier).check();
      return (container, platform);
    }

    test('fires once at the configured time with the summary', () async {
      final (container, platform) = await digestAt(DateTime(2026, 9, 30, 10, 5));
      expect(platform.notifications, hasLength(1));
      final n = platform.notifications.single;
      expect(n.title, 'Your PRs: 1 needs your action · 1 ready to merge · 1 stale');
      expect(n.body, contains('acme/widgets#1 — CI failing'));
      expect(n.body, contains('acme/widgets#2 — ready to merge'));
      expect(n.body, contains('acme/widgets#3 — stale · 4d'));
      expect(n.body, isNot(contains('#4')));

      await container.read(myPrsDigestProvider.notifier).check();
      expect(platform.notifications, hasLength(1), reason: 'once a day');
      final prefs = await SharedPreferences.getInstance();
      expect(prefs.getString('my_prs_last_digest'), '2026-09-30');
    });

    test('does not fire before the configured time', () async {
      final (_, platform) = await digestAt(DateTime(2026, 9, 30, 9, 59));
      expect(platform.notifications, isEmpty);
    });

    test('catches up on launch when the time passed while closed', () async {
      final (_, platform) = await digestAt(DateTime(2026, 9, 30, 18, 0));
      expect(platform.notifications, hasLength(1));
    });

    test('a digest already sent today (previous launch) is not repeated', () async {
      SharedPreferences.setMockInitialValues({'my_prs_last_digest': '2026-09-30'});
      final (_, platform) = await digestAt(DateTime(2026, 9, 30, 11, 0));
      expect(platform.notifications, isEmpty);
    });

    test('yesterday\'s digest does not suppress today\'s', () async {
      SharedPreferences.setMockInitialValues({'my_prs_last_digest': '2026-09-29'});
      final (_, platform) = await digestAt(DateTime(2026, 9, 30, 11, 0));
      expect(platform.notifications, hasLength(1));
    });

    test('a quiet day sends nothing and is marked done', () async {
      final (container, platform) = await digestAt(
        DateTime(2026, 9, 30, 10, 30),
        list: [_pr(4, attention: 'waiting')],
      );
      expect(platform.notifications, isEmpty);
      expect(container.read(myPrsDigestProvider), '2026-09-30');
    });

    test('respects the switches and the configured time', () async {
      final off = await digestAt(
        DateTime(2026, 9, 30, 12),
        myPrs: const MyPrsConfig(digestEnabled: false),
      );
      expect(off.$2.notifications, isEmpty);

      final watchOff = await digestAt(
        DateTime(2026, 9, 30, 12),
        myPrs: const MyPrsConfig(enabled: false),
      );
      expect(watchOff.$2.notifications, isEmpty);

      final late = await digestAt(
        DateTime(2026, 9, 30, 12),
        myPrs: const MyPrsConfig(digestTime: '17:30'),
      );
      expect(late.$2.notifications, isEmpty);
    });

    test('the body caps the list and says how many more', () {
      final many = [
        for (var i = 1; i <= 5; i++) _pr(i, attention: 'action'),
      ];
      final body = myPrsDigestBody(many, now: DateTime(2026, 9, 30));
      expect(body.split('\n'), hasLength(4));
      expect(body, endsWith('+ 2 more'));
    });
  });

  test('the tray gets the PRs that need the operator, most urgent first', () async {
    final platform = FakePlatformServices();
    final container = _container(
      platform: platform,
      entries: [
        _pr(1, attention: 'waiting', stale: true),
        _pr(2, attention: 'waiting'),
        _pr(3, attention: 'ready'),
        _pr(4, attention: 'action'),
        _pr(5, phase: 'merged', attention: 'action'),
      ],
    );
    container.listen(myPrsTraySyncProvider, (_, _) {});
    await container.read(mergeTrackingProvider.future);
    await _settle();

    expect(platform.trayMyPrs, isNotEmpty);
    expect(platform.trayMyPrs.last.map((e) => e.number), [4, 3, 1]);
  });
}

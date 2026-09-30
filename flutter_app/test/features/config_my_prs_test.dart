import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/config_model.dart';
import 'package:heimdallm/features/config/config_providers.dart'
    show computeGlobalDiffForTest;
import 'package:heimdallm/features/config/config_screen.dart'
    show myPrsDigestTimeError, myPrsStaleAfterError;

void main() {
  test('MyPrsConfig defaults match the daemon', () {
    final cfg = MyPrsConfig.fromJson(const {});
    expect(cfg, const MyPrsConfig());
    expect(cfg.enabled, isTrue);
    expect(cfg.includeAssigned, isTrue);
    expect(cfg.staleAfter, '3d');
    expect(cfg.notifyTransitions, isFalse);
    expect(cfg.digestEnabled, isTrue);
    expect(cfg.digestTime, '10:00');
    expect(AppConfig.fromJson(const {}).myPrs, const MyPrsConfig());
  });

  test('MyPrsConfig round-trips through JSON', () {
    const cfg = MyPrsConfig(
      enabled: false,
      includeAssigned: false,
      staleAfter: '12h',
      notifyTransitions: true,
      digestEnabled: false,
      digestTime: '08:30',
    );
    expect(MyPrsConfig.fromJson(cfg.toJson()), cfg);
    expect(
      AppConfig.fromJson({'my_prs': cfg.toJson()}).myPrs,
      cfg,
    );
  });

  test('parseHumanDuration mirrors the daemon', () {
    expect(parseHumanDuration('3d'), const Duration(days: 3));
    expect(parseHumanDuration('1.5d'), const Duration(hours: 36));
    expect(parseHumanDuration('90m'), const Duration(minutes: 90));
    expect(parseHumanDuration('1h30m'), const Duration(minutes: 90));
    expect(parseHumanDuration('45s'), const Duration(seconds: 45));
    expect(parseHumanDuration('0'), isNull);
    expect(parseHumanDuration(''), isNull);
    expect(parseHumanDuration('3'), isNull);
    expect(parseHumanDuration('3 days'), isNull);
    expect(parseHumanDuration('3w'), isNull);
  });

  test('digestHourMinute validates 24h HH:MM', () {
    expect(const MyPrsConfig(digestTime: '07:05').digestHourMinute, (hour: 7, minute: 5));
    expect(const MyPrsConfig(digestTime: '24:00').digestHourMinute, isNull);
    expect(const MyPrsConfig(digestTime: '7pm').digestHourMinute, isNull);
  });

  test('form validators', () {
    expect(myPrsStaleAfterError('3d'), isNull);
    expect(myPrsStaleAfterError('0'), isNull);
    expect(myPrsStaleAfterError(''), isNotNull);
    expect(myPrsStaleAfterError('soon'), isNotNull);
    expect(myPrsDigestTimeError('10:00'), isNull);
    expect(myPrsDigestTimeError('10'), isNotNull);
  });

  test('every my_prs field reaches the patch when it changes, and only then', () {
    const old = AppConfig(pollInterval: '5m', aiPrimary: 'claude');
    final cases = <String, MyPrsConfig>{
      'enabled': const MyPrsConfig(enabled: false),
      'include_assigned': const MyPrsConfig(includeAssigned: false),
      'stale_after': const MyPrsConfig(staleAfter: '12h'),
      'notify_transitions': const MyPrsConfig(notifyTransitions: true),
      'digest_enabled': const MyPrsConfig(digestEnabled: false),
      'digest_time': const MyPrsConfig(digestTime: '09:15'),
    };
    for (final entry in cases.entries) {
      final diff = computeGlobalDiffForTest(old, old.copyWith(myPrs: entry.value));
      final section = diff['my_prs'] as Map<String, dynamic>?;
      expect(section, isNotNull, reason: entry.key);
      expect(section!.keys, [entry.key], reason: entry.key);
      expect(section[entry.key], entry.value.toJson()[entry.key]);
    }
    expect(computeGlobalDiffForTest(old, old)['my_prs'], isNull);
  });
}

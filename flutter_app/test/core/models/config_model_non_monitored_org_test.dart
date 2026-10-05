import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/config_model.dart';

// theburrowhub/heimdallm#828: a bare org name (no slash) in the daemon's
// `non_monitored` list excludes an entire org. These tests pin how the
// Flutter model parses that entry and exposes it back to the UI.
void main() {
  group('AppConfig.fromJson — bare org non_monitored entries', () {
    test('a bare org entry is not treated as a fake repo', () {
      final config = AppConfig.fromJson({
        'repositories': ['otherorg/repo1'],
        'non_monitored': ['myorg'],
      });

      expect(config.repoConfigs.containsKey('myorg'), isFalse);
    });

    test('a bare org entry is exposed via nonMonitoredOrgs', () {
      final config = AppConfig.fromJson({
        'repositories': ['otherorg/repo1'],
        'non_monitored': ['myorg'],
      });

      expect(config.nonMonitoredOrgs, equals(['myorg']));
    });

    test('exact owner/repo entries still populate repoConfigs as before', () {
      final config = AppConfig.fromJson({
        'repositories': ['otherorg/repo1'],
        'non_monitored': ['otherorg/repo2'],
      });

      expect(config.repoConfigs.containsKey('otherorg/repo2'), isTrue);
      expect(config.nonMonitoredOrgs, isEmpty);
    });
  });

  group('AppConfig.knownOrganizations — bare org entries', () {
    test('an org with no known repos still appears via a bare entry', () {
      final config = AppConfig.fromJson({
        'repositories': <String>[],
        'non_monitored': ['myorg'],
      });

      expect(config.knownOrganizations, contains('myorg'));
    });
  });

  group('AppConfig.nonMonitoredList', () {
    test('combines bare org entries with per-repo non-monitored entries', () {
      final config = AppConfig.fromJson({
        'repositories': ['otherorg/repo1'],
        'non_monitored': ['myorg', 'otherorg/repo2'],
      });

      expect(
        config.nonMonitoredList,
        equals(['myorg', 'otherorg/repo2']),
      );
    });
  });

  group('RepoConfig.excludedByOrg / isEffectivelyMonitored', () {
    test('a repo in a bare-excluded org is flagged, even if isMonitored', () {
      final config = AppConfig.fromJson({
        'repositories': ['myorg/repo1', 'otherorg/repo1'],
        'non_monitored': ['myorg'],
      });

      final excluded = config.repoConfigs['myorg/repo1']!;
      expect(excluded.isMonitored, isTrue);
      expect(excluded.excludedByOrg, isTrue);
      expect(excluded.isEffectivelyMonitored, isFalse);

      final unaffected = config.repoConfigs['otherorg/repo1']!;
      expect(unaffected.excludedByOrg, isFalse);
      expect(unaffected.isEffectivelyMonitored, isTrue);
    });

    test('org matching for the exclusion flag is case-insensitive', () {
      final config = AppConfig.fromJson({
        'repositories': ['MyOrg/repo1'],
        'non_monitored': ['myorg'],
      });

      expect(config.repoConfigs['MyOrg/repo1']!.excludedByOrg, isTrue);
    });

    test('copyWith preserves excludedByOrg unless explicitly overridden', () {
      final config = AppConfig.fromJson({
        'repositories': ['myorg/repo1'],
        'non_monitored': ['myorg'],
      });
      final original = config.repoConfigs['myorg/repo1']!;

      final edited = original.copyWith(prEnabled: false);
      expect(edited.excludedByOrg, isTrue);
    });

    test('a repo not monitored at all is unaffected by excludedByOrg', () {
      final config = AppConfig.fromJson({
        'repositories': <String>[],
        'non_monitored': ['myorg', 'otherorg/repo1'],
      });

      final notMonitored = config.repoConfigs['otherorg/repo1']!;
      expect(notMonitored.isMonitored, isFalse);
      expect(notMonitored.excludedByOrg, isFalse);
      expect(notMonitored.isEffectivelyMonitored, isFalse);
    });
  });

  group('AppConfig.isOrgNonMonitored', () {
    test('matches case-insensitively', () {
      final config = AppConfig.fromJson({
        'repositories': <String>[],
        'non_monitored': ['MyOrg'],
      });

      expect(config.isOrgNonMonitored('myorg'), isTrue);
      expect(config.isOrgNonMonitored('MYORG'), isTrue);
      expect(config.isOrgNonMonitored('otherorg'), isFalse);
    });
  });
}

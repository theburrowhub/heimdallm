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
}

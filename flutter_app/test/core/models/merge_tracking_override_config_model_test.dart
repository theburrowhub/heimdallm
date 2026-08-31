import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/config_model.dart';

void main() {
  group('MergeTrackingOverride', () {
    test('round-trips every overridable field', () {
      final override = MergeTrackingOverride.fromJson({
        'enabled': false,
        'enable_auto_merge': true,
        'update_branch': false,
        'resolve_conflicts': true,
        'merge': false,
        'merge_method': 'rebase',
        'include_assigned': true,
        'require_approval': false,
        'max_update_attempts': 9,
        'max_resolve_attempts': 8,
        'max_merge_attempts': 7,
        'action_cooldown': '1m',
        'resolve_timeout': '45m',
        'resolve_effort': 'max',
      });

      expect(override.enabled, isFalse);
      expect(override.mergeMethod, 'rebase');
      expect(override.maxResolveAttempts, 8);
      expect(override.isEmpty, isFalse);
      expect(override.toJson(), {
        'enabled': false,
        'enable_auto_merge': true,
        'update_branch': false,
        'resolve_conflicts': true,
        'merge': false,
        'merge_method': 'rebase',
        'include_assigned': true,
        'require_approval': false,
        'max_update_attempts': 9,
        'max_resolve_attempts': 8,
        'max_merge_attempts': 7,
        'action_cooldown': '1m',
        'resolve_timeout': '45m',
        'resolve_effort': 'max',
      });
    });

    test('null and empty strings both mean inherit', () {
      final override = MergeTrackingOverride.fromJson({
        'merge_method': '',
        'action_cooldown': '',
        'resolve_timeout': '',
        'resolve_effort': '',
      });

      expect(override.isEmpty, isTrue);
      expect(override.toJson(), isEmpty);
    });

    test('copyWith and differential patch retain explicit deletes', () {
      const before = MergeTrackingOverride(
        enabled: false,
        mergeMethod: 'rebase',
        maxMergeAttempts: 4,
      );
      final after = before.copyWith(
        enabled: null,
        mergeMethod: '',
        maxMergeAttempts: null,
        updateBranch: true,
      );

      expect(diffMergeTrackingOverrides(before, after), {
        'enabled': null,
        'update_branch': true,
        'merge_method': null,
        'max_merge_attempts': null,
      });
    });
  });

  test('global config parses and retains complete org and repo maps', () {
    final config = MergeTrackingConfig.fromJson({
      'enabled': true,
      'orgs': {
        'acme': {
          'enabled': false,
          'enable_auto_merge': true,
          'resolve_effort': 'max',
        },
      },
      'repos': {
        'acme/widgets': {
          'merge': true,
          'merge_method': 'merge',
          'max_update_attempts': 6,
        },
      },
    });

    expect(config.orgs['acme']?.enabled, isFalse);
    expect(config.orgs['acme']?.enableAutoMerge, isTrue);
    expect(config.repos['acme/widgets']?.merge, isTrue);
    expect(config.repos['acme/widgets']?.maxUpdateAttempts, 6);

    final roundTrip = MergeTrackingConfig.fromJson(config.toJson());
    expect(roundTrip.orgs['acme']?.resolveEffort, 'max');
    expect(roundTrip.repos['acme/widgets']?.mergeMethod, 'merge');
  });

  test('AppConfig folds complete scoped overrides without inventing repos', () {
    final config = AppConfig.fromJson({
      'repositories': ['acme/widgets'],
      'merge_tracking': {
        'orgs': {
          'acme': {'update_branch': true, 'action_cooldown': '2m'},
        },
        'repos': {
          'acme/widgets': {
            'enabled': false,
            'merge_method': 'rebase',
            'max_resolve_attempts': 5,
          },
          'acme/not-a-member': {'merge': true},
        },
      },
    });

    expect(config.orgConfigs['acme']?.mergeTracking.updateBranch, isTrue);
    expect(config.orgConfigs['acme']?.mergeTracking.actionCooldown, '2m');
    expect(config.orgConfigs['acme']?.hasOverride, isTrue);
    expect(config.knownOrganizations, contains('acme'));
    expect(config.repoConfigs['acme/widgets']?.mtEnabled, isFalse);
    expect(
      config.repoConfigs['acme/widgets']?.mergeTracking.mergeMethod,
      'rebase',
    );
    expect(
      config.repoConfigs['acme/widgets']?.mergeTracking.maxResolveAttempts,
      5,
    );
    expect(config.repoConfigs, isNot(contains('acme/not-a-member')));
    expect(config.mergeTracking.repos['acme/not-a-member']?.merge, isTrue);
  });

  test('legacy mtEnabled construction and copy remain compatible', () {
    const legacyRepo = RepoConfig(mtEnabled: false);
    const legacyOrg = OrgConfig(mtEnabled: true);
    expect(legacyRepo.mtEnabled, isFalse);
    expect(legacyRepo.mergeTracking.enabled, isFalse);
    expect(legacyOrg.mtEnabled, isTrue);
    expect(legacyOrg.mergeTracking.enabled, isTrue);

    const detailed = RepoConfig(
      mergeTracking: MergeTrackingOverride(enabled: true, merge: true),
    );
    final inherited = detailed.copyWith(mtEnabled: null);
    expect(inherited.mtEnabled, isNull);
    expect(inherited.mergeTracking.merge, isTrue);
  });

  test('applyOverride resolves scoped values but keeps global-only limits', () {
    const global = MergeTrackingConfig(
      enabled: true,
      updateBranch: false,
      mergeMethod: 'squash',
      pollInterval: '3m',
      maxPrsPerTick: 11,
      maxMergeAttempts: 3,
    );

    final resolved = global.applyOverride(
      const MergeTrackingOverride(
        enabled: false,
        updateBranch: true,
        mergeMethod: 'rebase',
        maxMergeAttempts: 8,
      ),
    );
    expect(resolved.enabled, isFalse);
    expect(resolved.updateBranch, isTrue);
    expect(resolved.mergeMethod, 'rebase');
    expect(resolved.maxMergeAttempts, 8);
    expect(resolved.pollInterval, '3m');
    expect(resolved.maxPrsPerTick, 11);
  });
}

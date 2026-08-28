import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/config_model.dart';
import 'package:heimdallm/features/config/config_providers.dart'
    show computeGlobalDiffForTest;

AppConfig _withMergeTracking(MergeTrackingConfig mt) =>
    AppConfig(pollInterval: '5m', aiPrimary: 'claude', mergeTracking: mt);

void main() {
  _overrideParsing();

  // Only what changed is sent. A field the diff forgets is a setting the
  // operator can flip in the UI and never see take effect — silently, because
  // the save still succeeds.
  test('every merge-tracking field reaches the patch when it changes', () {
    const old = AppConfig(pollInterval: '5m', aiPrimary: 'claude');

    final cases = <String, MergeTrackingConfig>{
      'enabled': const MergeTrackingConfig(enabled: true),
      'enable_auto_merge': const MergeTrackingConfig(enableAutoMerge: true),
      'update_branch': const MergeTrackingConfig(updateBranch: true),
      'resolve_conflicts': const MergeTrackingConfig(resolveConflicts: true),
      'merge': const MergeTrackingConfig(merge: true),
      'merge_method': const MergeTrackingConfig(mergeMethod: 'rebase'),
      'include_assigned': const MergeTrackingConfig(includeAssigned: true),
      'require_approval': const MergeTrackingConfig(requireApproval: true),
      'poll_interval': const MergeTrackingConfig(pollInterval: '2m'),
      'max_prs_per_tick': const MergeTrackingConfig(maxPrsPerTick: 5),
      'max_update_attempts': const MergeTrackingConfig(maxUpdateAttempts: 9),
      'max_resolve_attempts': const MergeTrackingConfig(maxResolveAttempts: 9),
      'max_merge_attempts': const MergeTrackingConfig(maxMergeAttempts: 9),
      'action_cooldown': const MergeTrackingConfig(actionCooldown: '1m'),
      'resolve_timeout': const MergeTrackingConfig(resolveTimeout: '45m'),
      'resolve_effort': const MergeTrackingConfig(resolveEffort: 'max'),
    };

    for (final entry in cases.entries) {
      final diff = computeGlobalDiffForTest(old, _withMergeTracking(entry.value));
      final mt = diff['merge_tracking'] as Map<String, dynamic>?;
      expect(mt, isNotNull, reason: entry.key);
      expect(mt!.keys, [entry.key], reason: 'only ${entry.key} changed');
    }
  });

  // A no-op save must not send the section at all: the daemon rewrites
  // config.toml on every PATCH, and a patch full of unchanged values is a
  // rewrite waiting to lose a hand-edited comment.
  test('an unchanged section is absent from the patch', () {
    const cfg = AppConfig(
      pollInterval: '5m',
      aiPrimary: 'claude',
      mergeTracking: MergeTrackingConfig(enabled: true, merge: true),
    );
    expect(computeGlobalDiffForTest(cfg, cfg)['merge_tracking'], isNull);
  });

  test('several changes at once travel together', () {
    const old = AppConfig(pollInterval: '5m', aiPrimary: 'claude');
    final diff = computeGlobalDiffForTest(
      old,
      _withMergeTracking(
        const MergeTrackingConfig(
          enabled: true,
          merge: true,
          mergeMethod: 'merge',
        ),
      ),
    );
    expect(diff['merge_tracking'], {
      'enabled': true,
      'merge': true,
      'merge_method': 'merge',
    });
  });

  test('MergeTrackingConfig round-trips through JSON', () {
    const cfg = MergeTrackingConfig(
      enabled: true,
      enableAutoMerge: true,
      updateBranch: true,
      resolveConflicts: true,
      merge: true,
      mergeMethod: 'rebase',
      includeAssigned: true,
      requireApproval: true,
      pollInterval: '2m',
      maxPrsPerTick: 5,
      maxUpdateAttempts: 9,
      maxResolveAttempts: 8,
      maxMergeAttempts: 7,
      actionCooldown: '1m',
      resolveTimeout: '45m',
      resolveEffort: 'max',
    );
    final round = MergeTrackingConfig.fromJson(cfg.toJson());
    expect(round.toJson(), cfg.toJson());
    expect(round.mergeMethod, 'rebase');
    expect(round.maxResolveAttempts, 8);
  });

  // The daemon omits fields it has never been given, and the app has to start
  // from the same conservative defaults the daemon does: everything off.
  test('MergeTrackingConfig defaults to doing nothing', () {
    final cfg = MergeTrackingConfig.fromJson({});
    expect(cfg.enabled, isFalse);
    expect(cfg.enableAutoMerge, isFalse);
    expect(cfg.updateBranch, isFalse);
    expect(cfg.resolveConflicts, isFalse);
    expect(cfg.merge, isFalse);
    expect(cfg.mergeMethod, 'squash');
    expect(cfg.maxPrsPerTick, 20);
  });
}

// The daemon serves the section back on GET /config, and its per-repo and
// per-org overrides live inside it rather than in repo_overrides. Without this
// parsing the LED and the per-repo switch would have nothing to read.
void _overrideParsing() {
  test('per-repo and per-org overrides are folded into their configs', () {
    final cfg = AppConfig.fromJson({
      'repositories': ['acme/widgets'],
      'merge_tracking': {
        'enabled': true,
        'repos': {
          'acme/widgets': {'enabled': false},
          'acme/gadgets': {'enabled': true},
        },
        'orgs': {
          'acme': {'enabled': false},
        },
      },
    });

    expect(cfg.mergeTracking.enabled, isTrue);
    expect(cfg.repoConfigs['acme/widgets']?.mtEnabled, isFalse);
    // A repo that appears only in the override map still gets a config.
    expect(cfg.repoConfigs['acme/gadgets']?.mtEnabled, isTrue);
    expect(cfg.orgConfigs['acme']?.mtEnabled, isFalse);
  });

  test('an override with no enabled key leaves the config inheriting', () {
    final cfg = AppConfig.fromJson({
      'repositories': ['acme/widgets'],
      'merge_tracking': {
        'repos': {
          'acme/widgets': {'merge_method': 'rebase'},
        },
      },
    });
    expect(cfg.repoConfigs['acme/widgets']?.mtEnabled, isNull);
  });

  // The settings screen reset itself every time it was reopened because the
  // daemon never sent this section back at all.
  test('a config without the section falls back to the defaults', () {
    final cfg = AppConfig.fromJson({'repositories': ['acme/widgets']});
    expect(cfg.mergeTracking.enabled, isFalse);
    expect(cfg.repoConfigs['acme/widgets']?.mtEnabled, isNull);
  });
}

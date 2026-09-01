import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/instances/models.dart';

void main() {
  group('DaemonInstance', () {
    test('parses the API shape', () {
      final instance = DaemonInstance.fromJson({
        'id': 'srv-a',
        'name': 'Server A',
        'base_url': 'http://10.0.0.11:7842',
        'enabled': true,
        'self': false,
        'labels': ['linux', 'docker'],
        'assigned_repos': 3,
        'is_fallback': true,
        'in_pool': true,
        'state': {
          'reachable': true,
          'status': 'ok',
          'version': '0.9.0',
          'uptime_seconds': 90.5,
          'last_seen_at': '2026-09-01T10:00:00Z',
        },
      });

      expect(instance.id, 'srv-a');
      expect(instance.displayName, 'Server A');
      expect(instance.labels, ['linux', 'docker']);
      expect(instance.assignedRepos, 3);
      expect(instance.isFallback, isTrue);
      expect(instance.inPool, isTrue);
      expect(instance.state!.version, '0.9.0');
      expect(instance.state!.uptimeSeconds, 90.5);
      expect(instance.state!.lastSeenAt, isNotNull);
      expect(instance.usable, isTrue);
      expect(instance.reachable, isTrue);
    });

    test('display name falls back to the id', () {
      final instance = DaemonInstance.fromJson({'id': 'srv-a'});
      expect(instance.displayName, 'srv-a');
    });

    test('enabled defaults to true when absent', () {
      // Registering an instance and having it silently ignored would be the
      // wrong default.
      expect(DaemonInstance.fromJson({'id': 'a'}).enabled, isTrue);
      expect(
        DaemonInstance.fromJson({'id': 'a', 'enabled': false}).enabled,
        isFalse,
      );
    });

    test('a token error makes an instance unusable but still listed', () {
      final instance = DaemonInstance.fromJson({
        'id': 'a',
        'token_error': 'env var unset',
      });
      expect(instance.usable, isFalse);
      expect(instance.tokenError, 'env var unset');
    });

    test('a never-probed instance counts as reachable', () {
      // Showing every row as down for the first probe interval after a hub
      // restart would be misleading.
      expect(DaemonInstance.fromJson({'id': 'a'}).reachable, isTrue);
      expect(
        DaemonInstance.fromJson({
          'id': 'a',
          'state': {'reachable': false},
        }).reachable,
        isFalse,
      );
    });

    test('tolerates a malformed last_seen_at', () {
      final state = InstanceState.fromJson({'last_seen_at': 'not a date'});
      expect(state.lastSeenAt, isNull);
    });
  });

  group('ClusterRegistry', () {
    test('a single instance is not multi-instance', () {
      // One instance is indistinguishable from a plain single-daemon install,
      // so the extra navigation and badges must stay hidden.
      final registry = ClusterRegistry.fromJson({
        'instances': [
          {'id': 'only'},
        ],
      });
      expect(registry.isMultiInstance, isFalse);
    });

    test('reports usable instances and reachable count', () {
      final registry = ClusterRegistry.fromJson({
        'role': 'hub',
        'self_id': 'hub-1',
        'instances': [
          {
            'id': 'hub-1',
            'self': true,
            'state': {'reachable': true},
          },
          {
            'id': 'srv-a',
            'state': {'reachable': false},
          },
          {'id': 'off', 'enabled': false},
          {'id': 'broken', 'token_error': 'missing'},
        ],
      });

      expect(registry.isMultiInstance, isTrue);
      expect(registry.usable.map((i) => i.id), ['hub-1', 'srv-a']);
      expect(registry.reachableCount, 1);
      expect(registry.byId('srv-a')!.id, 'srv-a');
      expect(registry.byId('nope'), isNull);
    });

    test('empty registry is safe', () {
      expect(ClusterRegistry.empty.isMultiInstance, isFalse);
      expect(ClusterRegistry.empty.usable, isEmpty);
      expect(ClusterRegistry.fromJson(const {}).instances, isEmpty);
    });
  });

  group('RoutingRules', () {
    final rules = RoutingRules.fromJson({
      'mode': 'dispatch',
      'round_robin_pool': ['a', 'b'],
      'round_robin_ops': ['review'],
      'orgs': {'AcmeCorp': 'b'},
      'repos': {'acme/special': 'a'},
      'default_instance': 'c',
      'resolved_pool': ['a', 'b'],
      'enabled': true,
    });

    test('parses the API shape', () {
      expect(rules.mode, RoutingMode.dispatch);
      expect(rules.roundRobinPool, ['a', 'b']);
      expect(rules.roundRobinOps, ['review']);
      expect(rules.defaultInstance, 'c');
      expect(rules.enabled, isTrue);
    });

    test('ownerFor mirrors the daemon precedence', () {
      // Kept in sync with instances.Router.OwnerFor so the UI shows what will
      // actually happen: repo rule, then org rule, then the fallback.
      expect(rules.ownerFor('acme/special'), 'a');
      expect(rules.ownerFor('acmecorp/other'), 'b');
      expect(rules.ownerFor('unrelated/thing'), 'c');
    });

    test('org matching is case-insensitive like GitHub slugs', () {
      expect(rules.ownerFor('ACMECORP/x'), 'b');
    });

    test('hasExplicitRule separates configured from inherited', () {
      expect(rules.hasExplicitRule('acme/special'), isTrue);
      expect(rules.hasExplicitRule('acmecorp/other'), isTrue);
      expect(rules.hasExplicitRule('unrelated/thing'), isFalse);
    });

    test('copyWith replaces only what is given', () {
      final updated = rules.copyWith(mode: RoutingMode.assignment);
      expect(updated.mode, RoutingMode.assignment);
      expect(updated.repos, rules.repos);
    });

    test('empty rules resolve nothing', () {
      expect(RoutingRules.empty.ownerFor('a/b'), isEmpty);
      expect(RoutingRules.empty.enabled, isFalse);
    });
  });

  group('propagation and drift', () {
    test('report parses per-instance results', () {
      final report = PropagateReport.fromJson({
        'failures': 1,
        'skipped_local': ['server.port'],
        'results': [
          {'instance_id': 'hub-1', 'ok': true, 'skipped': true},
          {
            'instance_id': 'srv-a',
            'name': 'Server A',
            'ok': true,
            'applied_keys': ['ai.review_mode'],
          },
          {'instance_id': 'srv-b', 'ok': false, 'error': 'starting'},
        ],
      });

      expect(report.allOk, isFalse);
      expect(report.skippedLocal, ['server.port']);
      expect(report.results, hasLength(3));
      expect(report.results[1].displayName, 'Server A');
      expect(report.results[1].appliedKeys, ['ai.review_mode']);
      expect(report.results[2].error, 'starting');
    });

    test('drift distinguishes in-sync, differing and missing', () {
      final drift = InstanceDrift.fromJson({
        'instance_id': 'srv-a',
        'ok': true,
        'drifts': [
          {'key': 'ai.review_mode', 'hub_value': 'multi', 'remote_value': 'single'},
          {'key': 'tier3_enabled', 'hub_value': true, 'missing': true},
        ],
      });
      expect(drift.inSync, isFalse);
      expect(drift.drifts.first.remoteValue, 'single');
      expect(drift.drifts.last.missing, isTrue);

      final synced = InstanceDrift.fromJson({'instance_id': 'b', 'ok': true});
      expect(synced.inSync, isTrue);
    });
  });
}

import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/instances/models.dart';
import 'package:heimdallm/core/models/config_model.dart';
import 'package:heimdallm/features/config/config_providers.dart'
    show computeGlobalDiffForTest;

void main() {
  test('a role change emits exactly {cluster: {role: ...}}', () {
    const old = AppConfig(
      pollInterval: '5m',
      aiPrimary: 'claude',
      clusterRole: ClusterRole.standalone,
    );
    final updated = old.copyWith(clusterRole: ClusterRole.hub);

    final diff = computeGlobalDiffForTest(old, updated);

    expect(diff['cluster'], {'role': ClusterRole.hub});
    expect(diff.keys, ['cluster']);
  });

  // A no-op save must not rewrite [cluster] at all: DeepMerge would still
  // preserve cluster.instances/routing either way, but an unnecessary write
  // is an unnecessary write.
  test('an unchanged role is absent from the patch', () {
    const cfg = AppConfig(
      pollInterval: '5m',
      aiPrimary: 'claude',
      clusterRole: ClusterRole.hub,
    );
    expect(computeGlobalDiffForTest(cfg, cfg)['cluster'], isNull);
  });

  // The diff must only ever carry `role`: cluster.instances holds inline
  // secrets and cluster.routing is the hub's own topology, neither of which
  // the settings form has any business rewriting.
  test('the emitted patch never carries anything but role', () {
    const old = AppConfig(
      pollInterval: '5m',
      aiPrimary: 'claude',
      clusterRole: ClusterRole.standalone,
    );
    final updated = old.copyWith(clusterRole: ClusterRole.worker);

    final cluster =
        computeGlobalDiffForTest(old, updated)['cluster']
            as Map<String, dynamic>;
    expect(cluster.keys, ['role']);
  });
}

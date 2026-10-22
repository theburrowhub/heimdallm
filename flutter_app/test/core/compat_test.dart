import 'dart:convert';
import 'dart:io';

import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/compat.dart';

void main() {
  // compat.json at the repository root is what the release pipeline publishes;
  // the minimum compiled into the app must never drift from it.
  test('minDaemonApiVersion matches compat.json', () {
    final manifest =
        jsonDecode(File('../compat.json').readAsStringSync())
            as Map<String, dynamic>;
    final mins = manifest['min_daemon_api'] as Map<String, dynamic>;
    expect(mins['gui'], minDaemonApiVersion);
    expect(
      minDaemonApiVersion,
      lessThanOrEqualTo(manifest['daemon_api'] as int),
    );
  });

  test('a daemon without api_version counts as the baseline', () {
    expect(effectiveDaemonApiVersion(null), baselineDaemonApiVersion);
    expect(effectiveDaemonApiVersion('2'), baselineDaemonApiVersion);
    expect(effectiveDaemonApiVersion(0), baselineDaemonApiVersion);
    expect(effectiveDaemonApiVersion(3), 3);
  });

  test('reads min_daemon_api from a versions.json manifest', () {
    const manifest =
        '{"daemon":"1.0.0","daemon_api":3,"min_daemon_api":{"gui":2,"cli":3}}';
    expect(minDaemonApiFromVersionsManifest(manifest, 'gui'), 2);
    expect(minDaemonApiFromVersionsManifest(manifest, 'cli'), 3);
    expect(minDaemonApiFromVersionsManifest(manifest, 'web'), isNull);
    expect(
      minDaemonApiFromVersionsManifest('{"daemon":"1.0.0"}', 'gui'),
      isNull,
    );
    expect(minDaemonApiFromVersionsManifest('{nope', 'gui'), isNull);
    expect(minDaemonApiFromVersionsManifest('[]', 'gui'), isNull);
  });

  test('warns only when the daemon is known to be behind', () {
    expect(
      daemonCompatWarning(requiredApi: 2, daemonApi: 1),
      contains('API 2'),
    );
    expect(daemonCompatWarning(requiredApi: 2, daemonApi: 2), isNull);
    expect(daemonCompatWarning(requiredApi: 2, daemonApi: null), isNull);
    expect(daemonCompatWarning(requiredApi: null, daemonApi: 1), isNull);
  });
}

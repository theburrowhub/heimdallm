import 'dart:io';
import 'package:flutter_test/flutter_test.dart';
import 'package:toml/toml.dart';
import 'package:heimdallm/core/models/config_model.dart';
import 'package:heimdallm/core/setup/first_run_setup.dart';

/// Regression tests for the config-loss bug: "Save and start Heimdallm" used
/// to blindly overwrite config.toml with a TOML view built from AppConfig,
/// which does not model [cluster], [polling], [merge_tracking],
/// [circuit_breaker], [autonomous], [activity_log], server.bind_addr,
/// github.token, discovery_* — every one of those was silently dropped by a
/// save made from the config screen while the daemon was down (exactly the
/// path this button takes, since GET /config could not have populated the
/// real values in that state). This is what emptied config.toml today.
void main() {
  late Directory tmpDir;
  late String configPath;

  setUp(() {
    tmpDir = Directory.systemTemp.createTempSync('heimdallm_config_test');
    configPath = '${tmpDir.path}/config.toml';
  });

  tearDown(() {
    tmpDir.deleteSync(recursive: true);
  });

  test('no existing file: writes a fresh config with no merge needed', () async {
    await FirstRunSetup.writeConfig(
      const AppConfig(aiPrimary: 'codex'),
      configPathOverride: configPath,
    );

    final map = TomlDocument.parse(
      await File(configPath).readAsString(),
    ).toMap();
    expect((map['ai'] as Map)['primary'], 'codex');
  });

  test(
    'existing file: sections AppConfig does not model survive untouched',
    () async {
      await File(configPath).writeAsString('''
[ai]
primary = "claude"

[cluster]
role = "hub"
instance_id = "hub-1"
default_instance = "hub-1"
[cluster.instances.hub-1]
name = "hub"
base_url = "http://127.0.0.1:7842"
token = "seeded-token"

[polling]
adaptive = true
use_graphql = true

[merge_tracking]
enabled = true
enable_auto_merge = true

[retention]
max_days = 7
''');

      await FirstRunSetup.writeConfig(
        const AppConfig(aiPrimary: 'codex', retentionDays: 7),
        configPathOverride: configPath,
      );

      final map = TomlDocument.parse(
        await File(configPath).readAsString(),
      ).toMap();

      // The app's own value for a key it models wins.
      expect((map['ai'] as Map)['primary'], 'codex');

      // Everything AppConfig has never heard of survives byte-for-value.
      final cluster = map['cluster'] as Map?;
      expect(cluster, isNotNull, reason: '[cluster] must not be dropped');
      expect(cluster!['role'], 'hub');
      expect(cluster['instance_id'], 'hub-1');
      final instances = cluster['instances'] as Map;
      final hubInstance = instances['hub-1'] as Map;
      expect(hubInstance['token'], 'seeded-token');

      final polling = map['polling'] as Map?;
      expect(polling, isNotNull, reason: '[polling] must not be dropped');
      expect(polling!['adaptive'], true);

      final mergeTracking = map['merge_tracking'] as Map?;
      expect(
        mergeTracking,
        isNotNull,
        reason: '[merge_tracking] must not be dropped',
      );
      expect(mergeTracking!['enable_auto_merge'], true);
    },
  );

  test(
    'refuses to overwrite an existing file it cannot parse, leaving it '
    'untouched',
    () async {
      const garbage = 'this is not valid = = = toml [[[';
      await File(configPath).writeAsString(garbage);

      await expectLater(
        FirstRunSetup.writeConfig(
          const AppConfig(),
          configPathOverride: configPath,
        ),
        throwsA(anything),
      );

      expect(await File(configPath).readAsString(), garbage);
    },
  );

  test('write is atomic: no leftover temp file after a successful save', () async {
    await FirstRunSetup.writeConfig(
      const AppConfig(),
      configPathOverride: configPath,
    );

    final leftovers = tmpDir
        .listSync()
        .where((f) => f.path.contains('.tmp'))
        .toList();
    expect(leftovers, isEmpty);
  });
}

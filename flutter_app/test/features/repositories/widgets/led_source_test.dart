import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/config_model.dart';
import 'package:heimdallm/features/repositories/widgets/feature_palette.dart';
import 'package:heimdallm/features/repositories/widgets/led_source.dart';

void main() {
  final appConfig = const AppConfig(
    serverPort: 1, pollInterval: '60s', retentionDays: 30,
    aiPrimary: 'claude', aiFallback: '', reviewMode: 'single',
    repoConfigs: {'a/b': RepoConfig(prEnabled: true)},
    issueTracking: IssueTrackingConfig(enabled: true),
  );

  group('featureIsOn', () {
    test('PR explicit true → on', () {
      expect(featureIsOn(
        feature: Feature.prReview,
        repo: 'a/b',
        config: const RepoConfig(prEnabled: true),
        appConfig: appConfig,
      ), isTrue);
    });

    test('Develop off when no local dir and no explicit devEnabled', () {
      expect(featureIsOn(
        feature: Feature.develop,
        repo: 'a/b',
        config: const RepoConfig(),  // no localDir, no devEnabled
        appConfig: appConfig,
      ), isFalse);
    });

    test('Develop active with both devEnabled + local dir', () {
      expect(featureIsOn(
        feature: Feature.develop,
        repo: 'a/b',
        config: const RepoConfig(devEnabled: true, localDir: '/tmp/x'),
        appConfig: appConfig,
      ), isTrue);
    });
  });

  group('featureSourceLine', () {
    test('PR explicit true mentions prEnabled = true', () {
      final line = featureSourceLine(
        feature: Feature.prReview,
        repo: 'a/b',
        config: const RepoConfig(prEnabled: true),
        appConfig: appConfig,
      );
      expect(line, contains('prEnabled = true'));
    });

    test('Develop without local dir shows the "Requires local dir" reason', () {
      final line = featureSourceLine(
        feature: Feature.develop,
        repo: 'a/b',
        config: const RepoConfig(devEnabled: true),
        appConfig: appConfig,
      );
      expect(line, contains('no local directory configured'));
    });

    test('IT inherited when no per-repo override + global on', () {
      final line = featureSourceLine(
        feature: Feature.issueTracking,
        repo: 'a/b',
        config: const RepoConfig(prEnabled: true),
        appConfig: appConfig,
      );
      expect(line, contains('inherited from global issue tracking'));
    });
  });
}

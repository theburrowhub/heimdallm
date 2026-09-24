import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/models/config_model.dart';
import 'package:heimdallm/features/repositories/widgets/feature_palette.dart';
import 'package:heimdallm/features/repositories/widgets/led_source.dart';

void main() {
  _mergeTrackingLed();

  const appConfig = AppConfig(
    serverPort: 1,
    pollInterval: '60s',
    retentionDays: 30,
    aiPrimary: 'claude',
    aiFallback: '',
    reviewMode: 'single',
    repoConfigs: {'a/b': RepoConfig(prEnabled: true)},
  );

  group('featureIsOn', () {
    test('PR explicit true → on', () {
      expect(
        featureIsOn(
          feature: Feature.prReview,
          repo: 'a/b',
          config: const RepoConfig(prEnabled: true),
          appConfig: appConfig,
        ),
        isTrue,
      );
    });

    test('PR explicit false → off', () {
      expect(
        featureIsOn(
          feature: Feature.prReview,
          repo: 'a/b',
          config: const RepoConfig(prEnabled: false),
          appConfig: appConfig,
        ),
        isFalse,
      );
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

    test('PR explicit false says it is disabled per-repo', () {
      final line = featureSourceLine(
        feature: Feature.prReview,
        repo: 'a/b',
        config: const RepoConfig(prEnabled: false),
        appConfig: appConfig,
      );
      expect(line, contains('disabled per-repo'));
    });

    test('PR without an override reports the monitored-list source', () {
      final line = featureSourceLine(
        feature: Feature.prReview,
        repo: 'x/y',
        config: const RepoConfig(),
        appConfig: appConfig,
      );
      expect(line, contains('not in monitored list'));
    });
  });
}

// Merge tracking resolves repo > org > global, the same precedence the daemon
// applies in MergeTrackingForRepo. It has no label-based inference: it acts on
// who authored the PR, not on how anything is labelled.
void _mergeTrackingLed() {
  group('featureIsOn Merge Tracking', () {
    AppConfig cfg({bool global = false}) =>
        AppConfig(mergeTracking: MergeTrackingConfig(enabled: global));

    bool on(RepoConfig repo, {OrgConfig? org, bool global = false}) =>
        featureIsOn(
          feature: Feature.mergeTracking,
          repo: 'acme/widgets',
          config: repo,
          appConfig: cfg(global: global).copyWith(
            orgConfigs: org == null ? const {} : {'acme': org},
          ),
        );

    test('the repo override wins over the org and the global', () {
      expect(
        on(
          const RepoConfig(prEnabled: true, mtEnabled: true),
          org: const OrgConfig(mtEnabled: false),
        ),
        isTrue,
      );
      expect(
        on(
          const RepoConfig(prEnabled: true, mtEnabled: false),
          org: const OrgConfig(mtEnabled: true),
          global: true,
        ),
        isFalse,
      );
    });

    test('the org override wins over the global', () {
      expect(
        on(
          const RepoConfig(prEnabled: true),
          org: const OrgConfig(mtEnabled: true),
        ),
        isTrue,
      );
      expect(
        on(
          const RepoConfig(prEnabled: true),
          org: const OrgConfig(mtEnabled: false),
          global: true,
        ),
        isFalse,
      );
    });

    test('with nothing overridden it follows the global switch', () {
      expect(on(const RepoConfig(prEnabled: true), global: true), isTrue);
      expect(on(const RepoConfig(prEnabled: true)), isFalse);
    });

    test('an inherited setting stays off when the repo is not monitored', () {
      expect(on(const RepoConfig(), global: true), isFalse);
      expect(
        featureSourceLine(
          feature: Feature.mergeTracking,
          repo: 'acme/widgets',
          config: const RepoConfig(),
          appConfig: cfg(global: true),
        ),
        contains('not monitored'),
      );
    });

    test('the tooltip names where the answer came from', () {
      String source(RepoConfig repo, {OrgConfig? org, bool global = false}) =>
          featureSourceLine(
            feature: Feature.mergeTracking,
            repo: 'acme/widgets',
            config: repo,
            appConfig: cfg(global: global).copyWith(
              orgConfigs: org == null ? const {} : {'acme': org},
            ),
          );

      expect(
        source(const RepoConfig(prEnabled: true, mtEnabled: true)),
        contains('repo-level'),
      );
      expect(
        source(const RepoConfig(prEnabled: true, mtEnabled: false)),
        contains('disabled per-repo'),
      );
      expect(
        source(
          const RepoConfig(prEnabled: true),
          org: const OrgConfig(mtEnabled: true),
        ),
        contains('org'),
      );
      expect(
        source(const RepoConfig(prEnabled: true), global: true),
        contains('global'),
      );
      expect(
        source(const RepoConfig(prEnabled: true)),
        contains('globally disabled'),
      );
    });
  });
}

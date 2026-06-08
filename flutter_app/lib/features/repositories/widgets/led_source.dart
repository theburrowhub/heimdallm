// flutter_app/lib/features/repositories/widgets/led_source.dart
//
// Shared helpers for computing an LED's on/off state and its tooltip
// "Source: ..." line from a RepoConfig + AppConfig. Extracted from
// RepoListTile and RepoGridTile so the source-string vocabulary stays
// in one place.
import '../../../core/models/config_model.dart';
import 'feature_palette.dart';
import 'local_dir_resolution.dart';

/// Whether the feature LED should render in its "on" colour for the given
/// repo. Returns true when the daemon will act on the feature for this
/// repo — repo-level override on, global-list inheritance, or label-based
/// inference (depending on feature).
bool featureIsOn({
  required Feature feature,
  required String repo,
  required RepoConfig config,
  required AppConfig appConfig,
}) {
  final inGlobalList = appConfig.repositories.contains(repo);
  final org = _orgForRepo(repo);
  final orgConfig = org != null ? appConfig.orgConfigs[org] : null;
  final hasDir = LocalDirResolution.resolve(
    repo: repo,
    config: config,
    appConfig: appConfig,
  ).hasDir;
  return switch (feature) {
    Feature.prReview => config.prLedStatus(inGlobalList) != 'off',
    Feature.issueTracking => _issueTrackingOn(config, orgConfig, appConfig),
    Feature.develop => hasDir && _developOn(config, orgConfig, appConfig),
  };
}

/// "Source: …" line shown at the bottom of the LED tooltip. Answers the
/// question "why is this LED on (or off)?" in one short sentence.
String featureSourceLine({
  required Feature feature,
  required String repo,
  required RepoConfig config,
  required AppConfig appConfig,
}) {
  final inGlobalList = appConfig.repositories.contains(repo);
  final org = _orgForRepo(repo);
  final orgConfig = org != null ? appConfig.orgConfigs[org] : null;
  final hasDir = LocalDirResolution.resolve(
    repo: repo,
    config: config,
    appConfig: appConfig,
  ).hasDir;
  switch (feature) {
    case Feature.prReview:
      if (config.prEnabled == true) {
        return 'Source: repo-level (prEnabled = true)';
      }
      if (config.prEnabled == false) {
        return 'Source: disabled per-repo (prEnabled = false)';
      }
      return inGlobalList
          ? 'Source: inherited from global monitored list'
          : 'Source: not in monitored list';
    case Feature.issueTracking:
      if (config.itEnabled == true) {
        return 'Source: repo-level (itEnabled = true)';
      }
      if (config.itEnabled == false) {
        return 'Source: disabled per-repo (itEnabled = false)';
      }
      if ((config.reviewOnlyLabels ?? const []).isNotEmpty ||
          (config.refinementLabels ?? const []).isNotEmpty) {
        return 'Source: implied by per-repo labels';
      }
      if (orgConfig?.itEnabled == true) {
        return 'Source: inherited from org issue tracking';
      }
      if (orgConfig?.itEnabled == false) return 'Source: disabled at org level';
      if ((orgConfig?.reviewOnlyLabels ?? const []).isNotEmpty ||
          (orgConfig?.refinementLabels ?? const []).isNotEmpty ||
          (orgConfig?.developLabels ?? const []).isNotEmpty) {
        return 'Source: implied by org labels';
      }
      return appConfig.issueTracking.enabled
          ? 'Source: inherited from global issue tracking'
          : 'Source: globally disabled';
    case Feature.develop:
      if (config.devEnabled == true && hasDir) {
        return 'Source: repo-level (devEnabled = true)';
      }
      if (config.devEnabled == false) {
        return 'Source: disabled per-repo (devEnabled = false)';
      }
      if (!hasDir) {
        return 'Reason: no local directory configured (Develop requires one)';
      }
      if ((config.developLabels ?? const []).isNotEmpty) {
        return 'Source: implied by per-repo develop labels';
      }
      if (orgConfig?.devEnabled == true) {
        return 'Source: inherited from org develop setting';
      }
      if (orgConfig?.devEnabled == false) {
        return 'Source: disabled at org level';
      }
      if ((orgConfig?.developLabels ?? const []).isNotEmpty) {
        return 'Source: implied by org develop labels';
      }
      return appConfig.issueTracking.enabled
          ? 'Source: inherited from global issue tracking'
          : 'Source: globally disabled';
  }
}

String? _orgForRepo(String repo) {
  if (!repo.contains('/')) return null;
  final org = repo.split('/').first;
  return org.isEmpty ? null : org;
}

bool _issueTrackingOn(
  RepoConfig config,
  OrgConfig? orgConfig,
  AppConfig appConfig,
) {
  if (config.itEnabled == true) return true;
  if (config.itEnabled == false) return false;
  if ((config.reviewOnlyLabels ?? const []).isNotEmpty ||
      (config.refinementLabels ?? const []).isNotEmpty ||
      (config.developLabels ?? const []).isNotEmpty) {
    return true;
  }
  if (orgConfig?.itEnabled == true) return true;
  if (orgConfig?.itEnabled == false) return false;
  if ((orgConfig?.reviewOnlyLabels ?? const []).isNotEmpty ||
      (orgConfig?.refinementLabels ?? const []).isNotEmpty ||
      (orgConfig?.developLabels ?? const []).isNotEmpty) {
    return true;
  }
  return appConfig.issueTracking.enabled;
}

bool _developOn(RepoConfig config, OrgConfig? orgConfig, AppConfig appConfig) {
  if (config.devEnabled == true) return true;
  if (config.devEnabled == false) return false;
  if ((config.developLabels ?? const []).isNotEmpty) return true;
  if (orgConfig?.devEnabled == true) return true;
  if (orgConfig?.devEnabled == false) return false;
  if ((orgConfig?.developLabels ?? const []).isNotEmpty) return true;
  return appConfig.issueTracking.enabled;
}

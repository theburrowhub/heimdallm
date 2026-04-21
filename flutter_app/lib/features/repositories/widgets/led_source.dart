// flutter_app/lib/features/repositories/widgets/led_source.dart
//
// Shared helpers for computing an LED's on/off state and its tooltip
// "Source: ..." line from a RepoConfig + AppConfig. Extracted from
// RepoListTile and RepoGridTile so the source-string vocabulary stays
// in one place.
import '../../../core/models/config_model.dart';
import 'feature_palette.dart';

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
  final globalIt = appConfig.issueTracking.enabled;
  final hasDir = config.localDir != null && config.localDir!.isNotEmpty;
  final status = switch (feature) {
    Feature.prReview      => config.prLedStatus(inGlobalList),
    Feature.issueTracking => config.itLedStatus(globalIt),
    Feature.develop       => config.devLedStatus(globalIt, hasDir),
  };
  return status != 'off';
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
  final globalIt = appConfig.issueTracking.enabled;
  final hasDir = config.localDir != null && config.localDir!.isNotEmpty;
  switch (feature) {
    case Feature.prReview:
      if (config.prEnabled == true) return 'Source: repo-level (prEnabled = true)';
      if (config.prEnabled == false) return 'Source: disabled per-repo (prEnabled = false)';
      return inGlobalList
          ? 'Source: inherited from global monitored list'
          : 'Source: not in monitored list';
    case Feature.issueTracking:
      if (config.itEnabled == true) return 'Source: repo-level (itEnabled = true)';
      if (config.itEnabled == false) return 'Source: disabled per-repo (itEnabled = false)';
      if ((config.reviewOnlyLabels ?? const []).isNotEmpty) {
        return 'Source: implied by per-repo labels';
      }
      return globalIt
          ? 'Source: inherited from global issue tracking'
          : 'Source: globally disabled';
    case Feature.develop:
      if (config.devEnabled == true && hasDir) return 'Source: repo-level (devEnabled = true)';
      if (config.devEnabled == false) return 'Source: disabled per-repo (devEnabled = false)';
      if (!hasDir) return 'Reason: no local directory configured (Develop requires one)';
      if ((config.developLabels ?? const []).isNotEmpty) {
        return 'Source: implied by per-repo develop labels';
      }
      return globalIt
          ? 'Source: inherited from global issue tracking'
          : 'Source: globally disabled';
  }
}

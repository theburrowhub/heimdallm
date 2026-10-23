import 'dart:convert';

/// Oldest daemon API revision (the `api_version` the daemon reports on
/// `/health`) this app works against. Raise it in the same change that makes
/// the app depend on a newer endpoint or payload, together with
/// `min_daemon_api.gui` in the repository's `compat.json` (a test keeps them
/// equal). The release pipeline publishes that file in `versions.json`, and
/// the update banner warns before installing an app that is ahead of the
/// connected daemon.
const int minDaemonApiVersion = 1;

/// Revision assumed for a daemon that reports no `api_version`: daemons built
/// before the field existed served revision 1.
const int baselineDaemonApiVersion = 1;

/// Name of the unified release asset that carries the compatibility data.
const String versionsManifestAsset = 'versions.json';

/// Maps the `api_version` of a `/health` payload to the revision it stands
/// for: absent or invalid values are the baseline.
int effectiveDaemonApiVersion(Object? reported) =>
    reported is int && reported > 0 ? reported : baselineDaemonApiVersion;

/// Reads `min_daemon_api.<component>` from a release's `versions.json`, or
/// null when the manifest predates compatibility data or is malformed.
int? minDaemonApiFromVersionsManifest(String body, String component) {
  try {
    final decoded = jsonDecode(body);
    if (decoded is! Map<String, dynamic>) return null;
    final mins = decoded['min_daemon_api'];
    if (mins is! Map<String, dynamic>) return null;
    final value = mins[component];
    return value is int && value > 0 ? value : null;
  } on FormatException {
    return null;
  }
}

/// The warning the update banner shows when the release about to be installed
/// needs a newer daemon API than the connected daemon serves, or null.
String? daemonCompatWarning({
  required int? requiredApi,
  required int? daemonApi,
}) {
  if (requiredApi == null || daemonApi == null || daemonApi >= requiredApi) {
    return null;
  }
  return 'This update needs daemon API $requiredApi or newer; the connected '
      'daemon serves API $daemonApi. The bundled daemon is updated with the '
      'app; a separately managed daemon (Docker, another machine) must be '
      'updated too.';
}

import '../platform/platform_services.dart';

/// Where an [ApiClient] or [SseClient] sends its requests, and how it
/// authenticates.
///
/// Before instances existed, the endpoint was fused into [PlatformServices] —
/// the same object that owns the tray, the window and daemon spawning. That
/// worked while there was exactly one daemon on one loopback port; it cannot
/// express "the same app talking to three machines". Splitting the endpoint out
/// leaves [PlatformServices] for OS capabilities and makes the target a value
/// the caller chooses.
class DaemonEndpoint {
  /// Identifies the instance this endpoint targets. Empty for the local daemon
  /// (desktop) or the single upstream Nginx proxies to (web) — the one the app
  /// itself may spawn and manage.
  final String instanceId;

  /// Human-readable label for the UI. Empty for the local daemon.
  final String name;

  /// Prefix for every HTTP and SSE request. Never ends in a slash.
  final String baseUrl;

  final Future<String?> Function() _loadToken;
  final void Function() _clearTokenCache;

  const DaemonEndpoint._({
    required this.instanceId,
    required this.name,
    required this.baseUrl,
    required Future<String?> Function() loadToken,
    required void Function() clearTokenCache,
  }) : _loadToken = loadToken,
       _clearTokenCache = clearTokenCache;

  /// The daemon this app manages: `http://127.0.0.1:7842` on desktop, the
  /// relative `/api` prefix on web (where Nginx injects the token).
  factory DaemonEndpoint.local(PlatformServices platform) {
    return DaemonEndpoint._(
      instanceId: '',
      name: '',
      baseUrl: _trimTrailingSlash(platform.apiBaseUrl),
      loadToken: platform.loadApiToken,
      clearTokenCache: platform.clearApiTokenCache,
    );
  }

  /// Another instance, reached through the hub's authenticated reverse proxy.
  ///
  /// The app never opens a second connection to a remote daemon. Doing so would
  /// need CORS enabled on every instance and every instance's token shipped to
  /// the browser; routing through the hub means one origin and one credential,
  /// and the hub swaps in the instance's own token on the way out.
  factory DaemonEndpoint.viaHub({
    required DaemonEndpoint hub,
    required String instanceId,
    String name = '',
  }) {
    return DaemonEndpoint._(
      instanceId: instanceId,
      name: name,
      baseUrl: '${hub.baseUrl}/instances/$instanceId/proxy',
      loadToken: hub._loadToken,
      clearTokenCache: hub._clearTokenCache,
    );
  }

  /// Escape hatch for tests: an endpoint with an explicit URL and token.
  factory DaemonEndpoint.raw({
    required String baseUrl,
    String instanceId = '',
    String name = '',
    String? token,
  }) {
    return DaemonEndpoint._(
      instanceId: instanceId,
      name: name,
      baseUrl: _trimTrailingSlash(baseUrl),
      loadToken: () async => token,
      clearTokenCache: () {},
    );
  }

  /// Whether this endpoint is the daemon the app itself manages, as opposed to
  /// a remote instance reached through the hub. Only the local one may be
  /// spawned, stopped or restarted from the UI.
  bool get isLocal => instanceId.isEmpty;

  Future<String?> loadToken() => _loadToken();

  void clearTokenCache() => _clearTokenCache();

  Uri uri(String path) => Uri.parse('$baseUrl$path');

  static String _trimTrailingSlash(String url) {
    // A trailing slash would produce "//prs" once a path is appended, which
    // some proxies normalise and others do not.
    var out = url;
    while (out.endsWith('/')) {
      out = out.substring(0, out.length - 1);
    }
    return out;
  }

  @override
  String toString() =>
      'DaemonEndpoint(${instanceId.isEmpty ? 'local' : instanceId} @ $baseUrl)';
}

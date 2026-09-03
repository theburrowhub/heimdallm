import 'dart:convert';
import 'package:http/http.dart' as http;
import '../models/activity.dart';
import '../models/merge_tracking.dart';
import '../models/pr.dart';
import '../models/review.dart';
import '../models/tracked_issue.dart';
import '../platform/platform_services.dart';
import 'daemon_endpoint.dart';

/// Who, if anyone, answered the daemon HTTP endpoint. A [PortOwner.none]
/// result only permits entering [PlatformServices.spawnDaemon]; that operation
/// performs the authoritative TCP guard immediately before process creation.
enum PortOwner {
  /// No native HTTP responder could be identified. The guarded spawn operation
  /// still has to prove that the TCP port is free.
  none,

  /// Our daemon answered — healthy, degraded or still starting.
  daemon,

  /// A foreign HTTP service answered, or a web proxy endpoint was unreachable.
  foreign,
}

class ApiClient {
  final http.Client _client;
  final DaemonEndpoint _endpoint;
  final Duration _daemonReachabilityTimeout;

  /// Builds a client for one daemon.
  ///
  /// Pass [endpoint] to target a specific instance. [platform] remains
  /// supported and means "the daemon this app manages", which is what every
  /// single-daemon call site wants and keeps them unchanged.
  ApiClient({
    http.Client? httpClient,
    PlatformServices? platform,
    DaemonEndpoint? endpoint,
    Duration daemonReachabilityTimeout = const Duration(seconds: 3),
  }) : assert(
         platform != null || endpoint != null,
         'ApiClient needs either a platform (local daemon) or an endpoint',
       ),
       _client = httpClient ?? http.Client(),
       _endpoint = endpoint ?? DaemonEndpoint.local(platform!),
       _daemonReachabilityTimeout = daemonReachabilityTimeout;

  /// The daemon this client talks to.
  DaemonEndpoint get endpoint => _endpoint;

  /// Identifies the instance, or empty for the daemon the app manages.
  String get instanceId => _endpoint.instanceId;

  Uri _uri(String path) => _endpoint.uri(path);

  /// Clears the cached API token, forcing the next request to re-read it.
  void clearTokenCache() {
    _endpoint.clearTokenCache();
  }

  /// Headers for mutating requests (POST/PUT/DELETE). Adds
  /// X-Heimdallm-Token when the platform provides one (desktop). On web
  /// the token is null and the header is omitted — Nginx injects it.
  Future<Map<String, String>> _authHeaders() async {
    final token = await _endpoint.loadToken();
    return {
      'Content-Type': 'application/json',
      if (token != null && token.isNotEmpty) 'X-Heimdallm-Token': token,
    };
  }

  /// GET /health, with the auth token attached only when it is actually
  /// required.
  ///
  /// /health is deliberately the one unauthenticated route on the daemon
  /// itself — a rotated token must still show the local daemon as reachable
  /// — so the local endpoint skips the token load entirely, unchanged from
  /// before. A non-local (viaHub) endpoint's /health is different: it is
  /// reached through /instances/{id}/proxy/health, which lives under the
  /// same auth-gated prefix as every other /instances/* route and DOES
  /// require the token. checkHealth/daemonReachable were the only two calls
  /// in this whole client that never sent it, so every health check against
  /// a routed instance — including the local one whenever the app's "active
  /// instance" preference names a remote id — came back 401 and was misread
  /// as "not our daemon", flipping the UI to "Server unavailable".
  ///
  /// Extracted so checkHealth/daemonReachable can bound the token load *and*
  /// the request in one [Future.timeout]: putting `await _authHeaders()`
  /// directly in an argument list happens before the call it's an argument
  /// to even starts, so a `.timeout()` chained onto that call would not
  /// cover it.
  Future<http.Response> _getHealthWithAuth() async {
    if (_endpoint.isLocal) return _client.get(_uri('/health'));
    return _client.get(_uri('/health'), headers: await _authHeaders());
  }

  Future<bool> checkHealth() async {
    try {
      final resp = await _getHealthWithAuth().timeout(
        const Duration(seconds: 3),
      );
      return resp.statusCode == 200 && _looksLikeHeimdallm(resp);
    } catch (_) {
      return false;
    }
  }

  /// Whether *something* is serving the daemon port, regardless of how healthy
  /// it reports itself to be.
  ///
  /// Never use [checkHealth] to decide whether to spawn a daemon. A daemon that
  /// is alive but degraded answers /health with 503 — which it does whenever
  /// `last_poll` is older than twice the poll interval, i.e. exactly when
  /// GitHub rate limits are slowing polls down, and also while it is still
  /// wiring up at boot. Treating that as "no daemon" spawns a second one, which
  /// loses the port bind, keeps polling anyway and pushes the quota further
  /// under: the feedback loop behind #646.
  ///
  /// HTTP errors are deliberately not classified by exception type. The VM
  /// client wraps SocketException in a private `_ClientSocketException`, while
  /// web uses a different hierarchy again. Native callers may proceed only to
  /// the guarded [PlatformServices.spawnDaemon], which performs a raw TCP probe
  /// immediately before process creation. A relative web endpoint fails closed
  /// because a browser cannot perform that native guard.
  Future<PortOwner> daemonReachable() async {
    try {
      final resp = await _getHealthWithAuth().timeout(
        _daemonReachabilityTimeout,
      );
      return _looksLikeHeimdallm(resp) ? PortOwner.daemon : PortOwner.foreign;
    } catch (_) {
      return Uri.parse(_endpoint.baseUrl).isAbsolute
          ? PortOwner.none
          : PortOwner.foreign;
    }
  }

  /// Distinguishes our daemon from an unrelated process squatting on the port.
  /// Without this the app would silently never spawn, exhaust its retries and
  /// report a generic health failure with no hint that something else holds the
  /// port.
  ///
  /// The [HeaderDaemon] header is the authoritative signal. The body fallback
  /// exists only for daemons predating that header, and deliberately requires
  /// `checks` or `version` alongside `status`: a bare `{"status": "..."}` is the
  /// shape of most health endpoints in the wild (`{"status":"UP"}` from Spring
  /// Boot Actuator, `{"status":"ok"}` from countless Node/Go services), and
  /// accepting it would classify a foreign service as ours.
  ///
  static const _daemonHeader = 'x-heimdallm-daemon';

  static bool _looksLikeHeimdallm(http.Response resp) {
    if (resp.headers.containsKey(_daemonHeader)) return true;
    try {
      final body = jsonDecode(resp.body);
      if (body is! Map<String, dynamic>) return false;
      if (body['status'] is! String) return false;
      return body.containsKey('checks') || body.containsKey('version');
    } catch (_) {
      return false;
    }
  }

  /// The daemon port, derived from the configured base URL so error messages and
  /// diagnostics never hardcode the default.
  int get daemonPort => Uri.parse(_endpoint.baseUrl).port;

  /// Returns the full identified /health payload, including 503
  /// starting/stopping/degraded responses, or null if the responder is not a
  /// Heimdallm daemon. Diagnostics must remain available precisely when deep
  /// health is failing.
  /// Includes status, version (optional), started_at (optional, RFC3339).
  Future<Map<String, dynamic>?> fetchHealth() async {
    try {
      final resp = await _client.get(
        _uri('/health'),
        headers: await _authHeaders(),
      );
      if (!_looksLikeHeimdallm(resp)) return null;
      final body = jsonDecode(resp.body);
      return body is Map<String, dynamic> ? body : null;
    } catch (_) {
      return null;
    }
  }

  Future<List<PR>> fetchPRs({List<String> states = const []}) async {
    var path = '/prs';
    if (states.isNotEmpty) {
      path += '?state=${states.join(',')}';
    }
    final resp = await _client.get(_uri(path), headers: await _authHeaders());
    if (resp.statusCode != 200) {
      throw ApiException('GET /prs failed: ${resp.statusCode}');
    }
    final list = jsonDecode(resp.body) as List<dynamic>;
    return list
        .map((e) => _parsePRWithReview(e as Map<String, dynamic>))
        .toList();
  }

  Future<Map<String, dynamic>> fetchPR(int id) async {
    final resp = await _client.get(
      _uri('/prs/$id'),
      headers: await _authHeaders(),
    );
    if (resp.statusCode != 200) {
      throw ApiException('GET /prs/$id failed: ${resp.statusCode}');
    }
    final body = jsonDecode(resp.body) as Map<String, dynamic>;
    final pr = _parsePRWithReview(body['pr'] as Map<String, dynamic>);
    final reviewsRaw = body['reviews'] as List<dynamic>? ?? [];
    final reviews = reviewsRaw
        .map((r) => _parseReview(r as Map<String, dynamic>))
        .toList();
    return {'pr': pr, 'reviews': reviews};
  }

  Future<ActivityPage> fetchActivity(ActivityQuery q) async {
    final headers = await _authHeaders();
    // Build /activity via the shared _uri helper so both desktop
    // (http://127.0.0.1:7842/activity) and web (/api/activity — resolved
    // against the browser origin and proxied by Nginx) work unchanged.
    final uri = _uri(
      '/activity',
    ).replace(queryParameters: q.toQueryParameters());
    final resp = await _client.get(uri, headers: headers);
    if (resp.statusCode == 503) {
      throw ActivityDisabledException();
    }
    if (resp.statusCode != 200) {
      throw ApiException('GET /activity failed: ${resp.statusCode}');
    }
    final body = jsonDecode(resp.body) as Map<String, dynamic>;
    return ActivityPage.fromJson(body);
  }

  Future<void> triggerReview(int prId) async {
    final resp = await _client.post(
      _uri('/prs/$prId/review'),
      headers: await _authHeaders(),
    );
    if (resp.statusCode != 202) {
      throw ApiException('POST /prs/$prId/review failed: ${resp.statusCode}');
    }
  }

  Future<void> cancelReview(int prId) async {
    final resp = await _client.post(
      _uri('/prs/$prId/cancel'),
      headers: await _authHeaders(),
    );
    if (resp.statusCode != 202) {
      String message = 'POST /prs/$prId/cancel failed: ${resp.statusCode}';
      try {
        final body = jsonDecode(resp.body) as Map<String, dynamic>;
        final error = body['error'] as String?;
        if (error != null && error.isNotEmpty) message = error;
      } catch (_) {}
      throw ApiException(message);
    }
  }

  /// Adds a PR by its GitHub URL. The daemon adds the PR's repository to the
  /// monitored list (persisted to config) and reviews the PR immediately.
  /// Returns the created PR's store id (0 if the daemon deferred/omitted it).
  Future<int> addPRByUrl(String url) async {
    final resp = await _client.post(
      _uri('/prs/add'),
      headers: await _authHeaders(),
      body: jsonEncode({'url': url}),
    );
    if (resp.statusCode != 202) {
      String msg = 'POST /prs/add failed: ${resp.statusCode}';
      try {
        final err = (jsonDecode(resp.body) as Map<String, dynamic>)['error'];
        if (err is String && err.isNotEmpty) msg = err;
      } catch (_) {}
      throw ApiException(msg);
    }
    try {
      final pr =
          (jsonDecode(resp.body) as Map<String, dynamic>)['pr']
              as Map<String, dynamic>?;
      return (pr?['id'] as num?)?.toInt() ?? 0;
    } catch (_) {
      return 0;
    }
  }

  Future<void> dismissPR(int prId) async {
    final resp = await _client.post(
      _uri('/prs/$prId/dismiss'),
      headers: await _authHeaders(),
    );
    if (resp.statusCode != 200) {
      throw ApiException('POST /prs/$prId/dismiss failed: ${resp.statusCode}');
    }
  }

  /// Lists the PRs the authenticated user authored or is assigned to, with
  /// their merge-readiness state.
  ///
  /// Served from the daemon's store, so opening the view is instant and costs
  /// no GitHub API budget. The rows carry the check counts the listing needs
  /// to render its warning; the full per-check breakdown comes from
  /// [fetchMergeTracking].
  Future<List<MergeTrackingEntry>> fetchMergeTrackingList() async {
    final resp = await _client.get(
      _uri('/merge-tracking'),
      headers: await _authHeaders(),
    );
    if (resp.statusCode != 200) {
      throw ApiException('GET /merge-tracking failed: ${resp.statusCode}');
    }
    final list = jsonDecode(resp.body) as List<dynamic>;
    return list
        .map((e) => MergeTrackingEntry.fromJson(e as Map<String, dynamic>))
        .toList();
  }

  /// Fetches one tracked PR including the full per-check breakdown.
  Future<MergeTrackingEntry> fetchMergeTracking(int prId) async {
    final resp = await _client.get(
      _uri('/merge-tracking/$prId'),
      headers: await _authHeaders(),
    );
    if (resp.statusCode != 200) {
      throw ApiException(
        'GET /merge-tracking/$prId failed: ${resp.statusCode}',
      );
    }
    return MergeTrackingEntry.fromJson(
      jsonDecode(resp.body) as Map<String, dynamic>,
    );
  }

  /// Patches the per-repo merge-tracking override.
  ///
  /// Merge tracking keeps its overrides in `merge_tracking.repos.<repo>`, not
  /// in `repo_overrides`, so it cannot ride along with the rest of the per-repo
  /// config — it has its own endpoint on the daemon.
  Future<Map<String, dynamic>> patchMergeTrackingRepoConfig(
    String repo,
    Map<String, dynamic> patch,
  ) => _patchMergeTrackingScope('repos', repo, patch);

  /// The org-level half of [patchMergeTrackingRepoConfig].
  Future<Map<String, dynamic>> patchMergeTrackingOrgConfig(
    String org,
    Map<String, dynamic> patch,
  ) => _patchMergeTrackingScope('orgs', org, patch);

  Future<Map<String, dynamic>> _patchMergeTrackingScope(
    String scope,
    String id,
    Map<String, dynamic> patch,
  ) async {
    final updates = <String, dynamic>{
      for (final entry in patch.entries)
        if (entry.value != null) entry.key: entry.value,
    };
    final removals = patch.entries
        .where((entry) => entry.value == null)
        .map((entry) => entry.key);
    Map<String, dynamic> latest = {};

    if (updates.isNotEmpty) {
      final resp = await _client.patch(
        _uri('/config/merge_tracking/$scope/${Uri.encodeComponent(id)}'),
        headers: await _authHeaders(),
        body: jsonEncode(updates),
      );
      if (resp.statusCode != 200) {
        throw ApiException(
          _configMutationError(
            resp.body,
            'PATCH /config/merge_tracking/$scope/$id failed: ${resp.statusCode}',
          ),
        );
      }
      latest = jsonDecode(resp.body) as Map<String, dynamic>;
    }

    for (final field in removals) {
      final resp = await _client.delete(
        _uri(
          '/config/merge_tracking/$scope/${Uri.encodeComponent(id)}/${Uri.encodeComponent(field)}',
        ),
        headers: await _authHeaders(),
      );
      if (resp.statusCode != 200) {
        throw ApiException(
          _configMutationError(
            resp.body,
            'DELETE /config/merge_tracking/$scope/$id/$field failed: ${resp.statusCode}',
          ),
        );
      }
      latest = jsonDecode(resp.body) as Map<String, dynamic>;
    }
    return latest;
  }

  /// GETs a JSON object.
  ///
  /// A 404 returns null rather than throwing. That is how the cluster UI stays
  /// invisible on a plain single-daemon install: the control-plane routes
  /// answer 404 there, and surfacing that as an error would put a failure
  /// banner in front of every user who is not running instances.
  Future<Map<String, dynamic>?> getJson(String path) async {
    final resp = await _client.get(_uri(path), headers: await _authHeaders());
    if (resp.statusCode == 404) return null;
    if (resp.statusCode != 200) {
      throw ApiException(
        _configMutationError(resp.body, 'GET $path failed: ${resp.statusCode}'),
      );
    }
    final decoded = jsonDecode(resp.body);
    return decoded is Map<String, dynamic> ? decoded : null;
  }

  /// Sends a JSON request and decodes the object body, if any.
  ///
  /// [acceptStatuses] exists because some endpoints answer 202 (accepted) or
  /// 207 (partial success) and those are not failures — a propagation where one
  /// machine was rebooting still updated the others.
  Future<Map<String, dynamic>?> sendJson(
    String method,
    String path, {
    Map<String, dynamic>? body,
    Set<int> acceptStatuses = const {200, 201, 202, 204},
  }) async {
    final uri = _uri(path);
    final headers = await _authHeaders();
    final encoded = body == null ? null : jsonEncode(body);

    final http.Response resp;
    switch (method.toUpperCase()) {
      case 'POST':
        resp = await _client.post(uri, headers: headers, body: encoded);
      case 'PUT':
        resp = await _client.put(uri, headers: headers, body: encoded);
      case 'PATCH':
        resp = await _client.patch(uri, headers: headers, body: encoded);
      case 'DELETE':
        resp = await _client.delete(uri, headers: headers, body: encoded);
      default:
        throw ArgumentError('unsupported method $method');
    }
    if (!acceptStatuses.contains(resp.statusCode)) {
      throw ApiException(
        _configMutationError(
          resp.body,
          '$method $path failed: ${resp.statusCode}',
        ),
      );
    }
    if (resp.body.isEmpty) return null;
    try {
      final decoded = jsonDecode(resp.body);
      return decoded is Map<String, dynamic> ? decoded : null;
    } catch (_) {
      // A daemon that acted but answered with something we cannot parse did
      // still act; treating that as a failure would make the operator retry a
      // change that already landed.
      return null;
    }
  }

  String _configMutationError(String body, String fallback) {
    try {
      final err = (jsonDecode(body) as Map<String, dynamic>)['error'];
      if (err is String && err.isNotEmpty) return err;
    } catch (_) {}
    return fallback;
  }

  /// Adds a pull request to merge tracking by URL.
  ///
  /// Deliberately NOT [addPRByUrl]: that routes through the review pipeline,
  /// which refuses a PR the authenticated account authored — and Heimdallm
  /// authenticates as the operator, so that is every PR they open. Merge
  /// tracking exists for exactly those PRs, so it needs its own door.
  Future<MergeTrackingEntry> addMergeTracking(String url) async {
    final resp = await _client.post(
      _uri('/merge-tracking/add'),
      headers: await _authHeaders(),
      body: jsonEncode({'url': url}),
    );
    if (resp.statusCode != 200 && resp.statusCode != 202) {
      // The daemon explains refusals in prose — "merge tracking is disabled for
      // org/repo", "not a pull request URL" — and that text is the whole point
      // of the dialog's error line.
      String msg = 'POST /merge-tracking/add failed: ${resp.statusCode}';
      try {
        final err = (jsonDecode(resp.body) as Map<String, dynamic>)['error'];
        if (err is String && err.isNotEmpty) msg = err;
      } catch (_) {}
      throw ApiException(msg);
    }
    final decoded = jsonDecode(resp.body) as Map<String, dynamic>;
    if (decoded['pr_id'] != null) {
      return MergeTrackingEntry.fromJson(decoded);
    }

    // A successful enrolment can still race a failed read-back. Older daemons
    // answer that committed 202 with {status, pr}; adapt it to the same entry
    // shape so the dialog closes and refreshes instead of throwing a TypeError
    // after an operation that already took effect.
    final pr = decoded['pr'];
    if (pr is Map<String, dynamic> &&
        pr['id'] is num &&
        pr['repo'] is String &&
        pr['number'] is num) {
      return MergeTrackingEntry.fromJson({
        'pr_id': pr['id'],
        'repo': pr['repo'],
        'number': pr['number'],
        'title': pr['title'] ?? '',
        'url': pr['url'] ?? '',
        'author': pr['author'] ?? '',
        'phase': 'idle',
      });
    }
    throw ApiException(
      'POST /merge-tracking/add returned an invalid success response',
    );
  }

  /// Re-evaluates one tracked PR against GitHub.
  ///
  /// [dryRun] records the decision without acting on it — the honest answer to
  /// "why is this stuck?".
  Future<MergeTrackingEntry> evaluateMergeTracking(
    int prId, {
    bool dryRun = false,
  }) async {
    final path = dryRun
        ? '/merge-tracking/$prId/evaluate?dry_run=true'
        : '/merge-tracking/$prId/evaluate';
    final resp = await _client.post(_uri(path), headers: await _authHeaders());
    if (resp.statusCode != 200) {
      throw ApiException(
        'POST /merge-tracking/$prId/evaluate failed: ${resp.statusCode}',
      );
    }
    return MergeTrackingEntry.fromJson(
      jsonDecode(resp.body) as Map<String, dynamic>,
    );
  }

  /// Opts one PR out of (or back into) merge-tracking automation, without
  /// touching the repo-level config.
  Future<void> setMergeTrackingExcluded(int prId, bool excluded) async {
    final action = excluded ? 'exclude' : 'include';
    final resp = await _client.post(
      _uri('/merge-tracking/$prId/$action'),
      headers: await _authHeaders(),
    );
    if (resp.statusCode != 200) {
      throw ApiException(
        'POST /merge-tracking/$prId/$action failed: ${resp.statusCode}',
      );
    }
  }

  Future<void> undismissPR(int prId) async {
    final resp = await _client.post(
      _uri('/prs/$prId/undismiss'),
      headers: await _authHeaders(),
    );
    if (resp.statusCode != 200) {
      throw ApiException(
        'POST /prs/$prId/undismiss failed: ${resp.statusCode}',
      );
    }
  }

  /// Tells the daemon to reload its config from disk and restart the poll scheduler.
  Future<void> reloadConfig() async {
    try {
      await _client.post(_uri('/reload'), headers: await _authHeaders());
    } catch (_) {
      // Best-effort — daemon may not be running
    }
  }

  Future<void> shutdownDaemon() async {
    final resp = await _client.post(
      _uri('/shutdown'),
      headers: await _authHeaders(),
    );
    if (resp.statusCode != 202) {
      throw ApiException('POST /shutdown failed: ${resp.statusCode}');
    }
  }

  Future<Map<String, dynamic>> fetchConfig() async {
    final resp = await _client.get(
      _uri('/config'),
      headers: await _authHeaders(),
    );
    if (resp.statusCode != 200) {
      throw ApiException('GET /config failed: ${resp.statusCode}');
    }
    return jsonDecode(resp.body) as Map<String, dynamic>;
  }

  // ── Agents ──────────────────────────────────────────────────────────────

  Future<List<Map<String, dynamic>>> fetchAgents() async {
    final resp = await _client.get(
      _uri('/agents'),
      headers: await _authHeaders(),
    );
    if (resp.statusCode != 200) {
      throw ApiException('GET /agents failed: ${resp.statusCode}');
    }
    return (jsonDecode(resp.body) as List<dynamic>)
        .cast<Map<String, dynamic>>();
  }

  Future<void> upsertAgent(Map<String, dynamic> agent) async {
    final resp = await _client.post(
      _uri('/agents'),
      headers: await _authHeaders(),
      body: jsonEncode(agent),
    );
    if (resp.statusCode != 200) {
      throw ApiException('POST /agents failed: ${resp.statusCode}');
    }
  }

  Future<void> deleteAgent(String id) async {
    final resp = await _client.delete(
      _uri('/agents/$id'),
      headers: await _authHeaders(),
    );
    if (resp.statusCode != 200) {
      throw ApiException('DELETE /agents/$id failed: ${resp.statusCode}');
    }
  }

  Future<String> fetchMe() async {
    final resp = await _client.get(_uri('/me'), headers: await _authHeaders());
    if (resp.statusCode != 200) {
      throw ApiException('GET /me failed: ${resp.statusCode}');
    }
    final body = jsonDecode(resp.body) as Map<String, dynamic>;
    return body['login'] as String? ?? '';
  }

  Future<Map<String, dynamic>> fetchStats({
    List<String> repos = const [],
    List<String> orgs = const [],
  }) async {
    final params = <String, String>{};
    if (repos.isNotEmpty) params['repos'] = repos.join(',');
    if (orgs.isNotEmpty) params['orgs'] = orgs.join(',');
    final uri = _uri(
      '/stats',
    ).replace(queryParameters: params.isNotEmpty ? params : null);
    final resp = await _client.get(uri, headers: await _authHeaders());
    if (resp.statusCode != 200) {
      throw ApiException('GET /stats failed: ${resp.statusCode}');
    }
    return jsonDecode(resp.body) as Map<String, dynamic>;
  }

  /// Live GitHub API rate-limit buckets (core / search / graphql) for the
  /// daemon's token. Queries GitHub on each call — fetch on demand, not polled.
  Future<Map<String, dynamic>> fetchGitHubRateLimit() async {
    final resp = await _client.get(
      _uri('/github/rate_limit'),
      headers: await _authHeaders(),
    );
    if (resp.statusCode != 200) {
      throw ApiException('GET /github/rate_limit failed: ${resp.statusCode}');
    }
    return jsonDecode(resp.body) as Map<String, dynamic>;
  }

  Future<void> updateConfig(Map<String, dynamic> config) async {
    final resp = await _client.put(
      _uri('/config'),
      headers: await _authHeaders(),
      body: jsonEncode(config),
    );
    if (resp.statusCode != 200) {
      throw ApiException('PUT /config failed: ${resp.statusCode}');
    }
  }

  // ── Patch-based config (TOML merge) ─────────────────────────────────

  /// Sends a partial config update. The daemon deep-merges the patch into
  /// its TOML file. Only keys present in [patch] are updated; absent keys
  /// are left untouched. Returns the full config after the merge.
  Future<Map<String, dynamic>> patchConfig(Map<String, dynamic> patch) async {
    final resp = await _client.patch(
      _uri('/config'),
      headers: await _authHeaders(),
      body: jsonEncode(patch),
    );
    if (resp.statusCode != 200) {
      throw ApiException(
        'PATCH /config failed: ${resp.statusCode} ${resp.body}',
      );
    }
    return jsonDecode(resp.body) as Map<String, dynamic>;
  }

  /// Sends a partial per-repo override update. The daemon deep-merges the
  /// patch into [ai.repos."<repo>"] in the TOML file. Returns the full
  /// config after the merge.
  Future<Map<String, dynamic>> patchRepoConfig(
    String repo,
    Map<String, dynamic> patch,
  ) async {
    final resp = await _client.patch(
      _uri('/config/repos/${Uri.encodeComponent(repo)}'),
      headers: await _authHeaders(),
      body: jsonEncode(patch),
    );
    if (resp.statusCode != 200) {
      throw ApiException(
        'PATCH /config/repos failed: ${resp.statusCode} ${resp.body}',
      );
    }
    return jsonDecode(resp.body) as Map<String, dynamic>;
  }

  /// Sends a partial per-organization override update. The daemon deep-merges
  /// the patch into [ai.orgs."<org>"] in the TOML file. Returns the full
  /// config after the merge.
  Future<Map<String, dynamic>> patchOrgConfig(
    String org,
    Map<String, dynamic> patch,
  ) async {
    final resp = await _client.patch(
      _uri('/config/orgs/${Uri.encodeComponent(org)}'),
      headers: await _authHeaders(),
      body: jsonEncode(patch),
    );
    if (resp.statusCode != 200) {
      throw ApiException(
        'PATCH /config/orgs failed: ${resp.statusCode} ${resp.body}',
      );
    }
    return jsonDecode(resp.body) as Map<String, dynamic>;
  }

  /// Resets a per-repo override field back to the global default by
  /// removing it from the TOML file. [fieldPath] uses "/" for nested
  /// fields (e.g. "issue_tracking/develop_labels"). Returns the full
  /// config after the deletion.
  Future<Map<String, dynamic>> deleteRepoField(
    String repo,
    String fieldPath,
  ) async {
    final resp = await _client.delete(
      _uri('/config/repos/${Uri.encodeComponent(repo)}/$fieldPath'),
      headers: await _authHeaders(),
    );
    if (resp.statusCode != 200) {
      throw ApiException(
        'DELETE /config/repos field failed: ${resp.statusCode} ${resp.body}',
      );
    }
    return jsonDecode(resp.body) as Map<String, dynamic>;
  }

  /// Resets a per-organization override field back to the global default by
  /// removing it from the TOML file. [fieldPath] uses "/" for nested fields.
  Future<Map<String, dynamic>> deleteOrgField(
    String org,
    String fieldPath,
  ) async {
    final resp = await _client.delete(
      _uri('/config/orgs/${Uri.encodeComponent(org)}/$fieldPath'),
      headers: await _authHeaders(),
    );
    if (resp.statusCode != 200) {
      throw ApiException(
        'DELETE /config/orgs field failed: ${resp.statusCode} ${resp.body}',
      );
    }
    return jsonDecode(resp.body) as Map<String, dynamic>;
  }

  // ── Repo metadata (autocomplete) ─────────────────────────────────────

  Future<List<String>> fetchRepoLabels(String repo) async {
    final resp = await _client.get(
      _uri('/repos/${Uri.encodeComponent(repo)}/labels'),
      headers: await _authHeaders(),
    );
    if (resp.statusCode != 200) return [];
    return (jsonDecode(resp.body) as List<dynamic>).cast<String>();
  }

  Future<List<String>> fetchRepoCollaborators(String repo) async {
    final resp = await _client.get(
      _uri('/repos/${Uri.encodeComponent(repo)}/collaborators'),
      headers: await _authHeaders(),
    );
    if (resp.statusCode != 200) return [];
    return (jsonDecode(resp.body) as List<dynamic>).cast<String>();
  }

  // ── Issues ────────────────────────────────────────────────────────────

  Future<List<TrackedIssue>> fetchIssues({
    List<String> states = const [],
  }) async {
    var path = '/issues';
    if (states.isNotEmpty) {
      path += '?state=${states.join(',')}';
    }
    final resp = await _client.get(_uri(path), headers: await _authHeaders());
    if (resp.statusCode != 200) {
      throw ApiException('GET /issues failed: ${resp.statusCode}');
    }
    final list = jsonDecode(resp.body) as List<dynamic>;
    return list
        .map(
          (e) =>
              TrackedIssue.fromJson(_parseIssueMap(e as Map<String, dynamic>)),
        )
        .toList();
  }

  Future<Map<String, dynamic>> fetchIssue(int id) async {
    final resp = await _client.get(
      _uri('/issues/$id'),
      headers: await _authHeaders(),
    );
    if (resp.statusCode != 200) {
      throw ApiException('GET /issues/$id failed: ${resp.statusCode}');
    }
    final body = jsonDecode(resp.body) as Map<String, dynamic>;
    final issue = TrackedIssue.fromJson(
      _parseIssueMap(body['issue'] as Map<String, dynamic>),
    );
    final reviewsRaw = body['reviews'] as List<dynamic>? ?? [];
    final reviews = reviewsRaw
        .map(
          (r) => TrackedIssueReview.fromJson(
            _parseIssueReviewMap(r as Map<String, dynamic>),
          ),
        )
        .toList();
    return {'issue': issue, 'reviews': reviews};
  }

  Future<void> triggerIssueReview(int issueId) async {
    final resp = await _client.post(
      _uri('/issues/$issueId/review'),
      headers: await _authHeaders(),
    );
    if (resp.statusCode != 202) {
      throw ApiException(
        'POST /issues/$issueId/review failed: ${resp.statusCode}',
      );
    }
  }

  /// Moves an issue to the next configured stage by updating GitHub labels.
  /// The daemon poller executes the new stage after it observes the label swap.
  Future<void> promoteIssue(int issueId) async {
    final resp = await _client.post(
      _uri('/issues/$issueId/promote'),
      headers: await _authHeaders(),
    );
    if (resp.statusCode != 202) {
      throw ApiException(
        'POST /issues/$issueId/promote failed: ${resp.statusCode}',
      );
    }
  }

  Future<void> dismissIssue(int issueId) async {
    final resp = await _client.post(
      _uri('/issues/$issueId/dismiss'),
      headers: await _authHeaders(),
    );
    if (resp.statusCode != 200) {
      throw ApiException(
        'POST /issues/$issueId/dismiss failed: ${resp.statusCode}',
      );
    }
  }

  Future<void> undismissIssue(int issueId) async {
    final resp = await _client.post(
      _uri('/issues/$issueId/undismiss'),
      headers: await _authHeaders(),
    );
    if (resp.statusCode != 200) {
      throw ApiException(
        'POST /issues/$issueId/undismiss failed: ${resp.statusCode}',
      );
    }
  }

  PR _parsePRWithReview(Map<String, dynamic> json) {
    if (json['latest_review'] != null) {
      json = Map.from(json);
      json['latest_review'] = _parseReviewMap(
        json['latest_review'] as Map<String, dynamic>,
      );
    }
    return PR.fromJson(json);
  }

  Review _parseReview(Map<String, dynamic> json) {
    return Review.fromJson(_parseReviewMap(json));
  }

  Map<String, dynamic> _parseReviewMap(Map<String, dynamic> json) {
    final result = Map<String, dynamic>.from(json);
    if (result['issues'] is String) {
      result['issues'] = jsonDecode(result['issues'] as String);
    }
    result['issues'] ??= <dynamic>[];
    return result;
  }

  Map<String, dynamic> _parseIssueMap(Map<String, dynamic> json) {
    final result = Map<String, dynamic>.from(json);
    if (result['latest_review'] != null) {
      result['latest_review'] = _parseIssueReviewMap(
        result['latest_review'] as Map<String, dynamic>,
      );
    }
    return result;
  }

  Map<String, dynamic> _parseIssueReviewMap(Map<String, dynamic> json) {
    final result = Map<String, dynamic>.from(json);
    if (result['triage'] is String) {
      result['triage'] = jsonDecode(result['triage'] as String);
    }
    if (result['next_steps'] is String) {
      result['next_steps'] = jsonDecode(result['next_steps'] as String);
    }
    result['triage'] ??= <String, dynamic>{};
    result['next_steps'] ??= <dynamic>[];
    return result;
  }
}

class ApiException implements Exception {
  final String message;
  ApiException(this.message);
  @override
  String toString() => 'ApiException: $message';
}

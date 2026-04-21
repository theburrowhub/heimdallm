import 'dart:convert';
import 'package:http/http.dart' as http;
import '../models/activity.dart';
import '../models/pr.dart';
import '../models/review.dart';
import '../models/tracked_issue.dart';
import '../platform/platform_services.dart';

class ApiClient {
  final http.Client _client;
  final PlatformServices _platform;

  ApiClient({http.Client? httpClient, required PlatformServices platform})
      : _client = httpClient ?? http.Client(),
        _platform = platform;

  Uri _uri(String path) => Uri.parse('${_platform.apiBaseUrl}$path');

  /// Clears the cached API token, forcing the next request to re-read it.
  void clearTokenCache() {
    _platform.clearApiTokenCache();
  }

  /// Headers for mutating requests (POST/PUT/DELETE). Adds
  /// X-Heimdallm-Token when the platform provides one (desktop). On web
  /// the token is null and the header is omitted — Nginx injects it.
  Future<Map<String, String>> _authHeaders() async {
    final token = await _platform.loadApiToken();
    return {
      'Content-Type': 'application/json',
      if (token != null && token.isNotEmpty) 'X-Heimdallm-Token': token,
    };
  }

  Future<bool> checkHealth() async {
    try {
      final resp = await _client
          .get(_uri('/health'))
          .timeout(const Duration(seconds: 3));
      return resp.statusCode == 200;
    } catch (_) {
      return false;
    }
  }

  Future<List<PR>> fetchPRs() async {
    final resp = await _client.get(_uri('/prs'), headers: await _authHeaders());
    if (resp.statusCode != 200) {
      throw ApiException('GET /prs failed: ${resp.statusCode}');
    }
    final list = jsonDecode(resp.body) as List<dynamic>;
    return list.map((e) => _parsePRWithReview(e as Map<String, dynamic>)).toList();
  }

  Future<Map<String, dynamic>> fetchPR(int id) async {
    final resp = await _client.get(_uri('/prs/$id'), headers: await _authHeaders());
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
    final uri = _uri('/activity').replace(queryParameters: q.toQueryParameters());
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
    final resp = await _client.post(_uri('/prs/$prId/review'),
        headers: await _authHeaders());
    if (resp.statusCode != 202) {
      throw ApiException('POST /prs/$prId/review failed: ${resp.statusCode}');
    }
  }

  Future<void> dismissPR(int prId) async {
    final resp = await _client.post(_uri('/prs/$prId/dismiss'),
        headers: await _authHeaders());
    if (resp.statusCode != 200) {
      throw ApiException('POST /prs/$prId/dismiss failed: ${resp.statusCode}');
    }
  }

  Future<void> undismissPR(int prId) async {
    final resp = await _client.post(_uri('/prs/$prId/undismiss'),
        headers: await _authHeaders());
    if (resp.statusCode != 200) {
      throw ApiException('POST /prs/$prId/undismiss failed: ${resp.statusCode}');
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

  Future<Map<String, dynamic>> fetchConfig() async {
    final resp = await _client.get(_uri('/config'),
        headers: await _authHeaders());
    if (resp.statusCode != 200) {
      throw ApiException('GET /config failed: ${resp.statusCode}');
    }
    return jsonDecode(resp.body) as Map<String, dynamic>;
  }

  // ── Agents ──────────────────────────────────────────────────────────────

  Future<List<Map<String, dynamic>>> fetchAgents() async {
    final resp = await _client.get(_uri('/agents'),
        headers: await _authHeaders());
    if (resp.statusCode != 200) throw ApiException('GET /agents failed: ${resp.statusCode}');
    return (jsonDecode(resp.body) as List<dynamic>)
        .cast<Map<String, dynamic>>();
  }

  Future<void> upsertAgent(Map<String, dynamic> agent) async {
    final resp = await _client.post(_uri('/agents'),
        headers: await _authHeaders(),
        body: jsonEncode(agent));
    if (resp.statusCode != 200) throw ApiException('POST /agents failed: ${resp.statusCode}');
  }

  Future<void> deleteAgent(String id) async {
    final resp = await _client.delete(_uri('/agents/$id'),
        headers: await _authHeaders());
    if (resp.statusCode != 200) throw ApiException('DELETE /agents/$id failed: ${resp.statusCode}');
  }

  Future<String> fetchMe() async {
    final resp = await _client.get(_uri('/me'), headers: await _authHeaders());
    if (resp.statusCode != 200) throw ApiException('GET /me failed: ${resp.statusCode}');
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
    final uri = _uri('/stats').replace(queryParameters: params.isNotEmpty ? params : null);
    final resp = await _client.get(uri, headers: await _authHeaders());
    if (resp.statusCode != 200) throw ApiException('GET /stats failed: ${resp.statusCode}');
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
      throw ApiException('PATCH /config failed: ${resp.statusCode} ${resp.body}');
    }
    return jsonDecode(resp.body) as Map<String, dynamic>;
  }

  /// Sends a partial per-repo override update. The daemon deep-merges the
  /// patch into [ai.repos."<repo>"] in the TOML file. Returns the full
  /// config after the merge.
  Future<Map<String, dynamic>> patchRepoConfig(
      String repo, Map<String, dynamic> patch) async {
    final resp = await _client.patch(
      _uri('/config/repos/${Uri.encodeComponent(repo)}'),
      headers: await _authHeaders(),
      body: jsonEncode(patch),
    );
    if (resp.statusCode != 200) {
      throw ApiException(
          'PATCH /config/repos failed: ${resp.statusCode} ${resp.body}');
    }
    return jsonDecode(resp.body) as Map<String, dynamic>;
  }

  /// Resets a per-repo override field back to the global default by
  /// removing it from the TOML file. [fieldPath] uses "/" for nested
  /// fields (e.g. "issue_tracking/develop_labels"). Returns the full
  /// config after the deletion.
  Future<Map<String, dynamic>> deleteRepoField(
      String repo, String fieldPath) async {
    final resp = await _client.delete(
      _uri('/config/repos/${Uri.encodeComponent(repo)}/$fieldPath'),
      headers: await _authHeaders(),
    );
    if (resp.statusCode != 200) {
      throw ApiException(
          'DELETE /config/repos field failed: ${resp.statusCode} ${resp.body}');
    }
    return jsonDecode(resp.body) as Map<String, dynamic>;
  }

  // ── Repo metadata (autocomplete) ─────────────────────────────────────

  Future<List<String>> fetchRepoLabels(String repo) async {
    final resp = await _client.get(
        _uri('/repos/${Uri.encodeComponent(repo)}/labels'),
        headers: await _authHeaders());
    if (resp.statusCode != 200) return [];
    return (jsonDecode(resp.body) as List<dynamic>).cast<String>();
  }

  Future<List<String>> fetchRepoCollaborators(String repo) async {
    final resp = await _client.get(
        _uri('/repos/${Uri.encodeComponent(repo)}/collaborators'),
        headers: await _authHeaders());
    if (resp.statusCode != 200) return [];
    return (jsonDecode(resp.body) as List<dynamic>).cast<String>();
  }

  // ── Issues ────────────────────────────────────────────────────────────

  Future<List<TrackedIssue>> fetchIssues() async {
    final resp = await _client.get(_uri('/issues'), headers: await _authHeaders());
    if (resp.statusCode != 200) {
      throw ApiException('GET /issues failed: ${resp.statusCode}');
    }
    final list = jsonDecode(resp.body) as List<dynamic>;
    return list
        .map((e) => TrackedIssue.fromJson(_parseIssueMap(e as Map<String, dynamic>)))
        .toList();
  }

  Future<Map<String, dynamic>> fetchIssue(int id) async {
    final resp = await _client.get(_uri('/issues/$id'), headers: await _authHeaders());
    if (resp.statusCode != 200) {
      throw ApiException('GET /issues/$id failed: ${resp.statusCode}');
    }
    final body = jsonDecode(resp.body) as Map<String, dynamic>;
    final issue = TrackedIssue.fromJson(
        _parseIssueMap(body['issue'] as Map<String, dynamic>));
    final reviewsRaw = body['reviews'] as List<dynamic>? ?? [];
    final reviews = reviewsRaw
        .map((r) => TrackedIssueReview.fromJson(
            _parseIssueReviewMap(r as Map<String, dynamic>)))
        .toList();
    return {'issue': issue, 'reviews': reviews};
  }

  Future<void> triggerIssueReview(int issueId) async {
    final resp = await _client.post(_uri('/issues/$issueId/review'),
        headers: await _authHeaders());
    if (resp.statusCode != 202) {
      throw ApiException('POST /issues/$issueId/review failed: ${resp.statusCode}');
    }
  }

  Future<void> dismissIssue(int issueId) async {
    final resp = await _client.post(_uri('/issues/$issueId/dismiss'),
        headers: await _authHeaders());
    if (resp.statusCode != 200) {
      throw ApiException('POST /issues/$issueId/dismiss failed: ${resp.statusCode}');
    }
  }

  Future<void> undismissIssue(int issueId) async {
    final resp = await _client.post(_uri('/issues/$issueId/undismiss'),
        headers: await _authHeaders());
    if (resp.statusCode != 200) {
      throw ApiException('POST /issues/$issueId/undismiss failed: ${resp.statusCode}');
    }
  }

  PR _parsePRWithReview(Map<String, dynamic> json) {
    if (json['latest_review'] != null) {
      json = Map.from(json);
      json['latest_review'] = _parseReviewMap(
          json['latest_review'] as Map<String, dynamic>);
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
    if (result['suggestions'] is String) {
      result['suggestions'] = jsonDecode(result['suggestions'] as String);
    }
    result['issues'] ??= <dynamic>[];
    result['suggestions'] ??= <dynamic>[];
    return result;
  }

  Map<String, dynamic> _parseIssueMap(Map<String, dynamic> json) {
    final result = Map<String, dynamic>.from(json);
    if (result['latest_review'] != null) {
      result['latest_review'] = _parseIssueReviewMap(
          result['latest_review'] as Map<String, dynamic>);
    }
    return result;
  }

  Map<String, dynamic> _parseIssueReviewMap(Map<String, dynamic> json) {
    final result = Map<String, dynamic>.from(json);
    if (result['triage'] is String) {
      result['triage'] = jsonDecode(result['triage'] as String);
    }
    if (result['suggestions'] is String) {
      result['suggestions'] = jsonDecode(result['suggestions'] as String);
    }
    result['triage'] ??= <String, dynamic>{};
    result['suggestions'] ??= <dynamic>[];
    return result;
  }
}

class ApiException implements Exception {
  final String message;
  ApiException(this.message);
  @override
  String toString() => 'ApiException: $message';
}

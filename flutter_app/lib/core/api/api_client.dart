import 'dart:convert';
import 'dart:io';
import 'package:http/http.dart' as http;
import '../models/pr.dart';
import '../models/review.dart';

class ApiClient {
  final http.Client _client;
  final int port;
  String? _cachedToken;

  ApiClient({http.Client? httpClient, this.port = 7842})
      : _client = httpClient ?? http.Client();

  Uri _uri(String path) => Uri.parse('http://127.0.0.1:$port$path');

  /// Clears the cached API token, forcing the next request to re-read it from disk.
  /// Call this after storing a new token (e.g. on token rotation).
  void clearTokenCache() {
    _cachedToken = null;
  }

  /// Returns the API token read from the daemon's token file, or null if not found.
  /// Cached after first successful read.
  Future<String?> _apiToken() async {
    if (_cachedToken != null) return _cachedToken;
    final home = Platform.environment['HOME'] ?? '';
    if (home.isEmpty) return null;
    final file = File('$home/.local/share/heimdallr/api_token');
    if (await file.exists()) {
      _cachedToken = (await file.readAsString()).trim();
    }
    return _cachedToken;
  }

  /// Headers for mutating requests (POST/PUT/DELETE).
  /// Includes X-Heimdallr-Token to satisfy the auth middleware (issue #3).
  Future<Map<String, String>> _authHeaders() async {
    final token = await _apiToken();
    return {
      'Content-Type': 'application/json',
      if (token != null && token.isNotEmpty) 'X-Heimdallr-Token': token,
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

  Future<Map<String, dynamic>> fetchStats() async {
    final resp = await _client.get(_uri('/stats'), headers: await _authHeaders());
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
}

class ApiException implements Exception {
  final String message;
  ApiException(this.message);
  @override
  String toString() => 'ApiException: $message';
}

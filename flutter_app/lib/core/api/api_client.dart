import 'dart:convert';
import 'package:http/http.dart' as http;
import '../models/pr.dart';
import '../models/review.dart';

class ApiClient {
  final http.Client _client;
  final int port;

  ApiClient({http.Client? httpClient, this.port = 7842})
      : _client = httpClient ?? http.Client();

  Uri _uri(String path) => Uri.parse('http://127.0.0.1:$port$path');

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
    final resp = await _client.get(_uri('/prs'));
    if (resp.statusCode != 200) {
      throw ApiException('GET /prs failed: ${resp.statusCode}');
    }
    final list = jsonDecode(resp.body) as List<dynamic>;
    return list.map((e) => _parsePRWithReview(e as Map<String, dynamic>)).toList();
  }

  Future<Map<String, dynamic>> fetchPR(int id) async {
    final resp = await _client.get(_uri('/prs/$id'));
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
    final resp = await _client.post(_uri('/prs/$prId/review'));
    if (resp.statusCode != 202) {
      throw ApiException('POST /prs/$prId/review failed: ${resp.statusCode}');
    }
  }

  Future<Map<String, dynamic>> fetchConfig() async {
    final resp = await _client.get(_uri('/config'));
    if (resp.statusCode != 200) {
      throw ApiException('GET /config failed: ${resp.statusCode}');
    }
    return jsonDecode(resp.body) as Map<String, dynamic>;
  }

  Future<void> updateConfig(Map<String, dynamic> config) async {
    final resp = await _client.put(
      _uri('/config'),
      headers: {'Content-Type': 'application/json'},
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

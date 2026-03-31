import 'dart:convert';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:heimdallr/core/api/api_client.dart';

void main() {
  group('ApiClient', () {
    test('fetchPRs returns list of PRs', () async {
      final mockClient = MockClient((request) async {
        if (request.url.path == '/prs') {
          return http.Response(jsonEncode([
            {
              'id': 1, 'github_id': 101, 'repo': 'org/repo', 'number': 42,
              'title': 'Fix bug', 'author': 'alice', 'url': 'https://github.com/org/repo/pull/42',
              'state': 'open', 'updated_at': '2026-03-31T10:00:00Z',
              'latest_review': null,
            }
          ]), 200);
        }
        return http.Response('not found', 404);
      });

      final client = ApiClient(httpClient: mockClient, port: 7842);
      final prs = await client.fetchPRs();
      expect(prs.length, 1);
      expect(prs.first.title, 'Fix bug');
    });

    test('triggerReview returns 202', () async {
      final mockClient = MockClient((request) async {
        if (request.url.path == '/prs/1/review' && request.method == 'POST') {
          return http.Response(jsonEncode({'status': 'review queued'}), 202);
        }
        return http.Response('not found', 404);
      });

      final client = ApiClient(httpClient: mockClient, port: 7842);
      await expectLater(client.triggerReview(1), completes);
    });

    test('checkHealth returns true when daemon up', () async {
      final mockClient = MockClient((_) async =>
          http.Response(jsonEncode({'status': 'ok'}), 200));
      final client = ApiClient(httpClient: mockClient, port: 7842);
      final healthy = await client.checkHealth();
      expect(healthy, isTrue);
    });

    test('checkHealth returns false when daemon down', () async {
      final mockClient = MockClient((_) async => throw Exception('Connection refused'));
      final client = ApiClient(httpClient: mockClient, port: 7842);
      final healthy = await client.checkHealth();
      expect(healthy, isFalse);
    });
  });
}

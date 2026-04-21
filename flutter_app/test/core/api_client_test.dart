import 'dart:convert';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'platform/fake_platform_services.dart';

void main() {
  group('ApiClient (desktop shape — absolute URL + token)', () {
    test('fetchPRs sends X-Heimdallm-Token and hits absolute URL', () async {
      final platform = FakePlatformServices(
        apiBaseUrl: 'http://127.0.0.1:7842',
        token: 'abc-123',
      );
      http.BaseRequest? captured;
      final mockClient = MockClient((request) async {
        captured = request;
        if (request.url.path == '/prs') {
          return http.Response(jsonEncode([
            {
              'id': 1, 'github_id': 101, 'repo': 'org/repo', 'number': 42,
              'title': 'Fix bug', 'author': 'alice',
              'url': 'https://github.com/org/repo/pull/42',
              'state': 'open', 'updated_at': '2026-03-31T10:00:00Z',
              'latest_review': null,
            }
          ]), 200);
        }
        return http.Response('not found', 404);
      });

      final client = ApiClient(httpClient: mockClient, platform: platform);
      final prs = await client.fetchPRs();
      expect(prs.length, 1);
      expect(captured!.url.toString(), 'http://127.0.0.1:7842/prs');
      expect(captured!.headers['X-Heimdallm-Token'], 'abc-123');
    });

    test('triggerReview hits POST and returns 202', () async {
      final platform = FakePlatformServices(
        apiBaseUrl: 'http://127.0.0.1:7842',
        token: 'abc-123',
      );
      final mockClient = MockClient((request) async {
        if (request.url.path == '/prs/1/review' && request.method == 'POST') {
          return http.Response(jsonEncode({'status': 'review queued'}), 202);
        }
        return http.Response('not found', 404);
      });
      final client = ApiClient(httpClient: mockClient, platform: platform);
      await expectLater(client.triggerReview(1), completes);
    });

    test('checkHealth returns true when daemon up', () async {
      final platform = FakePlatformServices(token: 'abc-123');
      final mockClient = MockClient((_) async =>
          http.Response(jsonEncode({'status': 'ok'}), 200));
      final client = ApiClient(httpClient: mockClient, platform: platform);
      expect(await client.checkHealth(), isTrue);
    });

    test('checkHealth returns false when daemon down', () async {
      final platform = FakePlatformServices(token: 'abc-123');
      final mockClient = MockClient((_) async => throw Exception('Connection refused'));
      final client = ApiClient(httpClient: mockClient, platform: platform);
      expect(await client.checkHealth(), isFalse);
    });
  });

  group('ApiClient (web shape — relative URL, no token)', () {
    test('fetchPRs sends relative URL + no X-Heimdallm-Token header', () async {
      final platform = FakePlatformServices(
        apiBaseUrl: '/api',
        token: null,
      );
      http.BaseRequest? captured;
      final mockClient = MockClient((request) async {
        captured = request;
        return http.Response(jsonEncode([]), 200);
      });
      final client = ApiClient(httpClient: mockClient, platform: platform);
      await client.fetchPRs();
      // Dart's Uri.parse resolves a relative string against a default base;
      // we assert the path + absence of the auth header.
      expect(captured!.url.path.endsWith('/api/prs'), isTrue,
          reason: 'expected path ending in /api/prs, got ${captured!.url}');
      expect(captured!.headers.containsKey('X-Heimdallm-Token'), isFalse);
    });

    test('clearTokenCache delegates to the platform', () async {
      final platform = FakePlatformServices();
      final client = ApiClient(
        httpClient: MockClient((_) async => http.Response('', 200)),
        platform: platform,
      );
      client.clearTokenCache();
      expect(platform.clearApiTokenCacheCalls, 1);
    });
  });
}

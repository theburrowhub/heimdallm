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
          return http.Response(
            jsonEncode([
              {
                'id': 1,
                'github_id': 101,
                'repo': 'org/repo',
                'number': 42,
                'title': 'Fix bug',
                'author': 'alice',
                'url': 'https://github.com/org/repo/pull/42',
                'state': 'open',
                'updated_at': '2026-03-31T10:00:00Z',
                'latest_review': null,
              },
            ]),
            200,
          );
        }
        return http.Response('not found', 404);
      });

      final client = ApiClient(httpClient: mockClient, platform: platform);
      final prs = await client.fetchPRs();
      expect(prs.length, 1);
      expect(captured!.url.toString(), 'http://127.0.0.1:7842/prs');
      expect(captured!.headers['X-Heimdallm-Token'], 'abc-123');
    });

    test('fetchAgentModels returns the live CLI catalogs', () async {
      final platform = FakePlatformServices(
        apiBaseUrl: 'http://127.0.0.1:7842',
        token: 'abc-123',
      );
      http.BaseRequest? captured;
      final client = ApiClient(
        httpClient: MockClient((request) async {
          captured = request;
          return http.Response(
            jsonEncode({
              'claude': ['sonnet', 'opus[1m]'],
              'codex': ['gpt-current'],
              'gemini': <String>[],
            }),
            200,
          );
        }),
        platform: platform,
      );

      final models = await client.fetchAgentModels();

      expect(captured!.method, 'GET');
      expect(captured!.url.toString(), 'http://127.0.0.1:7842/agents/models');
      expect(captured!.headers['X-Heimdallm-Token'], 'abc-123');
      expect(models['claude'], ['sonnet', 'opus[1m]']);
      expect(models['codex'], ['gpt-current']);
      expect(models['gemini'], isEmpty);
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

    test(
      'cancelReview hits the scoped POST and surfaces daemon errors',
      () async {
        final platform = FakePlatformServices(
          apiBaseUrl: 'http://127.0.0.1:7842',
          token: 'abc-123',
        );
        var active = true;
        final mockClient = MockClient((request) async {
          if (request.url.path != '/prs/73/cancel' ||
              request.method != 'POST') {
            return http.Response('not found', 404);
          }
          if (active) {
            return http.Response(
              jsonEncode({'status': 'cancellation requested'}),
              202,
            );
          }
          return http.Response(
            jsonEncode({'error': 'no active review for this PR'}),
            409,
          );
        });
        final client = ApiClient(httpClient: mockClient, platform: platform);

        await expectLater(client.cancelReview(73), completes);
        active = false;
        await expectLater(
          client.cancelReview(73),
          throwsA(
            isA<ApiException>().having(
              (error) => error.message,
              'message',
              'no active review for this PR',
            ),
          ),
        );
      },
    );

    test('addPRByUrl posts the URL and returns the stored PR id', () async {
      final platform = FakePlatformServices(
        apiBaseUrl: 'http://127.0.0.1:7842',
        token: 'abc-123',
      );
      http.Request? captured;
      final client = ApiClient(
        httpClient: MockClient((request) async {
          captured = request;
          return http.Response(
            jsonEncode({
              'status': 'pr added; review queued',
              'pr': {'id': 73},
            }),
            202,
          );
        }),
        platform: platform,
      );

      final id = await client.addPRByUrl(
        'https://github.com/acme/widgets/pull/42',
      );

      expect(id, 73);
      expect(captured!.method, 'POST');
      expect(captured!.url.toString(), 'http://127.0.0.1:7842/prs/add');
      expect(captured!.headers['X-Heimdallm-Token'], 'abc-123');
      expect(jsonDecode(captured!.body), {
        'url': 'https://github.com/acme/widgets/pull/42',
      });
    });

    test('addPRByUrl surfaces a structured daemon error', () async {
      final client = ApiClient(
        httpClient: MockClient(
          (_) async =>
              http.Response(jsonEncode({'error': 'PR not found'}), 502),
        ),
        platform: FakePlatformServices(token: 'abc-123'),
      );

      await expectLater(
        client.addPRByUrl('https://github.com/acme/widgets/pull/404'),
        throwsA(
          isA<ApiException>().having(
            (error) => error.message,
            'message',
            'PR not found',
          ),
        ),
      );
    });

    test(
      'addPRByUrl falls back to status when error JSON is malformed',
      () async {
        final client = ApiClient(
          httpClient: MockClient(
            (_) async => http.Response('bad gateway', 500),
          ),
          platform: FakePlatformServices(token: 'abc-123'),
        );

        await expectLater(
          client.addPRByUrl('https://github.com/acme/widgets/pull/42'),
          throwsA(
            isA<ApiException>().having(
              (error) => error.message,
              'message',
              'POST /prs/add failed: 500',
            ),
          ),
        );
      },
    );

    test('addPRByUrl returns zero when a 202 omits a usable PR id', () async {
      var response = http.Response(jsonEncode({'status': 'deferred'}), 202);
      final client = ApiClient(
        httpClient: MockClient((_) async => response),
        platform: FakePlatformServices(token: 'abc-123'),
      );

      expect(
        await client.addPRByUrl('https://github.com/acme/widgets/pull/42'),
        0,
      );

      response = http.Response('not json', 202);
      expect(
        await client.addPRByUrl('https://github.com/acme/widgets/pull/42'),
        0,
      );
    });

    test('checkHealth returns true when daemon up', () async {
      final platform = FakePlatformServices(token: 'abc-123');
      final mockClient = MockClient(
        (_) async => http.Response(
          jsonEncode({'status': 'ok', 'checks': {}}),
          200,
          headers: {'x-heimdallm-daemon': '1'},
        ),
      );
      final client = ApiClient(httpClient: mockClient, platform: platform);
      expect(await client.checkHealth(), isTrue);
    });

    test('checkHealth rejects a foreign 200 health endpoint', () async {
      final platform = FakePlatformServices(token: 'abc-123');
      final mockClient = MockClient(
        (_) async => http.Response(jsonEncode({'status': 'ok'}), 200),
      );
      final client = ApiClient(httpClient: mockClient, platform: platform);
      expect(await client.checkHealth(), isFalse);
    });

    test('checkHealth returns false when daemon down', () async {
      final platform = FakePlatformServices(token: 'abc-123');
      final mockClient = MockClient(
        (_) async => throw Exception('Connection refused'),
      );
      final client = ApiClient(httpClient: mockClient, platform: platform);
      expect(await client.checkHealth(), isFalse);
    });

    test('fetchHealth preserves identified degraded diagnostics', () async {
      final platform = FakePlatformServices(token: 'abc-123');
      final mockClient = MockClient(
        (_) async => http.Response(
          jsonEncode({
            'status': 'degraded',
            'checks': {
              'last_poll': {'ok': false},
            },
          }),
          503,
          headers: {'x-heimdallm-daemon': '1'},
        ),
      );
      final client = ApiClient(httpClient: mockClient, platform: platform);

      final health = await client.fetchHealth();

      expect(health?['status'], 'degraded');
      expect(health?['checks'], isA<Map<String, dynamic>>());
    });

    test('fetchHealth rejects a foreign successful endpoint', () async {
      final platform = FakePlatformServices(token: 'abc-123');
      final mockClient = MockClient(
        (_) async => http.Response(jsonEncode({'status': 'ok'}), 200),
      );
      final client = ApiClient(httpClient: mockClient, platform: platform);

      expect(await client.fetchHealth(), isNull);
    });

    test('patchOrgConfig uses encoded org route and PATCH body', () async {
      final platform = FakePlatformServices(
        apiBaseUrl: 'http://127.0.0.1:7842',
        token: 'abc-123',
      );
      http.Request? captured;
      final mockClient = MockClient((request) async {
        captured = request;
        return http.Response(jsonEncode({'org_overrides': {}}), 200);
      });
      final client = ApiClient(httpClient: mockClient, platform: platform);

      await client.patchOrgConfig('acme-org', {
        'primary': 'gemini',
        'issue_tracking': {
          'review_only_labels': ['needs-review'],
        },
      });

      expect(captured!.method, 'PATCH');
      expect(
        captured!.url.toString(),
        'http://127.0.0.1:7842/config/orgs/acme-org',
      );
      expect(captured!.headers['X-Heimdallm-Token'], 'abc-123');
      expect(jsonDecode(captured!.body), {
        'primary': 'gemini',
        'issue_tracking': {
          'review_only_labels': ['needs-review'],
        },
      });
    });

    test('deleteOrgField uses nested field route', () async {
      final platform = FakePlatformServices(
        apiBaseUrl: 'http://127.0.0.1:7842',
        token: 'abc-123',
      );
      http.BaseRequest? captured;
      final mockClient = MockClient((request) async {
        captured = request;
        return http.Response(jsonEncode({'org_overrides': {}}), 200);
      });
      final client = ApiClient(httpClient: mockClient, platform: platform);

      await client.deleteOrgField('acme-org', 'issue_tracking/develop_labels');

      expect(captured!.method, 'DELETE');
      expect(
        captured!.url.toString(),
        'http://127.0.0.1:7842/config/orgs/acme-org/issue_tracking/develop_labels',
      );
    });
  });

  group('ApiClient (web shape — relative URL, no token)', () {
    test('fetchPRs sends relative URL + no X-Heimdallm-Token header', () async {
      final platform = FakePlatformServices(apiBaseUrl: '/api', token: null);
      http.BaseRequest? captured;
      final mockClient = MockClient((request) async {
        captured = request;
        return http.Response(jsonEncode([]), 200);
      });
      final client = ApiClient(httpClient: mockClient, platform: platform);
      await client.fetchPRs();
      // Dart's Uri.parse resolves a relative string against a default base;
      // we assert the path + absence of the auth header.
      expect(
        captured!.url.path.endsWith('/api/prs'),
        isTrue,
        reason: 'expected path ending in /api/prs, got ${captured!.url}',
      );
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

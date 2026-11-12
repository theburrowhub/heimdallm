import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:heimdallm/core/api/api_client.dart';
import '../platform/fake_platform_services.dart';

void main() {
  test('fetchGitHubPrincipals parses teams and users', () async {
    final api = apiClientWith(
      MockClient((req) async {
        expect(req.url.path, endsWith('/github/principals'));
        return http.Response(
          jsonEncode({
            'principals': [
              {'value': 'acme/core', 'kind': 'team'},
              {'value': 'alice', 'kind': 'user'},
            ],
          }),
          200,
        );
      }),
    );
    final got = await api.fetchGitHubPrincipals();
    expect(got.map((p) => (p.value, p.isTeam)), [
      ('acme/core', true),
      ('alice', false),
    ]);
  });

  test('fetchGitHubPrincipals throws on an error status', () async {
    final api = apiClientWith(
      MockClient((req) async => http.Response('{}', 503)),
    );
    expect(api.fetchGitHubPrincipals, throwsA(isA<ApiException>()));
  });
}

ApiClient apiClientWith(http.Client c) => ApiClient(
  httpClient: c,
  platform: FakePlatformServices(
    apiBaseUrl: 'http://127.0.0.1:7842',
    token: 'abc-123',
  ),
);

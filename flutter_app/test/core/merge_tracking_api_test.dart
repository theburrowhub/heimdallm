import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

import 'platform/fake_platform_services.dart';

/// One tracked PR as the daemon serves it, including the per-check breakdown
/// the detail view renders.
const _entryJson = {
  'pr_id': 1,
  'repo': 'acme/widgets',
  'number': 7,
  'title': 'Add widget cache',
  'url': 'https://github.com/acme/widgets/pull/7',
  'author': 'octocat',
  'phase': 'blocked',
  'block_reason': 'checks_failing',
  'block_detail': '1 required check is failing: build (GitHub Actions)',
  'checks_required_failing': 1,
  'decision': {
    'ready': false,
    'blocks': [
      {'reason': 'checks_failing', 'detail': 'build is failing'},
    ],
    'checks': [
      {'name': 'build', 'state': 'failure', 'required': true},
    ],
    'checks_summary': {'total': 1, 'required_total': 1, 'required_failing': 1},
  },
};

ApiClient _client(MockClient mock) => ApiClient(
  httpClient: mock,
  platform: FakePlatformServices(
    apiBaseUrl: 'http://127.0.0.1:7842',
    token: 'abc-123',
  ),
);

void main() {
  _addAndOverrideEndpoints();

  test('fetchMergeTrackingList decodes the listing and authenticates', () async {
    http.BaseRequest? captured;
    final entries = await _client(MockClient((request) async {
      captured = request;
      if (request.url.path == '/merge-tracking') {
        return http.Response(jsonEncode([_entryJson]), 200);
      }
      return http.Response('not found', 404);
    })).fetchMergeTrackingList();

    expect(entries.length, 1);
    expect(entries.first.blockedByChecks, isTrue);
    expect(entries.first.blockDetail, contains('build'));
    expect(captured!.url.toString(), 'http://127.0.0.1:7842/merge-tracking');
    expect(captured!.headers['X-Heimdallm-Token'], 'abc-123');
  });

  test('fetchMergeTracking decodes the per-check breakdown', () async {
    final entry = await _client(MockClient((request) async {
      expect(request.url.path, '/merge-tracking/1');
      return http.Response(jsonEncode(_entryJson), 200);
    })).fetchMergeTracking(1);

    expect(entry.decision, isNotNull);
    expect(entry.decision!.requiredChecks.single.name, 'build');
    expect(entry.decision!.checksSummary!.requiredFailing, 1);
  });

  test('evaluateMergeTracking asks a question, or takes an action', () async {
    final paths = <String>[];
    final client = _client(MockClient((request) async {
      paths.add(request.url.toString());
      return http.Response(jsonEncode(_entryJson), 200);
    }));

    await client.evaluateMergeTracking(1, dryRun: true);
    await client.evaluateMergeTracking(1);
    expect(paths, [
      'http://127.0.0.1:7842/merge-tracking/1/evaluate?dry_run=true',
      'http://127.0.0.1:7842/merge-tracking/1/evaluate',
    ]);
  });

  test('setMergeTrackingExcluded posts to exclude and to include', () async {
    final paths = <String>[];
    final client = _client(MockClient((request) async {
      paths.add(request.url.path);
      expect(request.method, 'POST');
      return http.Response('', 200);
    }));

    await client.setMergeTrackingExcluded(1, true);
    await client.setMergeTrackingExcluded(1, false);
    expect(paths, ['/merge-tracking/1/exclude', '/merge-tracking/1/include']);
  });

  // A daemon that answers with anything but 200 must not be decoded as an empty
  // listing: "no PRs are blocked" and "we could not ask" look identical in the
  // UI and mean opposite things.
  test('every endpoint reports a non-200 rather than degrading quietly', () async {
    final client = _client(MockClient((_) async => http.Response('boom', 500)));

    await expectLater(client.fetchMergeTrackingList(), throwsA(isA<ApiException>()));
    await expectLater(client.fetchMergeTracking(1), throwsA(isA<ApiException>()));
    await expectLater(
      client.evaluateMergeTracking(1, dryRun: true),
      throwsA(isA<ApiException>()),
    );
    await expectLater(
      client.setMergeTrackingExcluded(1, true),
      throwsA(isA<ApiException>()),
    );
  });
}

// The Merge tab's own add path. It must not be the review pipeline's: that one
// refuses PRs the authenticated account authored, which is every PR the
// operator opens.
void _addAndOverrideEndpoints() {
  test('addMergeTracking posts the URL and decodes the tracked row', () async {
    String? gotBody;
    String? gotPath;
    final entry = await _client(MockClient((request) async {
      gotPath = request.url.path;
      gotBody = request.body;
      return http.Response(jsonEncode(_entryJson), 202);
    })).addMergeTracking('https://github.com/acme/widgets/pull/7');

    expect(gotPath, '/merge-tracking/add');
    expect(jsonDecode(gotBody!), {
      'url': 'https://github.com/acme/widgets/pull/7',
    });
    expect(entry.repo, 'acme/widgets');
  });

  test('a refusal is surfaced in the daemon\'s own words', () async {
    final client = _client(MockClient((_) async => http.Response(
          jsonEncode({'error': 'merge tracking is disabled for acme/widgets'}),
          500,
        )));
    await expectLater(
      client.addMergeTracking('https://github.com/acme/widgets/pull/7'),
      throwsA(
        isA<ApiException>().having(
          (e) => e.message,
          'message',
          contains('disabled for acme/widgets'),
        ),
      ),
    );
  });

  // Merge tracking keeps its per-repo overrides in its own config section, so
  // they cannot ride along with the rest of the per-repo config.
  test('the per-repo and per-org overrides go to their own endpoints', () async {
    final paths = <String>[];
    final bodies = <String>[];
    final client = _client(MockClient((request) async {
      paths.add(request.url.path);
      bodies.add(request.body);
      expect(request.method, 'PATCH');
      return http.Response('{}', 200);
    }));

    await client.patchMergeTrackingRepoConfig('acme/widgets', {
      'enabled': true,
    });
    await client.patchMergeTrackingOrgConfig('acme', {'enabled': false});

    expect(paths, [
      '/config/merge_tracking/repos/acme%2Fwidgets',
      '/config/merge_tracking/orgs/acme',
    ]);
    expect(jsonDecode(bodies.first), {'enabled': true});
    expect(jsonDecode(bodies.last), {'enabled': false});
  });

  test('a null scoped override removes the field with DELETE', () async {
    late http.Request captured;
    final client = _client(
      MockClient((request) async {
        captured = request;
        return http.Response(
          jsonEncode({
            'repositories': ['acme/widgets'],
          }),
          200,
        );
      }),
    );

    final response = await client.patchMergeTrackingRepoConfig('acme/widgets', {
      'enabled': null,
    });

    expect(captured.method, 'DELETE');
    expect(
      captured.url.path,
      '/config/merge_tracking/repos/acme%2Fwidgets/enabled',
    );
    expect(response['repositories'], ['acme/widgets']);
  });

  test('a rejected override reports why', () async {
    final client = _client(MockClient((_) async => http.Response(
          jsonEncode({'error': 'merge_method "ff-only" must be one of squash, merge, rebase'}),
          400,
        )));
    await expectLater(
      client.patchMergeTrackingRepoConfig('acme/widgets', {
        'merge_method': 'ff-only',
      }),
      throwsA(
        isA<ApiException>().having(
          (e) => e.message,
          'message',
          contains('ff-only'),
        ),
      ),
    );
  });
}

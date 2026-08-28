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

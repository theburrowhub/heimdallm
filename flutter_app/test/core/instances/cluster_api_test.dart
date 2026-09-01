import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/api/cluster_api.dart';
import 'package:heimdallm/core/api/daemon_endpoint.dart';
import 'package:heimdallm/core/instances/models.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

/// Records every request so the tests can assert on method, path and body.
class _Recorder {
  final List<http.BaseRequest> requests = [];
  final List<String> bodies = [];
}

ApiClient _api(
  _Recorder recorder,
  Map<String, dynamic> Function(http.Request request) respond, {
  int status = 200,
}) {
  final client = MockClient((request) async {
    recorder.requests.add(request);
    recorder.bodies.add(request.body);
    return http.Response(jsonEncode(respond(request)), status);
  });
  return ApiClient(
    httpClient: client,
    endpoint: DaemonEndpoint.raw(baseUrl: 'http://hub:7842', token: 'tok'),
  );
}

void main() {
  test('fetchInstances parses the registry', () async {
    final recorder = _Recorder();
    final api = _api(
      recorder,
      (_) => {
        'role': 'hub',
        'self_id': 'hub-1',
        'instances': [
          {'id': 'hub-1', 'self': true},
          {'id': 'srv-a'},
        ],
      },
    );

    final registry = await api.fetchInstances();
    expect(registry.role, 'hub');
    expect(registry.instances, hasLength(2));
    expect(recorder.requests.single.url.path, '/instances');
    expect(
      recorder.requests.single.headers['X-Heimdallm-Token'],
      'tok',
    );
  });

  test('a 404 means "not a hub", not an error', () async {
    // A plain single-daemon install answers 404 on the control plane. Surfacing
    // that as an error would put a failure banner in front of every user who is
    // not running instances.
    final client = MockClient((_) async => http.Response('not found', 404));
    final api = ApiClient(
      httpClient: client,
      endpoint: DaemonEndpoint.raw(baseUrl: 'http://hub:7842'),
    );

    expect(await api.fetchInstances(), same(ClusterRegistry.empty));
    expect((await api.fetchRouting()).enabled, isFalse);
    expect(await api.fetchConfigDrift(), isEmpty);
  });

  test('registerInstance sends one token source and returns the id', () async {
    final recorder = _Recorder();
    final api = _api(recorder, (_) => {'id': 'srv-b'});

    final id = await api.registerInstance(
      baseUrl: 'http://10.0.0.12:7842',
      name: 'Server B',
      token: 'secret',
      labels: ['linux'],
    );

    expect(id, 'srv-b');
    final body = jsonDecode(recorder.bodies.single) as Map<String, dynamic>;
    expect(body['base_url'], 'http://10.0.0.12:7842');
    expect(body['token'], 'secret');
    expect(body['labels'], ['linux']);
    expect(body.containsKey('token_env'), isFalse);
    expect(body.containsKey('token_file'), isFalse);
  });

  test('patchInstance omits fields that were not supplied', () async {
    final recorder = _Recorder();
    final api = _api(recorder, (_) => const {});

    await api.patchInstance('srv-a', name: 'Renamed', enabled: false);

    final body = jsonDecode(recorder.bodies.single) as Map<String, dynamic>;
    expect(body, {'name': 'Renamed', 'enabled': false});
    expect(recorder.requests.single.method, 'PATCH');
    expect(recorder.requests.single.url.path, '/instances/srv-a');
  });

  test('assignRepo replaces the map so a rule can be removed', () async {
    final recorder = _Recorder();
    final api = _api(recorder, (_) => const {});
    const current = RoutingRules(repos: {'acme/a': 'x', 'acme/b': 'y'});

    await api.assignRepo(current, 'acme/a', 'z');
    var body = jsonDecode(recorder.bodies.last) as Map<String, dynamic>;
    expect(body['repos'], {'acme/a': 'z', 'acme/b': 'y'});

    // Clearing has to omit the key, not send an empty value: PUT replaces the
    // map wholesale, which is what makes deletion possible at all.
    await api.assignRepo(current, 'acme/a', null);
    body = jsonDecode(recorder.bodies.last) as Map<String, dynamic>;
    expect(body['repos'], {'acme/b': 'y'});
  });

  test('assignOrg mirrors assignRepo', () async {
    final recorder = _Recorder();
    final api = _api(recorder, (_) => const {});
    const current = RoutingRules(orgs: {'acme': 'x'});

    await api.assignOrg(current, 'other', 'y');
    final body = jsonDecode(recorder.bodies.last) as Map<String, dynamic>;
    expect(body['orgs'], {'acme': 'x', 'other': 'y'});
  });

  test('propagateConfig accepts 207 partial success', () async {
    // One machine rebooting must not hide that the others were updated.
    final client = MockClient(
      (_) async => http.Response(
        jsonEncode({
          'failures': 1,
          'results': [
            {'instance_id': 'a', 'ok': true},
            {'instance_id': 'b', 'ok': false, 'error': 'starting'},
          ],
        }),
        207,
      ),
    );
    final api = ApiClient(
      httpClient: client,
      endpoint: DaemonEndpoint.raw(baseUrl: 'http://hub:7842', token: 't'),
    );

    final report = await api.propagateConfig();
    expect(report.allOk, isFalse);
    expect(report.results, hasLength(2));
  });

  test('dispatch accepts 202 and returns the chosen instance', () async {
    final recorder = _Recorder();
    final client = MockClient((request) async {
      recorder.requests.add(request);
      recorder.bodies.add(request.body);
      return http.Response(jsonEncode({'instance_id': 'srv-a'}), 202);
    });
    final api = ApiClient(
      httpClient: client,
      endpoint: DaemonEndpoint.raw(baseUrl: 'http://hub:7842', token: 't'),
    );

    final target = await api.dispatch(
      'review',
      prId: 42,
      repo: 'acme/tools',
      number: 7,
      headSha: 'sha1',
    );

    expect(target, 'srv-a');
    expect(recorder.requests.single.url.path, '/cluster/dispatch/review');
    final body = jsonDecode(recorder.bodies.single) as Map<String, dynamic>;
    expect(body['pr_id'], 42);
    expect(body['head_sha'], 'sha1');
    expect(body.containsKey('dry_run'), isFalse);
  });

  test('a non-2xx surfaces the daemon error message', () async {
    final client = MockClient(
      (_) async => http.Response(
        jsonEncode({'error': 'unknown instance "ghost"'}),
        400,
      ),
    );
    final api = ApiClient(
      httpClient: client,
      endpoint: DaemonEndpoint.raw(baseUrl: 'http://hub:7842', token: 't'),
    );

    await expectLater(
      api.putRouting(orgs: const {'acme': 'ghost'}),
      throwsA(
        isA<ApiException>().having(
          (e) => e.message,
          'message',
          contains('ghost'),
        ),
      ),
    );
  });
}

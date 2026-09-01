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
  test('registerInstance supports env and file token sources', () async {
    final recorder = _Recorder();
    final api = _api(recorder, (_) => {'id': 'srv-b'});

    await api.registerInstance(
      baseUrl: 'http://a:7842',
      tokenEnv: 'HEIMDALLM_SRV_B',
    );
    var body = jsonDecode(recorder.bodies.last) as Map<String, dynamic>;
    expect(body['token_env'], 'HEIMDALLM_SRV_B');
    expect(body.containsKey('token'), isFalse);

    await api.registerInstance(
      baseUrl: 'http://a:7842',
      tokenFile: '/data/api_token',
      skipProbe: true,
      id: 'forced',
    );
    body = jsonDecode(recorder.bodies.last) as Map<String, dynamic>;
    expect(body['token_file'], '/data/api_token');
    expect(body['skip_probe'], isTrue);
    expect(body['id'], 'forced');
  });

  test('deleteInstance and probeInstance target the right paths', () async {
    final recorder = _Recorder();
    final api = _api(recorder, (_) => {'reachable': true, 'version': '0.9.0'});

    await api.deleteInstance('srv a');
    expect(recorder.requests.last.method, 'DELETE');
    // The id is percent-encoded, so it can never break out of its path segment.
    expect(recorder.requests.last.url.path, '/instances/srv%20a');

    final state = await api.probeInstance('srv-a');
    expect(state.reachable, isTrue);
    expect(state.version, '0.9.0');
    expect(recorder.requests.last.url.path, '/instances/srv-a/probe');
  });

  test('probeInstance tolerates an empty body', () async {
    final client = MockClient((_) async => http.Response('', 200));
    final api = ApiClient(
      httpClient: client,
      endpoint: DaemonEndpoint.raw(baseUrl: 'http://hub:7842', token: 't'),
    );
    expect((await api.probeInstance('srv-a')).reachable, isFalse);
  });

  test('patchInstance can rotate to each token source', () async {
    final recorder = _Recorder();
    final api = _api(recorder, (_) => const {});

    await api.patchInstance('srv-a', tokenEnv: 'X');
    expect(
      (jsonDecode(recorder.bodies.last) as Map<String, dynamic>)['token_env'],
      'X',
    );
    await api.patchInstance('srv-a', tokenFile: '/data/api_token');
    expect(
      (jsonDecode(recorder.bodies.last) as Map<String, dynamic>)['token_file'],
      '/data/api_token',
    );
    await api.patchInstance('srv-a', labels: const ['linux']);
    expect(
      (jsonDecode(recorder.bodies.last) as Map<String, dynamic>)['labels'],
      ['linux'],
    );
  });

  test('putRouting omits fields that were not supplied', () async {
    final recorder = _Recorder();
    final api = _api(recorder, (_) => const {});

    await api.putRouting(mode: 'dispatch');
    final body = jsonDecode(recorder.bodies.single) as Map<String, dynamic>;
    expect(body, {'mode': 'dispatch'});
  });

  test('putRouting can clear the pool and the ops list', () async {
    final recorder = _Recorder();
    final api = _api(recorder, (_) => const {});

    await api.putRouting(
      roundRobinPool: const [],
      roundRobinOps: const [],
      defaultInstance: '',
    );
    final body = jsonDecode(recorder.bodies.single) as Map<String, dynamic>;
    expect(body['round_robin_pool'], isEmpty);
    expect(body['round_robin_ops'], isEmpty);
    expect(body['default_instance'], '');
  });

  test('assignRepo on an unrouted repo adds the rule', () async {
    final recorder = _Recorder();
    final api = _api(recorder, (_) => const {});
    await api.assignRepo(RoutingRules.empty, 'acme/new', 'srv-a');
    final body = jsonDecode(recorder.bodies.single) as Map<String, dynamic>;
    expect(body['repos'], {'acme/new': 'srv-a'});
  });

  test('assignOrg can clear a rule', () async {
    final recorder = _Recorder();
    final api = _api(recorder, (_) => const {});
    await api.assignOrg(const RoutingRules(orgs: {'acme': 'x'}), 'acme', null);
    final body = jsonDecode(recorder.bodies.single) as Map<String, dynamic>;
    expect(body['orgs'], isEmpty);
  });

  test('propagateConfig can target specific instances with a patch', () async {
    final recorder = _Recorder();
    final api = _api(recorder, (_) => {'failures': 0, 'results': []});

    await api.propagateConfig(
      targets: const ['srv-a'],
      patch: const {'ai': {'review_mode': 'multi'}},
    );
    final body = jsonDecode(recorder.bodies.single) as Map<String, dynamic>;
    expect(body['targets'], ['srv-a']);
    expect(body['patch'], {'ai': {'review_mode': 'multi'}});
  });

  test('dispatch carries every optional field it is given', () async {
    final recorder = _Recorder();
    final api = _api(recorder, (_) => {'instance_id': 'srv-a'});

    await api.dispatch(
      'merge',
      prId: 1,
      issueId: 2,
      repo: 'acme/tools',
      number: 3,
      headSha: 'sha',
      prUrl: 'https://github.com/acme/tools/pull/3',
      dryRun: true,
      instance: 'srv-b',
    );
    final body = jsonDecode(recorder.bodies.single) as Map<String, dynamic>;
    expect(body['issue_id'], 2);
    expect(body['pr_url'], contains('pull/3'));
    expect(body['dry_run'], isTrue);
    expect(body['instance'], 'srv-b');
  });

  test('fetchConfigDrift parses per-instance entries', () async {
    final recorder = _Recorder();
    final api = _api(
      recorder,
      (_) => {
        'instances': [
          {
            'instance_id': 'srv-a',
            'ok': true,
            'drifts': [
              {'key': 'ai.review_mode', 'hub_value': 'multi'},
            ],
          },
        ],
      },
    );

    final drifts = await api.fetchConfigDrift();
    expect(drifts.single.instanceId, 'srv-a');
    expect(drifts.single.drifts.single.key, 'ai.review_mode');
  });

  test('decodeJsonObject tolerates junk', () {
    expect(decodeJsonObject(''), isNull);
    expect(decodeJsonObject('not json'), isNull);
    expect(decodeJsonObject('[1,2]'), isNull);
    expect(decodeJsonObject('{"a":1}'), {'a': 1});
  });
}

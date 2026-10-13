import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/flow.dart';
import '../platform/fake_platform_services.dart';

const _graph = ReviewFlow(
  name: 'x',
  start: 'a1',
  nodes: {'a1': FlowNode(id: 'a1', type: FlowNodeType.agent, agent: 'claude')},
);

void main() {
  test(
    'putFlow turns a 422 into FlowValidationException with issues',
    () async {
      final api = apiClientWith(
        MockClient(
          (req) async => http.Response(
            jsonEncode({
              'error': 'nodes.a1: cycle',
              'errors': [
                {'node': 'a1', 'message': 'cycle'},
              ],
            }),
            422,
          ),
        ),
      );
      expect(
        () => api.putFlow('x', _graph),
        throwsA(
          isA<FlowValidationException>().having(
            (e) => e.issues.single.node,
            'node',
            'a1',
          ),
        ),
      );
    },
  );

  test('simulateFlow sends an unsaved graph', () async {
    late Map<String, dynamic> sent;
    final api = apiClientWith(
      MockClient((req) async {
        sent = jsonDecode(req.body) as Map<String, dynamic>;
        return http.Response(
          jsonEncode({
            'flow_id': 'draft',
            'candidates': ['claude'],
            'path': [],
          }),
          200,
        );
      }),
    );
    final d = await api.simulateFlow(graph: _graph);
    expect((sent['graph'] as Map)['start'], 'a1');
    expect(d.candidates, ['claude']);
  });

  test('assistFlow posts the request and parses the draft', () async {
    late Map<String, dynamic> sent;
    final api = apiClientWith(
      MockClient((req) async {
        expect(req.url.path, endsWith('/flows/assist'));
        sent = jsonDecode(req.body) as Map<String, dynamic>;
        return http.Response(
          jsonEncode({
            'plan': 'p',
            'agent': 'claude',
            'warnings': ['w'],
            'duration_ms': 1200,
            'flow': _graph.toJson(),
          }),
          200,
        );
      }),
    );
    final r = await api.assistFlow(
      agent: 'claude',
      instruction: 'go',
      current: _graph,
    );
    expect(sent['instruction'], 'go');
    expect(sent['flow'], isNotNull);
    expect(r.plan, 'p');
    expect(r.flow.start, 'a1');
    expect(r.warnings, ['w']);
  });

  test('assistFlow 409 and 422 surface the daemon message', () async {
    final busy = apiClientWith(
      MockClient(
        (_) async => http.Response(
          jsonEncode({'error': 'another flow is being drafted'}),
          409,
        ),
      ),
    );
    expect(
      () => busy.assistFlow(agent: 'claude', instruction: 'x'),
      throwsA(
        isA<ApiException>().having(
          (e) => e.message,
          'message',
          contains('another flow'),
        ),
      ),
    );
    final bad = apiClientWith(
      MockClient(
        (_) async => http.Response(
          jsonEncode({
            'error': 'invalid',
            'errors': [
              {'node': 'a1', 'message': 'm'},
            ],
          }),
          422,
        ),
      ),
    );
    expect(
      () => bad.assistFlow(agent: 'claude', instruction: 'x'),
      throwsA(isA<FlowValidationException>()),
    );
  });
}

ApiClient apiClientWith(http.Client c) => ApiClient(
  httpClient: c,
  platform: FakePlatformServices(
    apiBaseUrl: 'http://127.0.0.1:7842',
    token: 'abc-123',
  ),
);

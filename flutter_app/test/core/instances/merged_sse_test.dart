import 'dart:async';
import 'dart:convert';

import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallm/core/api/daemon_endpoint.dart';
import 'package:heimdallm/core/api/sse_client.dart';
import 'package:heimdallm/core/instances/instances_providers.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';

/// An SseClient backed by a controllable stream, so a test can push events for
/// a specific instance.
SseClient _client(String id, Stream<String> chunks) {
  final http.Client mock = MockClient.streaming((request, body) async {
    return http.StreamedResponse(
      chunks.map(utf8.encode),
      200,
      headers: {'content-type': 'text/event-stream'},
    );
  });
  return SseClient(
    httpClient: mock,
    endpoint: DaemonEndpoint.raw(baseUrl: 'http://$id:7842', instanceId: id),
  );
}

String _frame(String type, Map<String, dynamic> data) =>
    'event: $type\ndata: ${jsonEncode(data)}\n\n';

void main() {
  test('SseEvent carries no instance by default', () {
    // Every event on a single-daemon install, so listeners that ignore the
    // field behave exactly as they did before instances existed.
    const event = SseEvent(type: 'review_started', data: '{}');
    expect(event.instanceId, isEmpty);
    expect(event.withInstance('srv-a').instanceId, 'srv-a');
    expect(event.withInstance('srv-a').type, 'review_started');
  });

  test('parseEvents leaves the instance blank', () {
    final events = SseClient.parseEvents(_frame('pr_detected', {'repo': 'a/b'}));
    expect(events.single.type, 'pr_detected');
    expect(events.single.instanceId, isEmpty);
  });

  test('a single client stream is tagged with its instance', () async {
    final controller = StreamController<String>();
    final merged = mergeInstanceEvents({'srv-a': _client('srv-a', controller.stream)});

    final received = <SseEvent>[];
    final sub = merged.listen(received.add);
    controller.add(_frame('review_started', {'repo': 'acme/a', 'pr_number': 1}));
    await Future<void>.delayed(const Duration(milliseconds: 50));

    expect(received, hasLength(1));
    expect(received.single.instanceId, 'srv-a');
    await sub.cancel();
    await controller.close();
  });

  test('events from several instances merge and keep their origin', () async {
    final a = StreamController<String>();
    final b = StreamController<String>();
    final merged = mergeInstanceEvents({
      'a': _client('a', a.stream),
      'b': _client('b', b.stream),
    });

    final received = <SseEvent>[];
    final sub = merged.listen(received.add);
    a.add(_frame('review_started', {'repo': 'x/y', 'pr_number': 1}));
    b.add(_frame('review_completed', {'repo': 'x/y', 'pr_number': 1}));
    await Future<void>.delayed(const Duration(milliseconds: 80));

    expect(received.map((e) => e.instanceId).toSet(), {'a', 'b'});
    // The same repo and PR number from two instances must stay
    // distinguishable, otherwise one machine's completion clears the other's
    // spinner.
    expect(received.map((e) => e.type).toSet(), {
      'review_started',
      'review_completed',
    });
    await sub.cancel();
    await a.close();
    await b.close();
  });

  test('one instance erroring does not tear down the merged stream', () async {
    final good = StreamController<String>();
    final bad = StreamController<String>();
    final merged = mergeInstanceEvents({
      'good': _client('good', good.stream),
      'bad': _client('bad', bad.stream),
    });

    final received = <SseEvent>[];
    final sub = merged.listen(received.add);
    bad.addError(Exception('connection dropped'));
    await Future<void>.delayed(const Duration(milliseconds: 30));
    good.add(_frame('pr_detected', {'repo': 'a/b'}));
    await Future<void>.delayed(const Duration(milliseconds: 60));

    expect(
      received.map((e) => e.instanceId),
      contains('good'),
      reason: 'the healthy instance must keep streaming',
    );
    await sub.cancel();
    await good.close();
    await bad.close();
  });

  test('no clients yields an empty stream', () async {
    expect(await mergeInstanceEvents(const {}).isEmpty, isTrue);
  });
}

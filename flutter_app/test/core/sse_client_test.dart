import 'dart:async';
import 'package:flutter_test/flutter_test.dart';
import 'package:heimdallr/core/api/sse_client.dart';

void main() {
  test('SseEvent parses type and data', () {
    const raw = 'event: review_completed\ndata: {"pr_id":1}\n\n';
    final events = SseClient.parseEvents(raw);
    expect(events.length, 1);
    expect(events.first.type, 'review_completed');
    expect(events.first.data, '{"pr_id":1}');
  });

  test('SseEvent parses data-only event', () {
    const raw = 'data: hello\n\n';
    final events = SseClient.parseEvents(raw);
    expect(events.length, 1);
    expect(events.first.type, 'message');
    expect(events.first.data, 'hello');
  });

  test('SseEvent skips comment lines', () {
    const raw = ': connected\n\ndata: ping\n\n';
    final events = SseClient.parseEvents(raw);
    expect(events.length, 1);
    expect(events.first.data, 'ping');
  });
}

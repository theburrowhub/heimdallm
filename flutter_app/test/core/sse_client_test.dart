import 'dart:async';
import 'dart:convert';
import 'package:flutter_test/flutter_test.dart';
import 'package:http/http.dart' as http;
import 'package:http/testing.dart';
import 'package:heimdallm/core/api/sse_client.dart';
import 'platform/fake_platform_services.dart';

void main() {
  // ── parser tests (existing) ─────────────────────────────────────────────
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

  // ── connection URL + header tests (new) ─────────────────────────────────
  group('SseClient connect', () {
    test('desktop uses absolute URL and sends X-Heimdallm-Token', () async {
      final platform = FakePlatformServices(
        apiBaseUrl: 'http://127.0.0.1:7842',
        token: 'tok-xyz',
      );
      http.BaseRequest? captured;
      final mockClient = MockClient.streaming((request, _) async {
        captured = request;
        final bytes = Stream<List<int>>.value(utf8.encode(': hello\n\n'));
        return http.StreamedResponse(
          bytes,
          200,
          headers: {'content-type': 'text/event-stream'},
        );
      });
      final client = SseClient(
        httpClient: mockClient,
        platform: platform,
        path: '/events',
      );
      final sub = client.connect().listen((_) {});
      await Future<void>.delayed(const Duration(milliseconds: 50));

      expect(captured, isNotNull);
      expect(captured!.url.toString(), 'http://127.0.0.1:7842/events');
      expect(captured!.headers['X-Heimdallm-Token'], 'tok-xyz');
      expect(captured!.headers['Accept'], 'text/event-stream');

      await sub.cancel();
    });

    test('web uses relative /api/<path> and omits the auth header', () async {
      final platform = FakePlatformServices(apiBaseUrl: '/api', token: null);
      http.BaseRequest? captured;
      final mockClient = MockClient.streaming((request, _) async {
        captured = request;
        final bytes = Stream<List<int>>.value(utf8.encode(': hi\n\n'));
        return http.StreamedResponse(
          bytes,
          200,
          headers: {'content-type': 'text/event-stream'},
        );
      });
      final client = SseClient(
        httpClient: mockClient,
        platform: platform,
        path: '/logs/stream',
      );
      final sub = client.connect().listen((_) {});
      await Future<void>.delayed(const Duration(milliseconds: 50));

      expect(captured, isNotNull);
      expect(
        captured!.url.path.endsWith('/api/logs/stream'),
        isTrue,
        reason:
            'expected path ending in /api/logs/stream, got ${captured!.url}',
      );
      expect(captured!.headers.containsKey('X-Heimdallm-Token'), isFalse);

      await sub.cancel();
    });

    test(
      'connect reuses the active stream instead of opening duplicates',
      () async {
        final platform = FakePlatformServices(
          apiBaseUrl: 'http://127.0.0.1:7842',
        );
        var requests = 0;
        final controller = StreamController<List<int>>();
        final mockClient = MockClient.streaming((request, _) async {
          requests++;
          return http.StreamedResponse(
            controller.stream,
            200,
            headers: {'content-type': 'text/event-stream'},
          );
        });
        final client = SseClient(
          httpClient: mockClient,
          platform: platform,
          path: '/events',
        );

        final sub1 = client.connect().listen((_) {});
        final sub2 = client.connect().listen((_) {});
        await Future<void>.delayed(const Duration(milliseconds: 50));

        expect(requests, 1);

        await sub1.cancel();
        await sub2.cancel();
        await controller.close();
      },
    );

    test(
      'stream error followed by close schedules only one reconnect',
      () async {
        final platform = FakePlatformServices(
          apiBaseUrl: 'http://127.0.0.1:7842',
        );
        var requests = 0;
        final controllers = <StreamController<List<int>>>[];
        final mockClient = MockClient.streaming((request, _) async {
          requests++;
          final controller = StreamController<List<int>>();
          controllers.add(controller);
          return http.StreamedResponse(
            controller.stream,
            200,
            headers: {'content-type': 'text/event-stream'},
          );
        });
        final client = SseClient(
          httpClient: mockClient,
          platform: platform,
          path: '/events',
          errorReconnectDelay: const Duration(milliseconds: 10),
          doneReconnectDelay: const Duration(milliseconds: 10),
        );

        final sub = client.connect().listen((_) {});
        await Future<void>.delayed(const Duration(milliseconds: 20));
        expect(requests, 1);

        controllers.first.addError(Exception('socket closed'));
        await controllers.first.close();
        await Future<void>.delayed(const Duration(milliseconds: 50));

        expect(requests, 2);

        await sub.cancel();
        for (final controller in controllers.skip(1)) {
          if (!controller.isClosed) {
            await controller.close();
          }
        }
      },
    );

    test('send failure schedules a reconnect', () async {
      final platform = FakePlatformServices(
        apiBaseUrl: 'http://127.0.0.1:7842',
      );
      var requests = 0;
      final controllers = <StreamController<List<int>>>[];
      final mockClient = MockClient.streaming((request, _) async {
        requests++;
        if (requests == 1) {
          throw Exception('connection refused');
        }
        final controller = StreamController<List<int>>();
        controllers.add(controller);
        return http.StreamedResponse(
          controller.stream,
          200,
          headers: {'content-type': 'text/event-stream'},
        );
      });
      final client = SseClient(
        httpClient: mockClient,
        platform: platform,
        path: '/events',
        errorReconnectDelay: const Duration(milliseconds: 10),
      );

      final sub = client.connect().listen((_) {}, onError: (_) {});
      await Future<void>.delayed(const Duration(milliseconds: 50));

      expect(requests, 2);

      await sub.cancel();
      for (final controller in controllers) {
        await controller.close();
      }
    });

    test('drains the response if disconnected while connecting', () async {
      final platform = FakePlatformServices(
        apiBaseUrl: 'http://127.0.0.1:7842',
      );
      final responseCompleter = Completer<http.StreamedResponse>();
      final requestStarted = Completer<void>();
      var drained = false;
      final responseBody = StreamController<List<int>>(
        onListen: () => drained = true,
      );
      final mockClient = MockClient.streaming((request, _) async {
        requestStarted.complete();
        return responseCompleter.future;
      });
      final client = SseClient(
        httpClient: mockClient,
        platform: platform,
        path: '/events',
      );

      final sub = client.connect().listen((_) {});
      await requestStarted.future;
      await sub.cancel();

      responseCompleter.complete(
        http.StreamedResponse(
          responseBody.stream,
          200,
          headers: {'content-type': 'text/event-stream'},
        ),
      );
      await Future<void>.delayed(const Duration(milliseconds: 10));

      expect(drained, isTrue);

      await responseBody.close();
    });

    test('buffer overflow reconnects after the error delay', () async {
      final platform = FakePlatformServices(
        apiBaseUrl: 'http://127.0.0.1:7842',
      );
      var requests = 0;
      final controllers = <StreamController<List<int>>>[];
      final mockClient = MockClient.streaming((request, _) async {
        requests++;
        final controller = StreamController<List<int>>();
        controllers.add(controller);
        return http.StreamedResponse(
          controller.stream,
          200,
          headers: {'content-type': 'text/event-stream'},
        );
      });
      final client = SseClient(
        httpClient: mockClient,
        platform: platform,
        path: '/events',
        errorReconnectDelay: const Duration(milliseconds: 30),
      );

      final sub = client.connect().listen((_) {});
      await Future<void>.delayed(const Duration(milliseconds: 20));
      expect(requests, 1);

      controllers.first.add(List<int>.filled(1024 * 1024 + 1, 65));
      await Future<void>.delayed(const Duration(milliseconds: 10));
      expect(requests, 1);

      await Future<void>.delayed(const Duration(milliseconds: 40));
      expect(requests, 2);

      await sub.cancel();
      for (final controller in controllers) {
        if (!controller.isClosed) {
          await controller.close();
        }
      }
    });
  });
}

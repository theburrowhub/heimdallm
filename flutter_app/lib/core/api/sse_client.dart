import 'dart:async';
import 'dart:convert';
import 'package:http/http.dart' as http;

class SseEvent {
  final String type;
  final String data;
  const SseEvent({required this.type, required this.data});
}

class SseClient {
  final int port;
  final http.Client _httpClient;
  StreamController<SseEvent>? _controller;
  StreamSubscription<String>? _subscription;

  SseClient({this.port = 7842, http.Client? httpClient})
      : _httpClient = httpClient ?? http.Client();

  /// Parses SSE wire format into events. Static for testability.
  static List<SseEvent> parseEvents(String raw) {
    final events = <SseEvent>[];
    for (final block in raw.split('\n\n')) {
      if (block.trim().isEmpty) continue;
      String type = 'message';
      final dataParts = <String>[];
      for (final line in block.split('\n')) {
        if (line.startsWith('event:')) {
          type = line.substring(6).trim();
        } else if (line.startsWith('data:')) {
          dataParts.add(line.substring(5).trim());
        }
        // Skip comment lines (start with ':')
      }
      if (dataParts.isNotEmpty) {
        events.add(SseEvent(type: type, data: dataParts.join('\n')));
      }
    }
    return events;
  }

  /// Returns a stream of SSE events from the daemon.
  Stream<SseEvent> connect() {
    _controller = StreamController<SseEvent>.broadcast(
      onCancel: () => disconnect(),
    );
    _startListening();
    return _controller!.stream;
  }

  void _startListening() async {
    try {
      final request = http.Request(
        'GET',
        Uri.parse('http://127.0.0.1:$port/events'),
      );
      request.headers['Accept'] = 'text/event-stream';
      request.headers['Cache-Control'] = 'no-cache';
      final response = await _httpClient.send(request);

      String buffer = '';
      _subscription = response.stream.transform(utf8.decoder).listen(
        (chunk) {
          buffer += chunk;
          while (buffer.contains('\n\n')) {
            final idx = buffer.indexOf('\n\n');
            final block = buffer.substring(0, idx + 2);
            buffer = buffer.substring(idx + 2);
            for (final event in parseEvents(block)) {
              _controller?.add(event);
            }
          }
        },
        onError: (e) => _controller?.addError(e),
        onDone: () => _controller?.close(),
      );
    } catch (e) {
      _controller?.addError(e);
    }
  }

  void disconnect() {
    _subscription?.cancel();
    _subscription = null;
    _controller?.close();
    _controller = null;
  }
}

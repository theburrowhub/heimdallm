import 'dart:async';
import 'dart:convert';
import 'package:http/http.dart' as http;
import '../platform/platform_services.dart';

class SseEvent {
  final String type;
  final String data;
  const SseEvent({required this.type, required this.data});
}

/// SSE client that maintains at most one active HTTP stream and one pending
/// reconnect timer per instance.
class SseClient {
  final String path;
  final http.Client _httpClient;
  final PlatformServices _platform;
  final Duration _errorReconnectDelay;
  final Duration _doneReconnectDelay;
  StreamController<SseEvent>? _controller;
  StreamSubscription<String>? _subscription;
  Timer? _reconnectTimer;
  bool _connecting = false;

  SseClient({
    required PlatformServices platform,
    this.path = '/events',
    http.Client? httpClient,
    Duration errorReconnectDelay = const Duration(seconds: 5),
    Duration doneReconnectDelay = const Duration(seconds: 3),
  }) : _platform = platform,
       _httpClient = httpClient ?? http.Client(),
       _errorReconnectDelay = errorReconnectDelay,
       _doneReconnectDelay = doneReconnectDelay;

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
    final existing = _controller;
    if (existing != null && !existing.isClosed) {
      return existing.stream;
    }
    _controller = StreamController<SseEvent>.broadcast(
      onCancel: () => disconnect(),
    );
    _startListening();
    return _controller!.stream;
  }

  static const _maxBufferSize = 1024 * 1024; // 1 MB

  void _startListening() async {
    final controller = _controller;
    if (controller == null ||
        controller.isClosed ||
        _connecting ||
        _subscription != null) {
      return;
    }
    _reconnectTimer?.cancel();
    _reconnectTimer = null;
    _connecting = true;
    try {
      final request = http.Request(
        'GET',
        Uri.parse('${_platform.apiBaseUrl}$path'),
      );
      request.headers['Accept'] = 'text/event-stream';
      request.headers['Cache-Control'] = 'no-cache';
      final token = await _platform.loadApiToken();
      if (token != null && token.isNotEmpty) {
        request.headers['X-Heimdallm-Token'] = token;
      }
      final response = await _httpClient.send(request);
      _connecting = false;
      if (_controller == null || _controller!.isClosed) {
        unawaited(response.stream.drain<void>().catchError((_) {}));
        return;
      }

      String buffer = '';
      _subscription = response.stream
          .transform(utf8.decoder)
          .listen(
            (chunk) {
              buffer += chunk;
              // Guard against unbounded buffer growth from a malformed/malicious stream.
              if (buffer.length > _maxBufferSize) {
                buffer = '';
                _cancelSubscription();
                _scheduleReconnect(_errorReconnectDelay);
                return;
              }
              while (buffer.contains('\n\n')) {
                final idx = buffer.indexOf('\n\n');
                final block = buffer.substring(0, idx + 2);
                buffer = buffer.substring(idx + 2);
                for (final event in parseEvents(block)) {
                  _controller?.add(event);
                }
              }
            },
            onError: (e) {
              _cancelSubscription();
              // Reconnect after a delay instead of propagating the error permanently.
              _scheduleReconnect(_errorReconnectDelay);
            },
            onDone: () {
              _subscription = null;
              // Server closed the connection (e.g. idle timeout) — reconnect.
              _scheduleReconnect(_doneReconnectDelay);
            },
          );
    } catch (e) {
      _connecting = false;
      _controller?.addError(e);
      _scheduleReconnect(_errorReconnectDelay);
    }
  }

  void _cancelSubscription() {
    final subscription = _subscription;
    _subscription = null;
    if (subscription != null) {
      unawaited(subscription.cancel());
    }
  }

  void _scheduleReconnect(Duration delay) {
    final controller = _controller;
    if (controller == null || controller.isClosed) return;
    if (_reconnectTimer?.isActive ?? false) return;
    _reconnectTimer = Timer(delay, () {
      _reconnectTimer = null;
      _startListening();
    });
  }

  void disconnect() {
    _reconnectTimer?.cancel();
    _reconnectTimer = null;
    _connecting = false;
    _cancelSubscription();
    _controller?.close();
    _controller = null;
  }
}

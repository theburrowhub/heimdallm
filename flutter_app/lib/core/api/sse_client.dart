import 'dart:async';
import 'dart:convert';
import 'package:http/http.dart' as http;
import '../platform/platform_services.dart';
import 'daemon_endpoint.dart';

class SseEvent {
  final String type;
  final String data;

  /// Which instance produced the event. Empty for the daemon the app manages,
  /// which is every event on a single-daemon install — so existing listeners
  /// that ignore this field keep behaving exactly as they did.
  final String instanceId;

  const SseEvent({
    required this.type,
    required this.data,
    this.instanceId = '',
  });

  SseEvent withInstance(String id) =>
      SseEvent(type: type, data: data, instanceId: id);
}

/// SSE client that maintains at most one active HTTP stream and one pending
/// reconnect timer per instance.
class SseClient {
  final String path;
  final http.Client _httpClient;
  final DaemonEndpoint _endpoint;
  final Duration _errorReconnectDelay;
  final Duration _doneReconnectDelay;
  StreamController<SseEvent>? _controller;
  StreamSubscription<String>? _subscription;
  Timer? _reconnectTimer;
  bool _connecting = false;

  // Consecutive connection failures (a non-2xx status, or a transport
  // error), reset on every successful connection. Without this a persistent
  // failure — a rotated token, a routed instance's cert not being trusted —
  // retried forever at the flat _errorReconnectDelay: one incident logged
  // ~17,500 attempts roughly every 3s. _errorBackoff grows the delay
  // exponentially, capped at _maxReconnectDelay, so a stuck connection backs
  // off instead of hot-looping while a transient blip still recovers fast.
  int _consecutiveFailures = 0;
  static const _maxBackoffShift = 5; // caps growth at base * 32
  static const _maxReconnectDelay = Duration(minutes: 5);

  Duration _errorBackoff() {
    final shift = _consecutiveFailures > _maxBackoffShift
        ? _maxBackoffShift
        : _consecutiveFailures;
    final delay = _errorReconnectDelay * (1 << shift);
    return delay > _maxReconnectDelay ? _maxReconnectDelay : delay;
  }

  SseClient({
    PlatformServices? platform,
    DaemonEndpoint? endpoint,
    this.path = '/events',
    http.Client? httpClient,
    Duration errorReconnectDelay = const Duration(seconds: 5),
    Duration doneReconnectDelay = const Duration(seconds: 3),
  }) : assert(
         platform != null || endpoint != null,
         'SseClient needs either a platform (local daemon) or an endpoint',
       ),
       _endpoint = endpoint ?? DaemonEndpoint.local(platform!),
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
        _endpoint.uri(path),
      );
      request.headers['Accept'] = 'text/event-stream';
      request.headers['Cache-Control'] = 'no-cache';
      final token = await _endpoint.loadToken();
      if (token != null && token.isNotEmpty) {
        request.headers['X-Heimdallm-Token'] = token;
      }
      final response = await _httpClient.send(request);
      _connecting = false;
      if (_controller == null || _controller!.isClosed) {
        unawaited(response.stream.drain<void>().catchError((_) {}));
        return;
      }
      if (response.statusCode < 200 || response.statusCode >= 300) {
        // The response body here is an error page or JSON error, never SSE
        // wire format — handing it to the line parser below would just
        // silently produce zero events and hit onDone, masking a real,
        // possibly persistent failure (auth, routing, an untrusted cert on
        // a proxied instance) as a benign disconnect.
        unawaited(response.stream.drain<void>().catchError((_) {}));
        _controller?.addError(
          http.ClientException(
            'SSE endpoint returned HTTP ${response.statusCode}',
            request.url,
          ),
        );
        final delay = _errorBackoff();
        _consecutiveFailures++;
        _scheduleReconnect(delay);
        return;
      }
      _consecutiveFailures = 0;

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
                final delay = _errorBackoff();
                _consecutiveFailures++;
                _scheduleReconnect(delay);
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
              // Surface the transport error to listeners (mirrors the outer
              // catch below) so the UI can reflect the dropped connection,
              // then reconnect after a delay rather than giving up.
              _controller?.addError(e);
              final delay = _errorBackoff();
              _consecutiveFailures++;
              _scheduleReconnect(delay);
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
      final delay = _errorBackoff();
      _consecutiveFailures++;
      _scheduleReconnect(delay);
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
    _consecutiveFailures = 0;
    _cancelSubscription();
    _controller?.close();
    _controller = null;
  }
}

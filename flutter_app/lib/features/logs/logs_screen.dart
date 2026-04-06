import 'dart:async';
import 'dart:convert';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import '../../core/api/sse_client.dart';

// Terminal-style colors per log level
Color _levelColor(String line) {
  if (line.contains('level=ERROR') || line.contains('level=FATAL')) {
    return const Color(0xFFFF6B6B);
  }
  if (line.contains('level=WARN')) {
    return const Color(0xFFFFB347);
  }
  if (line.contains('level=DEBUG')) {
    return const Color(0xFF888888);
  }
  return const Color(0xFFD4D4D4);
}

class LogsScreen extends StatefulWidget {
  const LogsScreen({super.key});
  @override
  State<LogsScreen> createState() => _LogsScreenState();
}

class _LogsScreenState extends State<LogsScreen> {
  final _lines = <String>[];
  final _scrollController = ScrollController();
  SseClient? _sseClient;
  StreamSubscription<SseEvent>? _sub;
  bool _connected = false;
  bool _atBottom = true;
  bool _wrap = false;
  static const _maxLines = 2000;
  static const _bottomThreshold = 20.0;
  static const _bgColor = Color(0xFF0D1117);
  static const _fontFamily = 'Courier New';

  @override
  void initState() {
    super.initState();
    _scrollController.addListener(_onScroll);
    _connect();
  }

  void _onScroll() {
    final pos = _scrollController.position;
    final atBottom = pos.pixels >= pos.maxScrollExtent - _bottomThreshold;
    if (atBottom != _atBottom) setState(() => _atBottom = atBottom);
  }

  void _connect() {
    _sseClient = SseClient(path: '/logs/stream');
    _sub = _sseClient!.connect().listen(
      (event) {
        if (event.type == 'log_line') {
          try {
            final data = jsonDecode(event.data) as Map<String, dynamic>;
            _appendLine(data['line'] as String? ?? event.data);
          } catch (_) {
            _appendLine(event.data);
          }
        }
      },
      onError: (_) => setState(() => _connected = false),
      onDone: () => setState(() => _connected = false),
    );
    setState(() => _connected = true);
  }

  void _appendLine(String line) {
    setState(() {
      _lines.add(line);
      if (_lines.length > _maxLines) {
        _lines.removeRange(0, _lines.length - _maxLines);
      }
    });
    if (_atBottom) {
      WidgetsBinding.instance.addPostFrameCallback((_) {
        if (_scrollController.hasClients) {
          _scrollController.jumpTo(_scrollController.position.maxScrollExtent);
        }
      });
    }
  }

  void _scrollToBottom() {
    _scrollController.animateTo(
      _scrollController.position.maxScrollExtent,
      duration: const Duration(milliseconds: 200),
      curve: Curves.easeOut,
    );
    setState(() => _atBottom = true);
  }

  Future<void> _copyAll() async {
    await Clipboard.setData(ClipboardData(text: _lines.join('\n')));
    if (mounted) {
      ScaffoldMessenger.of(context).showSnackBar(
        const SnackBar(
            content: Text('Logs copiados al portapapeles'),
            duration: Duration(seconds: 2)),
      );
    }
  }

  @override
  void dispose() {
    _scrollController.removeListener(_onScroll);
    _scrollController.dispose();
    _sub?.cancel();
    _sseClient?.disconnect();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return Scaffold(
      backgroundColor: _bgColor,
      appBar: AppBar(
        backgroundColor: const Color(0xFF161B22),
        foregroundColor: const Color(0xFFD4D4D4),
        title: Row(
          children: [
            const Text('Daemon Logs',
                style: TextStyle(fontFamily: _fontFamily, fontSize: 14)),
            const SizedBox(width: 8),
            Container(
              width: 7, height: 7,
              decoration: BoxDecoration(
                shape: BoxShape.circle,
                color: _connected ? const Color(0xFF3FB950) : const Color(0xFFFF6B6B),
                boxShadow: _connected
                    ? [BoxShadow(color: const Color(0xFF3FB950).withValues(alpha: 0.5), blurRadius: 4)]
                    : null,
              ),
            ),
          ],
        ),
        actions: [
          IconButton(
            icon: Icon(_wrap ? Icons.wrap_text : Icons.notes, size: 18),
            tooltip: _wrap ? 'Desactivar wrap' : 'Activar wrap',
            color: _wrap ? const Color(0xFF3FB950) : null,
            onPressed: () => setState(() => _wrap = !_wrap),
          ),
          IconButton(
            icon: const Icon(Icons.copy_outlined, size: 18),
            tooltip: 'Copiar todo',
            onPressed: _lines.isEmpty ? null : _copyAll,
          ),
        ],
      ),
      body: _lines.isEmpty
          ? Center(
              child: Column(
                mainAxisSize: MainAxisSize.min,
                children: [
                  const CircularProgressIndicator(
                      color: Color(0xFF3FB950), strokeWidth: 2),
                  const SizedBox(height: 12),
                  Text('Conectando...',
                      style: TextStyle(
                          fontFamily: _fontFamily,
                          fontSize: 12,
                          color: Colors.grey.shade600)),
                ],
              ),
            )
          : Scrollbar(
              controller: _scrollController,
              child: ListView.builder(
                controller: _scrollController,
                padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 8),
                itemCount: _lines.length,
                itemBuilder: (_, i) {
                  final line = _lines[i];
                  return Padding(
                    padding: const EdgeInsets.only(bottom: 1),
                    child: Text(
                      line,
                      softWrap: _wrap,
                      overflow: _wrap ? TextOverflow.visible : TextOverflow.fade,
                      style: TextStyle(
                        fontFamily: _fontFamily,
                        fontSize: 11.5,
                        height: 1.5,
                        color: _levelColor(line),
                        letterSpacing: 0,
                      ),
                    ),
                  );
                },
              ),
            ),
      floatingActionButton: _atBottom
          ? null
          : FloatingActionButton.small(
              onPressed: _scrollToBottom,
              backgroundColor: const Color(0xFF21262D),
              foregroundColor: const Color(0xFFD4D4D4),
              tooltip: 'Ir al final',
              child: const Icon(Icons.arrow_downward, size: 18),
            ),
    );
  }
}

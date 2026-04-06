import 'dart:async';
import 'dart:convert';
import 'package:flutter/material.dart';
import 'package:flutter/services.dart';
import '../../core/api/sse_client.dart';

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
  bool _atBottom = true; // tracks if user is at the bottom
  static const _maxLines = 2000;
  static const _bottomThreshold = 20.0;

  @override
  void initState() {
    super.initState();
    _scrollController.addListener(_onScroll);
    _connect();
  }

  void _onScroll() {
    final pos = _scrollController.position;
    final atBottom = pos.pixels >= pos.maxScrollExtent - _bottomThreshold;
    if (atBottom != _atBottom) {
      setState(() => _atBottom = atBottom);
    }
  }

  void _connect() {
    _sseClient = SseClient(path: '/logs/stream');
    _sub = _sseClient!.connect().listen(
      (event) {
        if (event.type == 'log_line') {
          try {
            final data = jsonDecode(event.data) as Map<String, dynamic>;
            final line = data['line'] as String? ?? event.data;
            _appendLine(line);
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
            content: Text('Logs copied to clipboard'),
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
      appBar: AppBar(
        title: Row(
          children: [
            const Text('Daemon Logs'),
            const SizedBox(width: 8),
            Container(
              width: 8,
              height: 8,
              decoration: BoxDecoration(
                shape: BoxShape.circle,
                color: _connected ? Colors.green : Colors.red,
              ),
            ),
          ],
        ),
        actions: [
          IconButton(
            icon: const Icon(Icons.copy),
            tooltip: 'Copy all',
            onPressed: _lines.isEmpty ? null : _copyAll,
          ),
        ],
      ),
      body: _lines.isEmpty
          ? const Center(child: CircularProgressIndicator())
          : ListView.builder(
              controller: _scrollController,
              padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 8),
              itemCount: _lines.length,
              itemBuilder: (_, i) => Text(
                _lines[i],
                style: const TextStyle(
                  fontFamily: 'monospace',
                  fontSize: 11,
                  height: 1.4,
                ),
              ),
            ),
      floatingActionButton: _atBottom
          ? null
          : FloatingActionButton.small(
              onPressed: _scrollToBottom,
              tooltip: 'Jump to bottom',
              child: const Icon(Icons.arrow_downward),
            ),
    );
  }
}

import 'dart:async';
import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../../core/api/sse_client.dart';
import '../../../core/platform/platform_services_provider.dart';
import '../event_summary.dart';

class EventsTab extends ConsumerStatefulWidget {
  const EventsTab({super.key});
  @override
  ConsumerState<EventsTab> createState() => _EventsTabState();
}

class _EventsTabState extends ConsumerState<EventsTab> {
  static const _maxEvents = 500;
  final _events = <_EventRow>[];
  SseClient? _client;
  StreamSubscription<SseEvent>? _sub;
  final _scroll = ScrollController();
  final _expanded = <int>{};

  @override
  void initState() {
    super.initState();
    final platform = ref.read(platformServicesProvider);
    _client = SseClient(platform: platform, path: '/events');
    _sub = _client!.connect().listen(_onEvent);
  }

  @override
  void dispose() {
    _sub?.cancel();
    _client?.disconnect();
    _scroll.dispose();
    super.dispose();
  }

  void _onEvent(SseEvent ev) {
    Map<String, dynamic> payload;
    try {
      final decoded = jsonDecode(ev.data);
      payload = decoded is Map<String, dynamic> ? decoded : <String, dynamic>{};
    } catch (_) {
      payload = const {};
    }
    setState(() {
      _events.add(_EventRow(
        timestamp: DateTime.now(),
        type: ev.type,
        payload: payload,
        rawData: ev.data,
      ));
      while (_events.length > _maxEvents) {
        _events.removeAt(0);
      }
    });
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (_scroll.hasClients) {
        _scroll.jumpTo(_scroll.position.maxScrollExtent);
      }
    });
  }

  @override
  Widget build(BuildContext context) {
    if (_events.isEmpty) {
      return const Center(
        child: Text(
          'Waiting for events. Polling cycle runs every 60 s by default.',
          style: TextStyle(color: Colors.grey),
        ),
      );
    }
    return ListView.builder(
      controller: _scroll,
      itemCount: _events.length,
      itemBuilder: (context, i) => _Row(
        row: _events[i],
        expanded: _expanded.contains(i),
        onTap: () => setState(() {
          _expanded.contains(i) ? _expanded.remove(i) : _expanded.add(i);
        }),
      ),
    );
  }
}

class _EventRow {
  final DateTime timestamp;
  final String type;
  final Map<String, dynamic> payload;
  final String rawData;
  const _EventRow({
    required this.timestamp,
    required this.type,
    required this.payload,
    required this.rawData,
  });
}

class _Row extends StatelessWidget {
  const _Row({required this.row, required this.expanded, required this.onTap});
  final _EventRow row;
  final bool expanded;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final glyph = glyphFor(row.type);
    final hh = row.timestamp.hour.toString().padLeft(2, '0');
    final mm = row.timestamp.minute.toString().padLeft(2, '0');
    final ss = row.timestamp.second.toString().padLeft(2, '0');
    return InkWell(
      onTap: onTap,
      child: Padding(
        padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 6),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Text('$hh:$mm:$ss',
                    style: const TextStyle(
                        fontFamily: 'monospace', fontSize: 12, color: Colors.grey)),
                const SizedBox(width: 12),
                Icon(glyph.icon, color: glyph.color, size: 16),
                const SizedBox(width: 8),
                Expanded(
                  child: Text(
                    summarize(row.type, row.payload),
                    style: const TextStyle(fontFamily: 'monospace', fontSize: 13),
                  ),
                ),
              ],
            ),
            if (expanded)
              Container(
                margin: const EdgeInsets.only(left: 60, top: 4),
                padding: const EdgeInsets.all(8),
                decoration: BoxDecoration(
                  color: const Color(0xFFF5F5F5),
                  borderRadius: BorderRadius.circular(4),
                ),
                child: SelectableText(
                  _pretty(row.rawData),
                  style: const TextStyle(fontFamily: 'monospace', fontSize: 12),
                ),
              ),
          ],
        ),
      ),
    );
  }

  String _pretty(String raw) {
    try {
      return const JsonEncoder.withIndent('  ').convert(jsonDecode(raw));
    } catch (_) {
      return raw;
    }
  }
}

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
  bool _autoScroll = true;
  final Set<String> _enabledGroups = {'pr', 'issue', 'polling', 'state', 'circuit_breaker'};
  String _searchQuery = '';

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

  String _groupOf(String type) {
    if (type == 'repo_discovered') return 'pr';
    if (type.startsWith('pr_')) return 'pr';
    if (type.startsWith('issue_')) return 'issue';
    if (type.startsWith('polling_')) return 'polling';
    if (type.contains('state_changed')) return 'state';
    if (type == 'circuit_breaker_tripped') return 'circuit_breaker';
    return 'other';
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
      if (_autoScroll && _scroll.hasClients) {
        _scroll.jumpTo(_scroll.position.maxScrollExtent);
      }
    });
  }

  @override
  Widget build(BuildContext context) {
    final visible = _events.where(_isVisible).toList(growable: false);
    return Column(
      children: [
        _Toolbar(
          autoScroll: _autoScroll,
          enabledGroups: _enabledGroups,
          searchQuery: _searchQuery,
          eventCount: _events.length,
          onAutoScrollChanged: (v) => setState(() => _autoScroll = v),
          onGroupToggled: (g) => setState(() {
            _enabledGroups.contains(g) ? _enabledGroups.remove(g) : _enabledGroups.add(g);
            _expanded.clear();
          }),
          onSearchChanged: (q) => setState(() {
            _searchQuery = q;
            _expanded.clear();
          }),
          onClear: () => setState(() {
            _events.clear();
            _expanded.clear();
          }),
        ),
        Expanded(
          child: visible.isEmpty
              ? const Center(
                  child: Text(
                    'Waiting for events. Polling cycle runs every 60 s by default.',
                    style: TextStyle(color: Colors.grey),
                  ),
                )
              : ListView.builder(
                  controller: _scroll,
                  itemCount: visible.length,
                  itemBuilder: (context, i) => _Row(
                    row: visible[i],
                    expanded: _expanded.contains(i),
                    onTap: () => setState(() {
                      _expanded.contains(i) ? _expanded.remove(i) : _expanded.add(i);
                    }),
                  ),
                ),
        ),
      ],
    );
  }

  bool _isVisible(_EventRow row) {
    if (!_enabledGroups.contains(_groupOf(row.type))) return false;
    if (_searchQuery.isEmpty) return true;
    final summary = summarize(row.type, row.payload).toLowerCase();
    return summary.contains(_searchQuery.toLowerCase());
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

class _Toolbar extends StatelessWidget {
  const _Toolbar({
    required this.autoScroll,
    required this.enabledGroups,
    required this.searchQuery,
    required this.eventCount,
    required this.onAutoScrollChanged,
    required this.onGroupToggled,
    required this.onSearchChanged,
    required this.onClear,
  });

  final bool autoScroll;
  final Set<String> enabledGroups;
  final String searchQuery;
  final int eventCount;
  final ValueChanged<bool> onAutoScrollChanged;
  final ValueChanged<String> onGroupToggled;
  final ValueChanged<String> onSearchChanged;
  final VoidCallback onClear;

  static const _groups = {
    'pr': 'PR',
    'issue': 'Issue',
    'polling': 'Polling',
    'state': 'State',
    'circuit_breaker': 'Circuit',
  };

  @override
  Widget build(BuildContext context) {
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 6),
      decoration: const BoxDecoration(
        border: Border(bottom: BorderSide(color: Color(0xFFE0E0E0))),
      ),
      child: Wrap(
        spacing: 8,
        runSpacing: 4,
        crossAxisAlignment: WrapCrossAlignment.center,
        children: [
          IconButton(
            tooltip: autoScroll ? 'Pause auto-scroll' : 'Resume auto-scroll',
            icon: Icon(autoScroll ? Icons.pause : Icons.play_arrow),
            onPressed: () => onAutoScrollChanged(!autoScroll),
            visualDensity: VisualDensity.compact,
          ),
          ..._groups.entries.map((e) => FilterChip(
                label: Text(e.value),
                selected: enabledGroups.contains(e.key),
                onSelected: (_) => onGroupToggled(e.key),
              )),
          SizedBox(
            width: 200,
            child: TextField(
              decoration: const InputDecoration(
                hintText: 'Search',
                isDense: true,
                prefixIcon: Icon(Icons.search, size: 16),
                border: OutlineInputBorder(),
              ),
              style: const TextStyle(fontSize: 12),
              onChanged: onSearchChanged,
            ),
          ),
          TextButton.icon(
            onPressed: onClear,
            icon: const Icon(Icons.clear_all, size: 16),
            label: Text('Clear ($eventCount)'),
          ),
        ],
      ),
    );
  }
}

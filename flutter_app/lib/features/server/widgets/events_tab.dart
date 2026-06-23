import 'dart:async';
import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../../core/api/sse_client.dart';
import '../../../core/platform/platform_services_provider.dart';
import '../event_summary.dart';
import 'connection_status_banner.dart';
import 'event_row.dart';

/// Server > Events tab — operational dashboard of live SSE events.
///
/// Each event is rendered through [format] (see event_summary.dart) so
/// the row shows a human title (e.g. "Review completed"), the target
/// repo/PR/issue, and chip-style details (agent, duration, reason)
/// instead of the raw event type name plus a JSON dump (#453). The full
/// JSON payload is still accessible by clicking a row to expand.
///
/// Newest events appear at the top so the operator can read top-down
/// without chasing the scroll; auto-scroll keeps the view pinned to the
/// freshest row until paused.
class EventsTab extends ConsumerStatefulWidget {
  const EventsTab({super.key, @visibleForTesting this.client});

  /// Test-only seam: inject a pre-built [SseClient] (typically backed by a
  /// mock HTTP client) so the connection-indicator behaviour can be driven
  /// deterministically. Production code leaves this null and the tab builds
  /// its own client against the daemon.
  final SseClient? client;

  @override
  ConsumerState<EventsTab> createState() => _EventsTabState();
}

class _EventsTabState extends ConsumerState<EventsTab> {
  static const _maxEvents = 500;
  // Newest at index 0. Render order matches insertion order.
  final _events = <_EventRow>[];
  SseClient? _client;
  StreamSubscription<SseEvent>? _sub;
  final _scroll = ScrollController();
  // Expanded rows are keyed by the row's monotonic id (set on insert) so
  // a prepend doesn't shift previously-expanded indices.
  final _expanded = <int>{};
  int _nextRowId = 0;
  bool _autoScroll = true;
  // This tab's own SSE connection health. The shared stream that drives the
  // global indicator is separate, so a drop isolated to this connection needs
  // its own in-tab signal (#572). Optimistic on connect — no events arrive for
  // up to a polling cycle (60 s) under normal operation.
  bool _connected = true;
  final Set<String> _enabledGroups = {'pr', 'issue', 'polling', 'state', 'circuit_breaker'};
  String _searchQuery = '';

  @override
  void initState() {
    super.initState();
    final platform = ref.read(platformServicesProvider);
    _client = widget.client ?? SseClient(platform: platform, path: '/events');
    // The SSE stream forwards transport errors to listeners; the client
    // auto-reconnects, so flip a local flag to drive an in-tab banner instead
    // of letting the drop surface as an unhandled async error. _connected is
    // restored in _onEvent when the stream resumes.
    _sub = _client!.connect().listen(
      _onEvent,
      onError: (_) => _setConnected(false),
      onDone: () => _setConnected(false),
    );
  }

  void _setConnected(bool value) {
    if (_connected == value || !mounted) return;
    setState(() => _connected = value);
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
    // A late event can arrive after dispose() cancels the subscription; bail
    // before touching state so setState() is never called after dispose.
    if (!mounted) return;
    Map<String, dynamic> payload;
    try {
      final decoded = jsonDecode(ev.data);
      payload = decoded is Map<String, dynamic> ? decoded : <String, dynamic>{};
    } catch (_) {
      payload = const {};
    }
    setState(() {
      // An event arriving means the stream is live again — clear any banner.
      _connected = true;
      final row = _EventRow(
        id: _nextRowId++,
        timestamp: DateTime.now(),
        type: ev.type,
        payload: payload,
        rawData: ev.data,
      );
      _events.insert(0, row);
      while (_events.length > _maxEvents) {
        final dropped = _events.removeLast();
        _expanded.remove(dropped.id);
      }
    });
    WidgetsBinding.instance.addPostFrameCallback((_) {
      if (_autoScroll && _scroll.hasClients) {
        // Newest is at the top — pin to 0.
        _scroll.jumpTo(0);
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
          }),
          onSearchChanged: (q) => setState(() => _searchQuery = q),
          onClear: () => setState(() {
            _events.clear();
            _expanded.clear();
          }),
        ),
        if (!_connected) const ConnectionStatusBanner(),
        Expanded(
          child: visible.isEmpty
              ? Center(
                  child: Text(
                    'Waiting for events. Polling cycle runs every 60 s by default.',
                    style: TextStyle(
                      color: Theme.of(context).colorScheme.onSurfaceVariant,
                    ),
                  ),
                )
              : ListView.builder(
                  controller: _scroll,
                  itemCount: visible.length,
                  itemBuilder: (context, i) {
                    final row = visible[i];
                    return EventRow(
                      timestamp: row.timestamp,
                      type: row.type,
                      payload: row.payload,
                      rawData: row.rawData,
                      expanded: _expanded.contains(row.id),
                      onTap: () => setState(() {
                        _expanded.contains(row.id) ? _expanded.remove(row.id) : _expanded.add(row.id);
                      }),
                    );
                  },
                ),
        ),
      ],
    );
  }

  bool _isVisible(_EventRow row) {
    if (!_enabledGroups.contains(_groupOf(row.type))) return false;
    if (_searchQuery.isEmpty) return true;
    final q = _searchQuery.toLowerCase();
    final ev = format(row.type, row.payload);
    if (ev.label.toLowerCase().contains(q)) return true;
    if (ev.target.toLowerCase().contains(q)) return true;
    for (final d in ev.details) {
      if (d.toLowerCase().contains(q)) return true;
    }
    if (row.type.toLowerCase().contains(q)) return true;
    return false;
  }
}

class _EventRow {
  /// Stable id assigned on insert; survives the FIFO trim so expand
  /// state doesn't latch onto the wrong row when older entries drop off.
  final int id;
  final DateTime timestamp;
  final String type;
  final Map<String, dynamic> payload;
  final String rawData;
  const _EventRow({
    required this.id,
    required this.timestamp,
    required this.type,
    required this.payload,
    required this.rawData,
  });
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
      decoration: BoxDecoration(
        border: Border(
          bottom: BorderSide(color: Theme.of(context).dividerColor),
        ),
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

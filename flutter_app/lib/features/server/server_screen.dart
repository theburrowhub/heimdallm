import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../config/config_providers.dart';
import '../logs/logs_screen.dart' show LogsView;
import 'widgets/events_tab.dart';
import 'widgets/status_tab.dart';

const _tabIndices = {'status': 0, 'events': 1, 'logs': 2};

class ServerScreen extends ConsumerStatefulWidget {
  const ServerScreen({super.key, this.initialTab = 'status'});
  final String initialTab;

  @override
  ConsumerState<ServerScreen> createState() => _ServerScreenState();
}

class _ServerScreenState extends ConsumerState<ServerScreen>
    with SingleTickerProviderStateMixin {
  late final TabController _controller;

  @override
  void initState() {
    super.initState();
    _controller = TabController(
      length: 3,
      vsync: this,
      initialIndex: _tabIndices[widget.initialTab] ?? 0,
    );
  }

  @override
  void dispose() {
    _controller.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final daemonRunning = ref.watch(daemonHealthProvider).valueOrNull ?? false;
    return Scaffold(
      appBar: AppBar(
        title: const Text('Server'),
        leading: IconButton(
          icon: const Icon(Icons.arrow_back),
          onPressed: () => Navigator.of(context).maybePop(),
        ),
        bottom: TabBar(
          controller: _controller,
          tabs: const [
            Tab(icon: Icon(Icons.dns_outlined), text: 'Status'),
            Tab(icon: Icon(Icons.bolt_outlined), text: 'Events'),
            Tab(icon: Icon(Icons.article_outlined), text: 'Logs'),
          ],
        ),
      ),
      body: TabBarView(
        controller: _controller,
        children: [
          const StatusTab(),
          daemonRunning
              ? const EventsTab()
              : const _DaemonStoppedPlaceholder(label: 'live events'),
          daemonRunning
              ? const LogsView()
              : const _DaemonStoppedPlaceholder(label: 'logs'),
        ],
      ),
    );
  }
}

class _DaemonStoppedPlaceholder extends StatelessWidget {
  const _DaemonStoppedPlaceholder({required this.label});
  final String label;

  @override
  Widget build(BuildContext context) {
    return Center(
      child: Column(
        mainAxisSize: MainAxisSize.min,
        children: [
          const Icon(Icons.power_off, size: 48, color: Colors.grey),
          const SizedBox(height: 12),
          Text('Server is stopped — start it to see $label.',
              style: const TextStyle(color: Colors.grey)),
          const SizedBox(height: 8),
          const Text('Switch to the Status tab to start the server.',
              style: TextStyle(color: Colors.grey, fontSize: 12)),
        ],
      ),
    );
  }
}

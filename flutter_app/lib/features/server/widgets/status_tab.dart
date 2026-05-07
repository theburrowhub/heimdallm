import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../config/config_providers.dart';
import '../../dashboard/dashboard_providers.dart';
import '../server_actions.dart' as server_actions;

class StatusTab extends ConsumerWidget {
  const StatusTab({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final daemonRunning = ref.watch(daemonHealthProvider).valueOrNull ?? false;
    final daemonStarting = ref.watch(daemonStartingProvider);
    return SingleChildScrollView(
      padding: const EdgeInsets.all(16),
      child: Card(
        child: Padding(
          padding: const EdgeInsets.all(16),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              _StateIndicator(
                running: daemonRunning,
                starting: daemonStarting,
              ),
              const SizedBox(height: 16),
              _StartStopButton(
                running: daemonRunning,
                starting: daemonStarting,
              ),
            ],
          ),
        ),
      ),
    );
  }
}

class _StateIndicator extends StatelessWidget {
  const _StateIndicator({required this.running, required this.starting});
  final bool running;
  final bool starting;

  @override
  Widget build(BuildContext context) {
    final label = starting
        ? 'Starting…'
        : running
            ? 'Running'
            : 'Stopped';
    final color = starting
        ? Colors.amber
        : running
            ? Colors.green
            : Colors.grey;
    return Row(
      children: [
        Icon(Icons.circle, size: 12, color: color),
        const SizedBox(width: 8),
        Text(label, style: const TextStyle(fontSize: 14)),
      ],
    );
  }
}

class _StartStopButton extends ConsumerWidget {
  const _StartStopButton({required this.running, required this.starting});
  final bool running;
  final bool starting;

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    if (starting) {
      return const Row(children: [
        SizedBox(width: 16, height: 16, child: CircularProgressIndicator(strokeWidth: 2)),
        SizedBox(width: 8),
        Text('Starting…'),
      ]);
    }
    return FilledButton.icon(
      icon: Icon(running ? Icons.power_settings_new : Icons.play_arrow),
      label: Text(running ? 'Stop server' : 'Start server'),
      onPressed: running
          ? () => server_actions.confirmShutdown(context, ref)
          : () => server_actions.startDaemon(context, ref),
    );
  }
}

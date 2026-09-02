import 'dart:async';

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../../shared/widgets/restart_required_banner.dart';
import '../../../shared/widgets/toast.dart';
import '../../config/config_providers.dart';
import '../../dashboard/dashboard_providers.dart';
import '../server_actions.dart' as server_actions;
import '../server_providers.dart';

class StatusTab extends ConsumerStatefulWidget {
  const StatusTab({super.key});
  @override
  ConsumerState<StatusTab> createState() => _StatusTabState();
}

class _StatusTabState extends ConsumerState<StatusTab> {
  String? _initialBindAddr;
  int? _initialPort;
  String? _editedBindAddr;
  int? _editedPort;
  Timer? _saveDebounce;

  bool get _bindAddrChanged =>
      _editedBindAddr != null && _editedBindAddr != _initialBindAddr;
  bool get _portChanged =>
      _editedPort != null && _editedPort != _initialPort;
  bool get _showBanner => _bindAddrChanged || _portChanged;

  @override
  void dispose() {
    _saveDebounce?.cancel();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final config = ref.watch(configNotifierProvider).value;
    if (config == null) {
      return const Center(child: CircularProgressIndicator());
    }
    _initialBindAddr ??= config.bindAddr ?? '127.0.0.1';
    _initialPort ??= config.serverPort;
    final daemonRunning = ref.watch(daemonHealthProvider).value ?? false;
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
              const Divider(height: 32),
              const Text('Listen URL',
                  style: TextStyle(fontWeight: FontWeight.w600)),
              const SizedBox(height: 8),
              _ListenUrlEditor(
                initialBindAddr: _initialBindAddr!,
                initialPort: _initialPort!,
                onBindAddrChanged: _onBindAddrChanged,
                onPortChanged: _onPortChanged,
              ),
              if (_showBanner) ...[
                const SizedBox(height: 16),
                RestartRequiredBanner(
                  message:
                      'Listen URL changed. Restart the server for it to take effect.',
                  detail: _portChanged
                      ? 'Port change also requires restarting the desktop app for the GUI to reconnect.'
                      : null,
                  onRestart: () => server_actions.restartDaemon(context, ref),
                  starting: daemonStarting,
                ),
              ],
              const SizedBox(height: 16),
              const Divider(),
              const SizedBox(height: 8),
              _HealthSummary(),
            ],
          ),
        ),
      ),
    );
  }

  void _onBindAddrChanged(String v) {
    setState(() => _editedBindAddr = v);
    _scheduleSave();
  }

  void _onPortChanged(int v) {
    setState(() => _editedPort = v);
    _scheduleSave();
  }

  void _scheduleSave() {
    _saveDebounce?.cancel();
    _saveDebounce = Timer(const Duration(milliseconds: 800), _save);
  }

  Future<void> _save() async {
    final api = ref.read(apiClientProvider);
    try {
      final patch = <String, dynamic>{};
      if (_bindAddrChanged) patch['bind_addr'] = _editedBindAddr;
      if (_portChanged) patch['server_port'] = _editedPort;
      if (patch.isEmpty) return;
      await api.patchConfig(patch);
      if (!mounted) return;
      showToast(context, 'Saved (restart required)');
    } catch (e) {
      if (mounted) showToast(context, 'Error: $e', isError: true);
    }
  }
}

class _ListenUrlEditor extends StatefulWidget {
  const _ListenUrlEditor({
    required this.initialBindAddr,
    required this.initialPort,
    required this.onBindAddrChanged,
    required this.onPortChanged,
  });
  final String initialBindAddr;
  final int initialPort;
  final ValueChanged<String> onBindAddrChanged;
  final ValueChanged<int> onPortChanged;

  @override
  State<_ListenUrlEditor> createState() => _ListenUrlEditorState();
}

class _ListenUrlEditorState extends State<_ListenUrlEditor> {
  late final TextEditingController _bindCtrl;
  late final TextEditingController _portCtrl;

  @override
  void initState() {
    super.initState();
    _bindCtrl = TextEditingController(text: widget.initialBindAddr);
    _portCtrl = TextEditingController(text: widget.initialPort.toString());
  }

  @override
  void dispose() {
    _bindCtrl.dispose();
    _portCtrl.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return Row(
      children: [
        Expanded(
          flex: 3,
          child: TextField(
            controller: _bindCtrl,
            decoration: const InputDecoration(
              labelText: 'Bind address',
              helperText: 'e.g. 127.0.0.1, 0.0.0.0',
              border: OutlineInputBorder(),
              isDense: true,
            ),
            onChanged: widget.onBindAddrChanged,
          ),
        ),
        const SizedBox(width: 12),
        Expanded(
          flex: 1,
          child: TextField(
            controller: _portCtrl,
            decoration: const InputDecoration(
              labelText: 'Port',
              border: OutlineInputBorder(),
              isDense: true,
            ),
            keyboardType: TextInputType.number,
            onChanged: (v) {
              final n = int.tryParse(v);
              if (n != null) widget.onPortChanged(n);
            },
          ),
        ),
      ],
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

class _HealthSummary extends ConsumerWidget {
  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final detail = ref.watch(serverHealthDetailProvider).value;
    if (detail == null) return const SizedBox.shrink();
    final parts = <String>[];
    if (detail.version != null && detail.version!.isNotEmpty) {
      parts.add('Heimdallm ${detail.version}');
    }
    if (detail.startedAt != null) {
      parts.add('running for ${_formatUptime(DateTime.now().difference(detail.startedAt!))}');
    }
    if (parts.isEmpty) return const SizedBox.shrink();
    return Text(
      parts.join(' — '),
      style: const TextStyle(fontSize: 12, color: Colors.grey),
    );
  }
}

String _formatUptime(Duration d) {
  if (d.inDays > 0) return '${d.inDays}d ${d.inHours % 24}h';
  if (d.inHours > 0) return '${d.inHours}h ${d.inMinutes % 60}m';
  if (d.inMinutes > 0) return '${d.inMinutes}m';
  return '${d.inSeconds}s';
}

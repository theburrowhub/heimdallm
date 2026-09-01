import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../core/api/api_client.dart';
import '../../core/api/cluster_api.dart';
import '../../core/instances/instances_providers.dart';
import '../../core/instances/models.dart';

/// Shows what differs between this hub and every instance, and offers to push
/// the shared configuration to all of them.
Future<void> showConfigPropagationDialog(BuildContext context, WidgetRef ref) {
  return showDialog<void>(
    context: context,
    builder: (context) => const _ConfigPropagationDialog(),
  );
}

class _ConfigPropagationDialog extends ConsumerStatefulWidget {
  const _ConfigPropagationDialog();

  @override
  ConsumerState<_ConfigPropagationDialog> createState() =>
      _ConfigPropagationDialogState();
}

class _ConfigPropagationDialogState
    extends ConsumerState<_ConfigPropagationDialog> {
  bool _pushing = false;
  PropagateReport? _report;
  String? _error;

  @override
  Widget build(BuildContext context) {
    final driftAsync = ref.watch(configDriftProvider);

    return AlertDialog(
      title: const Text('Configuration across instances'),
      content: SizedBox(
        width: 560,
        child: SingleChildScrollView(
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Text(
                'Shared settings — prompts, review and merge policy, polling, '
                'per-repo and per-org overrides — are pushed to every '
                'instance. Machine-specific ones are never sent: the port, '
                'the bind address, GitHub and API tokens, local directories, '
                'and each instance’s own repository lists.',
                style: TextStyle(
                  fontSize: 12,
                  color: Theme.of(context).colorScheme.onSurfaceVariant,
                ),
              ),
              const SizedBox(height: 16),
              if (_report != null)
                _ReportView(report: _report!)
              else
                driftAsync.when(
                  loading: () => const Padding(
                    padding: EdgeInsets.all(24),
                    child: Center(child: CircularProgressIndicator()),
                  ),
                  error: (e, _) => Text('Could not compare configuration: $e'),
                  data: (drifts) => _DriftView(drifts: drifts),
                ),
              if (_error != null) ...[
                const SizedBox(height: 12),
                Text(
                  _error!,
                  style: TextStyle(
                    color: Theme.of(context).colorScheme.error,
                    fontSize: 12,
                  ),
                ),
              ],
            ],
          ),
        ),
      ),
      actions: [
        TextButton(
          onPressed: _pushing ? null : () => Navigator.pop(context),
          child: Text(_report == null ? 'Cancel' : 'Close'),
        ),
        FilledButton(
          onPressed: _pushing ? null : _propagate,
          child: _pushing
              ? const SizedBox(
                  width: 16,
                  height: 16,
                  child: CircularProgressIndicator(strokeWidth: 2),
                )
              : const Text('Apply to all'),
        ),
      ],
    );
  }

  Future<void> _propagate() async {
    setState(() {
      _pushing = true;
      _error = null;
    });
    try {
      final report = await ref.read(hubApiClientProvider).propagateConfig();
      if (mounted) {
        setState(() {
          _report = report;
          _pushing = false;
        });
      }
      ref.invalidate(configDriftProvider);
    } on ApiException catch (e) {
      if (mounted) {
        setState(() {
          _error = e.message;
          _pushing = false;
        });
      }
    }
  }
}

class _DriftView extends StatelessWidget {
  const _DriftView({required this.drifts});

  final List<InstanceDrift> drifts;

  @override
  Widget build(BuildContext context) {
    final comparable = drifts.where((d) => !d.skipped).toList();
    if (comparable.isEmpty) {
      return const Text(
        'No other instances to compare against.',
        style: TextStyle(fontSize: 13),
      );
    }
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        for (final drift in comparable) _DriftTile(drift: drift),
      ],
    );
  }
}

class _DriftTile extends StatelessWidget {
  const _DriftTile({required this.drift});

  final InstanceDrift drift;

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;

    if (drift.error.isNotEmpty) {
      return ListTile(
        dense: true,
        contentPadding: EdgeInsets.zero,
        leading: Icon(Icons.cloud_off_outlined, color: scheme.error),
        title: Text(drift.displayName),
        subtitle: Text(
          drift.error,
          style: TextStyle(fontSize: 11, color: scheme.error),
        ),
      );
    }
    if (drift.inSync) {
      return ListTile(
        dense: true,
        contentPadding: EdgeInsets.zero,
        leading: const Icon(Icons.check_circle_outline, color: Colors.green),
        title: Text(drift.displayName),
        subtitle: const Text('In sync', style: TextStyle(fontSize: 11)),
      );
    }
    return ExpansionTile(
      tilePadding: EdgeInsets.zero,
      childrenPadding: const EdgeInsets.only(left: 8, bottom: 8),
      leading: Icon(Icons.sync_problem_outlined, color: scheme.tertiary),
      title: Text(drift.displayName),
      subtitle: Text(
        drift.drifts.length == 1
            ? '1 setting differs'
            : '${drift.drifts.length} settings differ',
        style: const TextStyle(fontSize: 11),
      ),
      children: [
        for (final d in drift.drifts)
          Padding(
            padding: const EdgeInsets.symmetric(vertical: 2),
            child: Row(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Expanded(
                  flex: 2,
                  child: Text(d.key, style: const TextStyle(fontSize: 11)),
                ),
                Expanded(
                  flex: 3,
                  child: Text(
                    d.missing
                        ? 'not set → ${_render(d.hubValue)}'
                        : '${_render(d.remoteValue)} → ${_render(d.hubValue)}',
                    style: TextStyle(
                      fontSize: 11,
                      color: scheme.onSurfaceVariant,
                    ),
                  ),
                ),
              ],
            ),
          ),
      ],
    );
  }

  static String _render(Object? value) {
    if (value == null) return 'unset';
    final text = '$value';
    return text.length > 60 ? '${text.substring(0, 57)}…' : text;
  }
}

class _ReportView extends StatelessWidget {
  const _ReportView({required this.report});

  final PropagateReport report;

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        for (final result in report.results)
          ListTile(
            dense: true,
            contentPadding: EdgeInsets.zero,
            leading: Icon(
              result.skipped
                  ? Icons.remove_circle_outline
                  : result.ok
                  ? Icons.check_circle_outline
                  : Icons.error_outline,
              color: result.skipped
                  ? scheme.onSurfaceVariant
                  : result.ok
                  ? Colors.green
                  : scheme.error,
            ),
            title: Text(result.displayName),
            subtitle: Text(
              result.error.isNotEmpty
                  ? result.error
                  : result.appliedKeys.isEmpty
                  ? 'Applied'
                  : 'Applied ${result.appliedKeys.length} settings',
              style: TextStyle(
                fontSize: 11,
                color: result.ok ? null : scheme.error,
              ),
            ),
          ),
        if (report.skippedLocal.isNotEmpty) ...[
          const SizedBox(height: 8),
          Text(
            'Kept local: ${report.skippedLocal.join(', ')}',
            style: TextStyle(fontSize: 11, color: scheme.onSurfaceVariant),
          ),
        ],
      ],
    );
  }
}

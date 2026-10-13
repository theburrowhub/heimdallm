import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:heimdallm/core/api/api_client.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/dashboard/dashboard_providers.dart';
import 'package:heimdallm/features/flows/editor/flow_editor_controller.dart';
import 'package:heimdallm/shared/design_system/color_resolver.dart';
import 'package:heimdallm/shared/design_system/components/components.dart';
import 'package:heimdallm/shared/design_system/tokens.dart';

/// Runs the flow being edited (saved or not) for a moment, and highlights
/// the path it takes on the canvas.
class FlowTestPanel extends ConsumerStatefulWidget {
  final VoidCallback? onClose;
  const FlowTestPanel({super.key, this.onClose});

  @override
  ConsumerState<FlowTestPanel> createState() => _FlowTestPanelState();
}

class _FlowTestPanelState extends ConsumerState<FlowTestPanel> {
  DateTime _at = DateTime.now();
  String? _error;
  bool _running = false;

  Future<void> _pick() async {
    final d = await showDatePicker(
      context: context,
      initialDate: _at,
      firstDate: DateTime(2020),
      lastDate: DateTime(2100),
    );
    if (d == null || !mounted) return;
    final t = await showTimePicker(
      context: context,
      initialTime: TimeOfDay.fromDateTime(_at),
    );
    if (t == null || !mounted) return;
    setState(() => _at = DateTime(d.year, d.month, d.day, t.hour, t.minute));
  }

  Future<void> _run() async {
    setState(() {
      _running = true;
      _error = null;
    });
    try {
      final d = await ref
          .read(apiClientProvider)
          .simulateFlow(graph: ref.read(flowEditorProvider).flow, at: _at);
      if (!mounted) return;
      ref.read(flowEditorProvider.notifier).setSimulation(d);
    } on ApiException catch (e) {
      if (mounted) setState(() => _error = e.message);
    } finally {
      if (mounted) setState(() => _running = false);
    }
  }

  @override
  Widget build(BuildContext context) {
    final sim = ref.watch(flowEditorProvider.select((s) => s.simulation));
    final local = MaterialLocalizations.of(context);
    final danger = resolveAppColor(context, AppColors.danger);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      mainAxisSize: MainAxisSize.min,
      children: [
        Row(
          children: [
            const Icon(Icons.play_circle_outline, size: 18),
            const SizedBox(width: 8),
            Expanded(child: AppText.sectionTitle('Test this flow')),
            if (widget.onClose != null)
              IconButton(
                key: const ValueKey('flow-test-close'),
                tooltip: 'Close',
                visualDensity: VisualDensity.compact,
                icon: const Icon(Icons.close, size: 18),
                onPressed: widget.onClose,
              ),
          ],
        ),
        const SizedBox(height: 4),
        AppText.muted(
          'Walks the current graph, saved or not, with live quota.',
        ),
        const SizedBox(height: 12),
        OutlinedButton.icon(
          key: const ValueKey('flow-test-when'),
          onPressed: _pick,
          icon: const Icon(Icons.event, size: 18),
          label: Text(
            '${local.formatShortDate(_at)} · '
            '${local.formatTimeOfDay(TimeOfDay.fromDateTime(_at))}',
          ),
        ),
        const SizedBox(height: 10),
        FilledButton.icon(
          key: const ValueKey('flow-test-run'),
          onPressed: _running ? null : _run,
          icon: _running
              ? const SizedBox.square(
                  dimension: 16,
                  child: CircularProgressIndicator(strokeWidth: 2),
                )
              : const Icon(Icons.play_arrow, size: 18),
          label: const Text('Run'),
        ),
        if (_error != null) ...[
          const SizedBox(height: 8),
          Text(
            _error!,
            style: Theme.of(
              context,
            ).textTheme.bodySmall?.copyWith(color: danger),
          ),
        ],
        if (sim != null) ...[const SizedBox(height: 14), _Result(sim: sim)],
      ],
    );
  }
}

class _Result extends StatelessWidget {
  final FlowDecision sim;
  const _Result({required this.sim});

  @override
  Widget build(BuildContext context) {
    final ok = resolveAppColor(context, AppColors.success);
    final no = resolveAppColor(context, AppColors.danger);
    final muted = resolveAppColor(context, AppColors.textMuted);
    final text = Theme.of(context).textTheme;
    final headline = sim.candidates.isEmpty
        ? 'No agent would review: no path reaches an available agent.'
        : 'Reviews with ${sim.candidates.first}'
              '${sim.candidates.length > 1 ? ', then ${sim.candidates.skip(1).join(', ')}' : ''}';
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Text(
          headline,
          style: text.bodyMedium?.copyWith(
            fontWeight: FontWeight.w600,
            color: sim.candidates.isEmpty ? no : null,
          ),
        ),
        const SizedBox(height: 8),
        for (final s in sim.path)
          Padding(
            padding: const EdgeInsets.symmetric(vertical: 3),
            child: Row(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Icon(_icon(s), size: 16, color: _color(s, ok, no, muted)),
                const SizedBox(width: 8),
                Expanded(
                  child: Text.rich(
                    TextSpan(
                      children: [
                        TextSpan(
                          text: _title(s),
                          style: const TextStyle(fontWeight: FontWeight.w600),
                        ),
                        if (s.reasons.isNotEmpty)
                          TextSpan(
                            text: '  ${s.reasons.join('; ')}',
                            style: TextStyle(color: muted),
                          ),
                      ],
                    ),
                    style: text.bodySmall,
                  ),
                ),
              ],
            ),
          ),
      ],
    );
  }

  /// A condition's "no" is an ordinary branch, not a failure.
  static Color _color(FlowPathStep s, Color ok, Color no, Color muted) =>
      s.type == 'condition'
      ? ((s.holds ?? false) ? ok : muted)
      : (s.added ? ok : no);

  static IconData _icon(FlowPathStep s) => s.type == 'condition'
      ? ((s.holds ?? false)
            ? Icons.check_circle_outline
            : Icons.cancel_outlined)
      : (s.added ? Icons.person_add_alt_1_outlined : Icons.block);

  static String _title(FlowPathStep s) => s.type == 'condition'
      ? '${s.node}: ${(s.holds ?? false) ? 'yes' : 'no'}'
      : '${s.node} · ${s.agent}${s.added ? '' : ' (unavailable)'}';
}

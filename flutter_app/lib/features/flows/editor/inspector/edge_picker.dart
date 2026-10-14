import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/flows/editor/flow_editor_controller.dart';

// Not valid step ids (those start with a lowercase letter or digit), so they
// never clash with a step.
const _newAgent = '+agent';
const _newCondition = '+condition';

/// Where one exit of a step (or Start, [from] '') leads: any step it may
/// lead to without a loop, nothing, or a new agent or condition placed to
/// the right and selected.
class EdgePicker extends ConsumerWidget {
  final String from;
  final EdgePort port;
  final String label;
  final bool readOnly;

  const EdgePicker({
    super.key,
    required this.from,
    required this.port,
    required this.label,
    this.readOnly = false,
  });

  static String describe(FlowNode n) {
    if (n.isCondition) return '${n.id} · condition';
    return n.agent.isEmpty ? '${n.id} · agent' : '${n.id} · ${n.agent}';
  }

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final flow = ref.watch(flowEditorProvider.select((s) => s.flow));
    final ctl = ref.read(flowEditorProvider.notifier);
    final current = port == EdgePort.start
        ? flow.start
        : edgeTarget(flow.nodes[from], port);
    final targets = connectableTargets(flow, from, port);
    // The current target always shows, even one the graph check rejects.
    final ids = [
      ...targets,
      if (current.isNotEmpty && !targets.contains(current)) current,
    ];
    final style = Theme.of(context).textTheme.bodyMedium;
    return DropdownButtonFormField<String>(
      key: ValueKey(switch (port) {
        EdgePort.yes => 'inspector-edge-yes',
        EdgePort.no => 'inspector-edge-no',
        EdgePort.next => 'inspector-edge-next',
        EdgePort.start => 'inspector-edge-start',
      }),
      // `value` (not `initialValue`) so undo and redo show through.
      // ignore: deprecated_member_use
      value: current,
      isExpanded: true,
      style: style,
      decoration: InputDecoration(
        labelText: label,
        isDense: true,
        border: const OutlineInputBorder(),
      ),
      items: [
        DropdownMenuItem(
          value: '',
          child: Text(port == EdgePort.start ? '— none —' : '— end —'),
        ),
        for (final id in ids)
          DropdownMenuItem(
            value: id,
            child: Text(
              flow.nodes[id] == null ? id : describe(flow.nodes[id]!),
              overflow: TextOverflow.ellipsis,
            ),
          ),
        const DropdownMenuItem(value: _newAgent, child: Text('+ New agent')),
        const DropdownMenuItem(
          value: _newCondition,
          child: Text('+ New condition'),
        ),
      ],
      onChanged: readOnly
          ? null
          : (v) {
              switch (v) {
                case null:
                  return;
                case _newAgent:
                  ctl.addConnected(from, port, FlowNodeType.agent);
                case _newCondition:
                  ctl.addConnected(from, port, FlowNodeType.condition);
                default:
                  ctl.setEdge(from, port, v);
              }
            },
    );
  }
}

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/flows/editor/flow_editor_controller.dart';
import 'package:heimdallm/features/flows/editor/inspector/inspector_parts.dart';

/// Properties of the selected agent step.
class AgentInspector extends ConsumerWidget {
  final FlowNode node;
  final List<FlowAgent> agents;
  final List<FlowIssue> issues;

  const AgentInspector({
    super.key,
    required this.node,
    required this.agents,
    this.issues = const [],
  });

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final ctl = ref.read(flowEditorProvider.notifier);
    // An agent the daemon no longer lists still shows, so the field never
    // goes blank on a flow that names it.
    final ids = [
      for (final a in agents) a.id,
      if (node.agent.isNotEmpty && !agents.any((a) => a.id == node.agent))
        node.agent,
    ];
    final installed = {
      for (final a in agents)
        if (a.installed) a.id,
    };
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        InspectorHeader(
          icon: Icons.smart_toy_outlined,
          accent: Theme.of(context).colorScheme.primary,
          title: 'Agent step',
          id: node.id,
          description:
              'Adds this agent to the reviewers, then continues through its '
              'fallback when the agent is unavailable.',
        ),
        const InspectorLabel('Reviewer'),
        DropdownButtonFormField<String>(
          key: const ValueKey('inspector-agent'),
          // `value` (not `initialValue`) so undo and redo show through.
          // ignore: deprecated_member_use
          value: node.agent.isEmpty ? null : node.agent,
          isExpanded: true,
          style: Theme.of(context).textTheme.bodyMedium,
          decoration: const InputDecoration(
            labelText: 'Agent',
            hintText: 'Pick an agent',
            isDense: true,
            border: OutlineInputBorder(),
          ),
          items: [
            for (final id in ids)
              DropdownMenuItem(
                value: id,
                child: Text(
                  installed.contains(id) ? id : '$id — not installed',
                ),
              ),
          ],
          onChanged: (v) => ctl.updateNode(node.copyWith(agent: v ?? '')),
        ),
        InspectorIssues(issues: issues),
      ],
    );
  }
}

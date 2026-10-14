import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/flows/editor/flow_editor_controller.dart';
import 'package:heimdallm/features/flows/editor/inspector/edge_picker.dart';
import 'package:heimdallm/features/flows/editor/inspector/inspector_parts.dart';
import 'package:heimdallm/shared/design_system/color_resolver.dart';
import 'package:heimdallm/shared/design_system/components/components.dart';
import 'package:heimdallm/shared/design_system/tokens.dart';

/// Flow properties, shown when nothing is selected.
class FlowInspector extends ConsumerWidget {
  final String flowId;
  final List<String> usedBy;
  final bool readOnly;

  /// Flow-level problems the daemon reported (not pinned to a step).
  final List<FlowIssue> issues;

  /// The agents to offer as the global primary and fallback.
  final List<FlowAgent> agents;

  /// Set on the default flow: changes the global `primary` or `fallback`
  /// agent ('' for no fallback). The default graph is built from them.
  final void Function(String key, String agent)? onSetDefaultAgent;

  /// A change of the global agents is in flight: the dropdowns wait for it.
  final bool defaultAgentsBusy;

  const FlowInspector({
    super.key,
    required this.flowId,
    required this.usedBy,
    this.readOnly = false,
    this.issues = const [],
    this.agents = const [],
    this.onSetDefaultAgent,
    this.defaultAgentsBusy = false,
  });

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final state = ref.watch(flowEditorProvider);
    final muted = resolveAppColor(context, AppColors.textMuted);
    final warning = resolveAppColor(context, AppColors.warning);
    final small = Theme.of(context).textTheme.bodySmall;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        InspectorHeader(
          icon: Icons.alt_route,
          accent: Theme.of(context).colorScheme.secondary,
          title: 'Flow',
          id: flowId,
          description: readOnly
              ? 'Built from the primary and fallback agents below. Duplicate '
                    'it to make an editable copy.'
              : 'Select a step to edit it, or click empty canvas to come '
                    'back here.',
        ),
        const SizedBox(height: 16),
        SyncedTextField(
          key: const ValueKey('flow-name'),
          value: state.flow.name,
          enabled: !readOnly,
          maxLength: 120,
          label: 'Name',
          onChanged: (v) => ref.read(flowEditorProvider.notifier).rename(v),
        ),
        const SizedBox(height: 4),
        EdgePicker(
          from: '',
          port: EdgePort.start,
          label: 'Starts with',
          readOnly: readOnly,
        ),
        if (onSetDefaultAgent case final set?)
          ..._defaultAgents(context, state.flow, set, defaultAgentsBusy),
        const InspectorLabel('Used by'),
        if (usedBy.isEmpty)
          AppText.muted('Not selected anywhere yet.')
        else
          Wrap(
            spacing: 6,
            runSpacing: 6,
            children: [
              for (final u in usedBy)
                AppBadge(
                  label: u,
                  foreground: muted,
                  background: AppColors.surfaceRaised.resolve(context),
                  border: AppColors.border.resolve(context),
                  letterSpacing: 0,
                ),
            ],
          ),
        if (state.check.warnings.isNotEmpty) ...[
          const InspectorLabel('Warnings'),
          for (final w in state.check.warnings)
            Padding(
              padding: const EdgeInsets.symmetric(vertical: 2),
              child: Row(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Icon(Icons.warning_amber_rounded, size: 16, color: warning),
                  const SizedBox(width: 8),
                  Expanded(
                    child: Text(
                      w.node.isEmpty ? w.message : '${w.node}: ${w.message}',
                      style: small,
                    ),
                  ),
                ],
              ),
            ),
        ],
        InspectorIssues(
          issues: [
            ...state.check.errors.where((e) => e.node.isEmpty),
            ...issues,
          ],
        ),
      ],
    );
  }

  /// The primary and fallback dropdowns, read off the default graph: Start
  /// leads to the primary, whose fallback is the fallback agent.
  List<Widget> _defaultAgents(
    BuildContext context,
    ReviewFlow flow,
    void Function(String, String) set,
    bool busy,
  ) {
    final first = flow.nodes[flow.start];
    final primary = first?.isCondition == false ? first!.agent : '';
    final fallback = flow.nodes[first?.next ?? '']?.agent ?? '';
    final ids = [
      for (final a in agents)
        if (a.installed) a.id,
    ];
    InputDecoration deco(String label) => InputDecoration(
      labelText: label,
      isDense: true,
      border: const OutlineInputBorder(),
    );
    final style = Theme.of(context).textTheme.bodyMedium;
    return [
      const InspectorLabel('Agents'),
      DropdownButtonFormField<String>(
        key: const ValueKey('default-flow-primary'),
        // `value` so the field follows the graph reloaded after a change.
        // ignore: deprecated_member_use
        value: ids.contains(primary) ? primary : null,
        isExpanded: true,
        style: style,
        decoration: deco('Primary agent'),
        items: [
          for (final id in ids) DropdownMenuItem(value: id, child: Text(id)),
        ],
        onChanged: busy
            ? null
            : (v) {
                if (v != null && v != primary) set('primary', v);
              },
      ),
      const SizedBox(height: 12),
      DropdownButtonFormField<String>(
        key: const ValueKey('default-flow-fallback'),
        // ignore: deprecated_member_use
        value: ids.contains(fallback) ? fallback : 'none',
        isExpanded: true,
        style: style,
        decoration: deco('Fallback agent'),
        items: [
          const DropdownMenuItem(value: 'none', child: Text('none')),
          for (final id in ids) DropdownMenuItem(value: id, child: Text(id)),
        ],
        onChanged: busy
            ? null
            : (v) {
                final next = v == null || v == 'none' ? '' : v;
                if (next != fallback) set('fallback', next);
              },
      ),
    ];
  }
}

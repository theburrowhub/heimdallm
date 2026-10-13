import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/flows/editor/flow_editor_controller.dart';
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

  const FlowInspector({
    super.key,
    required this.flowId,
    required this.usedBy,
    this.readOnly = false,
    this.issues = const [],
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
              ? 'Built from the primary and fallback agents. Duplicate it to '
                    'make an editable copy.'
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
}

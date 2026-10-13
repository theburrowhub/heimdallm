import 'package:flutter/material.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/flows/editor/inspector/inspector_parts.dart';
import 'package:heimdallm/shared/design_system/color_resolver.dart';
import 'package:heimdallm/shared/design_system/components/components.dart';
import 'package:heimdallm/shared/design_system/tokens.dart';

/// The step types the operator can add, dragged onto the canvas or
/// double-clicked, plus a short guide to the canvas gestures.
class FlowPalette extends StatelessWidget {
  final void Function(FlowNodeType type) onAdd;
  const FlowPalette({super.key, required this.onAdd});

  Widget _row(
    BuildContext context,
    FlowNodeType type,
    IconData icon,
    String title,
    String subtitle,
  ) {
    final scheme = Theme.of(context).colorScheme;
    final accent = type == FlowNodeType.condition
        ? scheme.tertiary
        : scheme.primary;
    final muted = resolveAppColor(context, AppColors.textMuted);
    final tile = AppSurface(
      elevation: AppSurfaceElevation.raised,
      padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 10),
      child: Row(
        children: [
          Container(
            width: 30,
            height: 30,
            decoration: BoxDecoration(
              color: accent.withValues(alpha: .14),
              borderRadius: BorderRadius.circular(8),
            ),
            child: Icon(icon, size: 17, color: accent),
          ),
          const SizedBox(width: 10),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(
                  title,
                  style: Theme.of(
                    context,
                  ).textTheme.bodyMedium?.copyWith(fontWeight: FontWeight.w600),
                ),
                const SizedBox(height: 2),
                Text(
                  subtitle,
                  style: Theme.of(
                    context,
                  ).textTheme.bodySmall?.copyWith(color: muted),
                ),
              ],
            ),
          ),
          Icon(Icons.drag_indicator, size: 16, color: muted),
        ],
      ),
    );
    return Padding(
      padding: const EdgeInsets.only(bottom: 8),
      child: Draggable<FlowNodeType>(
        key: ValueKey('palette-${type.name}'),
        data: type,
        feedback: Material(
          color: Colors.transparent,
          child: SizedBox(width: 200, child: Opacity(opacity: .9, child: tile)),
        ),
        childWhenDragging: Opacity(opacity: .5, child: tile),
        child: MouseRegion(
          cursor: SystemMouseCursors.grab,
          child: GestureDetector(onDoubleTap: () => onAdd(type), child: tile),
        ),
      ),
    );
  }

  @override
  Widget build(BuildContext context) {
    final muted = resolveAppColor(context, AppColors.textMuted);
    final small = Theme.of(context).textTheme.bodySmall?.copyWith(color: muted);
    return ListView(
      padding: const EdgeInsets.fromLTRB(16, 0, 16, 16),
      children: [
        const InspectorLabel('Add step'),
        _row(
          context,
          FlowNodeType.condition,
          Icons.call_split,
          'Condition',
          'schedule or quota · yes / no',
        ),
        _row(
          context,
          FlowNodeType.agent,
          Icons.smart_toy_outlined,
          'Agent',
          'reviews · then fallback',
        ),
        const InspectorLabel('How it works'),
        for (final (icon, line) in const [
          (Icons.add, 'Double-click or drag a row above to add a step.'),
          (
            Icons.link,
            'Drag from a dot on a step\'s right edge to another step to connect them.',
          ),
          (
            Icons.call_split,
            'A condition continues through yes or no; an agent through its fallback.',
          ),
          (
            Icons.content_cut,
            'Hover a connection and click the scissors to cut it.',
          ),
          (
            Icons.delete_outline,
            'Select a step or a connection and press Delete.',
          ),
        ])
          Padding(
            padding: const EdgeInsets.only(bottom: 10),
            child: Row(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Icon(icon, size: 15, color: muted),
                const SizedBox(width: 8),
                Expanded(child: Text(line, style: small)),
              ],
            ),
          ),
      ],
    );
  }
}

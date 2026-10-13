import 'package:flutter/material.dart';
import 'package:heimdallm/core/models/flow.dart';
import 'package:heimdallm/features/flows/canvas/canvas_geometry.dart';
import 'package:heimdallm/features/flows/graph/auto_layout.dart';
import 'package:heimdallm/features/flows/graph/flow_diff.dart';
import 'package:heimdallm/shared/design_system/color_resolver.dart';
import 'package:heimdallm/shared/design_system/tokens.dart';

const kPortSize = 14.0;

/// Card fill for canvas items: white-ish in light mode, lifted in dark mode.
Color canvasCardColor(ColorScheme scheme) =>
    scheme.brightness == Brightness.dark
    ? scheme.surfaceContainerHigh
    : scheme.surfaceContainerLowest;

/// A step on the canvas. Ports are separate widgets placed by FlowCanvas.
/// Conditions (a branch) and agents (a reviewer) differ in accent colour,
/// icon and caption so the graph reads without opening the inspector.
class FlowNodeCard extends StatelessWidget {
  final FlowNode node;
  final bool selected;
  final bool hasError;
  final bool unreachable;
  final bool onPath;
  final DiffKind? diff;
  final bool ghost;

  const FlowNodeCard({
    super.key,
    required this.node,
    required this.selected,
    required this.hasError,
    required this.unreachable,
    required this.onPath,
    this.diff,
    this.ghost = false,
  });

  String get _title {
    if (node.isCondition) return 'Condition';
    return node.agent.isEmpty ? 'Agent' : node.agent;
  }

  String get _subtitle {
    if (!node.isCondition) {
      return node.agent.isEmpty ? 'Pick an agent' : 'Reviews, then falls back';
    }
    final parts = [
      ...node.schedule.map((s) => s.describe()),
      ...node.quota.map((q) => q.describe()),
    ];
    if (parts.isEmpty) return 'Add a condition';
    return parts.join(node.match == 'any' ? ' · or · ' : ' · and · ');
  }

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final scheme = theme.colorScheme;
    final text = theme.textTheme;
    final danger = resolveAppColor(context, AppColors.danger);
    final accent = node.isCondition ? scheme.tertiary : scheme.primary;
    final border = switch ((hasError, diff, selected, onPath)) {
      (true, _, _, _) => danger,
      (_, DiffKind.added, _, _) => resolveAppColor(context, AppColors.success),
      (_, DiffKind.changed, _, _) => resolveAppColor(
        context,
        AppColors.warning,
      ),
      (_, DiffKind.removed, _, _) => danger,
      (_, _, true, _) => scheme.primary,
      (_, _, _, true) => scheme.secondary,
      _ => scheme.outlineVariant,
    };
    final strong = selected || diff != null || hasError || onPath;
    return Opacity(
      opacity: ghost ? .35 : (unreachable ? .55 : 1),
      child: Container(
        width: kNodeSize.width,
        height: kNodeSize.height,
        decoration: BoxDecoration(
          color: canvasCardColor(scheme),
          borderRadius: BorderRadius.circular(10),
          border: Border.all(color: border, width: strong ? 2 : 1),
          boxShadow: [
            BoxShadow(
              color: (selected ? scheme.primary : scheme.shadow).withValues(
                alpha: selected ? .25 : .08,
              ),
              blurRadius: selected ? 12 : 6,
              offset: const Offset(0, 2),
            ),
          ],
        ),
        child: Row(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Container(
              width: 44,
              decoration: BoxDecoration(
                color: accent.withValues(alpha: .14),
                borderRadius: const BorderRadius.horizontal(
                  left: Radius.circular(9),
                ),
              ),
              child: Icon(
                node.isCondition ? Icons.call_split : Icons.smart_toy_outlined,
                color: accent,
                size: 20,
              ),
            ),
            Expanded(
              child: Padding(
                padding: const EdgeInsets.fromLTRB(10, 6, 12, 6),
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  mainAxisAlignment: MainAxisAlignment.center,
                  children: [
                    Row(
                      children: [
                        Flexible(
                          child: Text(
                            _title,
                            style: text.titleSmall?.copyWith(
                              fontWeight: FontWeight.w600,
                            ),
                            maxLines: 1,
                            overflow: TextOverflow.ellipsis,
                          ),
                        ),
                        if (hasError) ...[
                          const SizedBox(width: 4),
                          Icon(Icons.error_outline, size: 14, color: danger),
                        ],
                        if (unreachable) ...[
                          const SizedBox(width: 4),
                          Tooltip(
                            message: 'Not reachable from Start',
                            child: Icon(
                              Icons.link_off,
                              size: 14,
                              color: scheme.onSurfaceVariant,
                            ),
                          ),
                        ],
                      ],
                    ),
                    const SizedBox(height: 2),
                    Text(
                      _subtitle,
                      style: text.bodySmall?.copyWith(
                        color: scheme.onSurfaceVariant,
                      ),
                      maxLines: 2,
                      overflow: TextOverflow.ellipsis,
                    ),
                  ],
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }
}

/// The fixed entry point of every flow.
class StartNodeCard extends StatelessWidget {
  const StartNodeCard({super.key});

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    return Container(
      width: kStartSize.width,
      height: kStartSize.height,
      alignment: Alignment.center,
      decoration: BoxDecoration(
        color: scheme.primaryContainer,
        borderRadius: BorderRadius.circular(kStartSize.height / 2),
        border: Border.all(color: scheme.primary.withValues(alpha: .4)),
      ),
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          Icon(Icons.play_arrow_rounded, size: 18, color: scheme.primary),
          const SizedBox(width: 2),
          Text(
            'Start',
            style: TextStyle(
              color: scheme.onPrimaryContainer,
              fontWeight: FontWeight.w600,
            ),
          ),
        ],
      ),
    );
  }
}

/// A connection point; filled when something is connected to it.
class PortDot extends StatelessWidget {
  final Color color;
  final bool filled;
  const PortDot({super.key, required this.color, this.filled = false});

  @override
  Widget build(BuildContext context) => Container(
    width: kPortSize,
    height: kPortSize,
    decoration: BoxDecoration(
      color: filled ? color : canvasCardColor(Theme.of(context).colorScheme),
      shape: BoxShape.circle,
      border: Border.all(color: color, width: 2),
    ),
  );
}

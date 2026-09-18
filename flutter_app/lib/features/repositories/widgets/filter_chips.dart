import 'package:flutter/material.dart';
import 'package:mix/mix.dart';

import '../../../shared/design_system/components/components.dart';
import '../../../shared/design_system/tokens.dart';

class RepoFilterChips extends StatelessWidget {
  /// Key set: 'all' | 'monitored' | 'not_monitored'.
  final Map<String, int> counts;
  final String current;
  final ValueChanged<String> onChanged;

  const RepoFilterChips({
    super.key,
    required this.counts,
    required this.current,
    required this.onChanged,
  });

  static const _labels = {
    'all': 'All',
    'monitored': 'Monitored',
    'not_monitored': 'Not monitored',
  };

  @override
  Widget build(BuildContext context) {
    final primary = Theme.of(context).colorScheme.primary;
    final muted = Theme.of(context).colorScheme.onSurfaceVariant;
    final raised = Theme.of(context).colorScheme.surfaceContainerHighest;
    return Box(
      style: BoxStyler()
          .color(AppColors.surface())
          .borderAll(color: AppColors.border(), width: 1)
          .borderRadiusAll(AppRadius.sm())
          .clipBehavior(Clip.antiAlias),
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          for (final e in _labels.entries) ...[
            InkWell(
              onTap: () => onChanged(e.key),
              child: Box(
                style: BoxStyler()
                    .color(
                      current == e.key
                          ? primary.withValues(alpha: 0.22)
                          : Colors.transparent,
                    )
                    .padding(
                      EdgeInsetsGeometryMix.value(
                        const EdgeInsets.symmetric(horizontal: 12, vertical: 7),
                      ),
                    ),
                child: Row(
                  children: [
                    AppText(
                      e.value,
                      role: AppTextRole.label,
                      color: current == e.key ? primary : null,
                    ),
                    const SizedBox(width: 6),
                    AppBadge(
                      label: '${counts[e.key] ?? 0}',
                      foreground: current == e.key ? primary : muted,
                      background: current == e.key
                          ? primary.withValues(alpha: 0.18)
                          : raised,
                      padding: const EdgeInsets.symmetric(
                        horizontal: 6,
                        vertical: 1,
                      ),
                      radius: 10,
                      fontSize: 10,
                      letterSpacing: 0,
                    ),
                  ],
                ),
              ),
            ),
            if (e.key != 'not_monitored')
              Box(
                style: BoxStyler()
                    .width(1)
                    .height(28)
                    .color(AppColors.border()),
              ),
          ],
        ],
      ),
    );
  }
}

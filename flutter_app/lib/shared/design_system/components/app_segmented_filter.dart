import 'package:flutter/material.dart';
import 'package:mix/mix.dart';

import '../tokens.dart';
import 'app_badge.dart';
import 'app_text.dart';

/// One option in an [AppSegmentedFilter].
class AppSegment<T> {
  final T value;
  final String label;
  final int? count;

  const AppSegment({required this.value, required this.label, this.count});
}

/// A bordered, connected segmented control with an optional count badge per
/// segment.
///
/// Generalizes `RepoFilterChips` (`repositories/widgets/filter_chips.dart`),
/// which hardcoded its three all/monitored/not_monitored labels in a
/// `static const _labels` map, so any screen can build one from a list of
/// [AppSegment]s. Also replaces the two Material `SegmentedButton`s in
/// `routing_screen.dart` and `instance_dialog.dart`.
class AppSegmentedFilter<T> extends StatelessWidget {
  final List<AppSegment<T>> segments;
  final T current;
  final ValueChanged<T> onChanged;

  const AppSegmentedFilter({
    super.key,
    required this.segments,
    required this.current,
    required this.onChanged,
  });

  @override
  Widget build(BuildContext context) {
    final primary = AppColors.accent.resolve(context);
    final muted = AppColors.textMuted.resolve(context);
    final raised = AppColors.surfaceRaised.resolve(context);

    return Box(
      style: BoxStyler()
          .color(AppColors.surface())
          .borderAll(color: AppColors.border(), width: 1)
          .borderRadiusAll(AppRadius.sm())
          .clipBehavior(Clip.antiAlias),
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          for (var i = 0; i < segments.length; i++) ...[
            _segment(context, segments[i], primary, muted, raised),
            if (i != segments.length - 1)
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

  Widget _segment(
    BuildContext context,
    AppSegment<T> segment,
    Color primary,
    Color muted,
    Color raised,
  ) {
    final selected = segment.value == current;
    // Same selection-semantics gap as AppFilterChip, and the same two
    // fixes: `selected`/`button` explicit (no `label` — the descendant
    // AppText/AppBadge already contribute one, avoiding a duplicated
    // "All\nAll"), and `onTap` repeated here because
    // `excludeFromSemantics` on the InkWell below removes its own
    // SemanticsAction.tap along with its default semantics.
    return Semantics(
      button: true,
      selected: selected,
      onTap: () => onChanged(segment.value),
      child: InkWell(
        onTap: () => onChanged(segment.value),
        excludeFromSemantics: true,
        child: Box(
          style: BoxStyler()
              .color(
                selected ? primary.withValues(alpha: 0.22) : Colors.transparent,
              )
              .padding(
                EdgeInsetsGeometryMix.value(
                  const EdgeInsets.symmetric(horizontal: 12, vertical: 7),
                ),
              ),
          child: Row(
            children: [
              AppText(
                segment.label,
                role: AppTextRole.label,
                color: selected ? primary : null,
              ),
              if (segment.count != null) ...[
                const SizedBox(width: 6),
                AppBadge(
                  label: '${segment.count}',
                  foreground: selected ? primary : muted,
                  background: selected
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
            ],
          ),
        ),
      ),
    );
  }
}

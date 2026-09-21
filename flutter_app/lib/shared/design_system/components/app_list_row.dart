import 'package:flutter/material.dart';
import 'package:mix/mix.dart';

import '../tokens.dart';

/// The single row shape for every list of PRs/issues/repos/instances/
/// prompts/events in the app.
///
/// Reproduces Activity's current row look (`dashboard_screen.dart`'s
/// `_PRTile`/`_IssueActivityTile`) through design-system tokens, so Merge,
/// Repositories, Organizations, Instances, Prompts and the server Events
/// tab render list rows with the exact same background, border, radius and
/// accent bar instead of four visibly different implementations.
///
/// [trailing] is always pinned to the row's right edge via a *tight*
/// [Flexible] wrapped in [Align] — not the `Flexible(fit: FlexFit.loose)`
/// the original Activity rows used. A loose [Flexible] shares flex 1 with
/// the title's `Expanded`, but shrinks to its own content instead of
/// consuming its share of the row, leaving a dead gap between the trailing
/// content and the row's edge that `WrapAlignment.end` could never close.
/// `FlexFit.tight` forces the trailing slot to claim its full share of the
/// row, and the inner [Align]/[Wrap] center that content against the real
/// right edge while still reflowing onto a second line at narrow widths.
class AppListRow extends StatelessWidget {
  final Widget title;
  final Widget? subtitle;
  final Color? accentColor;
  final double accentHeight;
  final List<Widget> leading;
  final List<Widget> trailing;
  final VoidCallback? onTap;
  final bool selected;
  final bool dimmed;
  final Widget? footer;

  const AppListRow({
    super.key,
    required this.title,
    this.subtitle,
    this.accentColor,
    this.accentHeight = 48,
    this.leading = const [],
    this.trailing = const [],
    this.onTap,
    this.selected = false,
    this.dimmed = false,
    this.footer,
  });

  @override
  Widget build(BuildContext context) {
    final accent = AppColors.accent.resolve(context);
    final radius = BorderRadius.all(AppRadius.lg.resolve(context));

    final style = BoxStyler()
        .color(selected ? accent.withValues(alpha: 0.12) : AppColors.surface())
        .borderAll(
          color: selected ? accent.withValues(alpha: 0.55) : AppColors.border(),
          width: 1,
        )
        .borderRadiusAll(AppRadius.lg())
        .clipBehavior(Clip.antiAlias);

    final row = Row(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        if (accentColor != null)
          Container(
            width: 4,
            height: accentHeight,
            margin: const EdgeInsets.only(right: 12),
            decoration: BoxDecoration(
              color: accentColor,
              borderRadius: BorderRadius.circular(2),
            ),
          ),
        if (leading.isNotEmpty)
          Padding(
            padding: const EdgeInsets.only(right: 6),
            child: Row(
              mainAxisSize: MainAxisSize.min,
              children: [
                for (var i = 0; i < leading.length; i++) ...[
                  leading[i],
                  if (i != leading.length - 1) const SizedBox(width: 4),
                ],
              ],
            ),
          ),
        Expanded(
          flex: 3,
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              title,
              if (subtitle != null) ...[const SizedBox(height: 4), subtitle!],
            ],
          ),
        ),
        if (trailing.isNotEmpty) ...[
          const SizedBox(width: 12),
          Flexible(
            flex: 1,
            fit: FlexFit.tight,
            child: Align(
              alignment: Alignment.centerRight,
              child: Wrap(
                alignment: WrapAlignment.end,
                crossAxisAlignment: WrapCrossAlignment.center,
                spacing: 8,
                runSpacing: 4,
                children: trailing,
              ),
            ),
          ),
        ],
      ],
    );

    final content = Padding(
      padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 12),
      child: footer == null
          ? row
          : Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [row, footer!],
            ),
    );

    return Opacity(
      opacity: dimmed ? 0.6 : 1.0,
      child: Padding(
        padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 3),
        child: Box(
          style: style,
          child: Material(
            type: MaterialType.transparency,
            child: InkWell(borderRadius: radius, onTap: onTap, child: content),
          ),
        ),
      ),
    );
  }
}

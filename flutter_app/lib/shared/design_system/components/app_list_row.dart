import 'dart:math' as math;

import 'package:flutter/material.dart';
import 'package:mix/mix.dart';

import '../tokens.dart';

/// Generous but bounded cap on the trailing zone's width — wide enough for
/// a badge, a button and an icon on one line (roughly 210px in practice) at
/// realistic desktop row widths, narrow enough that pathologically long
/// trailing content reflows onto a second line via [Wrap] instead of
/// squeezing the title arbitrarily thin. Also capped to a fraction of the
/// row's own available width (see [_trailingMaxWidthFraction]) so trailing
/// and leading together can never claim more than the row actually has —
/// a fixed cap alone still let the *sum* of leading + a single-line
/// trailing exceed the row's width at narrow widths (confirmed at 375px:
/// two leading badges + a full single-line trailing group overflowed the
/// row itself, not just the trailing zone).
const _trailingMaxWidth = 260.0;
const _trailingMaxWidthFraction = 0.5;

/// The single row shape for every list of PRs/issues/repos/instances/
/// prompts/events in the app.
///
/// Reproduces Activity's current row look (`dashboard_screen.dart`'s
/// `_PRTile`/`_IssueActivityTile`) through design-system tokens, so Merge,
/// Repositories, Organizations, Instances, Prompts and the server Events
/// tab render list rows with the exact same background, border, radius and
/// accent bar instead of four visibly different implementations.
///
/// [trailing] is always pinned to the row's right edge, and is never
/// starved of the width it needs to render without overflowing — unlike
/// two earlier approaches this component went through:
///
/// - The original Activity rows used `Flexible(fit: FlexFit.loose)` for
///   trailing, sharing flex with the title's `Expanded`. A loose `Flexible`
///   shrinks to its own content instead of claiming its share of the row,
///   leaving a dead gap between the trailing content and the row's edge
///   that `WrapAlignment.end` could never close.
/// - A first fix gave trailing `Flexible(fit: FlexFit.tight)` at a fixed
///   3:1 title:trailing ratio, which closed that gap but reintroduced a
///   different bug at narrow widths: `Wrap` reflows *between* its
///   children, never shrinks a single child below its own intrinsic size,
///   so a lone badge wider than the ratio's allotted share still overflows
///   (confirmed at 375px with a real `SeverityBadge` + button + dismiss
///   icon — `RenderFlex overflowed by 35 pixels`).
///
/// Instead, [trailing] is laid out as the Row's last, non-flex child
/// (`ConstrainedBox(maxWidth: _trailingMaxWidth)` around the `Wrap`), and
/// title is the row's *only* flexible child (`Expanded`). Flutter's
/// `RenderFlex` always sizes non-flexible children first at their natural
/// width, then gives flexible children whatever's left — so trailing
/// always gets the width it actually needs (reflowing onto a second line,
/// not overflowing, once it exceeds the cap), title always yields
/// whatever's left over instead of a fixed share, and — because trailing
/// is the last child and consumes exactly its own width — its right edge
/// lands exactly on the row's right edge with no `Align` needed.
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

    final content = Padding(
      padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 12),
      child: LayoutBuilder(
        builder: (context, constraints) {
          // Bound trailing to a fraction of the row's own available width —
          // not just a fixed cap — so leading + a single-line trailing can
          // never together exceed the row itself; below that fraction's
          // worth of content, Wrap reflows trailing onto more lines instead
          // of overflowing.
          final trailingMax = math.min(
            _trailingMaxWidth,
            constraints.maxWidth * _trailingMaxWidthFraction,
          );

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
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    title,
                    if (subtitle != null) ...[
                      const SizedBox(height: 4),
                      subtitle!,
                    ],
                  ],
                ),
              ),
              if (trailing.isNotEmpty) ...[
                const SizedBox(width: 12),
                ConstrainedBox(
                  constraints: BoxConstraints(maxWidth: trailingMax),
                  child: Wrap(
                    alignment: WrapAlignment.end,
                    crossAxisAlignment: WrapCrossAlignment.center,
                    spacing: 8,
                    runSpacing: 4,
                    children: trailing,
                  ),
                ),
              ],
            ],
          );

          return footer == null
              ? row
              : Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [row, footer!],
                );
        },
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

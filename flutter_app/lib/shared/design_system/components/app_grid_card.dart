import 'package:flutter/material.dart';
import 'package:mix/mix.dart';

import '../tokens.dart';

/// The single tile shape for every grid/mosaic view in the app.
///
/// Unifies `_ActivityGridTile` (`dashboard_screen.dart`, a Material `Card`)
/// and `RepoGridTile` (`repositories/widgets/repo_grid_tile.dart`, a
/// bordered `Box`) — two different container styles for the same kind of
/// tile — onto one shape shared with [AppListRow]'s border/radius/selection
/// treatment.
class AppGridCard extends StatelessWidget {
  final Widget? header;
  final Widget title;
  final Widget? subtitle;
  final Widget? footer;
  final VoidCallback? onTap;
  final bool selected;
  final bool dimmed;

  const AppGridCard({
    super.key,
    this.header,
    required this.title,
    this.subtitle,
    this.footer,
    this.onTap,
    this.selected = false,
    this.dimmed = false,
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

    return Opacity(
      opacity: dimmed ? 0.6 : 1.0,
      child: Box(
        style: style,
        child: Material(
          type: MaterialType.transparency,
          child: InkWell(
            borderRadius: radius,
            onTap: onTap,
            child: Padding(
              padding: const EdgeInsets.all(12),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  if (header != null) ...[header!, const SizedBox(height: 6)],
                  title,
                  if (subtitle != null) ...[
                    const SizedBox(height: 4),
                    subtitle!,
                  ],
                  if (footer != null) ...[const Spacer(), footer!],
                ],
              ),
            ),
          ),
        ),
      ),
    );
  }
}

/// Shared grid-layout metrics so every mosaic view uses the same tile size.
class AppGridDelegate {
  AppGridDelegate._();

  static SliverGridDelegate entities({
    double maxExtent = 300,
    double aspectRatio = 1.6,
    double spacing = 8,
  }) => SliverGridDelegateWithMaxCrossAxisExtent(
    maxCrossAxisExtent: maxExtent,
    childAspectRatio: aspectRatio,
    crossAxisSpacing: spacing,
    mainAxisSpacing: spacing,
  );
}

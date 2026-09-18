import 'package:flutter/widgets.dart';
import 'package:mix/mix.dart';

import '../tokens.dart';

/// Elevation levels for [AppSurface], mirroring the canvas/surface/raised
/// scale in `tokens.dart`.
enum AppSurfaceElevation { canvas, surface, raised }

/// A themed container built on Mix's [Box].
///
/// Use instead of a bare [Container]/[Card] so every panel/card in the
/// redesigned UI shares the same background, border and radius tokens.
class AppSurface extends StatelessWidget {
  final Widget child;
  final AppSurfaceElevation elevation;
  final EdgeInsetsGeometry? padding;
  final bool bordered;
  final RadiusToken radius;

  const AppSurface({
    super.key,
    required this.child,
    this.elevation = AppSurfaceElevation.surface,
    this.padding,
    this.bordered = true,
    this.radius = AppRadius.md,
  });

  ColorToken get _backgroundToken => switch (elevation) {
    AppSurfaceElevation.canvas => AppColors.canvas,
    AppSurfaceElevation.surface => AppColors.surface,
    AppSurfaceElevation.raised => AppColors.surfaceRaised,
  };

  @override
  Widget build(BuildContext context) {
    var style = BoxStyler()
        .color(_backgroundToken())
        .borderRadiusAll(radius())
        .clipBehavior(Clip.antiAlias);

    if (bordered) {
      style = style.borderAll(color: AppColors.border(), width: 1);
    }

    if (padding != null) {
      style = style.padding(EdgeInsetsGeometryMix.value(padding!));
    }

    return Box(style: style, child: child);
  }
}

import 'package:flutter/widgets.dart';

/// Lays [children] out in as many equal-width columns as fit within
/// [minFieldWidth] each (capped at 3), instead of stretching every short
/// field (poll interval, retention days, timeouts) to the full page width.
///
/// This is what lets a form section drop a `ConstrainedBox(maxWidth: ...)`
/// and still look intentional at full window width — the field grid, not
/// the page, is what keeps individual inputs a sane size.
class AppFieldGrid extends StatelessWidget {
  final List<Widget> children;
  final double minFieldWidth;
  final double spacing;

  const AppFieldGrid({
    super.key,
    required this.children,
    this.minFieldWidth = 280,
    this.spacing = 16,
  });

  @override
  Widget build(BuildContext context) {
    if (children.isEmpty) return const SizedBox.shrink();

    return LayoutBuilder(
      builder: (context, constraints) {
        final maxWidth = constraints.maxWidth;
        final columns = (maxWidth / minFieldWidth)
            .floor()
            .clamp(1, 3)
            .clamp(1, children.length);
        final fieldWidth = (maxWidth - spacing * (columns - 1)) / columns;

        return Wrap(
          spacing: spacing,
          runSpacing: spacing,
          children: [
            for (final child in children)
              SizedBox(width: fieldWidth, child: child),
          ],
        );
      },
    );
  }
}

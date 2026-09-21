import 'package:flutter/material.dart';

import '../color_resolver.dart';
import '../tokens.dart';

/// A small, themed icon button used in toolbars and list-row trailing
/// content.
///
/// Deliberately built on Material's [IconButton] rather than Mix's
/// [PressableBox]: this button's icon and [selected] state routinely change
/// between rebuilds (view-mode toggles, expand/collapse controls), and a
/// [PressableBox] whose child shape changes across rebuilds has previously
/// crashed with negative-width `BoxConstraints` elsewhere in the app — see
/// the design-system README's note on why `toast.dart` and the update/error
/// screen buttons stay on raw Flutter for the same reason.
class AppIconButton extends StatelessWidget {
  final IconData icon;
  final VoidCallback? onPressed;
  final String? tooltip;
  final bool selected;
  final double size;

  const AppIconButton({
    super.key,
    required this.icon,
    required this.onPressed,
    this.tooltip,
    this.selected = false,
    this.size = 18,
  });

  @override
  Widget build(BuildContext context) {
    final color = selected
        ? resolveAppColor(context, AppColors.accent)
        : resolveAppColor(context, AppColors.textMuted);

    return IconButton(
      icon: Icon(icon, size: size),
      color: color,
      tooltip: tooltip,
      visualDensity: VisualDensity.compact,
      onPressed: onPressed,
    );
  }
}

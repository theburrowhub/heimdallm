import 'package:flutter/material.dart';

import '../color_resolver.dart';
import '../tokens.dart';
import 'app_icon_button.dart';

/// The display mode a listing screen renders its items in. [board] is offered
/// only where a toggle sets `showBoard` (Activity).
enum AppViewMode { list, grid, board }

/// The list/grid toggle shared by every screen that offers both view modes.
///
/// Unifies the two `IconButton`s in `activity_filter_bar.dart` and the two
/// `_ViewToggleButton`s in `repos_screen.dart`. [listKey]/[gridKey] let
/// callers keep stable widget keys (`repos_view_toggle_list` /
/// `repos_view_toggle_grid`) that existing tests already look up.
class AppViewToggle extends StatelessWidget {
  final AppViewMode mode;
  final ValueChanged<AppViewMode> onChanged;
  final Key? listKey;
  final Key? gridKey;

  /// Adds a board button first.
  final bool showBoard;

  const AppViewToggle({
    super.key,
    required this.mode,
    required this.onChanged,
    this.listKey,
    this.gridKey,
    this.showBoard = false,
  });

  @override
  Widget build(BuildContext context) {
    // A segmented pill (#928): the views sit in a grey track and the current
    // one is a raised thumb.
    final pill = BorderRadius.all(AppRadius.pill.resolve(context));
    final thumb = BoxDecoration(
      color: resolveAppColor(context, AppColors.surfaceRaised),
      borderRadius: pill,
      boxShadow: [
        BoxShadow(
          color: Theme.of(context).colorScheme.shadow.withValues(alpha: 0.14),
          blurRadius: 3,
          offset: const Offset(0, 1),
        ),
      ],
    );
    Widget segment(AppViewMode value, Widget button) => DecoratedBox(
      decoration: mode == value ? thumb : const BoxDecoration(),
      child: button,
    );
    return DecoratedBox(
      decoration: BoxDecoration(
        color: resolveAppColor(context, AppColors.sidebar),
        borderRadius: pill,
      ),
      child: Padding(
        padding: const EdgeInsets.all(3),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            if (showBoard)
              segment(
                AppViewMode.board,
                AppIconButton(
                  icon: Icons.view_kanban_outlined,
                  selected: mode == AppViewMode.board,
                  tooltip: 'Board view',
                  onPressed: () => onChanged(AppViewMode.board),
                ),
              ),
            segment(
              AppViewMode.list,
              AppIconButton(
                key: listKey,
                icon: Icons.view_list,
                selected: mode == AppViewMode.list,
                tooltip: 'List view',
                onPressed: () => onChanged(AppViewMode.list),
              ),
            ),
            segment(
              AppViewMode.grid,
              AppIconButton(
                key: gridKey,
                icon: Icons.grid_view,
                selected: mode == AppViewMode.grid,
                tooltip: 'Grid view',
                onPressed: () => onChanged(AppViewMode.grid),
              ),
            ),
          ],
        ),
      ),
    );
  }
}

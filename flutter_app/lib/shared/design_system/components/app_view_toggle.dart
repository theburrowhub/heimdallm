import 'package:flutter/material.dart';

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
    return Row(
      mainAxisSize: MainAxisSize.min,
      children: [
        if (showBoard)
          AppIconButton(
            icon: Icons.view_kanban_outlined,
            selected: mode == AppViewMode.board,
            tooltip: 'Board view',
            onPressed: () => onChanged(AppViewMode.board),
          ),
        AppIconButton(
          key: listKey,
          icon: Icons.view_list,
          selected: mode == AppViewMode.list,
          tooltip: 'List view',
          onPressed: () => onChanged(AppViewMode.list),
        ),
        AppIconButton(
          key: gridKey,
          icon: Icons.grid_view,
          selected: mode == AppViewMode.grid,
          tooltip: 'Grid view',
          onPressed: () => onChanged(AppViewMode.grid),
        ),
      ],
    );
  }
}

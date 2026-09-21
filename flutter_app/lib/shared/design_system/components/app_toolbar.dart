import 'package:flutter/widgets.dart';

/// A single, themed filter/action bar shared by every screen.
///
/// Matches Activity's current toolbar exactly — no decorated container, a
/// [Wrap] of [leading]/[filters] controls on the left, and [trailing]
/// (typically a view toggle and/or a primary action button) pinned to the
/// right — since Activity is the app's reference toolbar style. The [Wrap]
/// lets the whole toolbar reflow onto more lines at narrow widths instead
/// of overflowing.
///
/// Replaces six ad-hoc toolbar implementations: `activity_filter_bar.dart`,
/// `stats_filter_bar.dart`, `events_tab.dart`'s `_Toolbar`,
/// `repos_screen.dart`'s inline toolbar, `merge_tracking_screen.dart`'s
/// `_TrackPRBar`, and `activity_screen.dart`'s `_DatePickerBar` — including
/// the boxed `AppSurface` some of those had, dropped so every toolbar in
/// the app looks the same.
///
/// [rowKey] lands on the [Row] that holds both the [Wrap] and [trailing] —
/// not on the outer [Padding] — so a caller/test measuring the toolbar's
/// geometry (e.g. to assert the trailing button sits flush against the
/// toolbar's own right edge) gets the row that actually spans that width.
class AppToolbar extends StatelessWidget {
  final List<Widget> leading;
  final List<Widget> filters;
  final List<Widget> trailing;
  final Key? rowKey;
  final EdgeInsetsGeometry padding;

  const AppToolbar({
    super.key,
    this.leading = const [],
    this.filters = const [],
    this.trailing = const [],
    this.rowKey,
    this.padding = const EdgeInsets.fromLTRB(16, 8, 16, 8),
  });

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: padding,
      child: Row(
        key: rowKey,
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Expanded(
            child: Wrap(
              spacing: 6,
              runSpacing: 6,
              crossAxisAlignment: WrapCrossAlignment.center,
              children: [...leading, ...filters],
            ),
          ),
          if (trailing.isNotEmpty) ...[
            const SizedBox(width: 12),
            Row(mainAxisSize: MainAxisSize.min, children: trailing),
          ],
        ],
      ),
    );
  }
}

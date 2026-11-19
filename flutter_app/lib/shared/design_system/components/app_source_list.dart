import 'package:flutter/material.dart';
import 'package:mix/mix.dart' show ColorToken;

import '../color_resolver.dart';
import '../tokens.dart';

/// An uppercase section label in a source-list sidebar (#917).
class AppSourceListSection extends StatelessWidget {
  final String text;
  const AppSourceListSection(this.text, {super.key});

  @override
  Widget build(BuildContext context) => Padding(
    padding: const EdgeInsets.fromLTRB(10, 0, 10, 6),
    child: Text(
      text.toUpperCase(),
      style: Theme.of(context).textTheme.labelSmall?.copyWith(
        color: resolveAppColor(context, AppColors.textMuted),
        fontWeight: FontWeight.w600,
        letterSpacing: 0.6,
      ),
    ),
  );
}

/// One entry of a source-list sidebar: an icon, a label and an optional count,
/// highlighted when [selected]. Shared by the app navigation and the Activity
/// inbox so both read as one sidebar style (#917).
///
/// [compact] draws the icon alone, for a sidebar collapsed to icons: the label
/// goes to the tooltip and a [badge] count sits over the icon.
class AppSourceListItem extends StatelessWidget {
  final IconData icon;
  final String label;

  /// Hover text; defaults to [label] when [compact].
  final String? tooltip;

  /// Shown on the right, or over the icon when [compact] (badged counts only).
  /// Null shows nothing.
  final int? count;

  /// Shows [count] as a badge in this colour: something waits on the
  /// operator. Null shows it as a plain number.
  final ColorToken? badge;
  final bool selected;
  final bool compact;
  final VoidCallback onTap;

  const AppSourceListItem({
    super.key,
    required this.icon,
    required this.label,
    this.tooltip,
    this.count,
    this.badge,
    required this.selected,
    this.compact = false,
    required this.onTap,
  });

  @override
  Widget build(BuildContext context) {
    final accent = resolveAppColor(context, AppColors.accent);
    final muted = resolveAppColor(context, AppColors.textMuted);
    final fill = badge == null ? null : resolveAppColor(context, badge!);
    // Dark mode's status colours are light: tint the badge there, as
    // SeverityBadge does, instead of white text on them.
    final dark = Theme.of(context).brightness == Brightness.dark;
    final text = Theme.of(context).textTheme.bodyMedium;
    final iconWidget = Icon(icon, size: 17, color: selected ? accent : muted);

    final Widget body;
    if (compact) {
      body = Padding(
        padding: const EdgeInsets.symmetric(vertical: 9),
        child: Center(
          child: fill != null && (count ?? 0) > 0
              ? Badge.count(
                  count: count!,
                  backgroundColor: fill,
                  child: iconWidget,
                )
              : iconWidget,
        ),
      );
    } else {
      body = Padding(
        padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 7),
        child: Row(
          children: [
            iconWidget,
            const SizedBox(width: 10),
            Expanded(
              child: Text(
                label,
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
                style: text?.copyWith(
                  fontWeight: selected ? FontWeight.w600 : null,
                  color: selected ? accent : null,
                ),
              ),
            ),
            if (count != null && fill != null)
              Container(
                padding: const EdgeInsets.symmetric(horizontal: 7, vertical: 1),
                decoration: BoxDecoration(
                  color: dark ? fill.withValues(alpha: 0.2) : fill,
                  borderRadius: BorderRadius.circular(10),
                ),
                child: Text(
                  '$count',
                  style: text?.copyWith(
                    color: dark ? fill : Colors.white,
                    fontSize: 11,
                    fontWeight: FontWeight.w700,
                  ),
                ),
              )
            else if (count != null)
              Text(
                '$count',
                style: text?.copyWith(
                  color: muted,
                  fontSize: 12,
                  fontFeatures: const [FontFeature.tabularFigures()],
                ),
              ),
          ],
        ),
      );
    }

    final item = Semantics(
      button: true,
      selected: selected,
      label: compact ? label : null,
      child: Material(
        color: selected ? accent.withValues(alpha: 0.14) : Colors.transparent,
        borderRadius: BorderRadius.circular(7),
        child: InkWell(
          borderRadius: BorderRadius.circular(7),
          onTap: onTap,
          child: body,
        ),
      ),
    );
    final hover = tooltip ?? (compact ? label : null);
    return Padding(
      padding: const EdgeInsets.only(bottom: 2),
      child: hover == null ? item : Tooltip(message: hover, child: item),
    );
  }
}

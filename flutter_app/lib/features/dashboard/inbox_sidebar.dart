import 'package:flutter/material.dart';

import '../../shared/design_system/color_resolver.dart';
import '../../shared/design_system/tokens.dart';
import 'inbox_buckets.dart';

/// The Activity screen's source list on wide windows (#910): the inbox buckets
/// with their counts, then the repositories in the list.
class InboxSidebar extends StatelessWidget {
  final InboxBucket bucket;
  final Map<InboxBucket, int> counts;
  final ValueChanged<InboxBucket> onBucket;

  /// Repositories with the number of PRs each has in the list, in display
  /// order.
  final List<(String, int)> repos;

  /// The one repository the list is narrowed to, if any.
  final String? repo;
  final ValueChanged<String> onRepo;

  static const double width = 232;

  const InboxSidebar({
    super.key,
    required this.bucket,
    required this.counts,
    required this.onBucket,
    required this.repos,
    required this.repo,
    required this.onRepo,
  });

  @override
  Widget build(BuildContext context) {
    final needsYou = counts[InboxBucket.needsYou] ?? 0;
    return SizedBox(
      width: width,
      child: ListView(
        key: const Key('inbox-sidebar'),
        padding: const EdgeInsets.fromLTRB(10, 14, 10, 20),
        children: [
          const _SectionLabel('Inbox'),
          for (final b in InboxBucket.values)
            _SidebarItem(
              key: Key('inbox-bucket-${b.name}'),
              icon: b.icon,
              label: b.label,
              count: counts[b] ?? 0,
              selected: b == bucket,
              alert: b == InboxBucket.needsYou && needsYou > 0,
              onTap: () => onBucket(b),
            ),
          if (repos.isNotEmpty) ...[
            const SizedBox(height: 18),
            const _SectionLabel('Repositories'),
            for (final (name, count) in repos)
              _SidebarItem(
                icon: Icons.folder_outlined,
                label: name.contains('/') ? name.split('/').last : name,
                tooltip: name,
                count: count,
                selected: name == repo,
                onTap: () => onRepo(name),
              ),
          ],
        ],
      ),
    );
  }
}

class _SectionLabel extends StatelessWidget {
  final String text;
  const _SectionLabel(this.text);

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

class _SidebarItem extends StatelessWidget {
  final IconData icon;
  final String label;
  final String? tooltip;
  final int count;
  final bool selected;

  /// Shows the count as a filled badge: something waits on the operator.
  final bool alert;
  final VoidCallback onTap;

  const _SidebarItem({
    super.key,
    required this.icon,
    required this.label,
    this.tooltip,
    required this.count,
    required this.selected,
    this.alert = false,
    required this.onTap,
  });

  @override
  Widget build(BuildContext context) {
    final accent = resolveAppColor(context, AppColors.accent);
    final muted = resolveAppColor(context, AppColors.textMuted);
    final danger = resolveAppColor(context, AppColors.danger);
    // Dark mode's status colours are light: tint the badge there, as
    // SeverityBadge does, instead of white text on them.
    final dark = Theme.of(context).brightness == Brightness.dark;
    final text = Theme.of(context).textTheme.bodyMedium;
    final item = Semantics(
      button: true,
      selected: selected,
      child: Material(
        color: selected ? accent.withValues(alpha: 0.14) : Colors.transparent,
        borderRadius: BorderRadius.circular(7),
        child: InkWell(
          borderRadius: BorderRadius.circular(7),
          onTap: onTap,
          child: Padding(
            padding: const EdgeInsets.symmetric(horizontal: 10, vertical: 7),
            child: Row(
              children: [
                Icon(icon, size: 17, color: selected ? accent : muted),
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
                if (alert)
                  Container(
                    padding: const EdgeInsets.symmetric(
                      horizontal: 7,
                      vertical: 1,
                    ),
                    decoration: BoxDecoration(
                      color: dark ? danger.withValues(alpha: 0.2) : danger,
                      borderRadius: BorderRadius.circular(10),
                    ),
                    child: Text(
                      '$count',
                      style: text?.copyWith(
                        color: dark ? danger : Colors.white,
                        fontSize: 11,
                        fontWeight: FontWeight.w700,
                      ),
                    ),
                  )
                else
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
          ),
        ),
      ),
    );
    return Padding(
      padding: const EdgeInsets.only(bottom: 2),
      child: tooltip == null ? item : Tooltip(message: tooltip!, child: item),
    );
  }
}

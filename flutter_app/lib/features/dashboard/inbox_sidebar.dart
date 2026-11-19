import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';

import '../../core/instances/aggregation.dart';
import '../../core/instances/instances_providers.dart';
import '../../core/models/flow.dart';
import '../../shared/design_system/color_resolver.dart';
import '../../shared/design_system/tokens.dart';
import 'dashboard_providers.dart';
import 'inbox_buckets.dart';

/// The `/health` answer of the daemon the UI is scoped to, null when it does
/// not answer. Fetched again when a review completes, never polled.
final inboxDaemonHealthProvider = FutureProvider<Map<String, dynamic>?>(
  // No automatic retry: the card is read again when a review completes, and
  // a daemon that is down or too old for an endpoint must not be polled.
  retry: (_, _) => null,
  (ref) async {
    ref.watch(prListRefreshProvider);
    return ref.watch(apiClientProvider).fetchHealth();
  },
);

/// Reviews made in the last 7 days, summed over every instance the list shows
/// (#913). Unfiltered on purpose: the Stats screen's repo/org filter belongs to
/// that screen.
final inboxWeekReviewsProvider = FutureProvider<int>(retry: (_, _) => null, (
  ref,
) async {
  ref.watch(prListRefreshProvider);
  final result = await aggregateOne<Map<String, dynamic>>(
    targets: ref.watch(targetInstancesProvider),
    clientFor: (id) => clientForInstance(ref, id),
    fetch: (client) => client.fetchStats(),
  );
  return weekReviews(result.values);
});

/// Sums each instance's `reviews_last_7_days`. The generic stats merge keeps
/// lists from the first instance only, which would undercount a fleet.
int weekReviews(List<Map<String, dynamic>> stats) {
  var reviews = 0;
  for (final s in stats) {
    for (final day in (s['reviews_last_7_days'] as List?) ?? const []) {
      reviews += ((day as Map)['count'] as num?)?.toInt() ?? 0;
    }
  }
  return reviews;
}

/// The agents' subscription windows (Claude's 5-hour and weekly, Codex's…),
/// as the CLI's status line shows them. A review's dollar figure is what the
/// API would charge, not what a subscription spends, so the card shows these
/// instead. Read again when a review completes; the daemon caches readings.
final inboxQuotasProvider = FutureProvider<List<AgentQuota>>(
  retry: (_, _) => null,
  (ref) async {
    ref.watch(prListRefreshProvider);
    return ref.watch(apiClientProvider).fetchQuotas();
  },
);

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
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Expanded(child: _list(needsYou)),
          const Padding(
            padding: EdgeInsets.fromLTRB(10, 0, 10, 12),
            child: _DaemonCard(),
          ),
        ],
      ),
    );
  }

  Widget _list(int needsYou) => ListView(
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
  );
}

/// Whether the daemon answers, its version, how much of each agent's
/// subscription windows is used and how many reviews the last 7 days saw.
/// Opens the Server screen.
class _DaemonCard extends ConsumerWidget {
  const _DaemonCard();

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final health = ref.watch(inboxDaemonHealthProvider);
    final quotas = ref.watch(inboxQuotasProvider).value ?? const [];
    final week = ref.watch(inboxWeekReviewsProvider).value;
    final theme = Theme.of(context);
    final muted = resolveAppColor(context, AppColors.textMuted);
    final small = theme.textTheme.bodySmall?.copyWith(color: muted);
    final raw = health.value;
    final (dot, label) = health.isLoading && !health.hasValue
        ? (muted, 'Checking daemon…')
        : raw != null
        ? (resolveAppColor(context, AppColors.success), 'Daemon running')
        : (resolveAppColor(context, AppColors.danger), 'Daemon not reachable');
    final version = (raw?['version'] as String?) ?? '';
    final windows = [
      for (final q in quotas)
        if (q.available)
          for (final w in q.windows)
            if (w.kind == 'session' || w.kind == 'weekly') (q.agent, w),
    ];
    return Material(
      color: resolveAppColor(context, AppColors.surfaceRaised),
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(10),
        side: BorderSide(color: resolveAppColor(context, AppColors.border)),
      ),
      child: InkWell(
        key: const Key('inbox-daemon-card'),
        borderRadius: BorderRadius.circular(10),
        onTap: () => context.push('/server'),
        child: Padding(
          padding: const EdgeInsets.fromLTRB(12, 10, 12, 11),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Row(
                children: [
                  Container(
                    width: 8,
                    height: 8,
                    decoration: BoxDecoration(
                      color: dot,
                      shape: BoxShape.circle,
                    ),
                  ),
                  const SizedBox(width: 8),
                  Expanded(
                    child: Text(
                      label,
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                      style: theme.textTheme.bodyMedium?.copyWith(
                        fontWeight: FontWeight.w600,
                      ),
                    ),
                  ),
                  if (version.isNotEmpty)
                    Text(
                      version.startsWith('v') ? version : 'v$version',
                      style: small,
                    ),
                ],
              ),
              for (final (agent, w) in windows) ...[
                const SizedBox(height: 8),
                _QuotaLine(agent: agent, window: w),
              ],
              if (week != null) ...[
                const SizedBox(height: 8),
                Text(
                  'Last 7 days · $week review${week == 1 ? '' : 's'}',
                  style: small,
                ),
              ],
            ],
          ),
        ),
      ),
    );
  }
}

/// "claude 5h ▓▓▓░░ 42%", with the reset time on hover.
class _QuotaLine extends StatelessWidget {
  final String agent;
  final QuotaWindow window;
  const _QuotaLine({required this.agent, required this.window});

  @override
  Widget build(BuildContext context) {
    final used = window.usedPercent.clamp(0, 100).toDouble();
    final color = resolveAppColor(
      context,
      used >= 90
          ? AppColors.danger
          : used >= 70
          ? AppColors.warning
          : AppColors.accent,
    );
    final small = Theme.of(context).textTheme.bodySmall?.copyWith(
      color: resolveAppColor(context, AppColors.textMuted),
      fontFeatures: const [FontFeature.tabularFigures()],
    );
    final reset = window.resetsAt?.toLocal();
    String two(int n) => n.toString().padLeft(2, '0');
    return Tooltip(
      message: reset == null
          ? '$agent ${window.shortLabel} window'
          : '$agent ${window.shortLabel} window resets '
                '${two(reset.day)}/${two(reset.month)} '
                '${two(reset.hour)}:${two(reset.minute)}',
      child: Row(
        children: [
          SizedBox(
            width: 70,
            child: Text(
              '$agent ${window.shortLabel}',
              maxLines: 1,
              overflow: TextOverflow.ellipsis,
              style: small,
            ),
          ),
          Expanded(
            child: ClipRRect(
              borderRadius: BorderRadius.circular(3),
              child: LinearProgressIndicator(
                value: used / 100,
                minHeight: 5,
                color: color,
                backgroundColor: color.withValues(alpha: 0.16),
              ),
            ),
          ),
          SizedBox(
            width: 38,
            child: Text(
              '${used.round()}%',
              textAlign: TextAlign.right,
              style: small,
            ),
          ),
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

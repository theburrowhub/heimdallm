import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:mix/mix.dart' show ColorToken;

import '../../core/models/flow.dart';
import '../../shared/design_system/color_resolver.dart';
import '../../shared/design_system/tokens.dart';
import '../config/config_providers.dart';
import 'dashboard_providers.dart';

/// The `/health` answer of the daemon the UI is scoped to, null when it does
/// not answer. Checked again when a review completes and whenever the event
/// stream's connection changes state, so a daemon that stops or comes back
/// shows on the card without polling.
final daemonCardHealthProvider = FutureProvider<Map<String, dynamic>?>(
  // No automatic retry: the triggers above re-read it, and a daemon that is
  // down must not be hammered.
  retry: (_, _) => null,
  (ref) async {
    ref.watch(prListRefreshProvider);
    ref.watch(daemonConnectionProvider.select((s) => s.phase));
    return ref.watch(apiClientProvider).fetchHealth();
  },
);

/// What the card says about the daemon from its `/health` answer: `ok`,
/// `degraded` (a check failing), `starting` or `stopping`, or no answer.
({String label, ColorToken color}) daemonStatusOf(Map<String, dynamic>? raw) {
  if (raw == null) {
    return (label: 'Daemon not reachable', color: AppColors.danger);
  }
  return switch (raw['status']) {
    'ok' || null => (label: 'Daemon running', color: AppColors.success),
    'degraded' => (label: 'Daemon degraded', color: AppColors.warning),
    'starting' => (label: 'Daemon starting', color: AppColors.warning),
    'stopping' => (label: 'Daemon stopping', color: AppColors.warning),
    final other => (label: 'Daemon $other', color: AppColors.warning),
  };
}

/// Reviews the card's daemon made in the last 7 days: the same daemon as its
/// status and quotas, not the whole fleet. Unfiltered on purpose: the Stats
/// screen's repo/org filter belongs to that screen.
final daemonCardWeekReviewsProvider = FutureProvider<int>(
  retry: (_, _) => null,
  (ref) async {
    ref.watch(prListRefreshProvider);
    return weekReviews(await ref.watch(apiClientProvider).fetchStats());
  },
);

/// Sums the daily counts in a `/stats` answer's `reviews_last_7_days`.
int weekReviews(Map<String, dynamic> stats) {
  var reviews = 0;
  for (final day in (stats['reviews_last_7_days'] as List?) ?? const []) {
    reviews += ((day as Map)['count'] as num?)?.toInt() ?? 0;
  }
  return reviews;
}

/// The agents' subscription windows (Claude's 5-hour and weekly, Codex's…),
/// as the CLI's status line shows them. A review's dollar figure is what the
/// API would charge, not what a subscription spends, so the card shows these
/// instead. Read again when a review completes; the daemon caches readings.
final daemonCardQuotasProvider = FutureProvider<List<AgentQuota>>(
  retry: (_, _) => null,
  (ref) async {
    ref.watch(prListRefreshProvider);
    return ref.watch(apiClientProvider).fetchQuotas();
  },
);

/// The daemon at the foot of the app sidebar (#920): whether it answers, its
/// version, how much of each agent's subscription windows is used and how many
/// reviews the last 7 days saw. Opens the Server screen.
///
/// While the daemon is stopped (the state the app bar's power button shows) it
/// says so and reads nothing, so it never polls a daemon that is not there.
/// [compact], for the sidebar collapsed to icons, draws the status dot alone
/// with the rest in its tooltip.
class DaemonCard extends ConsumerWidget {
  final bool compact;
  const DaemonCard({super.key, this.compact = false});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final running = ref.watch(daemonHealthProvider).value ?? false;
    final health = running
        ? ref.watch(daemonCardHealthProvider)
        : const AsyncValue<Map<String, dynamic>?>.data(null);
    final quotas = running
        ? ref.watch(daemonCardQuotasProvider).value ?? const <AgentQuota>[]
        : const <AgentQuota>[];
    final week = running
        ? ref.watch(daemonCardWeekReviewsProvider).value
        : null;
    final theme = Theme.of(context);
    final muted = resolveAppColor(context, AppColors.textMuted);
    final small = theme.textTheme.bodySmall?.copyWith(color: muted);
    final raw = health.value;
    final status = !running
        ? (label: 'Daemon stopped', color: AppColors.danger)
        : health.isLoading && !health.hasValue
        ? (label: 'Checking daemon…', color: AppColors.textMuted)
        : daemonStatusOf(raw);
    final dot = resolveAppColor(context, status.color);
    final label = status.label;
    final version = raw?['version'] is String ? raw!['version'] as String : '';
    final windows = [
      for (final q in quotas)
        if (q.available)
          for (final w in q.windows)
            if (w.kind == 'session' || w.kind == 'weekly') (q.agent, w),
    ];
    final dotWidget = Container(
      width: 8,
      height: 8,
      decoration: BoxDecoration(color: dot, shape: BoxShape.circle),
    );
    if (compact) {
      return Tooltip(
        message: [
          label,
          if (version.isNotEmpty) version,
          for (final (agent, w) in windows)
            '$agent ${w.shortLabel} ${w.usedPercent.clamp(0, 100).round()}%',
        ].join(' · '),
        child: InkWell(
          key: const Key('daemon-card'),
          borderRadius: BorderRadius.circular(8),
          onTap: () => context.push('/server'),
          child: SizedBox(
            width: 40,
            height: 32,
            child: Center(child: dotWidget),
          ),
        ),
      );
    }
    return Material(
      color: resolveAppColor(context, AppColors.surfaceRaised),
      shape: RoundedRectangleBorder(
        borderRadius: BorderRadius.circular(10),
        side: BorderSide(color: resolveAppColor(context, AppColors.border)),
      ),
      child: InkWell(
        key: const Key('daemon-card'),
        borderRadius: BorderRadius.circular(10),
        onTap: () => context.push('/server'),
        child: Padding(
          padding: const EdgeInsets.fromLTRB(12, 10, 12, 11),
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.start,
            children: [
              Row(
                children: [
                  dotWidget,
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

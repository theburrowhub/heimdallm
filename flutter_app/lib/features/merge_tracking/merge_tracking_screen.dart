import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:mix/mix.dart';
import 'package:url_launcher/url_launcher.dart';

import '../../core/models/merge_tracking.dart';
import '../../core/models/my_prs_summary.dart';
import '../../shared/design_system/color_resolver.dart';
import '../../shared/design_system/components/components.dart';
import '../../shared/design_system/tokens.dart';
import '../../shared/widgets/type_badge.dart';
import '../dashboard/dashboard_providers.dart';
import 'add_merge_pr_dialog.dart';
import 'merge_tracking_providers.dart';
import 'widgets/check_visuals.dart';
import 'widgets/checks_table.dart';
import 'widgets/merge_phase_badge.dart';

/// The My PRs tab: every open PR the user authored or is assigned to, grouped
/// by who it is waiting on, plus the ones merged or closed in the last day.
class MergeTrackingScreen extends ConsumerStatefulWidget {
  const MergeTrackingScreen({super.key});

  @override
  ConsumerState<MergeTrackingScreen> createState() =>
      _MergeTrackingScreenState();
}

/// One row of the flattened listing: a section header or a PR card.
sealed class _ListItem {
  const _ListItem();
}

class _HeaderItem extends _ListItem {
  final MyPrsSection section;
  final int count;
  const _HeaderItem(this.section, this.count);
}

class _EntryItem extends _ListItem {
  final MergeTrackingEntry entry;
  const _EntryItem(this.entry);
}

class _MergeTrackingScreenState extends ConsumerState<MergeTrackingScreen> {
  /// Finished PRs are history, not work: collapsed unless asked for.
  bool _showRecent = false;

  List<_ListItem> _items(MyPrsGroups groups) {
    final items = <_ListItem>[];
    for (final section in MyPrsSection.values) {
      final list = groups.of(section);
      if (list.isEmpty) continue;
      items.add(_HeaderItem(section, list.length));
      if (section == MyPrsSection.recent && !_showRecent) continue;
      items.addAll(list.map(_EntryItem.new));
    }
    return items;
  }

  @override
  Widget build(BuildContext context) {
    ref.watch(mergeTrackingSseListenerProvider);
    final async = ref.watch(mergeTrackingProvider);

    return async.when(
      // The listing reloads whenever an SSE event bumps the refresh counter,
      // which is a dependency change and therefore a *reload*: `when` skips the
      // spinner on refresh by default but NOT on reload, so every event tore
      // the list down, showed a spinner and rebuilt a fresh ListView — sending
      // the scroll position back to the top every few seconds, mid-read.
      skipLoadingOnReload: true,
      loading: () => const Center(child: CircularProgressIndicator()),
      error: (e, _) => Center(
        child: AppText(
          'Error loading merge tracking: $e',
          textAlign: TextAlign.center,
        ),
      ),
      data: (entries) {
        final items = _items(groupMyPrs(entries));
        return Column(
          children: [
            const _TrackPRBar(),
            Expanded(
              child: entries.isEmpty
                  ? const _EmptyState()
                  : ListView.builder(
                      // Survives the rebuilds the refresh counter causes.
                      key: const PageStorageKey('merge-tracking-list'),
                      padding: const EdgeInsets.symmetric(vertical: 8),
                      itemCount: items.length,
                      itemBuilder: (context, i) => switch (items[i]) {
                        _HeaderItem(:final section, :final count) =>
                          _SectionHeader(
                            key: ValueKey('section-${section.name}'),
                            section: section,
                            count: count,
                            expanded:
                                section != MyPrsSection.recent || _showRecent,
                            onToggle: section == MyPrsSection.recent
                                ? () =>
                                      setState(() => _showRecent = !_showRecent)
                                : null,
                          ),
                        _EntryItem(:final entry) => _MergeTrackingCard(
                          // Keyed by PR so a row keeps its expanded state when
                          // the daemon reorders the list under the reader.
                          key: ValueKey(entry.prId),
                          entry: entry,
                        ),
                      },
                    ),
            ),
          ],
        );
      },
    );
  }
}

/// A section title with its count. The recently-finished section is a toggle.
class _SectionHeader extends StatelessWidget {
  final MyPrsSection section;
  final int count;
  final bool expanded;
  final VoidCallback? onToggle;

  const _SectionHeader({
    super.key,
    required this.section,
    required this.count,
    required this.expanded,
    this.onToggle,
  });

  @override
  Widget build(BuildContext context) {
    final scheme = Theme.of(context).colorScheme;
    final (title, icon, color) = switch (section) {
      MyPrsSection.action => (
        'Needs your action',
        Icons.error_outline,
        scheme.error,
      ),
      MyPrsSection.ready => (
        'Ready to merge',
        Icons.check_circle_outline,
        const Color(0xFF3FB950),
      ),
      MyPrsSection.waiting => (
        'Waiting on others',
        Icons.hourglass_empty,
        scheme.onSurfaceVariant,
      ),
      MyPrsSection.recent => (
        'Recently merged or closed',
        Icons.history,
        scheme.onSurfaceVariant,
      ),
    };
    final row = Padding(
      padding: const EdgeInsets.fromLTRB(20, 14, 20, 6),
      child: Row(
        children: [
          Icon(icon, size: 16, color: color),
          const SizedBox(width: 8),
          StyledText(
            '$title · $count',
            style: TextStyler()
                .style(AppTextStyles.bodyMuted.mix())
                .fontWeight(FontWeight.w700)
                .color(color),
          ),
          if (onToggle != null) ...[
            const SizedBox(width: 4),
            Icon(
              expanded ? Icons.expand_less : Icons.expand_more,
              size: 18,
              color: color,
            ),
          ],
        ],
      ),
    );
    if (onToggle == null) return row;
    return InkWell(onTap: onToggle, child: row);
  }
}

/// The Merge tab's own way in.
///
/// The Activity tab's Add PR routes through the review pipeline, which refuses
/// PRs the authenticated account authored — every PR the operator opens. This
/// button adds one straight to merge tracking instead.
class _TrackPRBar extends StatelessWidget {
  const _TrackPRBar();

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.fromLTRB(12, 8, 12, 0),
      child: Row(
        children: [
          const Spacer(),
          TextButton.icon(
            key: const Key('track-pr-button'),
            icon: const Icon(Icons.add, size: 18),
            label: const Text('Track a PR'),
            onPressed: () => showAddMergePRDialog(context),
          ),
        ],
      ),
    );
  }
}

class _EmptyState extends StatelessWidget {
  const _EmptyState();

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    return Center(
      child: Padding(
        padding: const EdgeInsets.all(32),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            Icon(
              Icons.merge_type,
              size: 48,
              color: theme.colorScheme.onSurfaceVariant,
            ),
            const SizedBox(height: 12),
            AppText.sectionTitle('No open pull requests of yours'),
            const SizedBox(height: 6),
            AppText.muted(
              'Heimdallm watches the open PRs you authored or are assigned to, '
              'in the repositories it monitors, and tells you which ones need '
              'you. Check "My PRs" in Settings, or paste a PR link with '
              '"Track a PR" above.',
              textAlign: TextAlign.center,
            ),
          ],
        ),
      ),
    );
  }
}

class _MergeTrackingCard extends ConsumerStatefulWidget {
  final MergeTrackingEntry entry;

  const _MergeTrackingCard({super.key, required this.entry});

  @override
  ConsumerState<_MergeTrackingCard> createState() => _MergeTrackingCardState();
}

class _MergeTrackingCardState extends ConsumerState<_MergeTrackingCard> {
  bool _expanded = false;
  bool _busy = false;

  @override
  Widget build(BuildContext context) {
    final entry = widget.entry;
    final theme = Theme.of(context);

    // Same shape as the PR rows in Activity — card margins, accent bar, type
    // and state badges, then title over `repo · #n · author`. A reader moving
    // between the two tabs is looking at the same list of the same things, so
    // the two must not look like different applications.
    return Opacity(
      opacity: entry.isTerminal ? 0.6 : 1.0,
      child: Padding(
        padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 3),
        child: AppSurface(
          bordered: false,
          child: Padding(
            padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 12),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Row(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Container(
                      width: 4,
                      height: 48,
                      margin: const EdgeInsets.only(right: 12),
                      decoration: BoxDecoration(
                        color: _accentColour(context, entry),
                        borderRadius: BorderRadius.circular(2),
                      ),
                    ),
                    const Padding(
                      padding: EdgeInsets.only(right: 6),
                      child: TypeBadge(type: 'pr'),
                    ),
                    const SizedBox(width: 4),
                    MergePhaseBadge(phase: entry.phase),
                    const SizedBox(width: 8),
                    Expanded(
                      child: Column(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          StyledText(
                            entry.title.isNotEmpty
                                ? entry.title
                                : '${entry.repo} #${entry.number}',
                            style: TextStyler()
                                .style(AppTextStyles.body.mix())
                                .fontWeight(FontWeight.w600)
                                .maxLines(1)
                                .overflow(TextOverflow.ellipsis),
                          ),
                          const SizedBox(height: 4),
                          StyledText(
                            '${entry.repo} · #${entry.number}'
                            '${entry.author.isNotEmpty ? ' · ${entry.author}' : ''}'
                            '${entry.isAuthor ? ' · yours' : ''}'
                            '${!entry.isAuthor && entry.isAssignee ? ' · assigned to you' : ''}',
                            style: TextStyler()
                                .style(AppTextStyles.bodyMuted.mix())
                                .maxLines(1)
                                .overflow(TextOverflow.ellipsis),
                          ),
                        ],
                      ),
                    ),
                    if (entry.stale && !entry.isTerminal) ...[
                      const SizedBox(width: 12),
                      _StaleChip(entry: entry),
                    ],
                    const SizedBox(width: 12),
                    CheckCountChips(
                      failing: entry.checksRequiredFailing,
                      pending: entry.checksRequiredPending,
                    ),
                  ],
                ),

                // The check warning is the most important thing on the row when it
                // applies, so it sits directly under the title at full width.
                if (entry.blockedByChecks) ...[
                  const SizedBox(height: 10),
                  ChecksWarningBanner(entry: entry),
                ],
                // The primary blocker still gets its line when it is something
                // other than CI — a PR can be both behind its base and failing a
                // check, and the reader needs both facts.
                if (entry.blockReason.isNotEmpty &&
                    !entry.blockReasonIsChecks &&
                    !entry.isMerged) ...[
                  const SizedBox(height: 8),
                  _BlockLine(entry: entry),
                ],

                if (entry.lastError.isNotEmpty) ...[
                  const SizedBox(height: 8),
                  AppText(entry.lastError, color: theme.colorScheme.error),
                ],

                const SizedBox(height: 6),
                Row(
                  children: [
                    TextButton.icon(
                      icon: Icon(
                        _expanded ? Icons.expand_less : Icons.expand_more,
                        size: 18,
                      ),
                      label: Text(_expanded ? 'Hide checks' : 'Show checks'),
                      onPressed: () => setState(() => _expanded = !_expanded),
                    ),
                    const Spacer(),
                    if (_busy)
                      const Padding(
                        padding: EdgeInsets.symmetric(horizontal: 12),
                        child: SizedBox(
                          width: 16,
                          height: 16,
                          child: CircularProgressIndicator(strokeWidth: 2),
                        ),
                      )
                    else ...[
                      TextButton(
                        onPressed: _reEvaluate,
                        child: const Text('Re-check'),
                      ),
                      TextButton(
                        onPressed: _toggleExcluded,
                        child: Text(entry.excluded ? 'Include' : 'Exclude'),
                      ),
                    ],
                    if (entry.url.isNotEmpty)
                      IconButton(
                        icon: const Icon(Icons.open_in_new, size: 18),
                        tooltip: 'Open on GitHub',
                        onPressed: () => _open(entry.url),
                      ),
                  ],
                ),

                if (_expanded) ...[
                  const Divider(height: 20),
                  _ChecksSection(prId: entry.prId, onOpenUrl: _open),
                ],
              ],
            ),
          ),
        ),
      ),
    );
  }

  /// The accent bar's colour, matching how a PR row in Activity reads at a
  /// glance: red for a blocker that needs a human, amber while something is
  /// still running, green once the merge is on rails.
  Color _accentColour(BuildContext context, MergeTrackingEntry entry) {
    final scheme = Theme.of(context).colorScheme;
    if (entry.isMerged) return const Color(0xFF6A1B9A);
    if (entry.isTerminal) return Colors.grey.shade600;
    if (entry.checksRequiredFailing > 0 || entry.lastError.isNotEmpty) {
      return scheme.error;
    }
    if (entry.checksRequiredPending > 0) return const Color(0xFFE3B341);
    if (entry.autoMergeArmed) return const Color(0xFF00695C);
    if (entry.blockReason.isNotEmpty) return scheme.error;
    return const Color(0xFF3FB950);
  }

  /// Opens a URL externally.
  ///
  /// Only https is followed. Unlike the PR link, a check's log URL points at
  /// whatever CI provider ran it, so the host cannot be pinned to github.com —
  /// but a non-https scheme in a payload we did not author is not something to
  /// hand to the OS.
  void _open(String url) {
    final uri = Uri.tryParse(url);
    if (uri != null && uri.scheme == 'https') {
      launchUrl(uri);
    }
  }

  Future<void> _reEvaluate() async {
    setState(() => _busy = true);
    try {
      // Dry run: the button answers "why is this stuck?", it does not authorise
      // a merge the operator did not configure.
      await ref
          .read(apiClientProvider)
          .evaluateMergeTracking(widget.entry.prId, dryRun: true);
      ref.read(mergeTrackingRefreshProvider.notifier).update((s) => s + 1);
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(
          context,
        ).showSnackBar(SnackBar(content: Text('Re-check failed: $e')));
      }
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _toggleExcluded() async {
    setState(() => _busy = true);
    try {
      await ref
          .read(apiClientProvider)
          .setMergeTrackingExcluded(widget.entry.prId, !widget.entry.excluded);
      ref.read(mergeTrackingRefreshProvider.notifier).update((s) => s + 1);
    } catch (e) {
      if (mounted) {
        ScaffoldMessenger.of(
          context,
        ).showSnackBar(SnackBar(content: Text('Failed: $e')));
      }
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }
}

/// "Stale · 4d": no activity on GitHub for longer than the configured
/// threshold. The duration is the part that makes it actionable.
class _StaleChip extends StatelessWidget {
  final MergeTrackingEntry entry;

  const _StaleChip({required this.entry});

  @override
  Widget build(BuildContext context) {
    final idle = idleFor(entry, DateTime.now());
    final label = idle == null ? 'Stale' : 'Stale · ${formatIdle(idle)}';
    final color = resolveAppColor(context, AppColors.warning);
    final since = entry.lastActivityAt?.toLocal().toString().split('.').first;
    return Tooltip(
      message: since == null
          ? 'No recent activity on GitHub'
          : 'No activity on GitHub since $since',
      child: AppBadge(
        key: const Key('stale-chip'),
        label: label,
        foreground: color,
        background: color.withValues(alpha: 0.12),
        border: color.withValues(alpha: 0.4),
        icon: const Icon(Icons.snooze, size: 12),
        padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 2),
        radius: 4,
        fontSize: 11,
        fontWeight: FontWeight.w700,
        letterSpacing: 0,
      ),
    );
  }
}

/// The non-check block reason, rendered as a single readable line.
class _BlockLine extends StatelessWidget {
  final MergeTrackingEntry entry;

  const _BlockLine({required this.entry});

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final text = entry.blockDetail.isNotEmpty
        ? entry.blockDetail
        : humanBlockReason(entry.blockReason);
    if (text.isEmpty) return const SizedBox.shrink();
    return Row(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Icon(
          Icons.info_outline,
          size: 16,
          color: theme.colorScheme.onSurfaceVariant,
        ),
        const SizedBox(width: 6),
        Expanded(
          child: StyledText(
            text,
            style: TextStyler()
                .style(AppTextStyles.bodyMuted.mix())
                .color(theme.colorScheme.onSurfaceVariant),
          ),
        ),
      ],
    );
  }
}

/// Loads the full decision on demand so the listing stays small.
class _ChecksSection extends ConsumerWidget {
  final int prId;
  final void Function(String url) onOpenUrl;

  const _ChecksSection({required this.prId, required this.onOpenUrl});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final async = ref.watch(mergeTrackingDetailProvider(prId));
    return async.when(
      loading: () => const Padding(
        padding: EdgeInsets.all(8),
        child: LinearProgressIndicator(),
      ),
      error: (e, _) => AppText('Could not load checks: $e'),
      data: (entry) {
        final decision = entry.decision;
        if (decision == null) {
          return const AppText.muted('Heimdallm has not evaluated this PR yet.');
        }
        return ChecksTable(decision: decision, onOpenUrl: onOpenUrl);
      },
    );
  }
}

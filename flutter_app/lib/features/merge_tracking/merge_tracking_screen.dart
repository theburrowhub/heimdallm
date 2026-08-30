import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:url_launcher/url_launcher.dart';

import '../../core/models/merge_tracking.dart';
import '../../shared/widgets/type_badge.dart';
import '../dashboard/dashboard_providers.dart';
import 'add_merge_pr_dialog.dart';
import 'merge_tracking_providers.dart';
import 'widgets/check_visuals.dart';
import 'widgets/checks_table.dart';
import 'widgets/merge_phase_badge.dart';

/// The Merge tab: every PR the user authored or is assigned to, and what is
/// stopping each one from merging.
class MergeTrackingScreen extends ConsumerWidget {
  const MergeTrackingScreen({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
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
      error: (e, _) => Center(child: Text('Error loading merge tracking: $e')),
      data: (entries) => Column(
        children: [
          const _TrackPRBar(),
          Expanded(
            child: entries.isEmpty
                ? const _EmptyState()
                : ListView.builder(
                    // Survives the rebuilds the refresh counter causes.
                    key: const PageStorageKey('merge-tracking-list'),
                    padding: const EdgeInsets.symmetric(vertical: 8),
                    itemCount: entries.length,
                    itemBuilder: (context, i) => _MergeTrackingCard(
                      // Keyed by PR so a row keeps its expanded state when the
                      // daemon reorders the list under the reader.
                      key: ValueKey(entries[i].prId),
                      entry: entries[i],
                    ),
                  ),
          ),
        ],
      ),
    );
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
            Text(
              'No pull requests tracked yet',
              style: theme.textTheme.titleMedium,
            ),
            const SizedBox(height: 6),
            Text(
              'Heimdallm tracks the open PRs you authored or are assigned to, '
              'in the repositories it monitors. Turn merge tracking on in '
              'Settings, or paste a PR link with "Track a PR" above.',
              textAlign: TextAlign.center,
              style: theme.textTheme.bodySmall,
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
      child: Card(
        margin: const EdgeInsets.symmetric(horizontal: 16, vertical: 3),
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
                        Text(
                          entry.title.isNotEmpty
                              ? entry.title
                              : '${entry.repo} #${entry.number}',
                          style: const TextStyle(fontWeight: FontWeight.w600),
                          maxLines: 1,
                          overflow: TextOverflow.ellipsis,
                        ),
                        const SizedBox(height: 4),
                        Text(
                          '${entry.repo} · #${entry.number}'
                          '${entry.author.isNotEmpty ? ' · ${entry.author}' : ''}'
                          '${entry.isAuthor ? ' · yours' : ''}'
                          '${!entry.isAuthor && entry.isAssignee ? ' · assigned to you' : ''}',
                          style: theme.textTheme.bodySmall,
                          maxLines: 1,
                          overflow: TextOverflow.ellipsis,
                        ),
                      ],
                    ),
                  ),
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
                Text(
                  entry.lastError,
                  style: theme.textTheme.bodySmall?.copyWith(
                    color: theme.colorScheme.error,
                  ),
                ),
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
          child: Text(
            text,
            style: theme.textTheme.bodySmall?.copyWith(
              color: theme.colorScheme.onSurfaceVariant,
            ),
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
      error: (e, _) => Text('Could not load checks: $e'),
      data: (entry) {
        final decision = entry.decision;
        if (decision == null) {
          return Text(
            'Heimdallm has not evaluated this PR yet.',
            style: Theme.of(context).textTheme.bodySmall,
          );
        }
        return ChecksTable(decision: decision, onOpenUrl: onOpenUrl);
      },
    );
  }
}

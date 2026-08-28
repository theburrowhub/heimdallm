import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:url_launcher/url_launcher.dart';

import '../../core/models/merge_tracking.dart';
import '../dashboard/dashboard_providers.dart';
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
      loading: () => const Center(child: CircularProgressIndicator()),
      error: (e, _) => Center(child: Text('Error loading merge tracking: $e')),
      data: (entries) {
        if (entries.isEmpty) {
          return const _EmptyState();
        }
        return ListView.builder(
          padding: const EdgeInsets.symmetric(vertical: 8),
          itemCount: entries.length,
          itemBuilder: (context, i) => _MergeTrackingCard(entry: entries[i]),
        );
      },
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
              'Settings to start.',
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

  const _MergeTrackingCard({required this.entry});

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

    return Card(
      margin: const EdgeInsets.symmetric(horizontal: 12, vertical: 5),
      child: Padding(
        padding: const EdgeInsets.all(12),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Expanded(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(
                        entry.title.isNotEmpty
                            ? entry.title
                            : '${entry.repo}#${entry.number}',
                        style: theme.textTheme.titleSmall,
                        maxLines: 2,
                        overflow: TextOverflow.ellipsis,
                      ),
                      const SizedBox(height: 2),
                      Text(
                        '${entry.repo}#${entry.number}'
                        '${entry.isAuthor ? ' · yours' : ''}'
                        '${!entry.isAuthor && entry.isAssignee ? ' · assigned to you' : ''}',
                        style: theme.textTheme.bodySmall?.copyWith(
                          color: theme.colorScheme.onSurfaceVariant,
                        ),
                      ),
                    ],
                  ),
                ),
                const SizedBox(width: 8),
                CheckCountChips(
                  failing: entry.checksRequiredFailing,
                  pending: entry.checksRequiredPending,
                ),
                const SizedBox(width: 6),
                MergePhaseBadge(phase: entry.phase),
              ],
            ),

            // The check warning is the most important thing on the row when it
            // applies, so it sits directly under the title at full width.
            if (entry.blockedByChecks) ...[
              const SizedBox(height: 10),
              ChecksWarningBanner(entry: entry),
            ] else if (entry.blockReason.isNotEmpty && !entry.isMerged) ...[
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
    );
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

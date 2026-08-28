import 'dart:async';
import 'dart:convert';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:url_launcher/url_launcher.dart';
import '../merge_tracking/merge_tracking_providers.dart';
import '../merge_tracking/widgets/check_visuals.dart';
import '../merge_tracking/widgets/checks_table.dart';
import '../merge_tracking/widgets/merge_phase_badge.dart';
import '../../core/models/pr.dart';
import '../../core/models/review.dart';
import '../../core/models/review_status.dart';
import '../../shared/widgets/severity_badge.dart';
import '../../shared/widgets/toast.dart';
import '../dashboard/dashboard_providers.dart';
import 'pr_detail_providers.dart';

class PRDetailScreen extends ConsumerStatefulWidget {
  final int prId;
  const PRDetailScreen({super.key, required this.prId});

  @override
  ConsumerState<PRDetailScreen> createState() => _PRDetailScreenState();
}

class _PRDetailScreenState extends ConsumerState<PRDetailScreen> {
  bool _reviewing = false;
  bool _cancelling = false;
  Timer? _reviewTimeout;

  @override
  void dispose() {
    _reviewTimeout?.cancel();
    super.dispose();
  }

  void _startReviewing() {
    setState(() => _reviewing = true);
    _reviewTimeout?.cancel();
    // Safety net: reset spinner if no SSE event arrives within 90 seconds
    _reviewTimeout = Timer(const Duration(seconds: 90), () {
      if (mounted) setState(() => _reviewing = false);
    });
  }

  void _stopReviewing() {
    _reviewTimeout?.cancel();
    if (mounted) {
      setState(() {
        _reviewing = false;
        _cancelling = false;
      });
    }
  }

  Future<void> _dismiss(BuildContext context) async {
    final api = ref.read(apiClientProvider);
    try {
      await api.dismissPR(widget.prId);
      ref.invalidate(prsProvider);
      if (context.mounted) {
        context.canPop() ? context.pop() : context.go('/');
        showToast(
          context,
          'PR dismissed',
          duration: const Duration(seconds: 5),
          actionLabel: 'Undo',
          onAction: () async {
            await api.undismissPR(widget.prId);
            ref.invalidate(prsProvider);
          },
        );
      }
    } catch (e) {
      if (!context.mounted) return;
      showToast(context, 'Error: $e', isError: true);
    }
  }

  Future<void> _trigger() async {
    _startReviewing();
    final api = ref.read(apiClientProvider);
    try {
      await api.triggerReview(widget.prId);
      ref.invalidate(prDetailProvider(widget.prId));
    } catch (e) {
      _stopReviewing();
      if (mounted) showToast(context, 'Error: $e', isError: true);
    }
  }

  Future<void> _cancel() async {
    final detail = ref.read(prDetailProvider(widget.prId)).value;
    final pr = detail?['pr'] as PR?;
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (dialogContext) => AlertDialog(
        title: const Text('Cancel this review?'),
        content: Text(
          'The active agent process${pr == null ? '' : ' for ${pr.repo} #${pr.number}'} '
          'will be terminated. Other reviews will continue running.',
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(dialogContext, false),
            child: const Text('Keep running'),
          ),
          FilledButton(
            onPressed: () => Navigator.pop(dialogContext, true),
            child: const Text('Cancel review'),
          ),
        ],
      ),
    );
    if (confirmed != true || !mounted) return;
    setState(() => _cancelling = true);
    try {
      await ref.read(apiClientProvider).cancelReview(widget.prId);
      if (mounted) showToast(context, 'Cancellation requested');
    } catch (e) {
      if (mounted) {
        setState(() => _cancelling = false);
        showToast(context, 'Error: $e', isError: true);
      }
    }
  }

  @override
  Widget build(BuildContext context) {
    final detailAsync = ref.watch(prDetailProvider(widget.prId));

    // Listen to SSE events to update review state and surface errors
    ref.listen(sseStreamProvider, (_, next) {
      next.whenData((event) {
        try {
          final data = jsonDecode(event.data) as Map<String, dynamic>;
          final prId = (data['pr_id'] as num?)?.toInt();
          final prNumber = (data['pr_number'] as num?)?.toInt();
          final currentPrNumber = detailAsync.value?['pr'] is PR
              ? (detailAsync.value!['pr'] as PR).number
              : null;

          switch (event.type) {
            case 'review_started':
              if (prNumber != null && prNumber == currentPrNumber) {
                if (mounted) setState(() => _reviewing = true);
              }
            case 'review_completed':
              if (prId == widget.prId || prNumber == currentPrNumber) {
                _stopReviewing();
                ref.invalidate(prDetailProvider(widget.prId));
              }
            case 'review_error':
              if (prId == widget.prId || prNumber == currentPrNumber) {
                _stopReviewing();
                final error = data['error'] as String? ?? 'Unknown error';
                if (mounted) {
                  showToast(
                    context,
                    data['reason'] == 'manual_cancelled'
                        ? error
                        : 'Review failed: $error',
                    isError: data['reason'] != 'manual_cancelled',
                  );
                }
                ref.invalidate(prDetailProvider(widget.prId));
              }
          }
        } catch (_) {}
      });
    });

    final detailData = detailAsync.value;
    final reviews = detailData?['reviews'] as List<Review>? ?? [];
    final hasReviews = reviews.isNotEmpty;
    final pr = detailData?['pr'] as PR?;
    final status = pr?.reviewStatus;
    final failure = status != null && !status.active && status.error.isNotEmpty
        ? status
        : null;
    final repoMissing = pr != null && pr.repo.isEmpty;

    // Derive review key from loaded PR for shared in-progress state
    final reviewKey = pr != null ? '${pr.repo}:${pr.number}' : null;
    final isReviewingShared =
        reviewKey != null &&
        ref.watch(reviewingPRsProvider).containsKey(reviewKey);
    // Combine local trigger state with shared provider
    final reviewing =
        _reviewing || isReviewingShared || (status?.active ?? false);

    return Scaffold(
      appBar: AppBar(
        title: const Text('PR Review'),
        leading: IconButton(
          icon: const Icon(Icons.arrow_back),
          onPressed: () => context.canPop() ? context.pop() : context.go('/'),
        ),
        actions: [
          if (reviewing)
            Padding(
              padding: const EdgeInsets.symmetric(horizontal: 8),
              child: Row(
                children: [
                  const SizedBox(
                    width: 20,
                    height: 20,
                    child: CircularProgressIndicator(strokeWidth: 2),
                  ),
                  const SizedBox(width: 12),
                  OutlinedButton.icon(
                    icon: _cancelling
                        ? const SizedBox(
                            width: 14,
                            height: 14,
                            child: CircularProgressIndicator(strokeWidth: 2),
                          )
                        : const Icon(Icons.stop_circle_outlined, size: 16),
                    label: Text(_cancelling ? 'Cancelling…' : 'Cancel'),
                    onPressed: _cancelling ? null : _cancel,
                  ),
                ],
              ),
            )
          else ...[
            Tooltip(
              message: repoMissing
                  ? 'Repo unknown — wait for next poll or re-discover in Settings'
                  : '',
              child: ElevatedButton.icon(
                icon: const Icon(Icons.refresh, size: 16),
                label: Text(
                  failure != null
                      ? 'Retry'
                      : hasReviews
                      ? 'Re-review'
                      : 'Review',
                ),
                onPressed: repoMissing ? null : _trigger,
              ),
            ),
            const SizedBox(width: 8),
            OutlinedButton.icon(
              icon: const Icon(Icons.visibility_off_outlined, size: 16),
              label: const Text('Dismiss'),
              onPressed: () => _dismiss(context),
            ),
          ],
          const SizedBox(width: 12),
        ],
      ),
      body: Column(
        children: [
          // In-progress banner
          if (reviewing)
            LinearProgressIndicator(
              minHeight: 3,
              backgroundColor: Theme.of(
                context,
              ).colorScheme.surfaceContainerHighest,
            ),
          if (!reviewing && failure != null)
            Container(
              width: double.infinity,
              color: Theme.of(context).colorScheme.errorContainer,
              padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 10),
              child: Row(
                children: [
                  Icon(
                    failure.isCancelled
                        ? Icons.cancel_outlined
                        : Icons.error_outline,
                    color: Theme.of(context).colorScheme.onErrorContainer,
                  ),
                  const SizedBox(width: 10),
                  Expanded(
                    child: Text(
                      reviewFailureSummary(failure),
                      style: TextStyle(
                        color: Theme.of(context).colorScheme.onErrorContainer,
                      ),
                    ),
                  ),
                ],
              ),
            ),
          Expanded(
            child: detailAsync.when(
              loading: () => const Center(child: CircularProgressIndicator()),
              error: (e, _) => Center(child: Text('Error: $e')),
              data: (data) {
                final pr = data['pr'] as PR;
                final reviews = data['reviews'] as List<Review>;
                return Row(
                  children: [
                    Expanded(
                      flex: 2,
                      child: ListView(
                        padding: EdgeInsets.zero,
                        children: [
                          // Merge state comes before the review: a red required check
                          // is more urgent than review feedback, and it is the thing
                          // the author has to act on.
                          _MergeChecksPanel(prId: widget.prId),
                          _ReviewPanel(
                            pr: pr,
                            reviews: reviews,
                            embedded: true,
                          ),
                        ],
                      ),
                    ),
                    const VerticalDivider(width: 1),
                    Expanded(
                      flex: 1,
                      child: _PRMetaPanel(
                        pr: pr,
                        onReview: pr.repo.isEmpty ? null : _trigger,
                      ),
                    ),
                  ],
                );
              },
            ),
          ),
        ],
      ),
    );
  }
}

class _ReviewPanel extends StatelessWidget {
  final PR pr;
  final List<Review> reviews;

  /// When true the panel is a child of an outer scroll view, so it must not
  /// bring its own — a SingleChildScrollView inside a ListView has unbounded
  /// height and throws.
  final bool embedded;

  const _ReviewPanel({
    required this.pr,
    required this.reviews,
    this.embedded = false,
  });

  @override
  Widget build(BuildContext context) {
    final body = Padding(
      padding: const EdgeInsets.all(16),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(pr.title, style: Theme.of(context).textTheme.headlineSmall),
          Text(
            '${pr.repo} #${pr.number} by ${pr.author}',
            style: Theme.of(context).textTheme.bodySmall,
          ),
          const SizedBox(height: 16),
          if (reviews.isEmpty)
            const Text('No reviews yet.')
          else
            ...reviews.map((rev) => _ReviewCard(review: rev)),
        ],
      ),
    );
    return embedded ? body : SingleChildScrollView(child: body);
  }
}

/// The merge-readiness panel on the PR detail view: what is stopping this PR
/// from merging, and the full per-check breakdown.
///
/// Absent entirely for a PR merge tracking does not know about, so a PR someone
/// else owns does not grow an empty section.
class _MergeChecksPanel extends ConsumerWidget {
  final int prId;

  const _MergeChecksPanel({required this.prId});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final async = ref.watch(mergeTrackingDetailProvider(prId));
    return async.when(
      // A PR that is not tracked answers 404; showing nothing is right.
      loading: () => const SizedBox.shrink(),
      error: (_, _) => const SizedBox.shrink(),
      data: (entry) {
        final decision = entry.decision;
        if (decision == null) return const SizedBox.shrink();
        return Card(
          margin: const EdgeInsets.fromLTRB(16, 16, 16, 0),
          child: Padding(
            padding: const EdgeInsets.all(16),
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Row(
                  children: [
                    Text(
                      'Merge status',
                      style: Theme.of(context).textTheme.titleSmall,
                    ),
                    const SizedBox(width: 8),
                    MergePhaseBadge(phase: entry.phase),
                  ],
                ),
                const SizedBox(height: 12),
                if (entry.blockedByChecks)
                  ChecksWarningBanner(entry: entry)
                else if (entry.blockReason.isNotEmpty && !entry.isMerged)
                  Text(
                    entry.blockDetail.isNotEmpty
                        ? entry.blockDetail
                        : humanBlockReason(entry.blockReason),
                    style: Theme.of(context).textTheme.bodySmall,
                  ),
                const SizedBox(height: 12),
                ChecksTable(decision: decision, onOpenUrl: _openCheckLog),
              ],
            ),
          ),
        );
      },
    );
  }

  /// Only https is followed: a check's log lives on whatever CI provider ran
  /// it, so the host cannot be pinned, but the scheme can.
  void _openCheckLog(String url) {
    final uri = Uri.tryParse(url);
    if (uri != null && uri.scheme == 'https') {
      launchUrl(uri);
    }
  }
}

class _ReviewCard extends StatelessWidget {
  final Review review;
  const _ReviewCard({required this.review});

  @override
  Widget build(BuildContext context) {
    return Card(
      margin: const EdgeInsets.only(bottom: 12),
      child: Padding(
        padding: const EdgeInsets.all(12),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Text(
                  'Reviewed by ${review.cliUsed}',
                  style: Theme.of(context).textTheme.labelSmall,
                ),
                const Spacer(),
                SeverityBadge(severity: review.severity),
              ],
            ),
            const SizedBox(height: 8),
            Text(review.summary),
            if (review.issues.isNotEmpty) ...[
              const SizedBox(height: 8),
              Text('Issues', style: Theme.of(context).textTheme.labelMedium),
              ...review.issues.map(
                (issue) => Padding(
                  padding: const EdgeInsets.only(top: 4, left: 8),
                  child: Row(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      const Icon(Icons.warning_amber, size: 14),
                      const SizedBox(width: 4),
                      Expanded(
                        child: Text(
                          '${issue.file}:${issue.line} — ${issue.description}',
                        ),
                      ),
                    ],
                  ),
                ),
              ),
            ],
          ],
        ),
      ),
    );
  }
}

class _PRMetaPanel extends StatelessWidget {
  final PR pr;
  final VoidCallback? onReview;
  const _PRMetaPanel({required this.pr, this.onReview});

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.all(16),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text('Details', style: Theme.of(context).textTheme.titleMedium),
          const SizedBox(height: 12),
          if (pr.repo.isEmpty)
            Container(
              margin: const EdgeInsets.only(bottom: 12),
              padding: const EdgeInsets.all(10),
              decoration: BoxDecoration(
                color: Colors.orange.withValues(alpha: 0.1),
                border: Border.all(color: Colors.orange.withValues(alpha: 0.4)),
                borderRadius: BorderRadius.circular(6),
              ),
              child: const Row(
                children: [
                  Icon(Icons.warning_amber, size: 16, color: Colors.orange),
                  SizedBox(width: 6),
                  Expanded(
                    child: Text(
                      'Repo unknown. Re-discover repos in Settings to enable auto-review.',
                      style: TextStyle(fontSize: 12, color: Colors.orange),
                    ),
                  ),
                ],
              ),
            ),
          _row(context, 'Repo', pr.repo.isEmpty ? '(unknown)' : pr.repo),
          _row(context, 'Number', '#${pr.number}'),
          _row(context, 'Author', pr.author),
          _row(context, 'State', pr.state),
          _row(
            context,
            'Updated',
            pr.updatedAt.toLocal().toString().substring(0, 16),
          ),
          const SizedBox(height: 12),
          OutlinedButton.icon(
            icon: const Icon(Icons.open_in_browser),
            label: const Text('Open on GitHub'),
            onPressed: () {
              final uri = Uri.tryParse(pr.url);
              if (uri != null &&
                  uri.scheme == 'https' &&
                  uri.host == 'github.com') {
                launchUrl(uri);
              }
            },
          ),
        ],
      ),
    );
  }

  Widget _row(BuildContext context, String label, String value) {
    return Padding(
      padding: const EdgeInsets.only(bottom: 8),
      child: Row(
        children: [
          SizedBox(
            width: 72,
            child: Text(
              '$label:',
              style: const TextStyle(fontWeight: FontWeight.w600),
            ),
          ),
          Expanded(child: Text(value)),
        ],
      ),
    );
  }
}

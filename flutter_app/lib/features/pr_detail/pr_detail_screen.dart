import 'dart:async';
import 'dart:convert';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:url_launcher/url_launcher.dart';
import '../../core/api/api_client.dart';
import '../merge_tracking/merge_tracking_providers.dart';
import '../merge_tracking/widgets/check_visuals.dart';
import '../merge_tracking/widgets/checks_table.dart';
import '../merge_tracking/widgets/merge_phase_badge.dart';
import '../../core/models/merge_tracking.dart';
import '../../core/models/pr.dart';
import '../../core/models/review.dart';
import '../../core/models/review_status.dart';
import '../../shared/design_system/color_resolver.dart';
import '../../shared/design_system/components/app_surface.dart';
import '../../shared/design_system/components/app_text.dart';
import '../../shared/design_system/tokens.dart';
import '../../shared/widgets/toast.dart';
import '../dashboard/dashboard_providers.dart';
import 'pr_detail_providers.dart';
import 'review_workspace.dart';

class PRDetailScreen extends ConsumerStatefulWidget {
  final int prId;

  /// Which instance holds this record. Empty means the daemon this app
  /// manages, which is every record on a single-daemon install.
  final String instanceId;

  const PRDetailScreen({super.key, required this.prId, this.instanceId = ''});

  @override
  ConsumerState<PRDetailScreen> createState() => _PRDetailScreenState();
}

class _PRDetailScreenState extends ConsumerState<PRDetailScreen> {
  /// Key for the instance-scoped detail provider.
  PRRef get _ref => (instanceId: widget.instanceId, prId: widget.prId);

  /// The client for the instance holding this PR, not whichever one the
  /// dashboard is currently scoped to.
  ApiClient get _api => clientForInstanceOf(ref, widget.instanceId);

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
    final api = _api;
    try {
      await api.dismissPR(widget.prId);
      ref.invalidate(prsByInstanceProvider);
      if (context.mounted) {
        context.canPop() ? context.pop() : context.go('/');
        showToast(
          context,
          'PR dismissed',
          duration: const Duration(seconds: 5),
          actionLabel: 'Undo',
          onAction: () async {
            await api.undismissPR(widget.prId);
            ref.invalidate(prsByInstanceProvider);
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
    final api = _api;
    try {
      await api.triggerReview(widget.prId);
      ref.invalidate(prDetailProvider(_ref));
    } catch (e) {
      _stopReviewing();
      if (mounted) showToast(context, 'Error: $e', isError: true);
    }
  }

  Future<void> _cancel() async {
    final detail = ref.read(prDetailProvider(_ref)).value;
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
      await _api.cancelReview(widget.prId);
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
    final detailAsync = ref.watch(prDetailProvider(_ref));

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
                ref.invalidate(prDetailProvider(_ref));
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
                ref.invalidate(prDetailProvider(_ref));
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
    final reviewKey = pr != null
        ? reviewKeyFor(widget.instanceId, pr.repo, pr.number)
        : null;
    final isReviewingShared =
        reviewKey != null &&
        ref.watch(reviewingPRsProvider).containsKey(reviewKey);
    // Combine local trigger state with shared provider
    final reviewing =
        _reviewing || isReviewingShared || (status?.active ?? false);
    // The merge panel only for a PR merge tracking has decided on: an
    // untracked PR answers 404, and an empty panel would still take its gap in
    // the workspace's layout. A failed refresh hides it, as before.
    final mergeAsync = ref.watch(mergeTrackingDetailProvider(widget.prId));
    final merge = mergeAsync.hasError ? null : mergeAsync.value;
    final mergeDecision = merge?.decision;

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
              error: (e, _) => _LoadError(
                error: e,
                onRetry: () => ref.invalidate(prDetailProvider(_ref)),
              ),
              data: (data) {
                final pr = data['pr'] as PR;
                final reviews = data['reviews'] as List<Review>;
                return ReviewWorkspace(
                  pr: pr,
                  reviews: reviews,
                  // Off while a review runs: the daemon refuses a second one
                  // and the refusal reads as a failed review.
                  onReview: pr.repo.isEmpty || reviewing ? null : _trigger,
                  // Merge state comes first in the inspector: a red required
                  // check is the thing the author has to act on.
                  inspectorTop: merge != null && mergeDecision != null
                      ? _MergeChecksPanel(entry: merge, decision: mergeDecision)
                      : null,
                );
              },
            ),
          ),
        ],
      ),
    );
  }
}

/// A detail that failed to load says so and offers to try again.
class _LoadError extends StatelessWidget {
  final Object error;
  final VoidCallback onRetry;

  const _LoadError({required this.error, required this.onRetry});

  @override
  Widget build(BuildContext context) {
    return Center(
      child: Padding(
        padding: const EdgeInsets.all(32),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            Icon(
              Icons.cloud_off_outlined,
              size: 32,
              color: resolveAppColor(context, AppColors.textMuted),
            ),
            const SizedBox(height: 12),
            const AppText.sectionTitle('Could not load this PR'),
            const SizedBox(height: 6),
            AppText.muted('$error'),
            const SizedBox(height: 16),
            OutlinedButton.icon(
              icon: const Icon(Icons.refresh, size: 16),
              label: const Text('Try again'),
              onPressed: onRetry,
            ),
          ],
        ),
      ),
    );
  }
}

/// The merge-readiness panel on the PR detail view: what is stopping this PR
/// from merging, and the full per-check breakdown.
///
/// The screen builds it only for a PR merge tracking has a decision on, so a PR
/// someone else owns does not grow an empty section.
class _MergeChecksPanel extends StatelessWidget {
  final MergeTrackingEntry entry;
  final MergeDecision decision;

  const _MergeChecksPanel({required this.entry, required this.decision});

  @override
  Widget build(BuildContext context) {
    return AppSurface(
      elevation: AppSurfaceElevation.raised,
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

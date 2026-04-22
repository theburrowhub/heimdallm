import 'dart:async';
import 'dart:convert';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:url_launcher/url_launcher.dart';
import '../../core/models/tracked_issue.dart';
import '../../shared/widgets/severity_badge.dart';
import '../../shared/widgets/toast.dart';
import '../dashboard/dashboard_providers.dart';
import 'issues_providers.dart';

class IssueDetailScreen extends ConsumerStatefulWidget {
  final int issueId;
  const IssueDetailScreen({super.key, required this.issueId});

  @override
  ConsumerState<IssueDetailScreen> createState() => _IssueDetailScreenState();
}

class _IssueDetailScreenState extends ConsumerState<IssueDetailScreen> {
  bool _reviewing = false;
  Timer? _reviewTimeout;

  @override
  void dispose() {
    _reviewTimeout?.cancel();
    super.dispose();
  }

  void _startReviewing() {
    setState(() => _reviewing = true);
    _reviewTimeout?.cancel();
    _reviewTimeout = Timer(const Duration(seconds: 90), () {
      if (mounted) setState(() => _reviewing = false);
    });
  }

  void _stopReviewing() {
    _reviewTimeout?.cancel();
    if (mounted) setState(() => _reviewing = false);
  }

  Future<void> _dismiss(BuildContext context) async {
    final api = ref.read(apiClientProvider);
    try {
      await api.dismissIssue(widget.issueId);
      ref.invalidate(issuesProvider);
      if (context.mounted) {
        context.canPop() ? context.pop() : context.go('/');
        showToast(context, 'Issue dismissed',
            duration: const Duration(seconds: 5),
            actionLabel: 'Undo',
            onAction: () async {
              await api.undismissIssue(widget.issueId);
              ref.invalidate(issuesProvider);
            });
      }
    } catch (e) {
      if (!context.mounted) return;
      showToast(context, 'Error: $e', isError: true);
    }
  }

  Future<void> _promote() async {
    _startReviewing();
    final api = ref.read(apiClientProvider);
    try {
      await api.promoteIssue(widget.issueId);
      ref.invalidate(issueDetailProvider(widget.issueId));
      if (mounted) showToast(context, 'Promoted to auto-implement');
    } catch (e) {
      _stopReviewing();
      if (mounted) showToast(context, 'Error: $e', isError: true);
    }
  }

  Future<void> _trigger() async {
    _startReviewing();
    final api = ref.read(apiClientProvider);
    try {
      await api.triggerIssueReview(widget.issueId);
      ref.invalidate(issueDetailProvider(widget.issueId));
    } catch (e) {
      _stopReviewing();
      if (mounted) showToast(context, 'Error: $e', isError: true);
    }
  }

  @override
  Widget build(BuildContext context) {
    final detailAsync = ref.watch(issueDetailProvider(widget.issueId));

    // SSE listener for real-time review updates
    ref.listen(sseStreamProvider, (_, next) {
      next.whenData((event) {
        try {
          final data = jsonDecode(event.data) as Map<String, dynamic>;
          final issueId = (data['issue_id'] as num?)?.toInt();

          switch (event.type) {
            case 'issue_review_started':
              if (issueId == widget.issueId) {
                _startReviewing();
              }
            case 'issue_review_completed':
              if (issueId == widget.issueId) {
                _stopReviewing();
                ref.invalidate(issueDetailProvider(widget.issueId));
              }
            case 'issue_review_error':
              if (issueId == widget.issueId) {
                _stopReviewing();
                final error = data['error'] as String? ?? 'Unknown error';
                if (mounted) showToast(context, 'Review failed: $error', isError: true);
              }
          }
        } catch (_) {}
      });
    });

    final detailData = detailAsync.valueOrNull;
    final reviews = detailData?['reviews'] as List<TrackedIssueReview>? ?? [];
    final hasReviews = reviews.isNotEmpty;
    final issue = detailData?['issue'] as TrackedIssue?;

    final reviewKey = issue != null ? '${issue.repo}:${issue.number}' : null;
    final isReviewingShared =
        reviewKey != null && ref.watch(reviewingIssuesProvider).contains(reviewKey);
    final reviewing = _reviewing || isReviewingShared;

    return Scaffold(
      appBar: AppBar(
        title: const Text('Issue Review'),
        leading: IconButton(
          icon: const Icon(Icons.arrow_back),
          onPressed: () => context.canPop() ? context.pop() : context.go('/'),
        ),
        actions: [
          if (reviewing)
            const Padding(
              padding: EdgeInsets.symmetric(horizontal: 16),
              child: SizedBox(
                  width: 20, height: 20,
                  child: CircularProgressIndicator(strokeWidth: 2)),
            )
          else ...[
            ElevatedButton.icon(
              icon: const Icon(Icons.refresh, size: 16),
              label: Text(hasReviews ? 'Re-review' : 'Review'),
              onPressed: _trigger,
            ),
            if (hasReviews && reviews.last.actionTaken == 'review_only') ...[
              const SizedBox(width: 8),
              ElevatedButton.icon(
                icon: const Icon(Icons.rocket_launch, size: 16),
                label: const Text('Promote to Dev'),
                style: ElevatedButton.styleFrom(
                  backgroundColor: Theme.of(context).colorScheme.secondary,
                  foregroundColor: Theme.of(context).colorScheme.onSecondary,
                ),
                onPressed: _promote,
              ),
            ],
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
          if (reviewing)
            LinearProgressIndicator(
              minHeight: 3,
              backgroundColor: Theme.of(context).colorScheme.surfaceContainerHighest,
            ),
          Expanded(
            child: detailAsync.when(
              loading: () => const Center(child: CircularProgressIndicator()),
              error: (e, _) => Center(child: Text('Error: $e')),
              data: (data) {
                final issue = data['issue'] as TrackedIssue;
                final reviews = data['reviews'] as List<TrackedIssueReview>;
                return Row(
                  children: [
                    Expanded(flex: 2, child: _ReviewPanel(issue: issue, reviews: reviews)),
                    const VerticalDivider(width: 1),
                    Expanded(flex: 1, child: _IssueMetaPanel(issue: issue)),
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
  final TrackedIssue issue;
  final List<TrackedIssueReview> reviews;
  const _ReviewPanel({required this.issue, required this.reviews});

  @override
  Widget build(BuildContext context) {
    return SingleChildScrollView(
      padding: const EdgeInsets.all(16),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(issue.title, style: Theme.of(context).textTheme.headlineSmall),
          Text('${issue.repo} #${issue.number} by ${issue.author}',
              style: Theme.of(context).textTheme.bodySmall),
          const SizedBox(height: 16),
          if (reviews.isEmpty)
            const Text('No reviews yet.')
          else
            ...reviews.map((rev) => _IssueReviewCard(review: rev)),
        ],
      ),
    );
  }
}

class _IssueReviewCard extends StatelessWidget {
  final TrackedIssueReview review;
  const _IssueReviewCard({required this.review});

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
                Text('Reviewed by ${review.cliUsed}',
                    style: Theme.of(context).textTheme.labelSmall),
                const SizedBox(width: 8),
                Container(
                  padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 2),
                  decoration: BoxDecoration(
                    color: Theme.of(context).colorScheme.surfaceContainerHighest,
                    borderRadius: BorderRadius.circular(4),
                  ),
                  child: Text(review.actionTaken,
                      style: const TextStyle(fontSize: 10)),
                ),
                const Spacer(),
                SeverityBadge(severity: review.severity),
              ],
            ),
            const SizedBox(height: 8),
            Text(review.summary),
            if (review.category.isNotEmpty) ...[
              const SizedBox(height: 8),
              Text('Classification', style: Theme.of(context).textTheme.labelMedium),
              Padding(
                padding: const EdgeInsets.only(top: 4, left: 8),
                child: Text('Category: ${review.category}'),
              ),
            ],
            if (review.suggestions.isNotEmpty) ...[
              const SizedBox(height: 8),
              Text('Suggestions', style: Theme.of(context).textTheme.labelMedium),
              ...review.suggestions.map((s) => Padding(
                    padding: const EdgeInsets.only(top: 4, left: 8),
                    child: Row(
                      crossAxisAlignment: CrossAxisAlignment.start,
                      children: [
                        const Icon(Icons.lightbulb_outline, size: 14),
                        const SizedBox(width: 4),
                        Expanded(child: Text(s.toString())),
                      ],
                    ),
                  )),
            ],
          ],
        ),
      ),
    );
  }
}

class _IssueMetaPanel extends StatelessWidget {
  final TrackedIssue issue;
  const _IssueMetaPanel({required this.issue});

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.all(16),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text('Details', style: Theme.of(context).textTheme.titleMedium),
          const SizedBox(height: 12),
          _row(context, 'Repo', issue.repo),
          _row(context, 'Number', '#${issue.number}'),
          _row(context, 'Author', issue.author),
          _row(context, 'State', issue.state),
          _row(context, 'Created',
              issue.createdAt.toLocal().toString().substring(0, 16)),
          if (issue.assignees.isNotEmpty)
            _row(context, 'Assignees', issue.assignees.join(', ')),
          if (issue.labels.isNotEmpty) ...[
            const SizedBox(height: 8),
            Wrap(
              spacing: 4,
              runSpacing: 4,
              children: issue.labels
                  .map((l) => Chip(
                        label: Text(l.toString(), style: const TextStyle(fontSize: 11)),
                        materialTapTargetSize: MaterialTapTargetSize.shrinkWrap,
                        visualDensity: VisualDensity.compact,
                      ))
                  .toList(),
            ),
          ],
          const SizedBox(height: 12),
          OutlinedButton.icon(
            icon: const Icon(Icons.open_in_browser),
            label: const Text('Open on GitHub'),
            onPressed: () {
              final uri = Uri.tryParse(
                  'https://github.com/${issue.repo}/issues/${issue.number}');
              if (uri != null) launchUrl(uri);
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
              child: Text('$label:',
                  style: const TextStyle(fontWeight: FontWeight.w600))),
          Expanded(child: Text(value)),
        ],
      ),
    );
  }
}

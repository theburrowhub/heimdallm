import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import '../../core/models/tracked_issue.dart';
import '../../shared/widgets/severity_badge.dart';
import '../../shared/widgets/toast.dart';
import '../dashboard/dashboard_providers.dart';
import 'issues_providers.dart';

const _actionReviewOnly = 'review_only';

/// Filter state for the issues list.
final _repoFilterProvider = StateProvider<String?>((ref) => null);

class IssuesScreen extends ConsumerWidget {
  const IssuesScreen({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final issuesAsync = ref.watch(issuesProvider);
    final repoFilter = ref.watch(_repoFilterProvider);

    return issuesAsync.when(
      loading: () => const Center(child: CircularProgressIndicator()),
      error: (e, _) => Center(
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            const Icon(Icons.error_outline, size: 48, color: Colors.grey),
            const SizedBox(height: 12),
            Text('Error: $e'),
            const SizedBox(height: 16),
            FilledButton(
              onPressed: () => ref.invalidate(issuesProvider),
              child: const Text('Retry'),
            ),
          ],
        ),
      ),
      data: (issues) {
        if (issues.isEmpty) {
          return const Center(child: Text('No tracked issues'));
        }

        final repos = issues.map((i) => i.repo).toSet().toList()..sort();
        final filtered = repoFilter == null
            ? issues
            : issues.where((i) => i.repo == repoFilter).toList();

        return Column(
          children: [
            // Filter bar
            Padding(
              padding: const EdgeInsets.fromLTRB(16, 12, 16, 4),
              child: Row(
                children: [
                  Text('Repo:', style: TextStyle(fontSize: 12, color: Colors.grey.shade500)),
                  const SizedBox(width: 8),
                  DropdownButton<String?>(
                    value: repoFilter,
                    hint: const Text('All', style: TextStyle(fontSize: 13)),
                    isDense: true,
                    underline: const SizedBox.shrink(),
                    items: [
                      const DropdownMenuItem(value: null, child: Text('All')),
                      ...repos.map((r) =>
                          DropdownMenuItem(value: r, child: Text(r, style: const TextStyle(fontSize: 13)))),
                    ],
                    onChanged: (v) => ref.read(_repoFilterProvider.notifier).state = v,
                  ),
                  const Spacer(),
                  Text('${filtered.length} issue${filtered.length == 1 ? '' : 's'}',
                      style: TextStyle(fontSize: 12, color: Colors.grey.shade500)),
                ],
              ),
            ),
            // Issue list
            Expanded(
              child: ListView.builder(
                padding: const EdgeInsets.symmetric(vertical: 4),
                itemCount: filtered.length,
                itemBuilder: (_, i) => _IssueTile(issue: filtered[i]),
              ),
            ),
          ],
        );
      },
    );
  }
}

class _IssueTile extends ConsumerStatefulWidget {
  final TrackedIssue issue;
  const _IssueTile({required this.issue});

  @override
  ConsumerState<_IssueTile> createState() => _IssueTileState();
}

class _IssueTileState extends ConsumerState<_IssueTile> {
  String get _reviewKey => '${widget.issue.repo}:${widget.issue.number}';

  Future<void> _triggerReview() async {
    ref.read(reviewingIssuesProvider.notifier).update((s) => {...s, _reviewKey});
    try {
      await ref.read(apiClientProvider).triggerIssueReview(widget.issue.id);
    } catch (e) {
      ref.read(reviewingIssuesProvider.notifier).update((s) => s.difference({_reviewKey}));
      if (mounted) showToast(context, 'Error: $e', isError: true);
    }
  }

  Future<void> _promote() async {
    ref.read(promotingIssuesProvider.notifier).update((s) => {...s, _reviewKey});
    try {
      await ref.read(apiClientProvider).promoteIssue(widget.issue.id);
      if (mounted) showToast(context, 'Promoted to Develop — pipeline queued');
    } catch (e) {
      if (mounted) showToast(context, 'Error: $e', isError: true);
    } finally {
      ref.read(promotingIssuesProvider.notifier).update((s) => s.difference({_reviewKey}));
    }
  }

  Future<void> _dismiss() async {
    final api = ref.read(apiClientProvider);
    try {
      await api.dismissIssue(widget.issue.id);
      ref.invalidate(issuesProvider);
      if (mounted) {
        showToast(context, 'Issue #${widget.issue.number} dismissed',
            duration: const Duration(seconds: 5),
            actionLabel: 'Undo',
            onAction: () async {
              await api.undismissIssue(widget.issue.id);
              ref.invalidate(issuesProvider);
            });
      }
    } catch (e) {
      if (mounted) showToast(context, 'Error: $e', isError: true);
    }
  }

  @override
  Widget build(BuildContext context) {
    final issue = widget.issue;
    final reviewed = issue.latestReview != null;
    final isReviewing = ref.watch(reviewingIssuesProvider).contains(_reviewKey);
    final isPromoting = ref.watch(promotingIssuesProvider).contains(_reviewKey);
    final severity = issue.latestReview?.severity ?? '';
    // Show "Promote to Develop" only when the latest review action was review_only
    // and not currently running either pipeline on this issue.
    final canPromote = reviewed &&
        issue.latestReview!.actionTaken == _actionReviewOnly &&
        !isReviewing &&
        !isPromoting;

    return Card(
      margin: const EdgeInsets.symmetric(horizontal: 16, vertical: 3),
      child: InkWell(
        borderRadius: BorderRadius.circular(12),
        onTap: () => context.push('/issues/${issue.id}'),
        child: Padding(
          padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 12),
          child: Row(
            children: [
              // Severity bar
              Container(
                width: 4, height: 48,
                margin: const EdgeInsets.only(right: 12),
                decoration: BoxDecoration(
                  color: isReviewing
                      ? Theme.of(context).colorScheme.primary
                      : reviewed
                          ? _severityColor(severity)
                          : Colors.grey.shade600,
                  borderRadius: BorderRadius.circular(2),
                ),
              ),
              // Title + subtitle
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(issue.title,
                        style: const TextStyle(fontWeight: FontWeight.w600),
                        maxLines: 1, overflow: TextOverflow.ellipsis),
                    const SizedBox(height: 4),
                    Row(
                      children: [
                        Flexible(
                          child: Text('${issue.repo} · #${issue.number} · ${issue.author}',
                              style: Theme.of(context).textTheme.bodySmall,
                              overflow: TextOverflow.ellipsis),
                        ),
                        const SizedBox(width: 8),
                        ...issue.labels.take(3).map((l) => Padding(
                              padding: const EdgeInsets.only(right: 4),
                              child: Container(
                                padding: const EdgeInsets.symmetric(horizontal: 6, vertical: 1),
                                decoration: BoxDecoration(
                                  color: Theme.of(context).colorScheme.surfaceContainerHighest,
                                  borderRadius: BorderRadius.circular(4),
                                ),
                                child: Text(l.toString(),
                                    style: const TextStyle(fontSize: 10)),
                              ),
                            )),
                      ],
                    ),
                  ],
                ),
              ),
              const SizedBox(width: 12),
              // Trailing actions
              Row(
                mainAxisSize: MainAxisSize.min,
                children: [
                  if (isReviewing || isPromoting)
                    SizedBox(
                      width: 18, height: 18,
                      child: CircularProgressIndicator(
                        strokeWidth: 2,
                        color: isPromoting
                            ? Theme.of(context).colorScheme.secondary
                            : Theme.of(context).colorScheme.primary,
                      ),
                    )
                  else if (reviewed)
                    SeverityBadge(severity: severity)
                  else
                    _chip('PENDING', Colors.grey.shade700),
                  const SizedBox(width: 8),
                  if (!isReviewing && !isPromoting) ...[
                    SizedBox(
                      height: 28,
                      child: ElevatedButton(
                        style: ElevatedButton.styleFrom(
                            padding: const EdgeInsets.symmetric(horizontal: 10),
                            textStyle: const TextStyle(fontSize: 12)),
                        onPressed: _triggerReview,
                        child: const Text('Review'),
                      ),
                    ),
                    if (canPromote) ...[
                      const SizedBox(width: 6),
                      SizedBox(
                        height: 28,
                        child: ElevatedButton(
                          style: ElevatedButton.styleFrom(
                              padding: const EdgeInsets.symmetric(horizontal: 10),
                              textStyle: const TextStyle(fontSize: 12),
                              backgroundColor: Theme.of(context).colorScheme.secondary,
                              foregroundColor: Theme.of(context).colorScheme.onSecondary),
                          onPressed: _promote,
                          child: const Text('Promote to Dev'),
                        ),
                      ),
                    ],
                  ],
                  IconButton(
                    icon: const Icon(Icons.close, size: 14),
                    tooltip: 'Dismiss issue',
                    color: Colors.grey.shade600,
                    visualDensity: VisualDensity.compact,
                    onPressed: _dismiss,
                  ),
                ],
              ),
            ],
          ),
        ),
      ),
    );
  }

  Widget _chip(String label, Color color) => Container(
        padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 3),
        decoration: BoxDecoration(color: color, borderRadius: BorderRadius.circular(4)),
        child: Text(label,
            style: const TextStyle(
                color: Colors.white, fontSize: 11, fontWeight: FontWeight.w600)),
      );

  Color _severityColor(String s) {
    switch (s.toLowerCase()) {
      case 'critical': return Colors.red.shade900;
      case 'high':     return Colors.red.shade700;
      case 'medium':   return Colors.orange.shade700;
      default:         return Colors.green.shade700;
    }
  }
}

import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import '../../core/models/pr.dart';
import '../../core/models/review.dart';
import '../../shared/widgets/severity_badge.dart';
import '../../shared/widgets/toast.dart';
import '../dashboard/dashboard_providers.dart';
import 'pr_detail_providers.dart';

class PRDetailScreen extends ConsumerWidget {
  final int prId;
  const PRDetailScreen({super.key, required this.prId});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final detailAsync = ref.watch(prDetailProvider(prId));

    return Scaffold(
      appBar: AppBar(
        title: const Text('PR Review'),
        actions: [
          ElevatedButton.icon(
            icon: const Icon(Icons.refresh),
            label: const Text('Re-review'),
            onPressed: () async {
              final api = ref.read(apiClientProvider);
              try {
                await api.triggerReview(prId);
                ref.invalidate(prDetailProvider(prId));
                if (context.mounted) showToast(context, 'Review queued');
              } catch (e) {
                if (context.mounted) showToast(context, 'Error: $e', isError: true);
              }
            },
          ),
          const SizedBox(width: 12),
        ],
      ),
      body: detailAsync.when(
        loading: () => const Center(child: CircularProgressIndicator()),
        error: (e, _) => Center(child: Text('Error: $e')),
        data: (data) {
          final pr = data['pr'] as PR;
          final reviews = data['reviews'] as List<Review>;
          return Row(
            children: [
              Expanded(
                flex: 2,
                child: _ReviewPanel(pr: pr, reviews: reviews),
              ),
              const VerticalDivider(width: 1),
              Expanded(
                flex: 1,
                child: _PRMetaPanel(pr: pr),
              ),
            ],
          );
        },
      ),
    );
  }
}

class _ReviewPanel extends StatelessWidget {
  final PR pr;
  final List<Review> reviews;
  const _ReviewPanel({required this.pr, required this.reviews});

  @override
  Widget build(BuildContext context) {
    return SingleChildScrollView(
      padding: const EdgeInsets.all(16),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(pr.title, style: Theme.of(context).textTheme.headlineSmall),
          Text('${pr.repo} #${pr.number} by ${pr.author}',
              style: Theme.of(context).textTheme.bodySmall),
          const SizedBox(height: 16),
          if (reviews.isEmpty)
            const Text('No reviews yet.')
          else
            ...reviews.map((rev) => _ReviewCard(review: rev)),
        ],
      ),
    );
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
                Text('Reviewed by ${review.cliUsed}',
                    style: Theme.of(context).textTheme.labelSmall),
                const Spacer(),
                SeverityBadge(severity: review.severity),
              ],
            ),
            const SizedBox(height: 8),
            Text(review.summary),
            if (review.issues.isNotEmpty) ...[
              const SizedBox(height: 8),
              Text('Issues', style: Theme.of(context).textTheme.labelMedium),
              ...review.issues.map((issue) => Padding(
                padding: const EdgeInsets.only(top: 4, left: 8),
                child: Row(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    const Icon(Icons.warning_amber, size: 14),
                    const SizedBox(width: 4),
                    Expanded(child: Text('${issue.file}:${issue.line} — ${issue.description}')),
                  ],
                ),
              )),
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
                    Expanded(child: Text(s)),
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

class _PRMetaPanel extends StatelessWidget {
  final PR pr;
  const _PRMetaPanel({required this.pr});

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.all(16),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text('Details', style: Theme.of(context).textTheme.titleMedium),
          const SizedBox(height: 12),
          _row(context, 'Repo', pr.repo),
          _row(context, 'Number', '#${pr.number}'),
          _row(context, 'Author', pr.author),
          _row(context, 'State', pr.state),
          _row(context, 'Updated', pr.updatedAt.toLocal().toString().substring(0, 16)),
          const SizedBox(height: 12),
          OutlinedButton.icon(
            icon: const Icon(Icons.open_in_browser),
            label: const Text('Open on GitHub'),
            onPressed: () {},
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
          SizedBox(width: 72, child: Text('$label:', style: const TextStyle(fontWeight: FontWeight.w600))),
          Expanded(child: Text(value)),
        ],
      ),
    );
  }
}

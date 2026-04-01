import 'dart:async';
import 'dart:convert';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:url_launcher/url_launcher.dart';
import '../../core/models/pr.dart';
import '../../core/models/review.dart';
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
    if (mounted) setState(() => _reviewing = false);
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
          final currentPrNumber = detailAsync.valueOrNull?['pr'] is PR
              ? (detailAsync.valueOrNull!['pr'] as PR).number
              : null;

          switch (event.type) {
            case 'review_started':
              if (prNumber != null && prNumber == currentPrNumber) {
                if (mounted) setState(() => _reviewing = true);
              }
            case 'review_completed':
              if (prId == widget.prId) {
                _stopReviewing();
                ref.invalidate(prDetailProvider(widget.prId));
              }
            case 'review_error':
              if (prId == widget.prId) {
                _stopReviewing();
                final error = data['error'] as String? ?? 'Unknown error';
                if (mounted) showToast(context, 'Review failed: $error', isError: true);
              }
          }
        } catch (_) {}
      });
    });

    final reviews = detailAsync.valueOrNull?['reviews'] as List<Review>? ?? [];
    final hasReviews = reviews.isNotEmpty;

    return Scaffold(
      appBar: AppBar(
        title: const Text('PR Review'),
        leading: IconButton(
            icon: const Icon(Icons.arrow_back),
            onPressed: () => context.canPop() ? context.pop() : context.go('/')),
        actions: [
          _reviewing
              ? const Padding(
                  padding: EdgeInsets.symmetric(horizontal: 16),
                  child: SizedBox(
                    width: 20,
                    height: 20,
                    child: CircularProgressIndicator(strokeWidth: 2),
                  ),
                )
              : ElevatedButton.icon(
                  icon: const Icon(Icons.refresh, size: 16),
                  label: Text(hasReviews ? 'Re-review' : 'Review'),
                  onPressed: _trigger,
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
                        Expanded(
                            child: Text(
                                '${issue.file}:${issue.line} — ${issue.description}')),
                      ],
                    ),
                  )),
            ],
            if (review.suggestions.isNotEmpty) ...[
              const SizedBox(height: 8),
              Text('Suggestions',
                  style: Theme.of(context).textTheme.labelMedium),
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
          _row(context, 'Updated',
              pr.updatedAt.toLocal().toString().substring(0, 16)),
          const SizedBox(height: 12),
          OutlinedButton.icon(
            icon: const Icon(Icons.open_in_browser),
            label: const Text('Open on GitHub'),
            onPressed: () => launchUrl(Uri.parse(pr.url)),
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

import 'dart:async';

import 'package:clock/clock.dart';
import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../../core/models/review_limit_status.dart';
import '../../shared/design_system/components/components.dart';
import '../dashboard/dashboard_providers.dart';

/// Live usage of every configured review budget (GET /review-limits).
final reviewLimitsStatusProvider =
    FutureProvider.autoDispose<List<ReviewLimitStatus>>((ref) {
      return ref.watch(apiClientProvider).fetchReviewLimits();
    });

/// Card listing each review budget with a used/limit bar per window. Renders
/// nothing when no budget is configured, so it can sit on the stats screen
/// without adding noise for operators who never set one.
class ReviewLimitsUsageCard extends ConsumerStatefulWidget {
  /// Only show these scope kinds (null = all).
  final Set<String>? kinds;

  const ReviewLimitsUsageCard({super.key, this.kinds});

  @override
  ConsumerState<ReviewLimitsUsageCard> createState() =>
      _ReviewLimitsUsageCardState();
}

class _ReviewLimitsUsageCardState extends ConsumerState<ReviewLimitsUsageCard> {
  Timer? _refreshTimer;

  @override
  void initState() {
    super.initState();
    _refreshTimer = Timer.periodic(const Duration(seconds: 30), (_) {
      if (mounted) ref.invalidate(reviewLimitsStatusProvider);
    });
  }

  @override
  void dispose() {
    _refreshTimer?.cancel();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final async = ref.watch(reviewLimitsStatusProvider);
    final statuses = (async.value ?? const <ReviewLimitStatus>[])
        .where((s) => widget.kinds == null || widget.kinds!.contains(s.kind))
        .where((s) => s.windows.isNotEmpty)
        .toList();
    if (async.hasError) {
      // Older daemons have no /review-limits; stay out of the way.
      return const SizedBox.shrink();
    }
    if (statuses.isEmpty) return const SizedBox.shrink();
    return Padding(
      padding: const EdgeInsets.fromLTRB(12, 4, 12, 4),
      child: AppSurface(
        padding: const EdgeInsets.all(12),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                const Icon(Icons.hourglass_bottom, size: 18),
                const SizedBox(width: 8),
                const Expanded(
                  child: Text(
                    'Review limits',
                    style: TextStyle(fontWeight: FontWeight.w600, fontSize: 14),
                  ),
                ),
                IconButton(
                  icon: const Icon(Icons.refresh, size: 18),
                  tooltip: 'Refresh',
                  visualDensity: VisualDensity.compact,
                  onPressed: () => ref.invalidate(reviewLimitsStatusProvider),
                ),
              ],
            ),
            for (final s in statuses) ReviewLimitStatusRow(status: s),
          ],
        ),
      ),
    );
  }
}

/// One budget: its label and a bar per limited window.
class ReviewLimitStatusRow extends StatelessWidget {
  final ReviewLimitStatus status;

  const ReviewLimitStatusRow({super.key, required this.status});

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 4),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(
            status.label,
            style: const TextStyle(fontSize: 12, fontWeight: FontWeight.w600),
          ),
          for (final w in status.windows) _window(w),
        ],
      ),
    );
  }

  Widget _window(ReviewLimitWindowUsage w) {
    final frac = w.limit > 0 ? (w.used / w.limit).clamp(0.0, 1.0) : 0.0;
    final full = w.exhausted;
    return Padding(
      padding: const EdgeInsets.only(top: 3),
      child: Row(
        children: [
          SizedBox(
            width: 72,
            child: Text(
              'per ${w.window}',
              style: const TextStyle(fontSize: 12),
            ),
          ),
          Expanded(
            child: ClipRRect(
              borderRadius: BorderRadius.circular(4),
              child: LinearProgressIndicator(
                value: frac,
                minHeight: 6,
                color: full ? Colors.red.shade400 : null,
              ),
            ),
          ),
          const SizedBox(width: 8),
          SizedBox(
            width: 64,
            child: Text(
              '${w.used} / ${w.limit}',
              textAlign: TextAlign.right,
              style: TextStyle(
                fontSize: 12,
                color: full ? Colors.red.shade400 : null,
              ),
            ),
          ),
          const SizedBox(width: 8),
          SizedBox(
            width: 92,
            child: Text(
              full ? _slotLabel(w.resetAt) : '',
              textAlign: TextAlign.right,
              style: TextStyle(fontSize: 10, color: Colors.grey.shade600),
            ),
          ),
        ],
      ),
    );
  }

  static String _slotLabel(DateTime? at) {
    if (at == null) return '';
    final diff = at.difference(clock.now());
    if (diff.isNegative) return 'slot free now';
    if (diff.inMinutes < 1) return 'slot in ${diff.inSeconds}s';
    if (diff.inMinutes < 60) return 'slot in ${diff.inMinutes}m';
    return 'slot in ${diff.inHours}h';
  }
}

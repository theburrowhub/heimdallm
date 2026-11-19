import 'package:flutter/material.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:mix/mix.dart' show ColorToken;

import '../../core/models/pr.dart';
import '../../core/models/review.dart';
import '../../core/state/local_state_notifier.dart';
import '../../shared/design_system/tokens.dart';
import '../pr_detail/review_workspace.dart' show verdictOf;

/// The inbox buckets of the Activity screen (#910). [all] holds every PR; the
/// other three never share a PR.
enum InboxBucket {
  needsYou('Needs you', Icons.flag_outlined),
  inProgress('In progress', Icons.autorenew),
  doneToday('Done today', Icons.task_alt),
  all('All', Icons.all_inbox_outlined);

  const InboxBucket(this.label, this.icon);
  final String label;
  final IconData icon;
}

/// The bucket other than [InboxBucket.all] that [pr] belongs to, or null.
///
/// [reviewing] is the app's optimistic flag for a review it just started,
/// before the daemon reports it as active.
InboxBucket? bucketOf(PR pr, {required bool reviewing, required DateTime now}) {
  final open = pr.state == 'open';
  final review = pr.latestReview;
  final status = pr.reviewStatus;
  final running = reviewing || (status?.active ?? false);
  if (open && !running) {
    final failed = status != null && status.error.isNotEmpty;
    // On an open PR an abandoned review is one GitHub rejected for good.
    if (failed || review?.publication == ReviewPublication.abandoned) {
      return InboxBucket.needsYou;
    }
  }
  if (open &&
      (running ||
          review == null ||
          review.publication == ReviewPublication.pending)) {
    return InboxBucket.inProgress;
  }
  if (review != null && _sameLocalDay(review.createdAt, now)) {
    return InboxBucket.doneToday;
  }
  return null;
}

bool _sameLocalDay(DateTime a, DateTime b) {
  final x = a.toLocal(), y = b.toLocal();
  return x.year == y.year && x.month == y.month && x.day == y.day;
}

/// The bucket the Activity list shows. Not persisted: the app opens on All so
/// no PR seems to be missing.
final inboxBucketProvider =
    NotifierProvider<LocalStateNotifier<InboxBucket>, InboxBucket>(
      () => LocalStateNotifier(InboxBucket.all),
    );

/// What a PR row says about its review, most urgent state first.
typedef PrStatus = ({String label, ColorToken color, IconData icon});

PrStatus prStatusOf(PR pr, {required bool reviewing}) {
  final status = pr.reviewStatus;
  if (reviewing || (status?.active ?? false)) {
    return (
      label: 'Reviewing…',
      color: AppColors.accent,
      icon: Icons.autorenew,
    );
  }
  if (status != null && status.error.isNotEmpty) {
    return status.isCancelled
        ? (
            label: 'Review cancelled',
            color: AppColors.danger,
            icon: Icons.stop_circle_outlined,
          )
        : (
            label: 'Review failed',
            color: AppColors.danger,
            icon: Icons.error_outline,
          );
  }
  final review = pr.latestReview;
  if (review == null) {
    return (
      label: 'Not reviewed yet',
      color: AppColors.textMuted,
      icon: Icons.schedule,
    );
  }
  switch (review.publication) {
    case ReviewPublication.pending:
      return (
        label: 'Waiting to publish',
        color: AppColors.warning,
        icon: Icons.schedule_send_outlined,
      );
    case ReviewPublication.abandoned:
    case ReviewPublication.superseded:
      return (
        label: 'Not posted',
        color:
            pr.state == 'open' &&
                review.publication == ReviewPublication.abandoned
            ? AppColors.danger
            : AppColors.textMuted,
        icon: Icons.cloud_off_outlined,
      );
    case ReviewPublication.posted:
      break;
  }
  return switch (verdictOf(review)) {
    ReviewVerdict.changesRequested => (
      label: 'Changes requested',
      color: AppColors.danger,
      icon: Icons.do_not_disturb_on_outlined,
    ),
    ReviewVerdict.approved => (
      label: 'Approved',
      color: AppColors.success,
      icon: Icons.check_circle_outline,
    ),
    ReviewVerdict.commented => (
      label: 'Commented',
      color: AppColors.info,
      icon: Icons.chat_bubble_outline,
    ),
    ReviewVerdict.dismissed => (
      label: 'Review dismissed',
      color: AppColors.textMuted,
      icon: Icons.remove_circle_outline,
    ),
  };
}

/// "2 high · 1 medium", or empty when the review found nothing.
String findingCounts(Review review) {
  final counts = <String, int>{};
  for (final issue in review.issues) {
    final s = issue.severity.toLowerCase();
    counts[s] = (counts[s] ?? 0) + 1;
  }
  return [
    for (final s in const ['high', 'medium', 'low'])
      if (counts[s] != null) '${counts[s]} $s',
  ].join(' · ');
}
